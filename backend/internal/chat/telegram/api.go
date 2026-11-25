package telegram

// Bot API 的调用:出站、去机密、限流、错误的归类。
//
// 令牌在地址里,而 net/http 的每个错误都带着地址,所以错误一律先拆出来、去过机密再往外走。

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"aegisdb/internal/chat"
)

const (
	defaultBase = "https://api.telegram.org"

	callWithin  = 10 * time.Second
	pollSeconds = 50               // 一次长轮询挂多久
	pollWithin  = 65 * time.Second // 客户端等它的时限,要比上面长

	maxCallBody = 1 << 20
	maxPollBody = 4 << 20
	maxErrRunes = 200

	shortWait = 3 * time.Second // 平台要求等的在这以内就等一下再试,再久的交给上层
	maxWait   = 5 * time.Minute // 被限流时,一次最多等这么久
)

var (
	errBlocked  = errors.New("outbound host blocked")
	errTooLarge = errors.New("response too large")
	errBadToken = errors.New("telegram: malformed bot token")
	errBadProxy = errors.New("telegram: malformed proxy URL")
)

// ---------------------------------------------------------------- 配置

// 真的是「数字:35 个字符」。放宽一些,但必须能原样拼进地址。
var tokenShape = regexp.MustCompile(`\A[0-9]{3,20}:[A-Za-z0-9_-]{20,128}\z`)

// ValidToken 报告一个串像不像机器人令牌。
func ValidToken(token string) bool { return tokenShape.MatchString(token) }

// ParseProxy 认一个代理地址:http / https / socks5,可以带口令;空串是没有代理(nil)。错误里不带地址。
//
// 不收 socks5h:Go 的 socks5 本来就把域名交给代理去解析,两者一样,少一种写法少一处误会。
func ParseProxy(raw string) (*url.URL, error) {
	if raw == "" {
		return nil, nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || u.Hostname() == "" || u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" ||
		strings.ContainsFunc(u.Host, func(r rune) bool { return r <= ' ' || r > '~' }) {
		return nil, errBadProxy
	}
	if _, ok := proxyPorts[u.Scheme]; !ok {
		return nil, errBadProxy
	}
	if p := u.Port(); p != "" {
		if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
			return nil, errBadProxy
		}
	}
	u.Path, u.RawPath = "", ""
	return u, nil
}

var proxyPorts = map[string]string{"http": "80", "https": "443", "socks5": "1080"}

func dialAddr(u *url.URL, port string) string {
	if p := u.Port(); p != "" {
		port = p
	}
	return net.JoinHostPort(u.Hostname(), port)
}

// ---------------------------------------------------------------- 出站

// hostGuard 只放行一个主机;地址都是自己拼的,这里是兜底。
type hostGuard struct {
	base string
	next http.RoundTripper
}

func (g hostGuard) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme+"://"+r.URL.Host != g.base {
		return nil, errBlocked
	}
	return g.next.RoundTrip(r)
}

// newHTTPClient 的请求只去 api.telegram.org;拨号只拨它,配了代理则只拨代理(代理可以在内网,所以不走
// 防内网的拦截)。环境变量里的代理一概不看。
//
// 不开 HTTP/2:长轮询与别的调用会挤在一条连接上,那条连接悄悄断了,所有调用都要等到超时;HTTP/1.1 的
// 一次超时只废掉它自己那条。
func (t *Transport) newHTTPClient() *http.Client {
	return &http.Client{
		// 不跟跳转:接口不会跳转,跟过去只会把令牌带去别处。3xx 在 do 里按错误处理。时限由每次调用的 ctx 管。
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: hostGuard{base: t.base, next: &http.Transport{
			Proxy:       func(*http.Request) (*url.URL, error) { return t.proxy, nil },
			DialContext: t.dial,
			OnProxyConnectResponse: func(_ context.Context, _ *url.URL, _ *http.Request, resp *http.Response) error {
				if resp.StatusCode != http.StatusOK {
					return proxyStatus(resp.StatusCode)
				}
				return nil
			},
			TLSHandshakeTimeout: callWithin,
			MaxIdleConns:        4,
			IdleConnTimeout:     90 * time.Second,
		}},
	}
}

