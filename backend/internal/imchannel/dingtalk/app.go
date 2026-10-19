// Package dingtalk 是钉钉渠道:企业内部应用的机器人在单聊里投放互动卡片、收起卡片,并经 Stream
// 长连接收回卡片点击与私聊(stream.go)。群通知(自定义机器人)在第 6 期。
package dingtalk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"velagateway/internal/imchannel"
)

const (
	defaultBase = "https://api.dingtalk.com"

	pathToken   = "/v1.0/oauth2/accessToken"
	pathOpen    = "/v1.0/gateway/connections/open"
	pathDeliver = "/v1.0/card/instances/createAndDeliver"
	pathCard    = "/v1.0/card/instances"
	pathOTO     = "/v1.0/robot/oToMessages/batchSend"

	tokenHeader = "x-acs-dingtalk-access-token"
)

// errBadBase:出站防护拒绝了开放平台地址(不是 https、解析到内网)。改配置之前怎么重试都一样。
var errBadBase = errors.New("开放平台地址不可用")

// errNoTemplate:钉钉的 Stream 卡片回调只支持互动卡片高级版,高级版只能用卡片平台上建好的模板。
var errNoTemplate = errors.New("未配置卡片模板 ID:按启用手册在钉钉卡片平台搭好模板,把模板 ID 填进设置")

// userIDRe 是钉钉 userid(staffId)。它会拼进以 . 分段的 openSpaceId,所以按白名单收。
var userIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// App 是钉钉开放平台的企业内部应用:投放卡片、收起卡片,并收回点击与私聊。
type App struct {
	clientID     string
	clientSecret string
	robotCode    string
	template     string
	base         string
	client       *http.Client
	validate     func(string) error
	seen         imchannel.Recent // 已处理的私聊 msgId

	tokMu  sync.Mutex
	tok    string
	tokExp time.Time
}

// New 在配了 Client ID 与 Client Secret 时造一个应用,否则返回 nil。不做任何 I/O —— 它在 Hub
// 的锁里被调用。卡片模板 ID 没填也照样造:Run 会把「缺模板」报到设置页上,比渠道静悄悄地显示
// 「未配置」更好排查。
func New(d imchannel.Deps) imchannel.Channel {
	key := func(s string) string { return imchannel.Key(imchannel.DingTalk, s) }
	id := strings.TrimSpace(d.Settings.String(key("clientID")))
	secret := strings.TrimSpace(d.Settings.Secret(key("clientSecret")))
	if id == "" || secret == "" {
		return nil
	}
	robot := strings.TrimSpace(d.Settings.String(key("robotCode")))
	if robot == "" {
		robot = id // 企业内部应用的机器人,robotCode 默认就是 Client ID(原 AppKey)
	}
	base := strings.TrimRight(strings.TrimSpace(d.Settings.String(key("apiBase"))), "/")
	if base == "" {
		base = defaultBase
	}
	return &App{
		clientID: id, clientSecret: secret, robotCode: robot, base: base,
		template: strings.TrimSpace(d.Settings.String(key("cardTemplateId"))),
		client:   imchannel.NoRedirects(d.HTTP), validate: d.ValidateURL,
	}
}

func (a *App) Kind() string { return imchannel.DingTalk }

// apiError 是钉钉明确回了错误:HTTP 非 2xx,或者回了个解析不了的响应。Code 是钉钉的字符串错误码。
type apiError struct {
	Path string
	HTTP int
	Code string
	Msg  string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("钉钉 %s: HTTP %d %s %s", e.Path, e.HTTP, e.Code, e.Msg)
}

// throttled 认出钉钉的限流码(如 Forbidden.AccessDenied.QpsLimitForApi):暂时的,退避重试。
func throttled(code string) bool {
	return strings.Contains(code, "QpsLimit") || strings.Contains(strings.ToLower(code), "throttl")
}

// permanent 判断重试无望的错误:钉钉明确拒绝(凭据错、没开权限 …)、开放平台地址填错(回了
// 3xx / 4xx、回了解析不了的 2xx、被出站防护拒绝)、没配卡片模板。限流、429、5xx、网络错误
// (含 DNS 解析失败)是暂时的。
func permanent(err error) bool {
	if errors.Is(err, errBadBase) || errors.Is(err, errNoTemplate) {
		return true
	}
	var ae *apiError
	if !errors.As(err, &ae) {
		return false
	}
	return ae.HTTP < 500 && ae.HTTP != http.StatusTooManyRequests && !throttled(ae.Code)
}

