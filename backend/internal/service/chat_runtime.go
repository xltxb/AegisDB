package service

// 聊天渠道的连接管理与设置(.scratch/chat-approval-cards/PRD.md §5.6、§5.7)。
//
// 每个渠道两层协程:一层盯设置(chatSupervisor),设置变了就换连接;一层维持连接(runChatConn),断了退避
// 重连。渠道出任何事 —— 连不上、panic、挂住 —— 都停在这里,只反映在状态上。
//
// 渠道对象一建好就登记(SetChatTransport),断线期间也不撤:发送走自己的请求,不靠长连接;撤下来的话,
// 断线那几秒建的单就不会排卡片了。

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"aegisdb/internal/apimsg"
	"aegisdb/internal/chat"
	"aegisdb/internal/chat/lark"
	"aegisdb/internal/chat/telegram"
	"aegisdb/internal/dto"
	"aegisdb/internal/model"
	"aegisdb/pkg/crypto"
)

var (
	ErrChatNotBound     = errors.New("你还没有绑定这个审批机器人,请先在右上角用户菜单的「审批机器人」里绑定")
	ErrChatTestTooOften = errors.New("测试卡片发得太频繁,请稍后再试")
)

const (
	chatDetailMax = 200             // 状态里的原因最多这么多字
	chatTestEvery = 5 * time.Second // 同一个人两次测试卡片之间至少隔这么久
)

// chatTimings 是连接管理的各个时限。
type chatTimings struct {
	poll       time.Duration // 多久看一眼设置:别处(直接改库、另一个进程)改的也要数秒内生效
	sweep      time.Duration
	stop       time.Duration // 换连接时等旧的停下,最多这么久
	backoff    time.Duration // 第一次断开之后等多久,之后翻倍
	backoffMax time.Duration
	stable     time.Duration // 连着超过这么久才把退避清零
}

// chatTiming 是变量只为测试能缩短它;RunChat 开始时取一份,之后不再看。
var chatTiming = chatTimings{
	poll: 5 * time.Second, sweep: 5 * time.Second, stop: 5 * time.Second,
	backoff: time.Second, backoffMax: time.Minute, stable: time.Minute,
}

// after 是连续第 n 次(从 0 起)断开之后等多久再连。
func (t chatTimings) after(n int) time.Duration {
	d := t.backoff
	for ; n > 0 && d < t.backoffMax; n-- {
		d *= 2
	}
	return min(d, t.backoffMax)
}

// ---------------------------------------------------------------- 设置

// ChatConfig 是一个渠道此刻的连接配置。AppSecret、BotToken、Proxy 是明文机密:不要打印整个值。
type ChatConfig struct {
	Enabled                  bool
	Region, AppID, AppSecret string // Lark
	BotToken, Proxy          string // Telegram;Proxy 空 = 直连
	// Problem 非空:开着却没法连,这是给人看的原因。
	Problem string
}

// chatSettings 读全部 chat.* 设置。不用 Repo.GetSetting:键不存在时 GORM 每读一次记一行 record not
// found,而老库里没有这些键,每轮扫描、每次点击都要读。
//
// 每读成一次就顺手记下卡片语言,供不能等库的地方用(见 quickCardLang)。
func (s *Services) chatSettings() (map[string]string, error) {
	all, err := s.Repo.SettingsByPrefix("chat.")
	if err != nil {
		return nil, err
	}
	lang := orDefault(chatText(all["chat.cardLang"]), "zh")
	s.chatLang.Store(&lang)
	return all, nil
}

func (s *Services) chatBool(key string) bool {
	all, err := s.chatSettings()
	return err == nil && all[key] == "true"
}

// cardLang 是发到聊天里的文字用的语言(全局设置,默认 zh)。
func (s *Services) cardLang() string {
	_, _ = s.chatSettings()
	return s.quickCardLang()
}