func (t *Transport) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	if addr != t.dialTo {
		return nil, errBlocked
	}
	return (&net.Dialer{Timeout: callWithin, KeepAlive: 30 * time.Second}).DialContext(ctx, network, addr)
}

type proxyStatus int

func (p proxyStatus) Error() string { return fmt.Sprintf("HTTP %d", int(p)) }

// netError 处理没拿到应答的错误。url.Error 里有整个地址,只取里面那层。
//
// 配了代理时原话一句都不用:里面会有代理的主机、解析出来的内网地址、代理自己写的话。只说是哪一种。
func (t *Transport) netError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	if t.proxy == nil {
		return err
	}
	var (
		kind   = "failed"
		ne     net.Error
		dns    *net.DNSError
		status proxyStatus
		cert   *tls.CertificateVerificationError
		host   x509.HostnameError
		record tls.RecordHeaderError
	)
	switch {
	case errors.Is(err, errBlocked):
		return err
	case errors.Is(err, context.Canceled):
		kind = "canceled"
	case errors.As(err, &status):
		kind = "the proxy answered " + status.Error()
	case errors.As(err, &dns):
		kind = "proxy host lookup failed"
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		kind = "timed out"
	case errors.Is(err, syscall.ECONNREFUSED):
		kind = "connection refused"
	case errors.Is(err, syscall.EHOSTUNREACH), errors.Is(err, syscall.ENETUNREACH):
		kind = "unreachable"
	case errors.Is(err, syscall.ECONNRESET), errors.Is(err, syscall.EPIPE), errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		kind = "connection closed"
	case errors.As(err, &cert), errors.As(err, &host), errors.As(err, &record):
		kind = "TLS error"
	}
	return &cleanError{msg: "via proxy: " + kind, err: err}
}

// ---------------------------------------------------------------- 去机密

var (
	botURL    = regexp.MustCompile(`\S*/bot\S*`)
	tokenWord = regexp.MustCompile(`[0-9]{3,20}:[A-Za-z0-9_-]{20,}`)
)

// scrub 把令牌与代理地址从要给人看的文字里去掉,再截短:反过来,截断处会剩下半个令牌。平台的报错可能把
// 请求原样念回来,所以去的是值,不是字段名。
func (t *Transport) scrub(msg string, extra ...string) string {
	msg = botURL.ReplaceAllString(msg, "***")
	for _, sec := range slices.Concat(t.secrets, extra) {
		if sec != "" {
			msg = strings.ReplaceAll(msg, sec, "***")
		}
	}
	msg = chat.OneLine(tokenWord.ReplaceAllString(msg, "***"))
	if r := []rune(msg); len(r) > maxErrRunes {
		msg = string(r[:maxErrRunes]) + "…"
	}
	return msg
}

// secretsOf 是要去掉的值,长的在前:先去短的,会剩下长的其余部分。
func secretsOf(token string, proxy *url.URL, rawProxy string) []string {
	_, secret, _ := strings.Cut(token, ":")
	out := []string{token, secret}
	if proxy != nil {
		out = append(out, rawProxy, proxy.String(), proxy.Host, proxy.Hostname())
		if proxy.User != nil {
			pass, _ := proxy.User.Password()
			out = append(out, proxy.User.String(), proxy.User.Username(), pass, url.QueryEscape(pass), url.PathEscape(pass))
		}
	}
	// 太短的不去:会把整句话都打上星号,而有代理时本来就不说网络错误的原话(见 netError)。
	out = slices.DeleteFunc(out, func(s string) bool { return len(s) < 4 })
	slices.SortFunc(out, func(a, b string) int { return len(b) - len(a) })
	return slices.Compact(out)
}

// cleanError 是去过机密的错误。原来的错误只用来认种类,不交出去:它的文本没有去过。
type cleanError struct {
	msg string
	err error
}