// request 发一次 OpenAPI 请求;HTTP 非 2xx、响应不是 JSON 都是 *apiError,出站校验拒绝是 errBadBase。
func (a *App) request(ctx context.Context, method, path, token string, body any) ([]byte, error) {
	if err := a.validate(a.base); err != nil {
		// DNS 解析失败可能只是一时的网络问题,照常重试;其余(不是 https、解析到内网)是配置错。
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %v", errBadBase, err)
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
		req.Header.Set(tokenHeader, token)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, a.scrub(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if !json.Valid(raw) {
		// 5xx 回 HTML 多半是钉钉那头临时出错;5xx 以下还不是 JSON(200 的页面、3xx 重定向),就是
		// 地址指错了地方。不把响应体带进错误:那里面什么都可能有。
		msg := "响应无法解析"
		if resp.StatusCode < 500 {
			msg = "响应无法解析,检查开放平台地址"
		}
		return nil, &apiError{Path: path, HTTP: resp.StatusCode, Msg: msg}
	}
	if resp.StatusCode >= 300 {
		var env struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(raw, &env)
		return nil, &apiError{Path: path, HTTP: resp.StatusCode, Code: env.Code, Msg: a.scrub(errors.New(env.Message)).Error()}
	}
	return raw, nil
}

// call 带着 access token 调一次;token 被拒(401)就换一个再试一次 —— 缓存的 token 被提前作废时
// (管理员在后台重置了 Secret),不至于之后每张卡片都失败到 token 自然过期。
func (a *App) call(ctx context.Context, method, path string, body any) ([]byte, error) {
	tok, err := a.token(ctx)
	if err != nil {
		return nil, err
	}
	raw, err := a.request(ctx, method, path, tok, body)
	var ae *apiError
	if errors.As(err, &ae) && ae.HTTP == http.StatusUnauthorized {
		a.dropToken(tok)
		if tok, err = a.token(ctx); err != nil {
			return nil, err
		}
		raw, err = a.request(ctx, method, path, tok, body)
	}
	return raw, err
}

// token 返回缓存的 access token,快过期(剩 5 分钟)就换新的。
func (a *App) token(ctx context.Context) (string, error) {
	a.tokMu.Lock()
	defer a.tokMu.Unlock()
	if a.tok != "" && time.Now().Before(a.tokExp) {
		return a.tok, nil
	}
	raw, err := a.request(ctx, http.MethodPost, pathToken, "", map[string]string{"appKey": a.clientID, "appSecret": a.clientSecret})
	if err != nil {
		return "", err
	}
	var r struct {
		AccessToken string `json:"accessToken"`
		ExpireIn    int    `json:"expireIn"`
	}
	if json.Unmarshal(raw, &r) != nil || r.AccessToken == "" {
		// 2xx 却没有 token:多半是开放平台地址填成了别的服务(旧的 oapi 域名、会回 200 JSON 的网关)。
		// 当暂时错误会无限重试且 LastError 为空,按永久错误报给设置页。
		return "", &apiError{Path: pathToken, HTTP: http.StatusOK, Msg: "响应里没有 accessToken,检查开放平台地址"}
	}
	a.tok = r.AccessToken
	a.tokExp = time.Now().Add(time.Duration(max(r.ExpireIn-300, 60)) * time.Second)
	return a.tok, nil
}

func (a *App) dropToken(old string) {
	a.tokMu.Lock()
	defer a.tokMu.Unlock()
	if a.tok == old {
		a.tok = ""
	}
}

// scrub 把 Client Secret 从错误文本里换掉。它只在请求体里,正常不会出现在错误里 —— 这是防一个
// 往错误里回显请求的代理或私有化部署。
func (a *App) scrub(err error) error {
	if err == nil || a.clientSecret == "" {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), a.clientSecret, "<client-secret>"))
}

