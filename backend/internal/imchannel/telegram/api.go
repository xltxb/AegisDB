// Package telegram 是 Telegram 渠道:把审批卡片私信给已绑定的人,收回按钮点击与私聊。
//
// 只用标准库调 Bot API(https://core.telegram.org/bots/api)。它**不做任何判定** ——
// 点击交给 ActionSink,由 service 认人、走 DecideBlockFor。
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"velagateway/internal/imchannel"
)

const (
	defaultAPIBase = "https://api.telegram.org"
	// WebhookPath 是网关上接 Telegram 推送的路径,setWebhook 登记的就是 <回调根地址>+它。
	WebhookPath = "/api/v1/im/telegram/webhook"
	// DoneData 是收卡后那颗状态按钮的 callback_data:点它只回一句「已处理」。
	DoneData = "done"

	modePolling = "polling"
	modeWebhook = "webhook"

	// pollTimeout 是 getUpdates 的长轮询秒数。HTTP 客户端的超时要比它长,否则每一轮都以
	// 超时收场。
	pollTimeout = 25
)

// Bot API 的对象,只取用得到的字段。
type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username,omitempty"`
}

type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"` // private | group | supergroup | channel
}

type Message struct {
	MessageID int64  `json:"message_id"`
	From      *User  `json:"from,omitempty"`
	Chat      Chat   `json:"chat"`
	Text      string `json:"text,omitempty"`
}

type CallbackQuery struct {
	ID      string   `json:"id"`
	From    User     `json:"from"`
	Message *Message `json:"message,omitempty"`
	Data    string   `json:"data,omitempty"`
}

type Update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *Message       `json:"message,omitempty"`
	CallbackQuery *CallbackQuery `json:"callback_query,omitempty"`
}

// allowedUpdates 只订阅用得到的两类事件。
var allowedUpdates = []string{"message", "callback_query"}

type Channel struct {
	token    string
	secret   string // webhook 的 secret_token
	mode     string
	apiBase  string
	hookURL  string // webhook 模式下登记给 Telegram 的完整地址;没配回调根地址时为空
	client   *http.Client
	poll     *http.Client // 同一个受 SSRF 防护的 Transport,只是超时够 long polling 用
	validate func(string) error
	bot      atomic.Value // string:getMe 拿到的用户名,Run 里写
}

// New 在配置了 bot token 时造一个渠道,否则返回 nil(接口 nil)。不做任何 I/O ——
// 它在 Hub 的锁里被调用。
func New(d imchannel.Deps) imchannel.Channel {
	key := func(n string) string { return imchannel.Key(imchannel.Telegram, n) }
	token := strings.TrimSpace(d.Settings.Secret(key("botToken")))
	if token == "" {
		return nil
	}
	base := strings.TrimRight(strings.TrimSpace(d.Settings.String(key("apiBase"))), "/")
	if base == "" {
		base = defaultAPIBase
	}
	mode := d.Settings.String(key("mode"))
	if mode != modeWebhook {
		mode = modePolling
	}
	hook := ""
	if cb := strings.TrimRight(strings.TrimSpace(d.Settings.String("im.callbackBaseURL")), "/"); cb != "" {
		hook = cb + WebhookPath
	}
	// 复制一份客户端只改超时:Transport(拨号时的内网拦截)仍是同一份。
	poll := *d.HTTP
	poll.Timeout = (pollTimeout + 15) * time.Second
	c := &Channel{
		token: token, secret: d.Settings.Secret(key("webhookSecret")), mode: mode,
		apiBase: base, hookURL: hook, client: d.HTTP, poll: &poll, validate: d.ValidateURL,
	}
	c.bot.Store("")
	return c
}

func (c *Channel) Kind() string { return imchannel.Telegram }

// BotName 返回 Run 里 getMe 拿到并缓存的用户名。Hub 锁内调用,所以不做 I/O。
func (c *Channel) BotName() string { return c.bot.Load().(string) }

type apiResp struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	Description string          `json:"description"`
	ErrorCode   int             `json:"error_code"`
}

// apiError 是 Telegram 明确回了 ok:false。
type apiError struct {
	Method string
	Code   int
	Desc   string
}

func (e *apiError) Error() string { return fmt.Sprintf("telegram %s: %d %s", e.Method, e.Code, e.Desc) }

// isPermanent 判断重试无望的错误:Telegram 明确回了 4xx(401 token 被拒、404 token 格式错、
// 400 setWebhook 被拒、403 …),不改配置重试多少次结果都一样。409(另一个实例在轮询)和
// 429(限流)是暂时的,不算。
func isPermanent(err error) bool {
	var ae *apiError
	if !errors.As(err, &ae) {
		return false
	}
	return ae.Code >= 400 && ae.Code < 500 && ae.Code != http.StatusConflict && ae.Code != http.StatusTooManyRequests
}

// maxBody 是每个方法响应体的上限。getUpdates 一批可能带很多 update,放宽;其它方法的响应都很小。
func maxBody(method string) int64 {
	if method == "getUpdates" {
		return 16 << 20
	}
	return 1 << 20
}

// call 调一个 Bot API 方法。
//
// bot token 就在请求 URL 里(/bot<token>/method),而 Go 的 *url.Error 会把整个 URL 写进
// 错误文本 —— 一次网络抖动就能把 token 带进日志和设置页的状态。所以从这里出去的每一个
// 错误都先把 token 换掉。
func (c *Channel) call(ctx context.Context, hc *http.Client, method string, params, out any) error {
	if err := c.validate(c.apiBase); err != nil {
		return c.scrub(err)
	}
	body, err := json.Marshal(params)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiBase+"/bot"+c.token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return c.scrub(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return c.scrub(err)
	}
	defer resp.Body.Close()
	limit := maxBody(method)
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if int64(len(raw)) > limit {
		return fmt.Errorf("telegram: %s response exceeds %d bytes", method, limit)
	}
	var r apiResp
	if err := json.Unmarshal(raw, &r); err != nil {
		return fmt.Errorf("telegram %s: HTTP %d,响应无法解析", method, resp.StatusCode)
	}
	if !r.OK {
		return &apiError{Method: method, Code: r.ErrorCode, Desc: c.scrub(errors.New(r.Description)).Error()}
	}
	if out != nil {
		if err := json.Unmarshal(r.Result, out); err != nil {
			return fmt.Errorf("telegram %s: 结果无法解析: %v", method, err)
		}
	}
	return nil
}

func (c *Channel) scrub(err error) error {
	if err == nil || c.token == "" {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), c.token, "<bot-token>"))
}

// clipRunes 截到 n 个字符(不是字节 —— 中文一刀切在半个字上,Telegram 会拒收)。
func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