func (e *cleanError) Error() string             { return e.msg }
func (e *cleanError) Is(target error) bool      { return errors.Is(e.err, target) }
func (e *cleanError) RetryAfter() time.Duration { return chat.RetryAfter(e.err) }

func (t *Transport) wrap(what string, err error, extra ...string) error {
	return &cleanError{msg: "telegram: " + what + ": " + t.scrub(err.Error(), extra...), err: err}
}

// ---------------------------------------------------------------- 调用

// apiError 是平台回的错误。
type apiError struct {
	code  int
	desc  string
	after time.Duration
}

func (e *apiError) Error() string             { return fmt.Sprintf("code %d: %s", e.code, e.desc) }
func (e *apiError) RetryAfter() time.Duration { return e.after }

// Is:令牌被撤销是 401;令牌不存在时平台连地址都不认,回 404。
func (e *apiError) Is(target error) bool {
	return target == chat.ErrCredentials && (e.code == http.StatusUnauthorized || e.code == http.StatusNotFound)
}

// says 报告平台的话里有没有这几句之一。
func (e *apiError) says(words ...string) bool {
	desc := strings.ToLower(e.desc)
	return slices.ContainsFunc(words, func(w string) bool { return strings.Contains(desc, w) })
}

func asAPI(err error) *apiError {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae
	}
	return &apiError{}
}

// do 发一次请求。out 收 result。
func (t *Transport) do(ctx context.Context, method string, in, out any, limit int64) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.base+"/bot"+t.token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return errors.New("bad request") // 原话里有地址
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.http.Do(req)
	if err != nil {
		return t.netError(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return t.netError(err)
	}
	if int64(len(raw)) > limit {
		return errTooLarge
	}
	var env struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		Code        int             `json:"error_code"`
		Description string          `json:"description"`
		Parameters  struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	// 解不开的内容不进错误文本:不知道里面是什么。
	if json.Unmarshal(raw, &env) != nil || env.OK != (resp.StatusCode == http.StatusOK) || !env.OK && env.Code == 0 {
		return fmt.Errorf("unexpected response (HTTP %d)", resp.StatusCode)
	}
	if !env.OK {
		return &apiError{code: env.Code, desc: env.Description, after: time.Duration(max(env.Parameters.RetryAfter, 0)) * time.Second}
	}
	if json.Unmarshal(env.Result, out) != nil {
		return errors.New("unexpected response")
	}
	return nil
}

// call 调一个接口,有时限。平台嫌太频繁、而等得起的,等一下再试一次。
func (t *Transport) call(ctx context.Context, method string, in, out any) error {
	ctx, cancel := context.WithTimeout(ctx, callWithin)
	defer cancel()
	err := t.do(ctx, method, in, out, maxCallBody)
	wait := chat.RetryAfter(err)
	if deadline, _ := ctx.Deadline(); wait <= 0 || wait > shortWait || time.Until(deadline) <= wait {
		return err
	}
	if !sleep(ctx, wait) {
		return err
	}
	return t.do(ctx, method, in, out, maxCallBody)
}

// sleep 等一会儿;ctx 先结束则返回假。
func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// pollWait 是被限流时等多久。
func pollWait(asked time.Duration) time.Duration { return min(max(asked, time.Second), maxWait) }

// ---------------------------------------------------------------- 发与改

// noPreview:不去抓消息里的链接,也不在消息下面展开它。
var noPreview = map[string]any{"is_disabled": true}

func cardBody(chatID int64, c chat.Card) map[string]any {
	m := render(c)
	return map[string]any{
		"chat_id": chatID, "text": m.text, "parse_mode": "HTML", "link_preview_options": noPreview,
		"reply_markup": map[string]any{"inline_keyboard": m.keyboard},
	}
}