// quickCardLang 是最近一次读到的卡片语言,不碰库。渠道自己要说的话用它:「正在处理」正是在处理得太慢
// 时才说的,而慢的多半就是库。连着的渠道由 chatSupervisor 按 poll 的间隔、以及每次保存设置时刷新它。
func (s *Services) quickCardLang() string {
	if lang := s.chatLang.Load(); lang != nil {
		return *lang
	}
	return "zh"
}

// chatText 解出一个字符串设置(JSON 编码);没有或不是字符串为空。
func chatText(raw string) string {
	var v string
	_ = json.Unmarshal([]byte(raw), &v)
	return strings.TrimSpace(v)
}

// chatConfig 读一个渠道的配置。关着的一律是零值:关着时改 App ID 不算变化。
func (s *Services) chatConfig(ch chat.Channel) (ChatConfig, error) {
	all, err := s.chatSettings()
	if err != nil || all["chat."+string(ch)+".enabled"] != "true" {
		return ChatConfig{}, err
	}
	c := ChatConfig{Enabled: true}
	secret := func(key string) string {
		plain, derr := crypto.DecryptSecret(chatText(all[key]))
		if derr != nil { // 解密的错误不带出去,里面没有对人有用的东西
			c.Problem = apimsg.Text("密钥无法解密")
		}
		return strings.TrimSpace(plain)
	}
	switch ch {
	case chat.Lark:
		c.Region, c.AppID = chatText(all["chat.lark.region"]), chatText(all["chat.lark.appId"])
		if c.Region != "lark" {
			c.Region = "feishu"
		}
		if c.AppSecret = secret("chat.lark.appSecret"); c.Problem == "" && (c.AppID == "" || c.AppSecret == "") {
			c.Problem = apimsg.Text("没有填写 App ID 或 App Secret")
		}
	case chat.Telegram:
		c.BotToken, c.Proxy = secret("chat.telegram.botToken"), secret("chat.telegram.proxyToken")
		_, perr := telegram.ParseProxy(c.Proxy)
		switch {
		case c.Problem != "":
		case c.BotToken == "":
			c.Problem = apimsg.Text("没有填写 Bot Token")
		case perr != nil || !telegram.ValidToken(c.BotToken): // 有人直接改了库
			c.Problem = apimsg.Text("Bot Token 或代理地址的格式不对,请重新填写")
		}
	default:
		return ChatConfig{}, nil
	}
	return c, nil
}

// newChatTransport 按配置建一个渠道;建不成返回 nil。
func (s *Services) newChatTransport(ch chat.Channel, c ChatConfig) chat.Transport {
	switch ch {
	case chat.Lark:
		return s.newLark(c)
	case chat.Telegram:
		return s.newTelegram(c)
	}
	return nil
}

func (s *Services) newTelegram(c ChatConfig, opts ...telegram.Option) chat.Transport {
	t, err := telegram.New(c.BotToken, c.Proxy, append(opts, telegram.WithLang(s.quickCardLang))...)
	if err != nil {
		return nil // 格式已在 chatConfig 里核对过
	}
	return t
}

func (s *Services) newLark(c ChatConfig, opts ...lark.Option) chat.Transport {
	return lark.New(c.Region, c.AppID, c.AppSecret, append(opts, lark.WithLang(s.quickCardLang))...)
}

// SetChatFactory 换掉建渠道的函数,只给测试用,要在 RunChat 之前调。
func (s *Services) SetChatFactory(f func(chat.Channel, ChatConfig) chat.Transport) {
	s.chatRun.mu.Lock()
	defer s.chatRun.mu.Unlock()
	s.chatRun.factory = f
}

// ---------------------------------------------------------------- 运行

// chatRuntime 是连接管理的共享状态。零值可用。
type chatRuntime struct {
	mu      sync.Mutex
	running bool
	factory func(chat.Channel, ChatConfig) chat.Transport
	wake    map[chat.Channel]chan struct{}
	conns   map[chat.Channel]*chatConn // 每个渠道此刻算数的那条连接
	status  map[chat.Channel]chatState
}

