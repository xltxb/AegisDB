// Package telegram 是 Telegram 渠道:长轮询 getUpdates 收绑定码与卡片点击,sendMessage / editMessageText
// 发卡片、改卡片。只用标准库。不需要公网入口;支持经代理出网。
//
// 只认私聊:会话类型是 private、会话标识就是发的人自己、且不是机器人。点的人是不是那张卡片的收件人,由
// service 再核对一遍。机器人的标识(BotID)要问过 getMe 才知道,在那之前是空的。
//
// update 的处理:
//   - 同一个人的按先后一件件来(点「通过」再点「返回」,卡片不能停在前一个上),不同的人互不等待;
//   - 处理完才推进 offset。有一条还没处理完,offset 就停在它那里:平台会把它和它之后的再给一遍,认得出来
//     的不再处理,新来的照常处理,所以一条慢的不会堵住别的;
//   - 停着有两个例外:收下(不是开始处理)已经 holdFor 还没完的;以及平台一次给满了 —— 后面可能还有,停着就
//     看不到。不再为它停着之后它照样做完,只有进程在它做完之前崩掉才会丢,而点击可以再点一次;
//   - 文字谁都能发,所以文字占的位子有上限,多出来的丢掉;回话不在处理的路上发,发得慢、发不出都不拖着谁;
//   - 重放靠 update_id 认出来,所以输错的绑定码不会多算一次。记号按机器人由上层保管(WithSeen),换连接也认得;
//     进程重启之后认不出来,靠幂等兜住。
package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"aegisdb/internal/chat"
)

const (
	// 平台对点击的应答有时限(没写明是多久),与 Lark 一样,来不及就先答「正在处理」。
	answerWithin = 2500 * time.Millisecond
	answerCall   = 4 * time.Second // 应答那一次调用最多花这么久,给换卡片留时间
	// Run 因 ctx 取消而返回之前,等还在处理的这么久;再久的让它自己做完。
	drainWithin = 3 * time.Second

	settleFor   = time.Second // 还有没处理完的,最多等这么久就再看一眼有没有新的
	holdFor     = 30 * time.Second
	maxUpdates  = 100 // 一次最多要多少条,也是平台的上限
	maxInflight = 200 // 没处理完的积到这么多就先不收新的
	maxTexts    = 100 // 其中文字最多占这么多,余下的留给点击
	laneMax     = 32  // 一个人排队的上限,多出来的丢掉

	// 回话:同时最多这么几句,每句发完还占着位子一会儿。发消息的额度是整个机器人的(约每秒 30 条),要留给卡片。
	replySlots = 8
	replyGap   = 500 * time.Millisecond

	seenMax = 4096
	seenFor = 24 * time.Hour // 平台替人存 update 的期限
)

var _ chat.Transport = (*Transport)(nil)

// Transport 是一个 Telegram 机器人。一份配置一个;同一时刻只能有一个 Run。
type Transport struct {
	token    string
	base     string   // scheme://host
	proxy    *url.URL // nil = 直连
	dialTo   string   // 只许拨这个地址
	secrets  []string
	lang     func() string
	http     *http.Client
	identity atomic.Pointer[identity]
	seen     *chat.Seen

	answerWithin, cardWithin, settle, holdFor time.Duration

	limit int // 下一次要多少条;只有 poll 用

	mu       sync.Mutex
	next     int64               // 收到过的最后一条之后
	passed   int64               // 向平台用过的最大 offset:比它小的不会再给了
	full     bool                // 上一批给满了
	inflight map[int64]time.Time // 没处理完的 update,从什么时候收下的
	texts    int                 // 其中的文字
	lanes    map[int64][]job     // 每个人排着的;有键就有一个协程在做
	changed  chan struct{}       // 有一条处理完了

	replies chan struct{} // 回话的位子
	quiet   atomic.Int64  // 平台嫌太频繁:这之前(UnixNano)不回话
}

type identity struct{ id, name string }

type job struct {
	id   int64 // update_id
	text bool
	run  *running
	do   func()
}

// running 是一次 Run:事件交给谁、用哪个 ctx。
type running struct {
	ctx context.Context
	h   chat.Handler
	wg  sync.WaitGroup
}

