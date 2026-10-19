package lark

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"

	"velagateway/internal/imchannel"
)

const (
	feishuBase = "https://open.feishu.cn"
	larkBase   = "https://open.larksuite.com"

	// WebhookPath 是飞书推事件与卡片回调到网关的路径。开发者后台「事件配置」与「回调配置」的
	// 请求地址都填 <回调根地址>+它 —— 飞书不像 Telegram 那样由网关自己登记。
	WebhookPath = "/api/v1/im/lark/webhook"

	modeWS      = "ws"
	modeWebhook = "webhook"

	pathToken    = "/open-apis/auth/v3/tenant_access_token/internal"
	pathBotInfo  = "/open-apis/bot/v3/info"
	pathMessages = "/open-apis/im/v1/messages"

	// codeRateLimited 是飞书的频控错误码:暂时的,退避重试。
	codeRateLimited = 99991400
)

// tokenRejected 是「tenant_access_token 缺失 / 无效」。缓存的 token 被提前作废时(管理员在
// 后台重置了 App Secret)换一个再试一次,而不是之后每张卡片都失败到 token 自然过期。
var tokenRejected = map[int]bool{99991661: true, 99991663: true}

// msgIDRe 是飞书消息 ID。收卡时 ref 来自库里,但仍按白名单拼进 URL 路径。
var msgIDRe = regexp.MustCompile(`^om_[A-Za-z0-9_-]{1,128}$`)

// App 是开放平台自建应用:私信卡片、收起卡片,并收回点击与私聊。
type App struct {
	*Channel // 群通知那一半;没配群机器人 webhook 时 NotifyApproval 报「未配置」

	appID       string
	secret      string
	base        string
	mode        string
	encryptKey  string
	verifyToken string
	client      *http.Client
	validate    func(string) error
	bot         atomic.Value                // string:Run 里查到的应用名
	disp        *dispatcher.EventDispatcher // 长连接与 webhook 共用;处理函数从 ctx 里取 sink
	seen        recent                      // 已处理的私聊 message_id

	tokMu  sync.Mutex
	tok    string
	tokExp time.Time
}

// newApp 在配了 App ID 与 App Secret 时造一个应用,否则返回 nil。不做任何 I/O ——
// 它在 Hub 的锁里被调用。
func newApp(d imchannel.Deps, n *Channel) *App {
	key := func(s string) string { return imchannel.Key(imchannel.Lark, s) }
	appID := strings.TrimSpace(d.Settings.String(key("appID")))
	secret := strings.TrimSpace(d.Settings.Secret(key("appSecret")))
	if appID == "" || secret == "" {
		return nil
	}
	base := strings.TrimRight(strings.TrimSpace(d.Settings.String(key("apiBase"))), "/")
	if base == "" {
		base = feishuBase
		if d.Settings.String(key("domain")) == "lark" {
			base = larkBase
		}
	}
	mode := d.Settings.String(key("mode"))
	if mode != modeWebhook {
		mode = modeWS
	}
	a := &App{
		Channel: n, appID: appID, secret: secret, base: base, mode: mode,
		encryptKey:  strings.TrimSpace(d.Settings.Secret(key("encryptKey"))),
		verifyToken: strings.TrimSpace(d.Settings.Secret(key("verificationToken"))),
		client:      d.HTTP, validate: d.ValidateURL,
	}
	a.bot.Store("")
	a.disp = a.newDispatcher()
	return a
}

// BotName 返回 Run 里查到并缓存的应用名。Hub 锁内调用,所以不做 I/O。
func (a *App) BotName() string { return a.bot.Load().(string) }

// apiError 是飞书明确回了错误:code 非零,或者回了个解析不了的 4xx/5xx。
type apiError struct {
	Path string
	HTTP int
	Code int
	Msg  string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("飞书 %s: HTTP %d, code %d %s", e.Path, e.HTTP, e.Code, e.Msg)
}

// permanent 判断重试无望的错误:飞书明确拒绝(凭据错、应用没开机器人能力 …),或开放平台
// 地址填错回了 4xx。频控、5xx、网络错误是暂时的。
func permanent(err error) bool {
	var ae *apiError
	if !errors.As(err, &ae) || ae.HTTP >= 500 || ae.Code == codeRateLimited {
		return false
	}
	return ae.Code != 0 || (ae.HTTP >= 400 && ae.HTTP != http.StatusTooManyRequests)
}

