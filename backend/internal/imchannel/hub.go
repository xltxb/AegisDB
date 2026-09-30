package imchannel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// ErrNotConfigured 表示这个渠道没配凭据,或者不具备被要求的能力。
var ErrNotConfigured = errors.New("im channel not configured")

const sendTimeout = 15 * time.Second

// Caps 是渠道**能**做什么(与是否打开无关)。企业微信只有 Notify。
type Caps struct {
	Notify  bool
	Approve bool
}

// Card 是一张已经发出去的交互卡片。
type Card struct {
	ID         int64
	ApprovalID int64
	Channel    string
	UserID     int64
	MsgRef     string
}

// Store 是 Hub 需要的持久化,由 service 用仓储实现。
type Store interface {
	PlatformIDs(channel string, userIDs []int64) (map[int64]string, error)
	RecordCard(c Card) error
	OpenCards(approvalID int64) ([]Card, error)
	MarkCardClosed(id int64) error
}

// Status 是设置页上一个渠道的运行状况。
type Status struct {
	Kind       string `json:"kind"`
	Label      string `json:"label"`
	CanNotify  bool   `json:"canNotify"`
	CanApprove bool   `json:"canApprove"`
	Configured bool   `json:"configured"`
	Notify     bool   `json:"notify"`
	Approval   bool   `json:"approval"`
	Running    bool   `json:"running"`
	BotName    string `json:"botName,omitempty"`
	LastError  string `json:"lastError,omitempty"`
	// LastNotifyError 是最近一次群通知的失败原因;下一次发送成功就清空。群通知是
	// 异步发的,不记在这里,失败就只剩一条没人看的 WARN。
	LastNotifyError string `json:"lastNotifyError,omitempty"`
}

type entry struct {
	caps      Caps
	build     Builder
	ch        Channel
	cancel    context.CancelFunc
	running   bool
	lastErr   string
	notifyErr string
}

// pinnedSink 把入站事件的渠道钉成投递它的那个适配器。
//
// 渠道名决定了去哪张绑定表认人:让适配器自己填,一个写错了 Channel 的适配器就能拿
// 别的渠道上的绑定来认人。
type pinnedSink struct {
	kind string
	next ActionSink
}

func (p pinnedSink) OnAction(ctx context.Context, a Action) ActionResult {
	a.Channel = p.kind
	return p.next.OnAction(ctx, a)
}

func (p pinnedSink) OnDirectMessage(ctx context.Context, dm DM) string {
	dm.Channel = p.kind
	return p.next.OnDirectMessage(ctx, dm)
}

// panicErr 把 recover 到的值记一条 ERROR 并转成错误。适配器的 bug 不能拖垮网关。
func panicErr(kind, where string, p any) error {
	slog.Error("im channel panicked", "channel", kind, "where", where, "panic", p)
	return fmt.Errorf("渠道内部错误(panic): %v", p)
}

// Hub 管理所有渠道:扇出、记录、收起、长连接。它不做任何判定。
type Hub struct {
	deps  Deps
	store Store

	reloadMu sync.Mutex // Reload 串行
	mu       sync.Mutex
	order    []string
	entries  map[string]*entry
	root     context.Context
	sink     ActionSink
	// stopped 之后不再接任何新活。Stop 在 mu 里置位,而所有 work.Add 也在 mu 里做 ——
	// 于是 Stop 里的 work.Wait 不会撞上一个从零开始的 Add(sync.WaitGroup 禁止这样用)。
	stopped bool

	work sync.WaitGroup // 出站异步任务
	runs sync.WaitGroup // 长连接

	apMu   sync.Mutex
	queues map[int64]*apQueue
}

type apQueue struct{ jobs []func() }

func NewHub(deps Deps, store Store) *Hub {
	return &Hub{deps: deps, store: store, entries: map[string]*entry{}, queues: map[int64]*apQueue{}}
}

// Register 登记一个渠道。登记完要 Reload 才生效。
//
// 同一个渠道登记第二次时,先取消旧实例的长连接:旧 entry 被替换后,stopRuns 再也找不到
// 它的 cancel,而 runs.Wait 会一直等那个没人能叫停的循环。
func (h *Hub) Register(kind string, caps Caps, b Builder) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if old, ok := h.entries[kind]; ok {
		if old.cancel != nil {
			old.cancel()
			old.cancel = nil
		}
	} else {
		h.order = append(h.order, kind)
	}
	h.entries[kind] = &entry{caps: caps, build: b}
}

func (h *Hub) SetSink(s ActionSink) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sink = s
}