// SendCard 在机器人与 userID 的单聊里投放一张审批卡片。ref 是 outTrackId:收卡、认点击都靠它。
func (a *App) SendCard(ctx context.Context, userID string, c imchannel.ApprovalCard) (string, error) {
	if a.template == "" {
		return "", errNoTemplate
	}
	if !userIDRe.MatchString(userID) {
		return "", fmt.Errorf("无法识别的钉钉 userid %q", userID)
	}
	track, err := newTrackID(c.ApNo)
	if err != nil {
		return "", err
	}
	raw, err := a.call(ctx, http.MethodPost, pathDeliver, map[string]any{
		"cardTemplateId": a.template,
		"outTrackId":     track,
		"callbackType":   "STREAM",
		"cardData":       map[string]any{"cardParamMap": cardParams(c)},
		"openSpaceId":    "dtv1.card//IM_ROBOT." + userID,
		// 不许转发:按钮谁点都只认点击者本人,转出去也批不了,只会让人困惑。
		"imRobotOpenSpaceModel":   map[string]any{"supportForward": false},
		"imRobotOpenDeliverModel": map[string]any{"spaceType": "IM_ROBOT", "robotCode": a.robotCode},
		"userIdType":              1,
	})
	if err != nil {
		return "", err
	}
	var r struct {
		Result struct {
			DeliverResults []struct {
				Success  bool   `json:"success"`
				ErrorMsg string `json:"errorMsg"`
			} `json:"deliverResults"`
		} `json:"result"`
	}
	_ = json.Unmarshal(raw, &r)
	// 接口成功不等于投放成功:对方不在应用可见范围内时钉钉照样回 200,只在 deliverResults 里说
	// 失败。当成功的话,库里会记下一张根本不存在的卡片。
	if len(r.Result.DeliverResults) == 0 {
		return "", &apiError{Path: pathDeliver, HTTP: http.StatusOK, Msg: "响应里没有投放结果,检查开放平台地址"}
	}
	for _, d := range r.Result.DeliverResults {
		if !d.Success {
			return "", fmt.Errorf("钉钉投放卡片失败: %s", a.scrub(errors.New(d.ErrorMsg)))
		}
	}
	return track, nil
}

// CloseCard 把卡片改成终态:只改 status / result / tip,卡片其余内容原样保留。
func (a *App) CloseCard(ctx context.Context, ref string, final imchannel.CardState) error {
	if _, ok := trackApNo(ref); !ok {
		return fmt.Errorf("无法识别的卡片引用 %q", ref)
	}
	raw, err := a.call(ctx, http.MethodPut, pathCard, map[string]any{
		"outTrackId":        ref,
		"cardData":          map[string]any{"cardParamMap": closedParams(final)},
		"cardUpdateOptions": map[string]any{"updateCardDataByKey": true},
	})
	if err != nil {
		return err
	}
	// HTTP 200 不等于更新成功:UpdateCardResponseBody 带 success 字段。明说失败就报错,
	// 免得把没收起来的卡片记成已收起。字段缺失按成功处理(与老版本/私有化兼容)。
	var r struct {
		Success *bool `json:"success"`
	}
	if json.Unmarshal(raw, &r) == nil && r.Success != nil && !*r.Success {
		return fmt.Errorf("钉钉更新卡片失败: 响应 success=false")
	}
	return nil
}

// sendText 以机器人身份给 userID 发一条单聊文本(绑定结果的回复)。
func (a *App) sendText(ctx context.Context, userID, text string) error {
	param, err := json.Marshal(map[string]string{"content": text})
	if err != nil {
		return err
	}
	raw, err := a.call(ctx, http.MethodPost, pathOTO, map[string]any{
		"robotCode": a.robotCode, "userIds": []string{userID}, "msgKey": "sampleText", "msgParam": string(param),
	})
	if err != nil {
		return err
	}
	var r struct {
		Invalid []string `json:"invalidStaffIdList"`
		Limited []string `json:"flowControlledStaffIdList"`
	}
	_ = json.Unmarshal(raw, &r)
	if len(r.Invalid) > 0 || len(r.Limited) > 0 {
		return fmt.Errorf("钉钉单聊消息没有送达 %s(无效 %v,限流 %v)", userID, r.Invalid, r.Limited)
	}
	return nil
}
