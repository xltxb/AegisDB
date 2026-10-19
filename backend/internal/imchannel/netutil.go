package imchannel

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"
)

// maxBackoff 是渠道重试的退避上限。
const maxBackoff = 30 * time.Second

// Retry 反复调 fn,直到成功、遇到 permanent 认定的错误,或 ctx 结束(后者返回 nil —— 那是有人
// 叫它停,不是失败)。退避从 1 秒翻倍到 30 秒。name 是渠道名,只进日志。
//
// 永久性错误(凭据被拒、地址填错)必须让调用方停下来,把原因交给设置页:无限重试只会留下一串
// WARN,而设置页上是一个开关开着、状态「运行中」、什么也发不出去的渠道。
func Retry(ctx context.Context, name string, permanent func(error) bool, fn func() error) error {
	backoff := time.Second
	for {
		err := fn()
		if err == nil || permanent(err) {
			return err
		}
		if ctx.Err() != nil {
			return nil
		}
		slog.Warn(name+": call failed, retrying", "err", err, "retryIn", backoff)
		if !Sleep(ctx, backoff) {
			return nil
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

// Sleep 睡 d;ctx 先结束就提前返回 false。
func Sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// NoRedirects 复制一份出站客户端(Transport 即受控拨号器照旧共用),遇到 3xx 不跟随、把它原样
// 交回来。网关的出站客户端本来就拒绝跟随,但它报的是一个普通错误,与网络错误分不开,只能当暂时
// 的无限重试;拿到 3xx 响应本身,渠道才能把它判成「开放平台地址填错」。
func NoRedirects(c *http.Client) *http.Client {
	if c == nil {
		return nil
	}
	cp := *c
	cp.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &cp
}

// GuardedDial 取出网关出站客户端里带 SSRF 拦截的拨号器,给长连接的 WebSocket 用。拿不到就拒绝,
// 不退回默认拨号器 —— 那样长连接就绕开了内网拦截。
func GuardedDial(c *http.Client) (func(ctx context.Context, network, addr string) (net.Conn, error), error) {
	if c != nil {
		if t, ok := c.Transport.(*http.Transport); ok && t.DialContext != nil {
			return t.DialContext, nil
		}
	}
	return nil, errors.New("出站客户端没有受控的拨号器,拒绝建立长连接")
}

// Recent 记住最近处理过的消息 ID。平台没及时收到确认会重推同一条消息;绑定码第二次到达时已被
// 消费,不去重的话用户会在「已绑定」之后又收到一句「无效或已过期」。零值可用。
type Recent struct {
	mu    sync.Mutex
	ids   map[string]struct{}
	order []string
}

const recentMax = 512

// First 报告 id 是不是第一次出现。空 id 不去重。
func (r *Recent) First(id string) bool {
	if id == "" {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ids == nil {
		r.ids = map[string]struct{}{}
	}
	if _, dup := r.ids[id]; dup {
		return false
	}
	r.ids[id] = struct{}{}
	r.order = append(r.order, id)
	if len(r.order) > recentMax {
		delete(r.ids, r.order[0])
		r.order = r.order[1:]
	}
	return true
}