// Start 记下进程级的 ctx 并按当前配置起渠道。ctx 取消时所有长连接退出。
func (h *Hub) Start(ctx context.Context) {
	h.mu.Lock()
	h.root = ctx
	h.mu.Unlock()
	h.Reload()
}

// Reload 按当前配置重建渠道实例并重启长连接。设置保存后调用。
//
// 先停后起,而且**等旧的真正退出**再起新的:Telegram 的 getUpdates 同一时刻只认一个
// 连接,旧循环没退干净就起新的,两边会互相踢(409)。
func (h *Hub) Reload() {
	h.reloadMu.Lock()
	defer h.reloadMu.Unlock()
	h.stopRuns()

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stopped {
		return
	}
	for _, kind := range h.order {
		e := h.entries[kind]
		e.ch = e.build(h.deps)
		e.lastErr, e.notifyErr = "", ""
		ap, ok := e.ch.(Approver)
		if !ok || !e.caps.Approve || h.root == nil || h.sink == nil || !h.deps.Settings.Bool(Key(kind, "approval")) {
			continue
		}
		ctx, cancel := context.WithCancel(h.root)
		e.cancel, e.running = cancel, true
		h.runs.Add(1)
		go h.run(e, kind, ap, ctx, pinnedSink{kind: kind, next: h.sink})
	}
}

// Stop 停掉所有长连接并等出站任务收敛。进程退出与测试清理时调用。
func (h *Hub) Stop() {
	h.reloadMu.Lock()
	defer h.reloadMu.Unlock()
	h.mu.Lock()
	h.stopped = true
	h.mu.Unlock()
	h.stopRuns()
	h.work.Wait()
}

func (h *Hub) stopRuns() {
	h.mu.Lock()
	for _, e := range h.entries {
		if e.cancel != nil {
			e.cancel()
			e.cancel = nil
		}
	}
	h.mu.Unlock()
	h.runs.Wait()
}

// run 把状态记在启动它的那个 entry 上,而不是按 kind 去查:渠道被重新登记后,
// 旧循环退出时不该改写新 entry 的状态。
func (h *Hub) run(e *entry, kind string, ap Approver, ctx context.Context, sink ActionSink) {
	defer h.runs.Done()
	panicked := false
	var err error
	func() {
		defer func() {
			if p := recover(); p != nil {
				panicked, err = true, panicErr(kind, "run", p)
			}
		}()
		err = ap.Run(ctx, sink)
	}()
	h.mu.Lock()
	defer h.mu.Unlock()
	e.running = false
	switch {
	case err != nil && (ctx.Err() == nil || panicked):
		e.lastErr = err.Error()
		slog.Warn("im channel stopped", "channel", kind, "err", err)
	case err == nil && ctx.Err() == nil:
		// 没人叫它停,它却自己回来了,还不说为什么 —— 设置页上必须看得见,否则就是一个
		// 开关开着、状态「未运行」、没有任何原因的渠道。
		e.lastErr = "长连接已退出(渠道未说明原因)"
		slog.Warn("im channel run loop returned without error", "channel", kind)
	}
}

type target struct {
	kind string
	ap   Approver
}

// approvers 返回已配置的交互渠道;enabledOnly 时只要开关打开的。
func (h *Hub) approvers(enabledOnly bool) []target {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []target
	for _, kind := range h.order {
		e := h.entries[kind]
		ap, ok := e.ch.(Approver)
		if !ok || !e.caps.Approve {
			continue
		}
		if enabledOnly && !h.deps.Settings.Bool(Key(kind, "approval")) {
			continue
		}
		out = append(out, target{kind, ap})
	}
	return out
}

func (h *Hub) approver(kind string) Approver {
	h.mu.Lock()
	defer h.mu.Unlock()
	if e := h.entries[kind]; e != nil {
		if ap, ok := e.ch.(Approver); ok {
			return ap
		}
	}
	return nil
}

// enqueue 把一个任务排进这张单自己的 FIFO 队列,立即返回。
//
// 同一张单的「发卡」与「收卡」必须按调用顺序执行。不排队的话,工单在控制台被秒批时,
// 收卡那一轮会先跑完,而晚一步发出去的卡片从此没人收 —— 一张已经批掉的单,在某人的
// 私聊里还挂着两个能点的按钮。
//
// 用队列而不是锁:锁得在调用方的 goroutine 里拿才能保证顺序,而调用方是控制台上那次
// 「批准」请求 —— 它不该等着十几张卡片发完才返回。
func (h *Hub) enqueue(id int64, job func()) {
	h.mu.Lock()
	if h.stopped {
		h.mu.Unlock()
		return
	}
	h.work.Add(1)
	h.mu.Unlock()
	h.apMu.Lock()
	if q, busy := h.queues[id]; busy {
		q.jobs = append(q.jobs, job)
		h.apMu.Unlock()
		return
	}
	h.queues[id] = &apQueue{}
	h.apMu.Unlock()
	go func() {
		for {
			h.runJob(id, job)
			h.work.Done()
			h.apMu.Lock()
			q := h.queues[id]
			if len(q.jobs) == 0 {
				delete(h.queues, id)
				h.apMu.Unlock()
				return
			}
			job, q.jobs = q.jobs[0], q.jobs[1:]
			h.apMu.Unlock()
		}
	}()
}