type chatState struct {
	state  chat.State
	detail string
}

// chatConn 是一条连接(一份配置)的一生,其间可以断开重连多次。
type chatConn struct {
	ch     chat.Channel
	cfg    ChatConfig
	t      chat.Transport // 只有 runChatConn 的协程碰
	cancel context.CancelFunc
	done   chan struct{}
}

// RunChat 管理聊天渠道的连接,并每 5 秒扫一次审批卡片,直到 ctx 取消、所有连接停下。一个进程只起一个。
func (s *Services) RunChat(ctx context.Context) {
	rt, timing := &s.chatRun, chatTiming
	wake := map[chat.Channel]chan struct{}{}
	for _, ch := range chat.Channels {
		wake[ch] = make(chan struct{}, 1)
	}
	rt.mu.Lock()
	if rt.running {
		rt.mu.Unlock()
		slog.Error("聊天渠道已经在运行")
		return
	}
	rt.running, rt.wake = true, wake
	rt.conns, rt.status = map[chat.Channel]*chatConn{}, map[chat.Channel]chatState{}
	rt.mu.Unlock()

	var wg sync.WaitGroup
	for _, ch := range chat.Channels {
		wg.Add(1)
		go func() {
			defer wg.Done()
			(&chatSupervisor{s: s, ch: ch, timing: timing}).run(ctx, wake[ch])
		}()
	}
	sweep := time.NewTicker(timing.sweep)
	defer sweep.Stop()
	for ctx.Err() == nil {
		select {
		case <-ctx.Done():
		case <-sweep.C:
			go s.SweepChatCards(ctx) // 不等它:一个渠道慢,另一个照常(见 SweepChatCards)
		}
	}
	wg.Wait()
	rt.mu.Lock()
	rt.running, rt.wake = false, nil
	rt.mu.Unlock()
}

// ReloadChat 让连接管理马上重看一眼设置。设置没变就什么都不做;没在运行也什么都不做。
func (s *Services) ReloadChat() {
	s.chatRun.mu.Lock()
	defer s.chatRun.mu.Unlock()
	for _, w := range s.chatRun.wake {
		select {
		case w <- struct{}{}:
		default: // 已经叫过了,它还没看
		}
	}
}

// ChatStatus 是各渠道此刻的状态,不含任何密钥。
func (s *Services) ChatStatus() map[chat.Channel]dto.ChatStatus {
	out := map[chat.Channel]dto.ChatStatus{}
	for _, ch := range chat.Channels {
		s.chatRun.mu.Lock()
		st, ok := s.chatRun.status[ch]
		s.chatRun.mu.Unlock()
		if !ok {
			st.state = chat.StateOff
		}
		cfg, _ := s.chatConfig(ch)
		out[ch] = dto.ChatStatus{Enabled: cfg.Enabled, State: st.state, Detail: st.detail,
			DetailRef: apimsg.Lookup(st.detail), BotName: s.ChatBotName(ch)}
	}
	return out
}

// chatSupervisor 盯着一个渠道的设置,设置变了就换连接。
type chatSupervisor struct {
	s      *Services
	ch     chat.Channel
	timing chatTimings
	cur    ChatConfig // 已经照办的配置;零值 = 关着
	conn   *chatConn
}

func (v *chatSupervisor) run(ctx context.Context, wake <-chan struct{}) {
	poll := time.NewTicker(v.timing.poll)
	defer poll.Stop()
	for ctx.Err() == nil {
		v.reconcile(ctx)
		select {
		case <-ctx.Done():
		case <-wake:
		case <-poll.C:
		}
	}
	if v.cur.Enabled {
		v.apply(ctx, ChatConfig{})
	}
}