// Option 调整一个 Transport。
type Option func(*Transport)

// WithBaseURL 换掉平台地址,只给测试用。
func WithBaseURL(base string) Option {
	return func(t *Transport) { t.base = strings.TrimRight(base, "/") }
}

// WithLang 给出卡片语言(chat.cardLang),用在渠道自己要说的那几句上;不给就是 zh。它不许阻塞。
func WithLang(lang func() string) Option {
	return func(t *Transport) { t.lang = lang }
}

// NewSeen 是记 update_id 的本子,一个机器人一本。
func NewSeen() *chat.Seen { return chat.NewSeen(seenMax, seenFor) }

// WithSeen 用一本已有的记号:同一个机器人换了连接(存一次设置就会),处理过的还认得。
func WithSeen(s *chat.Seen) Option {
	return func(t *Transport) { t.seen = s }
}

// New 建一个渠道。proxyURL 为空就是直连;它可能带口令,与令牌一样当机密。
func New(token, proxyURL string, opts ...Option) (*Transport, error) {
	if !ValidToken(token) {
		return nil, errBadToken
	}
	proxy, err := ParseProxy(proxyURL)
	if err != nil {
		return nil, err
	}
	t := &Transport{
		token: token, base: defaultBase, proxy: proxy,
		secrets:      secretsOf(token, proxy, proxyURL),
		lang:         func() string { return "" },
		seen:         NewSeen(),
		answerWithin: answerWithin, cardWithin: chat.CardWithin, settle: settleFor, holdFor: holdFor,
		limit:    maxUpdates,
		inflight: map[int64]time.Time{},
		lanes:    map[int64][]job{},
		changed:  make(chan struct{}, 1),
		replies:  make(chan struct{}, replySlots),
	}
	for _, o := range opts {
		o(t)
	}
	to := proxy
	if to == nil {
		if to, err = url.Parse(t.base); err != nil {
			return nil, errors.New("telegram: malformed base URL")
		}
	}
	t.dialTo = dialAddr(to, proxyPorts[to.Scheme])
	t.http = t.newHTTPClient()
	return t, nil
}

// BotID 是 getMe 的 id;还没问到,或令牌不能用、挂着 webhook,为空。
func (t *Transport) BotID() string {
	if me := t.identity.Load(); me != nil {
		return me.id
	}
	return ""
}

// BotName 是机器人的用户名(t.me/<它>);还没问到为空。
func (t *Transport) BotName() string {
	if me := t.identity.Load(); me != nil {
		return me.name
	}
	return ""
}

// ---------------------------------------------------------------- 连接

// Run 认出机器人、收 update,断开就返回,不自己重连。ctx 取消时返回 ctx 的错误。同一个机器人在别处也在收
// 时先报 StateConflict 再返回;其余失败只在返回的错误里,由调用方报 StateError。每轮收成了报一次
// StateConnected。
func (t *Transport) Run(ctx context.Context, h chat.Handler, onState func(chat.State, string)) error {
	onState(chat.StateConnecting, "")
	run := &running{ctx: ctx, h: h}
	err := t.hello(ctx)
	for err != nil && chat.RetryAfter(err) > 0 { // 嫌太频繁:等够了再来
		onState(chat.StateError, err.Error())
		if !sleep(ctx, pollWait(chat.RetryAfter(err))) {
			break
		}
		err = t.hello(ctx)
	}
	if err == nil {
		err = t.poll(run, onState)
	}
	if ctx.Err() != nil {
		t.unqueue()
		run.drain()
		return ctx.Err()
	}
	// 令牌不能用、点击会落到别人的 webhook 上:不算就绪。排着的卡片等着,不去白试、也不发出没人收的按钮。
	if errors.Is(err, chat.ErrCredentials) || errors.Is(err, chat.ErrWebhook) {
		t.identity.Store(nil)
	}
	return err
}

