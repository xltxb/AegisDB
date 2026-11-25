package lark

// 飞书服务端接口:换令牌、发消息、改消息。
//
// 不用 SDK 的 im/v1 客户端:体积上省不下什么(长连接那个包已经把它带进来了),但它按 appId 在进程里缓存
// 令牌(换了密钥还用旧的)、会跟跳转、出错文本也不归这里管。用到的只有三个接口。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"aegisdb/internal/chat"
)

const (
	apiTimeout  = 10 * time.Second
	maxRespBody = 1 << 20
	maxErrRunes = 200
)

// goneCodes:更新消息时,这些错误码说明那条消息再也改不了了(官方文档「更新应用发送的消息卡片」)。
var goneCodes = map[int]bool{
	230011: true, // 消息已被撤回
	230031: true, // 发出超过 14 天
	230110: true, // 消息已被删除
}

// staleTokenCodes:令牌失效,换一个再试(与 SDK core 里的判断一致)。
var staleTokenCodes = map[int]bool{99991663: true, 99991664: true, 99991671: true}

// ---------------------------------------------------------------- 出站

// hostGuard 只放行一个主机。地址都是自己拼的,这里是兜底:连引导地址也由 SDK 经它发出。
type hostGuard struct {
	base string
	next http.RoundTripper
}

func (g hostGuard) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme+"://"+r.URL.Host != g.base {
		return nil, errors.New("outbound host blocked")
	}
	return g.next.RoundTrip(r)
}

// newHTTPClient 不走环境变量里的代理,与 webhook 的出站客户端一致:代理的报错会带出代理地址。
func newHTTPClient(base string) *http.Client {
	return &http.Client{
		Timeout: apiTimeout,
		// 不跟跳转:接口不会跳转,跟过去只会把令牌带去别处。3xx 在 do 里按错误处理。
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: hostGuard{base: base, next: &http.Transport{
			DialContext:         (&net.Dialer{Timeout: apiTimeout, KeepAlive: 30 * time.Second}).DialContext,
			ForceAttemptHTTP2:   true,
			TLSHandshakeTimeout: apiTimeout,
			MaxIdleConns:        4,
			IdleConnTimeout:     90 * time.Second,
		}},
	}
}

// ---------------------------------------------------------------- 去密钥

// scrubber 把密钥从要给人看的文字里去掉。平台的报错可能把请求原样念回来,所以去的是值,不是字段名。
type scrubber struct {
	mu    sync.Mutex
	fixed []string
	// 租户令牌与长连接的票据分开记,各自只留最近几个,最新的在末尾。不能放一起:令牌有效近两小时,其间
	// 连不上会换许多次票据,会把还在用的令牌挤出去。当前的令牌总是最后记下的那个,所以总在。
	tokens, tickets []string
}

const (
	maxTokens  = 4
	maxTickets = 32
)

var (
	urlQuery   = regexp.MustCompile(`((?:wss?|https?)://[^\s"'?]+)\?[^\s"']*`)
	bearerWord = regexp.MustCompile(`(?i)\bbearer\s+\S+`)
)

func (s *scrubber) token(secret string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens = keepLast(s.tokens, secret, maxTokens)
}

func (s *scrubber) ticket(secret string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tickets = keepLast(s.tickets, secret, maxTickets)
}

// keepLast 把 v 放到末尾;已有的挪过来,不重复占位。
func keepLast(list []string, v string, max int) []string {
	list = append(slices.DeleteFunc(list, func(x string) bool { return x == v }), v)
	return list[len(list)-min(len(list), max):]
}

// text 去掉密钥之后再截短:反过来,截断处会剩下半个密钥。
func (s *scrubber) text(msg string, extra ...string) string {
	msg = urlQuery.ReplaceAllString(msg, "$1?***")
	s.mu.Lock()
	secrets := slices.Concat(s.fixed, s.tokens, s.tickets, extra)
	s.mu.Unlock()
	for _, sec := range secrets {
		if sec == "" {
			continue
		}
		quoted, _ := json.Marshal(sec)
		for _, form := range []string{sec, strings.Trim(string(quoted), `"`), url.QueryEscape(sec), url.PathEscape(sec)} {
			msg = strings.ReplaceAll(msg, form, "***")
		}
	}
	msg = chat.OneLine(bearerWord.ReplaceAllString(msg, "***"))
	if r := []rune(msg); len(r) > maxErrRunes {
		msg = string(r[:maxErrRunes]) + "…"
	}
	return msg
}

// cleanError 是去过密钥的错误。原来的错误只用来认种类(errors.Is),不交出去:它的文本没有去过。
type cleanError struct {
	msg string
	err error
}

func (e *cleanError) Error() string        { return e.msg }
func (e *cleanError) Is(target error) bool { return errors.Is(e.err, target) }

func (s *scrubber) wrap(what string, err error, extra ...string) error {
	return &cleanError{msg: "lark: " + what + ": " + s.text(err.Error(), extra...), err: err}
}

// ---------------------------------------------------------------- 调用

// apiError 是飞书回的错误码。
type apiError struct {
	code int
	msg  string
}