// post 发或改一张卡片。「查看详情」的地址平台不认(内网主机名这类)时去掉它再来一次:不能因为一个按钮
// 整张发不出去。
func (t *Transport) post(ctx context.Context, method string, chatID int64, c chat.Card, more map[string]any, out any) error {
	body := func() map[string]any {
		b := cardBody(chatID, c)
		for k, v := range more {
			b[k] = v
		}
		return b
	}
	err := t.call(ctx, method, body(), out)
	if c.DetailURL != "" && asAPI(err).says("button_url_invalid", "button url") {
		c.DetailURL = ""
		err = t.call(ctx, method, body(), out)
	}
	return err
}

// Send 私聊发一张卡片,不许转发与保存。私聊的会话标识就是那个人的标识。
func (t *Transport) Send(ctx context.Context, accountID string, c chat.Card) (chat.Ref, error) {
	id, ok := parseID(accountID)
	if !ok {
		return chat.Ref{}, errors.New("telegram: send: bad account id")
	}
	var out struct {
		MessageID int64 `json:"message_id"`
		Chat      struct {
			ID int64 `json:"id"`
		} `json:"chat"`
	}
	err := t.post(ctx, "sendMessage", id, c, map[string]any{"protect_content": true}, &out)
	if err == nil && (out.MessageID <= 0 || out.Chat.ID != id) {
		err = errors.New("unexpected response")
	}
	if err != nil {
		return chat.Ref{}, t.wrap("send", err, c.Token)
	}
	return chat.Ref{ChatID: formatID(out.Chat.ID), MessageID: formatID(out.MessageID)}, nil
}

// Update 就地更新一张卡片。
func (t *Transport) Update(ctx context.Context, r chat.Ref, c chat.Card) error {
	chatID, ok1 := parseID(r.ChatID)
	messageID, ok2 := parseID(r.MessageID)
	if !ok1 || !ok2 {
		return fmt.Errorf("telegram: update: bad message id: %w", chat.ErrGone)
	}
	return t.edit(ctx, chatID, messageID, c)
}

func (t *Transport) edit(ctx context.Context, chatID, messageID int64, c chat.Card) error {
	var out json.RawMessage
	err := t.post(ctx, "editMessageText", chatID, c, map[string]any{"message_id": messageID}, &out)
	ae := asAPI(err)
	if err == nil || ae.says("message is not modified") { // 后一种:已经就是这个样子
		return nil
	}
	clean := t.wrap("update", err, c.Token)
	if ae.says("message to edit not found", "message can't be edited", "chat not found", "bot was blocked by the user", "user is deactivated") {
		return fmt.Errorf("%w (%w)", clean, chat.ErrGone)
	}
	return clean
}

// reply 回一句话,只试一次。话里有别人起的名字:不带 parse_mode,里面什么标记都不解析;整句标成 code,平台也
// 不去认里面的链接、@ 某人和 /命令。
func (t *Transport) reply(ctx context.Context, chatID int64, text string) error {
	text, _ = clipUTF16(chat.Visible(text), maxUnits)
	ctx, cancel := context.WithTimeout(ctx, callWithin)
	defer cancel()
	var out json.RawMessage
	if err := t.do(ctx, "sendMessage", map[string]any{"chat_id": chatID, "text": text, "link_preview_options": noPreview,
		"entities": []any{map[string]any{"type": "code", "offset": 0, "length": unitsOf(text)}}}, &out, maxCallBody); err != nil {
		return t.wrap("reply", err)
	}
	return nil
}

// answer 应答一次点击;不应答,按钮上会一直转圈。
func (t *Transport) answer(ctx context.Context, queryID, text string, alert bool, token string) error {
	text, _ = clipUTF16(chat.Visible(text), maxToast)
	var out json.RawMessage
	if err := t.call(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": queryID, "text": text, "show_alert": alert}, &out); err != nil {
		return t.wrap("answer", err, token)
	}
	return nil
}

func formatID(id int64) string { return strconv.FormatInt(id, 10) }

// parseID 认一个正的十进制标识,只认规范写法。
func parseID(s string) (int64, bool) {
	id, err := strconv.ParseInt(s, 10, 64)
	return id, err == nil && id > 0 && formatID(id) == s
}