// hello 认出自己是谁,并确认没有挂着 webhook(挂着就收不到;替人删会弄断别人的服务)。
func (t *Transport) hello(ctx context.Context) error {
	var me struct {
		ID       int64  `json:"id"`
		Username string `json:"username"`
	}
	err := t.call(ctx, "getMe", map[string]any{}, &me)
	if err == nil && me.ID <= 0 {
		err = errors.New("unexpected response")
	}
	if err != nil {
		return t.wrap("getMe", err)
	}
	var hook struct {
		URL string `json:"url"`
	}
	if err := t.call(ctx, "getWebhookInfo", map[string]any{}, &hook); err != nil {
		return t.wrap("getWebhookInfo", err)
	}
	if hook.URL != "" {
		return fmt.Errorf("telegram: getWebhookInfo: %w", chat.ErrWebhook)
	}
	t.identity.Store(&identity{formatID(me.ID), me.Username})
	return nil
}

// poll 一直收,到出错为止。
func (t *Transport) poll(run *running, onState func(chat.State, string)) error {
	ctx := run.ctx
	quick := true // 第一次不挂着等:马上知道通不通
	for {
		offset, holding := t.offset()
		seconds := pollSeconds
		if quick || holding { // 停在一条没处理完的上,平台反正马上就答
			seconds = 0
		}
		ups, err := t.getUpdates(ctx, offset, seconds)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			if errors.Is(err, errTooLarge) && t.limit > 1 { // 少要几条
				t.limit /= 2
				continue
			}
			clean, ae := t.wrap("getUpdates", err), asAPI(err)
			switch {
			case chat.RetryAfter(err) > 0:
				onState(chat.StateError, clean.Error())
				if !sleep(ctx, pollWait(chat.RetryAfter(err))) {
					return ctx.Err()
				}
				quick = true
				continue
			case ae.code == http.StatusConflict && ae.says("webhook"):
				return fmt.Errorf("telegram: getUpdates: %w", chat.ErrWebhook)
			case ae.code == http.StatusConflict:
				onState(chat.StateConflict, clean.Error())
			}
			return clean
		}
		quick = false
		if !t.dispatch(run, ups, len(ups) >= t.limit) && len(ups) > 0 {
			return t.wrap("getUpdates", errors.New("unexpected response")) // 推进不了,再问还是这些
		}
		t.limit = min(t.limit*2, maxUpdates)
		onState(chat.StateConnected, "")
		t.rest(ctx)
	}
}

func (t *Transport) getUpdates(ctx context.Context, offset int64, seconds int) ([]json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, pollWithin-time.Duration(pollSeconds-seconds)*time.Second)
	defer cancel()
	in := map[string]any{"limit": t.limit, "timeout": seconds, "allowed_updates": []string{"message", "callback_query"}}
	if offset > 0 {
		in["offset"] = offset
		t.mu.Lock()
		t.passed = max(t.passed, offset)
		t.mu.Unlock()
	}
	var out []json.RawMessage
	err := t.do(ctx, "getUpdates", in, &out, maxPollBody)
	return out, err
}

// drain 等这次 Run 里还在处理的做完,有上限。
func (r *running) drain() {
	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(drainWithin):
	}
}

// ---------------------------------------------------------------- offset 与分派

// offset 是下一次从哪条收起,以及是不是停在一条没处理完的上(见包注释)。
func (t *Transport) offset() (next int64, holding bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	next = t.next
	if t.full {
		return next, false
	}
	for id, since := range t.inflight {
		if time.Since(since) < t.holdFor {
			holding = true
			next = min(next, id)
		}
	}
	return next, holding
}

// rest 等手上的处理完;等不完,过一会儿也回去看一眼有没有新的。积得太多就一直等。
func (t *Transport) rest(ctx context.Context) {
	timer := time.NewTimer(t.settle)
	defer timer.Stop()
	late := false
	for {
		_, holding := t.offset()
		t.mu.Lock()
		n := len(t.inflight)
		t.mu.Unlock()
		if n < maxInflight && (!holding || late) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-t.changed:
		case <-timer.C:
			late = true
			timer.Reset(t.settle)
		}
	}
}

// update 里只要这几样。
type update struct {
	ID       int64        `json:"update_id"`
	Message  *textMessage `json:"message"`
	Callback *struct {
		ID      string       `json:"id"`
		From    *sender      `json:"from"`
		Message *textMessage `json:"message"`
		Data    string       `json:"data"`
	} `json:"callback_query"`
}