// runJob 跑一个排队任务并吞掉它的 panic:队列的 worker 必须活着把后面的任务做完,
// 否则这张单之后的收卡永远排不上,而 Flush 也永远等不到头。
func (h *Hub) runJob(approvalID int64, job func()) {
	defer func() {
		if p := recover(); p != nil {
			slog.Error("im job panicked", "approvalId", approvalID, "panic", p)
		}
	}()
	job()
}

// ApprovalCreated 给 recipients 里每个在已开启渠道上绑定过的人私信一张卡片。
func (h *Hub) ApprovalCreated(approvalID int64, c ApprovalCard, recipients []int64) {
	if len(recipients) == 0 {
		return
	}
	targets := h.approvers(true)
	if len(targets) == 0 {
		return
	}
	h.enqueue(approvalID, func() {
		for _, t := range targets {
			ids, err := h.store.PlatformIDs(t.kind, recipients)
			if err != nil {
				slog.Warn("im: load identities failed", "channel", t.kind, "apNo", c.ApNo, "err", err)
				continue
			}
			for _, uid := range recipients {
				pid, ok := ids[uid]
				if !ok {
					continue
				}
				ctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
				ref, err := t.ap.SendCard(ctx, pid, c)
				cancel()
				if err != nil {
					slog.Warn("im: send card failed", "channel", t.kind, "apNo", c.ApNo, "userId", uid, "err", err)
					continue
				}
				if err := h.store.RecordCard(Card{ApprovalID: approvalID, Channel: t.kind, UserID: uid, MsgRef: ref}); err != nil {
					slog.Warn("im: record card failed", "channel", t.kind, "apNo", c.ApNo, "err", err)
				}
			}
		}
	})
}

// ApprovalSettled 收起这张单所有还开着的卡片。
//
// 按卡片行上的渠道找适配器,**不看当前开关**:渠道被关掉之后,它发出去的旧卡片仍要收起,
// 否则会留下一批永远挂着、点了没反应的按钮。凭据也清空了就只能记一条 WARN。
func (h *Hub) ApprovalSettled(approvalID int64, final CardState) {
	h.enqueue(approvalID, func() {
		cards, err := h.store.OpenCards(approvalID)
		if err != nil {
			slog.Warn("im: load open cards failed", "approvalId", approvalID, "err", err)
			return
		}
		for _, c := range cards {
			ap := h.approver(c.Channel)
			if ap == nil {
				slog.Warn("im: card left open, channel not configured", "channel", c.Channel, "approvalId", approvalID)
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
			err := ap.CloseCard(ctx, c.MsgRef, final)
			cancel()
			if err != nil {
				slog.Warn("im: close card failed", "channel", c.Channel, "approvalId", approvalID, "err", err)
				continue
			}
			if err := h.store.MarkCardClosed(c.ID); err != nil {
				slog.Warn("im: mark card closed failed", "cardId", c.ID, "err", err)
			}
		}
	})
}

// NotifyApproval 往每个开了群通知的渠道发一条新工单通知。
func (h *Hub) NotifyApproval(c ApprovalCard) {
	type notifyTarget struct {
		kind string
		n    Notifier
	}
	h.mu.Lock()
	if h.stopped {
		h.mu.Unlock()
		return
	}
	var ns []notifyTarget
	for _, kind := range h.order {
		e := h.entries[kind]
		if n, ok := e.ch.(Notifier); ok && e.caps.Notify && h.deps.Settings.Bool(Key(kind, "notify")) {
			ns = append(ns, notifyTarget{kind, n})
		}
	}
	h.work.Add(len(ns)) // 在锁内加:见 stopped 字段的注释
	h.mu.Unlock()
	for _, t := range ns {
		go func(t notifyTarget) {
			defer h.work.Done()
			err := h.notifyOne(t.kind, t.n, c)
			if err != nil {
				slog.Warn("im: notify failed", "channel", t.kind, "apNo", c.ApNo, "err", err)
			}
			h.recordNotify(t.kind, err)
		}(t)
	}
}

func (h *Hub) notifyOne(kind string, n Notifier, c ApprovalCard) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = panicErr(kind, "notify", p)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
	defer cancel()
	return n.NotifyApproval(ctx, c)
}