func (v *chatSupervisor) reconcile(ctx context.Context) {
	defer func() {
		if recover() != nil {
			logChatPanic(v.ch)
		}
	}()
	cfg, err := v.s.chatConfig(v.ch)
	// 库暂时读不出来(GORM 已记日志):照旧,不能因此断开。
	if err != nil || cfg == v.cur {
		return
	}
	v.apply(ctx, cfg)
}

// apply 换上一份配置。关掉、没法连的先撤下渠道再等旧连接停,让它立刻生效;要换新的则等旧的停了再换。
func (v *chatSupervisor) apply(ctx context.Context, cfg ChatConfig) {
	rt := &v.s.chatRun
	v.cur = cfg
	start := cfg.Enabled && cfg.Problem == ""
	st := chatState{state: chat.StateConnecting}
	switch {
	case !cfg.Enabled:
		st.state = chat.StateOff
	case cfg.Problem != "":
		st = chatState{chat.StateError, cfg.Problem}
	}
	rt.mu.Lock()
	delete(rt.conns, v.ch) // 从此旧连接说什么都不算数
	rt.status[v.ch] = st
	if !start {
		v.s.SetChatTransport(v.ch, nil)
	}
	rt.mu.Unlock()

	if old := v.conn; old != nil {
		v.conn = nil
		old.cancel()
		timer := time.NewTimer(v.timing.stop)
		defer timer.Stop()
		select {
		case <-old.done:
		case <-timer.C:
			slog.Warn("聊天渠道的旧连接没有按时停下,不再等它", "channel", v.ch)
		}
	}
	if !start {
		return
	}
	cctx, cancel := context.WithCancel(ctx)
	v.conn = &chatConn{ch: v.ch, cfg: cfg, cancel: cancel, done: make(chan struct{})}
	rt.mu.Lock()
	rt.conns[v.ch] = v.conn
	rt.mu.Unlock()
	go v.s.runChatConn(cctx, v.conn, v.timing)
}

// runChatConn 维持一条连接:断了退避重连,直到 ctx 取消。
func (s *Services) runChatConn(ctx context.Context, conn *chatConn, timing chatTimings) {
	defer close(conn.done)
	for n := 0; ctx.Err() == nil; n++ {
		up, err := s.chatSession(ctx, conn)
		if ctx.Err() != nil {
			return
		}
		if up >= timing.stable {
			n = 0
		}
		wait := timing.after(n)
		slog.Warn("聊天渠道断开,稍后重连", "channel", conn.ch, "wait", wait, "err", chatDetail(err.Error()))
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
		case <-timer.C:
		}
		timer.Stop()
	}
}

// chatSession 连一次,到断开为止。返回连上了多久(没连上过为 0)与断开的原因(不为 nil)。
//
// 状态也在这里收:Run 一返回(或 panic),它之后再报的一概不算数;它报过冲突的,保留冲突,不改成出错。
func (s *Services) chatSession(ctx context.Context, conn *chatConn) (up time.Duration, err error) {
	rt := &s.chatRun
	var (
		live  = true // 下面几样由 rt.mu 保护
		last  chat.State
		since time.Time     // 这一段从什么时候连着;零值 = 此刻没连着
		total time.Duration // 已经结束的各段加起来
	)
	set := func(st chat.State, detail string) { // 要持 rt.mu
		if rt.conns[conn.ch] == conn {
			rt.status[conn.ch] = chatState{st, chatDetail(detail)}
		}
	}
	defer func() {
		if recover() != nil {
			logChatPanic(conn.ch)
			err = errChatPanic
		}
		if err == nil {
			err = errors.New("connection closed")
		}
		rt.mu.Lock()
		defer rt.mu.Unlock()
		live = false
		if up = total; !since.IsZero() {
			up += time.Since(since)
		}
		if conn.t == nil && rt.conns[conn.ch] == conn {
			s.SetChatTransport(conn.ch, nil) // 新的没建成,换下来的旧渠道不能还顶在那里
		}
		if last != chat.StateConflict {
			set(chat.StateError, chatReason(err))
		}
	}()
	if conn.t == nil {
		if err := s.openChat(conn); err != nil {
			return 0, err
		}
	}
	return 0, conn.t.Run(ctx, s, func(st chat.State, detail string) {
		rt.mu.Lock()
		defer rt.mu.Unlock()
		if !live {
			return
		}
		last = st
		switch {
		case st == chat.StateConnected && since.IsZero(): // 轮询的渠道每一轮都报,只认第一次
			since = time.Now()
		case st != chat.StateConnected && !since.IsZero():
			total += time.Since(since)
			since = time.Time{}
		}
		if st == chat.StateConnecting && detail == "" {
			detail = rt.status[conn.ch].detail // 重连期间仍看得到上一次为什么断
		}
		set(st, detail)
	})
}