type textMessage struct {
	ID   int64   `json:"message_id"`
	From *sender `json:"from"`
	Chat struct {
		ID   int64  `json:"id"`
		Type string `json:"type"`
	} `json:"chat"`
	SenderChat *struct{} `json:"sender_chat"` // 以频道、群的身份发的
	Text       string    `json:"text"`
}

type sender struct {
	ID    int64 `json:"id"`
	IsBot bool  `json:"is_bot"`
}

// with 报告这条消息在不在与 who 的私聊里。
func (m *textMessage) with(who *sender) bool {
	return m != nil && who != nil && who.ID > 0 && !who.IsBot && m.Chat.Type == "private" && m.Chat.ID == who.ID && m.SenderChat == nil
}

// dispatch 把一批 update 交下去,并记下收到了哪里;full 是这一批给满了。返回里面有没有认得出标识的。
func (t *Transport) dispatch(run *running, batch []json.RawMessage, full bool) bool {
	var last int64
	for _, raw := range batch {
		var u update
		bad := json.Unmarshal(raw, &u) != nil // 有一处类型不对也会把其余的解出来,所以标识多半还在
		if u.ID <= 0 {
			continue
		}
		last = max(last, u.ID)
		if t.seen.Again(formatID(u.ID)) || bad {
			continue
		}
		if who, do := t.jobOf(run, &u); do != nil {
			t.enqueue(who, job{id: u.ID, text: u.Message != nil, run: run, do: do})
		}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.full = full
	if last > 0 {
		t.next = last + 1 // 不取历来最大的:平台闲置一周之后会另起一段标识
	}
	return last > 0
}

// jobOf 是一条 update 要做的事与它是谁的;不归这里管的返回 nil。
func (t *Transport) jobOf(run *running, u *update) (who int64, do func()) {
	if m := u.Message; m != nil {
		if !m.with(m.From) || strings.TrimSpace(m.Text) == "" {
			return 0, nil
		}
		return m.From.ID, func() { t.onText(run, m) }
	}
	c := u.Callback
	if c == nil || c.ID == "" || !c.Message.with(c.From) || c.Message.From != nil && formatID(c.Message.From.ID) != t.BotID() {
		return 0, nil
	}
	return c.From.ID, func() { t.onClick(run, c.ID, c.Data, c.From.ID, c.Message.ID) }
}

func (t *Transport) enqueue(who int64, j job) {
	t.mu.Lock()
	defer t.mu.Unlock()
	queue, working := t.lanes[who]
	if len(queue) >= laneMax || j.text && t.texts >= maxTexts {
		return
	}
	if j.text {
		t.texts++
	}
	t.inflight[j.id] = time.Now()
	t.lanes[who] = append(queue, j)
	j.run.wg.Add(1)
	if !working {
		go t.work(who)
	}
}

// work 把一个人排着的一件件做完。
func (t *Transport) work(who int64) {
	for {
		t.mu.Lock()
		queue := t.lanes[who]
		if len(queue) == 0 {
			delete(t.lanes, who)
			t.mu.Unlock()
			return
		}
		j := queue[0]
		t.lanes[who] = queue[1:]
		t.mu.Unlock()
		t.one(j)
	}
}

func (t *Transport) one(j job) {
	defer j.run.wg.Done()
	defer t.finish(j)
	defer func() {
		if recover() != nil {
			logPanic()
		}
	}()
	j.do()
}

// unqueue 在停下时把还没开始的放回去:offset 没越过它们,平台会再给,由下一个连接处理 —— 现在处理的话,回的
// 话已经送不出去。offset 已经越过的留着做完,不然就丢了。
func (t *Transport) unqueue() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for who, queue := range t.lanes {
		var keep []job
		for _, j := range queue {
			if j.id < t.passed {
				keep = append(keep, j)
				continue
			}
			t.seen.Forget(formatID(j.id))
			t.drop(j)
			j.run.wg.Done()
		}
		t.lanes[who] = keep
	}
}

// drop 把一条从没处理完的里面拿掉;要持 t.mu。
func (t *Transport) drop(j job) {
	delete(t.inflight, j.id)
	if j.text {
		t.texts--
	}
}

func (t *Transport) finish(j job) {
	t.mu.Lock()
	t.drop(j)
	t.mu.Unlock()
	select {
	case t.changed <- struct{}{}:
	default:
	}
}