// recordNotify 把这一次群通知的结果记进状态:失败留下原因,成功清掉上一次的。
func (h *Hub) recordNotify(kind string, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	e := h.entries[kind]
	if e == nil {
		return
	}
	if err != nil {
		e.notifyErr = err.Error()
	} else {
		e.notifyErr = ""
	}
}

// TestNotify 同步发一条测试通知。不看开关 —— 管理员要能在打开之前先试。
func (h *Hub) TestNotify(ctx context.Context, kind string, c ApprovalCard) error {
	h.mu.Lock()
	e := h.entries[kind]
	var n Notifier
	if e != nil && e.caps.Notify {
		n, _ = e.ch.(Notifier)
	}
	h.mu.Unlock()
	if n == nil {
		return ErrNotConfigured
	}
	return n.NotifyApproval(ctx, c)
}

// TestCard 同步私信一张测试卡片,不记录、不需要收起。
func (h *Hub) TestCard(ctx context.Context, kind, platformUserID string, c ApprovalCard) error {
	if !h.CanApprove(kind) {
		return ErrNotConfigured
	}
	ap := h.approver(kind)
	if ap == nil {
		return ErrNotConfigured
	}
	_, err := ap.SendCard(ctx, platformUserID, c)
	return err
}

// CanApprove 说这个渠道**能**做交互审批(登记时声明),与是否配置、是否打开无关。
func (h *Hub) CanApprove(kind string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	e := h.entries[kind]
	return e != nil && e.caps.Approve
}

// ApprovalEnabled = 能做 + 已配置 + 开关打开。
func (h *Hub) ApprovalEnabled(kind string) bool {
	return h.CanApprove(kind) && h.approver(kind) != nil && h.deps.Settings.Bool(Key(kind, "approval"))
}

// ApproveKinds 按登记顺序列出所有能做交互审批的渠道。
func (h *Hub) ApproveKinds() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, kind := range h.order {
		if h.entries[kind].caps.Approve {
			out = append(out, kind)
		}
	}
	return out
}

func (h *Hub) BotName(kind string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if e := h.entries[kind]; e != nil {
		if b, ok := e.ch.(BotNamer); ok {
			return b.BotName()
		}
	}
	return ""
}

func (h *Hub) Status() []Status {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]Status, 0, len(h.order))
	for _, kind := range h.order {
		e := h.entries[kind]
		st := Status{
			Kind: kind, Label: Label(kind), CanNotify: e.caps.Notify, CanApprove: e.caps.Approve,
			Configured: e.ch != nil,
			Notify:     h.deps.Settings.Bool(Key(kind, "notify")),
			Approval:   h.deps.Settings.Bool(Key(kind, "approval")),
			Running:    e.running, LastError: e.lastErr, LastNotifyError: e.notifyErr,
		}
		if b, ok := e.ch.(BotNamer); ok {
			st.BotName = b.BotName()
		}
		out = append(out, st)
	}
	return out
}

// Flush 等所有已派出的出站任务结束。给测试和优雅退出用。
func (h *Hub) Flush() { h.work.Wait() }

// ServeWebhook 把一次入站 webhook 交给对应的渠道。
//
// 这是一扇在 JWT 之外、能驱动审批的门,所以每一步都失败朝严:渠道没登记 → 404;
// 不能审批、没配置、交互审批没打开、不收 webhook、Hub 还没有 sink → 403。真伪由渠道
// 自己验(Webhooker)。渠道 panic 时回 200:一个解析不了的请求,重投一万次也一样。
func (h *Hub) ServeWebhook(ctx context.Context, kind string, header http.Header, body []byte) (status int) {
	h.mu.Lock()
	e := h.entries[kind]
	var wh Webhooker
	if e != nil && e.caps.Approve && e.ch != nil {
		wh, _ = e.ch.(Webhooker)
	}
	sink := h.sink
	h.mu.Unlock()
	if e == nil {
		return http.StatusNotFound
	}
	if wh == nil || sink == nil || !h.deps.Settings.Bool(Key(kind, "approval")) {
		return http.StatusForbidden
	}
	defer func() {
		if p := recover(); p != nil {
			_ = panicErr(kind, "webhook", p)
			status = http.StatusOK
		}
	}()
	return wh.HandleWebhook(ctx, header, body, pinnedSink{kind: kind, next: sink})
}