// chatReason 是断开的原因在状态里的样子:管理员改得了的几种说人话,其余是渠道的原话。
func chatReason(err error) string {
	switch {
	case errors.Is(err, chat.ErrCredentials):
		return apimsg.Text("机器人令牌无效或已被撤销,请重新填写")
	case errors.Is(err, chat.ErrWebhook):
		return apimsg.Text("这个机器人设置了 webhook,网关收不到消息;请先在原来使用它的地方删除 webhook")
	}
	return err.Error()
}

// openChat 建渠道对象并登记。建好时这条连接已经被换掉的,不登记。
func (s *Services) openChat(conn *chatConn) error {
	rt := &s.chatRun
	rt.mu.Lock()
	build := rt.factory
	rt.mu.Unlock()
	if build == nil {
		build = s.newChatTransport
	}
	t := build(conn.ch, conn.cfg)
	if t == nil {
		return errors.New("no transport")
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.conns[conn.ch] != conn {
		return errors.New("replaced")
	}
	conn.t = t
	s.SetChatTransport(conn.ch, t)
	return nil
}

// chatDetail 是状态与日志里的原因:一行、截短。渠道自己保证里面没有机密。
func chatDetail(text string) string { return clipColumn(chatOneLine(text), chatDetailMax) }

// logChatPanic 只记调用栈,不记 panic 的内容:里面可能有带机器人令牌的地址。
func logChatPanic(ch chat.Channel) {
	slog.Error("聊天渠道 panic", "channel", ch, "stack", string(debug.Stack()))
}

// ---------------------------------------------------------------- 测试卡片

// chatTester 是测试卡片的频控键。
type chatTester struct {
	user int64
	ch   chat.Channel
}

// SendChatTest 发一张测试卡片给 u 自己。按钮只有「测试」:令牌形状是真的、库里却没有,点了只回一句提示
// (见 OnClick 里的 ActTest),不碰任何单子。
func (s *Services) SendChatTest(ctx context.Context, u *model.User, ch chat.Channel) error {
	t := s.chatTransport(ch)
	if t == nil {
		return ErrChatOff
	}
	bot := t.BotID()
	if bot == "" {
		return ErrChatNotReady
	}
	b := s.ChatBindingOf(u.ID, ch)
	if b == nil || b.BotID != bot {
		return ErrChatNotBound
	}
	// 借失败计数表:一个窗口里只有第一次返回真。查与记是同一步,同时来的几次只放行一次。
	if !s.chatTests.fail(chatTester{u.ID, ch}, time.Now()) {
		return ErrChatTestTooOften
	}
	token, err := newChatToken()
	if err != nil {
		return err
	}
	lang := s.cardLang()
	card := chat.Card{Title: chat.Tr(lang, "title.test"), Risk: model.RiskLow, Lang: lang, Token: token, Test: true}
	if _, err := chatCall(ctx, ch, func(ctx context.Context) (chat.Ref, error) { return t.Send(ctx, b.AccountID, card) }); err != nil {
		text := chatDetail(err.Error())
		slog.Warn("发送测试卡片失败", "channel", ch, "user", u.ID, "err", text)
		return apimsg.Errorf("测试卡片发送失败:%s", text)
	}
	return nil
}