// ---------------------------------------------------------------- 文字

// onText 处理一条私聊文字。交上去就算处理完;回的那句话另发。
func (t *Transport) onText(run *running, m *textMessage) {
	if reply := t.safeText(run, m); reply != "" {
		t.later(run, m.Chat.ID, reply)
	}
}

// later 另起一路回一句话,只试一次;位子占满了、平台正嫌太频繁,就不回了。要在一件事处理的途中调(run.wg
// 还没归零)。
func (t *Transport) later(run *running, chatID int64, text string) {
	if time.Now().UnixNano() < t.quiet.Load() {
		return
	}
	select {
	case t.replies <- struct{}{}:
	default:
		return
	}
	run.wg.Add(1)
	go func() {
		defer run.wg.Done()
		defer time.AfterFunc(replyGap, func() { <-t.replies })
		if err := t.reply(run.ctx, chatID, text); err != nil {
			if wait := chat.RetryAfter(err); wait > 0 {
				t.quiet.Store(time.Now().Add(pollWait(wait)).UnixNano())
			}
			slog.Warn("Telegram 回复失败", "err", err)
		}
	}()
}

func (t *Transport) safeText(run *running, m *textMessage) (reply string) {
	defer func() {
		if recover() != nil {
			logPanic()
			reply = t.say("bind.failed")
		}
	}()
	return run.h.OnText(run.ctx, chat.Telegram, t.BotID(), formatID(m.From.ID), formatID(m.Chat.ID), m.Text)
}

// ---------------------------------------------------------------- 点击

// onClick 处理一次点击:先决定,再应答,再换卡片。处理得太慢就先答「正在处理」,做完只换卡片。
//
// 换卡片不重试到底:没换成,单子有了结论的由扫描收敛,还没有结论的再点一次就是。
func (t *Transport) onClick(run *running, queryID, data string, who, messageID int64) {
	m := dataShape.FindStringSubmatch(data)
	if m == nil {
		t.tryAnswer(run.ctx, queryID, t.say("click.invalid"), true, "")
		return
	}
	click := chat.Click{Channel: chat.Telegram, BotID: t.BotID(), AccountID: formatID(who), Token: m[2], Action: actions[m[1]]}
	res := make(chan chat.ClickResult, 1)
	go func() {
		defer func() {
			if recover() != nil {
				logPanic()
				res <- chat.ClickResult{Toast: t.say("click.retry")}
			}
		}()
		res <- run.h.OnClick(run.ctx, click)
	}()
	var r chat.ClickResult
	answered := false
	timer := time.NewTimer(t.answerWithin)
	defer timer.Stop()
	select {
	case r = <-res:
	case <-timer.C:
		answered = true
		// 应答另走,不占着:结果一出来就要能换卡片,时限从那时起算。
		go t.tryAnswer(run.ctx, queryID, t.say("click.processing"), false, click.Token)
		r = <-res
	}
	ctx, cancel := context.WithTimeout(run.ctx, t.cardWithin)
	defer cancel()
	if !answered {
		t.tryAnswer(ctx, queryID, r.Toast, !r.OK && r.Toast != "", click.Token)
	}
	if r.Card == nil {
		if answered && r.Toast != "" { // 弹窗的机会已经用在「正在处理」上了
			t.later(run, who, r.Toast)
		}
		return
	}
	if err := t.edit(ctx, who, messageID, *r.Card); err != nil {
		slog.Warn("Telegram 更新卡片失败", "err", err)
	}
}

func (t *Transport) tryAnswer(ctx context.Context, queryID, text string, alert bool, token string) {
	ctx, cancel := context.WithTimeout(ctx, answerCall)
	defer cancel()
	if err := t.answer(ctx, queryID, text, alert, token); err != nil {
		slog.Warn("Telegram 应答点击失败", "err", err)
	}
}

// ---------------------------------------------------------------- 零碎

func (t *Transport) say(key string) string { return chat.Tr(t.lang(), key) }

// logPanic 只记调用栈,不记 panic 的内容:里面可能有令牌。
func logPanic() { slog.Error("Telegram 事件处理 panic", "stack", string(debug.Stack())) }
