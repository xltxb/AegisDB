package telegram

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"velagateway/internal/imchannel"
)

// callbackRe 是卡片按钮的 callback_data。ApNo 只认字母数字和连字符 —— 按钮回调可以被
// 伪造,别让任意字符串流进后面的查询和提示语。
var callbackRe = regexp.MustCompile(`^ap:([A-Za-z0-9-]{1,32}):([ar])$`)

// pollLimit 限定每批 getUpdates 的条数。call 只读 1 MiB 响应体,而任何人都能私信 bot 发超长
// 文本(JSON 里非 ASCII 会被转义成 \uXXXX,单条可达几十 KB);默认 100 条一批会超限,
// 解析失败后退避重试同一个 offset,轮询就卡死到那些 update 过期,审批点击也进不来。
const pollLimit = 10

const startReply = "你好,我是数据库网关的审批机器人。\n" +
	"请在网关控制台右上角头像菜单「IM 账号绑定」里生成绑定码,再把绑定码发给我。\n" +
	"绑定之后,待你审批的工单会以卡片的形式私信给你。"

// Run 阻塞到 ctx 取消。它只在两种情况下提前返回错误:Telegram 回了永久性的 4xx(token 被拒、格式错、
// setWebhook 被拒 —— 改配置之前重试多少次都一样),或 webhook 模式缺配置。其它错误一律退避重试。
func (c *Channel) Run(ctx context.Context, sink imchannel.ActionSink) error {
	var me User
	if err := c.retry(ctx, func() error { return c.call(ctx, c.client, "getMe", map[string]any{}, &me) }); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return nil
	}
	c.bot.Store(me.Username)
	if c.mode == modeWebhook {
		return c.runWebhook(ctx)
	}
	return c.runPolling(ctx, sink)
}

func (c *Channel) runWebhook(ctx context.Context) error {
	if c.hookURL == "" || c.secret == "" {
		return errors.New("webhook 模式需要配置回调根地址(im.callbackBaseURL)与 webhook secret")
	}
	err := c.retry(ctx, func() error {
		return c.call(ctx, c.client, "setWebhook", map[string]any{
			"url": c.hookURL, "secret_token": c.secret, "allowed_updates": allowedUpdates,
		}, nil)
	})
	if err != nil {
		return err
	}
	slog.Info("telegram: webhook registered", "url", c.hookURL)
	<-ctx.Done()
	return nil
}

func (c *Channel) runPolling(ctx context.Context, sink imchannel.ActionSink) error {
	// 切回 polling 时 Telegram 上可能还登记着上一次的 webhook,不删掉它 getUpdates 会一直 409。
	err := c.retry(ctx, func() error {
		return c.call(ctx, c.client, "deleteWebhook", map[string]any{"drop_pending_updates": false}, nil)
	})
	if err != nil || ctx.Err() != nil {
		return err
	}
	slog.Info("telegram: long polling started — only ONE gateway instance may poll a bot; use webhook mode when running several")
	var offset int64
	backoff := time.Second
	for ctx.Err() == nil {
		var ups []Update
		err := c.call(ctx, c.poll, "getUpdates", map[string]any{
			"offset": offset, "limit": pollLimit, "timeout": pollTimeout, "allowed_updates": allowedUpdates,
		}, &ups)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if isPermanent(err) {
				return err
			}
			// 409 = 另一个实例在 getUpdates,或 webhook 又被人登记上了。退避,不要互相踢。
			slog.Warn("telegram: getUpdates failed", "err", err, "retryIn", backoff)
			if !sleep(ctx, backoff) {
				return nil
			}
			backoff = min(backoff*2, 30*time.Second)
			continue
		}
		backoff = time.Second
		for _, u := range ups {
			// ctx 取消(Reload)后不再处理半批:没确认的 update 会被重投,处理一半只会重复消费(比如绑定码)。
			if ctx.Err() != nil {
				return nil
			}
			// 先推进 offset 再处理:下一次 getUpdates 带着它就是在确认。处理到一半进程挂了,
			// 这条会被重投 —— 重投由 OnAction 的幂等(ErrAlreadyDecided)兜住。
			if u.UpdateID >= offset {
				offset = u.UpdateID + 1
			}
			c.dispatch(ctx, u, sink)
		}
	}
	return nil
}