func (e *apiError) Error() string { return fmt.Sprintf("code %d: %s", e.code, e.msg) }

// do 发一次请求。out 收整个应答。
func (t *Transport) do(ctx context.Context, method, path, token string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, t.base+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := t.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxRespBody))
	if err != nil {
		return err
	}
	var head struct {
		Code *int   `json:"code"`
		Msg  string `json:"msg"`
	}
	// 解不开的内容不进错误文本:不知道里面是什么。
	if json.Unmarshal(raw, &head) != nil || head.Code == nil {
		return fmt.Errorf("unexpected response (HTTP %d)", resp.StatusCode)
	}
	if *head.Code != 0 {
		return &apiError{code: *head.Code, msg: head.Msg}
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected response (HTTP %d)", resp.StatusCode)
	}
	if json.Unmarshal(raw, out) != nil {
		return errors.New("unexpected response")
	}
	return nil
}

// tenantToken 取租户令牌,提前 5 分钟换。tokenLock 是个容量为 1 的通道而不是锁:等的人要能被 ctx 叫走。
func (t *Transport) tenantToken(ctx context.Context) (string, error) {
	select {
	case t.tokenLock <- struct{}{}:
		defer func() { <-t.tokenLock }()
	case <-ctx.Done():
		return "", ctx.Err()
	}
	if t.token != "" && time.Now().Before(t.tokenUntil) {
		return t.token, nil
	}
	var out struct {
		Token  string `json:"tenant_access_token"`
		Expire int    `json:"expire"`
	}
	err := t.do(ctx, http.MethodPost, "/open-apis/auth/v3/tenant_access_token/internal", "",
		map[string]string{"app_id": t.appID, "app_secret": t.appSecret}, &out)
	if err != nil {
		return "", err
	}
	if out.Token == "" {
		return "", errors.New("no tenant_access_token in response")
	}
	t.scrub.token(out.Token)
	t.token, t.tokenUntil = out.Token, time.Now().Add(time.Duration(out.Expire)*time.Second-5*time.Minute)
	return t.token, nil
}

// dropToken 丢掉一个已经失效的令牌;别人已经换过新的就不动。
func (t *Transport) dropToken(token string) {
	t.tokenLock <- struct{}{}
	if t.token == token {
		t.token = ""
	}
	<-t.tokenLock
}

// call 带着令牌调一个接口;令牌失效就换一个再试一次。
func (t *Transport) call(ctx context.Context, method, path string, in, out any) error {
	for try := 0; ; try++ {
		token, err := t.tenantToken(ctx)
		if err != nil {
			return err
		}
		err = t.do(ctx, method, path, token, in, out)
		var ae *apiError
		if try == 0 && errors.As(err, &ae) && staleTokenCodes[ae.code] {
			t.dropToken(token)
			continue
		}
		return err
	}
}

type messageResp struct {
	Data struct {
		MessageID string `json:"message_id"`
		ChatID    string `json:"chat_id"`
	} `json:"data"`
}

func (t *Transport) post(ctx context.Context, idType, id, msgType, content string) (messageResp, error) {
	var out messageResp
	err := t.call(ctx, http.MethodPost, "/open-apis/im/v1/messages?receive_id_type="+idType,
		map[string]string{"receive_id": id, "msg_type": msgType, "content": content}, &out)
	return out, err
}

// Send 私聊发一张卡片。没有用 uuid 去重:重发的卡片换了令牌,本来就是另一张。
func (t *Transport) Send(ctx context.Context, accountID string, c chat.Card) (chat.Ref, error) {
	out, err := t.post(ctx, "open_id", accountID, "interactive", RenderCard(c))
	if err == nil && out.Data.MessageID == "" {
		err = errors.New("no message_id in response")
	}
	if err != nil {
		return chat.Ref{}, t.scrub.wrap("send", err, c.Token)
	}
	return chat.Ref{ChatID: out.Data.ChatID, MessageID: out.Data.MessageID}, nil
}

var messageIDShape = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// Update 就地更新一张卡片。
func (t *Transport) Update(ctx context.Context, r chat.Ref, c chat.Card) error {
	if !messageIDShape.MatchString(r.MessageID) {
		return fmt.Errorf("lark: update: bad message id: %w", chat.ErrGone)
	}
	var out struct{}
	err := t.call(ctx, http.MethodPatch, "/open-apis/im/v1/messages/"+r.MessageID, map[string]string{"content": RenderCard(c)}, &out)
	if err == nil {
		return nil
	}
	clean := t.scrub.wrap("update", err, c.Token)
	var ae *apiError
	if errors.As(err, &ae) && goneCodes[ae.code] {
		return fmt.Errorf("%w (%w)", clean, chat.ErrGone)
	}
	return clean
}

// reply 回一句话。
func (t *Transport) reply(ctx context.Context, chatID, text string) error {
	if _, err := t.post(ctx, "chat_id", chatID, "interactive", renderReply(text)); err != nil {
		return t.scrub.wrap("reply", err)
	}
	return nil
}