// request 发一次 OpenAPI 请求;code 非零或 HTTP 非 2xx 都是 *apiError。
func (a *App) request(ctx context.Context, method, path, token string, body any) ([]byte, error) {
	if err := a.validate(a.base); err != nil {
		return nil, err
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.base+path, rd)
	if err != nil {
		return nil, a.scrub(err)
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, a.scrub(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	short, _, _ := strings.Cut(path, "?")
	var env struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, &apiError{Path: short, HTTP: resp.StatusCode, Msg: "响应无法解析"}
	}
	if env.Code != 0 || resp.StatusCode >= 300 {
		return nil, &apiError{Path: short, HTTP: resp.StatusCode, Code: env.Code, Msg: a.scrub(errors.New(env.Msg)).Error()}
	}
	return raw, nil
}

// call 带着 tenant_access_token 调一次;token 被拒就换一个再试一次。
func (a *App) call(ctx context.Context, method, path string, body any) ([]byte, error) {
	tok, err := a.token(ctx)
	if err != nil {
		return nil, err
	}
	raw, err := a.request(ctx, method, path, tok, body)
	var ae *apiError
	if errors.As(err, &ae) && tokenRejected[ae.Code] {
		a.dropToken(tok)
		if tok, err = a.token(ctx); err != nil {
			return nil, err
		}
		raw, err = a.request(ctx, method, path, tok, body)
	}
	return raw, err
}

// token 返回缓存的 tenant_access_token,快过期(剩 5 分钟)就换新的。
func (a *App) token(ctx context.Context) (string, error) {
	a.tokMu.Lock()
	defer a.tokMu.Unlock()
	if a.tok != "" && time.Now().Before(a.tokExp) {
		return a.tok, nil
	}
	raw, err := a.request(ctx, http.MethodPost, pathToken, "", map[string]string{"app_id": a.appID, "app_secret": a.secret})
	if err != nil {
		return "", err
	}
	var r struct {
		Token  string `json:"tenant_access_token"`
		Expire int    `json:"expire"`
	}
	if json.Unmarshal(raw, &r) != nil || r.Token == "" {
		return "", errors.New("飞书 tenant_access_token 响应里没有 token")
	}
	a.tok = r.Token
	a.tokExp = time.Now().Add(time.Duration(max(r.Expire-300, 60)) * time.Second)
	return a.tok, nil
}

func (a *App) dropToken(old string) {
	a.tokMu.Lock()
	defer a.tokMu.Unlock()
	if a.tok == old {
		a.tok = ""
	}
}

// scrub 把 App Secret 从错误文本里换掉。它只在请求体里,正常不会出现在错误里 ——
// 这是防一个往错误里回显请求的代理或私有化部署。
func (a *App) scrub(err error) error {
	if err == nil || a.secret == "" {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), a.secret, "<app-secret>"))
}

// retry 反复调 fn 直到成功、遇到永久性错误或 ctx 结束(后者返回 nil)。
func (a *App) retry(ctx context.Context, fn func() error) error {
	backoff := time.Second
	for {
		err := fn()
		if err == nil || permanent(err) {
			return err
		}
		if ctx.Err() != nil {
			return nil
		}
		slog.Warn("lark: call failed, retrying", "err", err, "retryIn", backoff)
		t := time.NewTimer(backoff)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return nil
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

// botInfo 查应用名:绑定弹窗用它告诉用户私聊谁。查不到说明应用没开机器人能力 —— 那样
// 卡片也发不出去,所以 Run 把它当成配置错误。
func (a *App) botInfo(ctx context.Context) (string, error) {
	raw, err := a.call(ctx, http.MethodGet, pathBotInfo, nil)
	if err != nil {
		return "", err
	}
	var r struct {
		Bot struct {
			AppName string `json:"app_name"`
		} `json:"bot"`
	}
	_ = json.Unmarshal(raw, &r)
	return r.Bot.AppName, nil
}

// send 给一个 open_id 私信一条消息,返回 message_id。
func (a *App) send(ctx context.Context, openID, msgType string, content any) (string, error) {
	b, err := json.Marshal(content)
	if err != nil {
		return "", err
	}
	raw, err := a.call(ctx, http.MethodPost, pathMessages+"?receive_id_type=open_id", map[string]string{
		"receive_id": openID, "msg_type": msgType, "content": string(b),
	})
	if err != nil {
		return "", err
	}
	var r struct {
		Data struct {
			MessageID string `json:"message_id"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &r) != nil || r.Data.MessageID == "" {
		return "", errors.New("飞书发送消息:响应里没有 message_id")
	}
	return r.Data.MessageID, nil
}

func (a *App) sendText(ctx context.Context, openID, text string) error {
	_, err := a.send(ctx, openID, "text", map[string]string{"text": text})
	return err
}

// SendCard 私信一张审批卡片。ref 是 "<message_id>|<ApNo>":收卡时整张替换,得知道是哪张单。
func (a *App) SendCard(ctx context.Context, openID string, c imchannel.ApprovalCard) (string, error) {
	id, err := a.send(ctx, openID, "interactive", interactiveCard(c))
	if err != nil {
		return "", err
	}
	return id + "|" + c.ApNo, nil
}

// CloseCard 把卡片换成终态。
func (a *App) CloseCard(ctx context.Context, ref string, final imchannel.CardState) error {
	id, apNo, _ := strings.Cut(ref, "|")
	if !msgIDRe.MatchString(id) {
		return fmt.Errorf("无法识别的卡片引用 %q", ref)
	}
	b, err := json.Marshal(closedCard(apNo, final))
	if err != nil {
		return err
	}
	_, err = a.call(ctx, http.MethodPatch, pathMessages+"/"+id, map[string]string{"content": string(b)})
	return err
}