// retry 反复调 fn 直到成功、遇到永久性错误(4xx,409/429 除外)或 ctx 结束(后者返回 nil)。
func (c *Channel) retry(ctx context.Context, fn func() error) error {
	backoff := time.Second
	for {
		err := fn()
		if err == nil || isPermanent(err) {
			return err
		}
		if ctx.Err() != nil {
			return nil
		}
		slog.Warn("telegram: call failed, retrying", "err", err, "retryIn", backoff)
		if !sleep(ctx, backoff) {
			return nil
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// HandleWebhook 实现 imchannel.Webhooker。只有 webhook 模式、且配了 secret 时才收;
// secret 用常量时间比较。鉴权通过后一律 200 —— Telegram 会重投非 2xx 的请求。
func (c *Channel) HandleWebhook(ctx context.Context, header http.Header, body []byte, sink imchannel.ActionSink) int {
	if c.mode != modeWebhook || c.secret == "" {
		return http.StatusForbidden
	}
	got := header.Get("X-Telegram-Bot-Api-Secret-Token")
	if subtle.ConstantTimeCompare([]byte(got), []byte(c.secret)) != 1 {
		return http.StatusForbidden
	}
	var u Update
	if err := json.Unmarshal(body, &u); err != nil {
		slog.Warn("telegram: webhook body unparsable", "err", err)
		return http.StatusOK
	}
	c.dispatch(ctx, u, sink)
	return http.StatusOK
}

func (c *Channel) dispatch(ctx context.Context, u Update, sink imchannel.ActionSink) {
	switch {
	case u.CallbackQuery != nil:
		c.onCallback(ctx, u.CallbackQuery, sink)
	case u.Message != nil:
		c.onMessage(ctx, u.Message, sink)
	}
}

func (c *Channel) onCallback(ctx context.Context, q *CallbackQuery, sink imchannel.ActionSink) {
	var toast string
	switch m := callbackRe.FindStringSubmatch(q.Data); {
	case q.Data == DoneData:
		toast = "该工单已处理"
	case m == nil:
		toast = "无法识别的操作"
	default:
		// 认人只凭 from.id —— Telegram 保证它是真正的点击者。对应哪个网关账户、能不能批,
		// 都是 sink 那一侧的事。
		toast = sink.OnAction(ctx, imchannel.Action{
			PlatformUserID: strconv.FormatInt(q.From.ID, 10), ApNo: m[1], Approve: m[2] == "a",
		}).Toast
	}
	// 必须应答。不应答,按钮会在对方的客户端上一直转圈,而他刚刚可能批准了一条 DROP。
	// 决定已经记下了,应答不能因为 ctx 刚好被取消(Reload)而丢掉。
	ackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := c.call(ackCtx, c.client, "answerCallbackQuery", map[string]any{
		"callback_query_id": q.ID, "text": clipRunes(toast, 190),
	}, nil); err != nil {
		slog.Warn("telegram: answerCallbackQuery failed", "err", err)
	}
}

func (c *Channel) onMessage(ctx context.Context, m *Message, sink imchannel.ActionSink) {
	// 只理私聊。群里的任何文字都不触发任何逻辑 —— 绑定码发在群里就是泄露了,更不能认。
	if m.Chat.Type != "private" || m.From == nil {
		return
	}
	text := strings.TrimSpace(m.Text)
	if text == "" {
		return
	}
	reply := startReply
	if !strings.HasPrefix(text, "/start") {
		reply = sink.OnDirectMessage(ctx, imchannel.DM{PlatformUserID: strconv.FormatInt(m.From.ID, 10), Text: text})
	}
	if reply == "" {
		return
	}
	ackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := c.call(ackCtx, c.client, "sendMessage", map[string]any{"chat_id": m.Chat.ID, "text": reply}, nil); err != nil {
		slog.Warn("telegram: reply failed", "err", err)
	}
}
