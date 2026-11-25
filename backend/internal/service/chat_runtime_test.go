package service

// 聊天渠道的连接监督(.scratch/chat-approval-cards/PRD.md §5.6)。设置页那一侧在
// internal/bootstrap/chat_settings_test.go。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
	gormlogger "gorm.io/gorm/logger"

	"aegisdb/internal/chat"
	"aegisdb/internal/chat/lark"
	"aegisdb/internal/chat/telegram"
	"aegisdb/internal/dto"
	"aegisdb/internal/model"
	"aegisdb/pkg/crypto"
)

const runSecret = "s3cr3t-app-secret"

// runTransport 是渠道的替身:每次 Run 由 run 决定怎么结束。
type runTransport struct {
	cfg ChatConfig
	run func(ctx context.Context, nth int, onState func(chat.State, string)) error

	mu   sync.Mutex
	runs int
}

func (f *runTransport) BotID() string   { return f.cfg.AppID }
func (f *runTransport) BotName() string { return "bot " + f.cfg.AppID }
func (f *runTransport) Run(ctx context.Context, _ chat.Handler, onState func(chat.State, string)) error {
	f.mu.Lock()
	f.runs++
	n := f.runs
	f.mu.Unlock()
	return f.run(ctx, n, onState)
}
func (f *runTransport) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.runs
}
func (f *runTransport) Send(context.Context, string, chat.Card) (chat.Ref, error) {
	return chat.Ref{}, errors.New("down")
}
func (f *runTransport) Update(context.Context, chat.Ref, chat.Card) error { return errors.New("down") }

// stayUp 连上并一直连着。
func stayUp(ctx context.Context, _ int, onState func(chat.State, string)) error {
	onState(chat.StateConnected, "")
	<-ctx.Done()
	return ctx.Err()
}

type runRig struct {
	*Services
	t *testing.T

	mu    sync.Mutex
	built []*runTransport
}

// runApp 起一个渠道管理在跑的网关,Lark 默认关着。run 决定每个渠道实例怎么跑。
func runApp(t *testing.T, tune func(*chatTimings), factory func(r *runRig, cfg ChatConfig) chat.Transport) *runRig {
	t.Helper()
	crypto.SetSecretKey("chat-runtime-test")
	old := chatTiming
	chatTiming = chatTimings{poll: time.Hour, sweep: time.Hour, stop: 5 * time.Second,
		backoff: time.Millisecond, backoffMax: 5 * time.Millisecond, stable: time.Minute}
	if tune != nil {
		tune(&chatTiming)
	}
	s, _ := cardDB(t)
	r := &runRig{Services: s, t: t}
	s.SetChatFactory(func(ch chat.Channel, cfg ChatConfig) chat.Transport {
		if (ch == chat.Telegram) != (cfg.BotToken != "") || (ch == chat.Lark) != (cfg.AppID != "") {
			t.Errorf("不该建渠道 %s", ch)
		}
		return factory(r, cfg)
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.RunChat(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("RunChat 没有停下")
		}
		chatTiming = old
	})
	return r
}

func scripted(run func(ctx context.Context, nth int, onState func(chat.State, string)) error) func(*runRig, ChatConfig) chat.Transport {
	return func(r *runRig, cfg ChatConfig) chat.Transport {
		f := &runTransport{cfg: cfg, run: run}
		r.mu.Lock()
		defer r.mu.Unlock()
		r.built = append(r.built, f)
		return f
	}
}

func (r *runRig) transports() []*runTransport {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.built)
}

func (r *runRig) set(key string, v any) {
	r.t.Helper()
	if key == "chat.lark.appSecret" || strings.HasPrefix(key, "chat.telegram.") && strings.HasSuffix(key, "Token") {
		enc, err := crypto.EncryptSecret(v.(string))
		if err != nil {
			r.t.Fatal(err)
		}
		v = enc
	}
	b, _ := json.Marshal(v)
	if err := r.Repo.SetSetting(key, string(b)); err != nil {
		r.t.Fatal(err)
	}
}

// enable 按 appID 打开 Lark 并叫醒管理协程。
func (r *runRig) enable(appID string) {
	r.t.Helper()
	r.set("chat.lark.appId", appID)
	r.set("chat.lark.appSecret", runSecret)
	r.set("chat.lark.region", "lark")
	r.set("chat.lark.enabled", true)
	r.ReloadChat()
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(2 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("没等到:%s", what)
}

func (r *runRig) waitState(st chat.State) {
	r.t.Helper()
	waitFor(r.t, "状态 "+string(st), func() bool { return r.ChatStatus()[chat.Lark].State == st })
}

// captureLogs 把 slog 与 GORM 的日志收进来。
func captureLogs(t *testing.T, s *Services) func() string {
	t.Helper()
	var mu sync.Mutex
	var buf bytes.Buffer
	w := writerFunc(func(p []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		return buf.Write(p)
	})
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(w, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	if s != nil {
		s.Repo.DB().Logger = gormlogger.New(log.New(w, "", 0), gormlogger.Config{LogLevel: gormlogger.Warn, SlowThreshold: time.Minute})
	}
	return func() string {
		mu.Lock()
		defer mu.Unlock()
		return buf.String()
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// 关着的渠道不建、状态是 off;打开后按设置建(密钥解了密),连上;关掉后取消连接、渠道撤下。
func TestRunChat_OnOff(t *testing.T) {
	stopped := make(chan struct{})
	r := runApp(t, nil, scripted(func(ctx context.Context, nth int, onState func(chat.State, string)) error {
		defer close(stopped)
		return stayUp(ctx, nth, onState)
	}))
	for _, ch := range chat.Channels {
		if st := r.ChatStatus()[ch]; st.State != chat.StateOff || st.Enabled {
			t.Errorf("%s: %+v", ch, st)
		}
	}
	r.enable("cli_a")
	r.waitState(chat.StateConnected)
	got := r.transports()
	if len(got) != 1 || got[0].cfg != (ChatConfig{Enabled: true, Region: "lark", AppID: "cli_a", AppSecret: runSecret}) {
		t.Fatalf("建出来的渠道: %+v", got)
	}
	if st := r.ChatStatus()[chat.Lark]; !st.Enabled || st.BotName != "bot cli_a" || st.Detail != "" {
		t.Errorf("状态: %+v", st)
	}
	if r.chatTransport(chat.Lark) != chat.Transport(got[0]) {
		t.Error("渠道没有换上")
	}
	r.set("chat.lark.enabled", false)
	r.ReloadChat()
	r.waitState(chat.StateOff)
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("旧连接没有被取消")
	}
	if r.chatTransport(chat.Lark) != nil {
		t.Error("关掉之后渠道还在")
	}
	if st := r.ChatStatus()[chat.Lark]; st.Enabled || st.BotName != "" {
		t.Errorf("状态: %+v", st)
	}
}

// 保存同样的设置、改无关的设置,不重建;密钥换了才重建。
func TestRunChat_RebuildsOnlyOnChange(t *testing.T) {
	r := runApp(t, func(c *chatTimings) { c.poll = 2 * time.Millisecond }, scripted(stayUp))
	r.enable("cli_a")
	r.waitState(chat.StateConnected)
	r.set("chat.cardLang", "en")
	r.set("chat.lark.appSecret", runSecret) // 重新加密,密文不同
	r.ReloadChat()
	time.Sleep(50 * time.Millisecond)
	if n := len(r.transports()); n != 1 {
		t.Fatalf("没变也重建了:%d", n)
	}
	r.set("chat.lark.appSecret", "another-secret") // 不叫醒:别处改的设置靠轮询看到
	waitFor(t, "换密钥后重建", func() bool { return len(r.transports()) == 2 })
	if got := r.transports()[1].cfg.AppSecret; got != "another-secret" {
		t.Errorf("新渠道的密钥 %q", got)
	}
}

// 换配置:先取消旧连接、等它停下,再起新的;旧的停下之前渠道仍在(这期间建的单照样排队)。
func TestRunChat_SwapWaitsForOldToStop(t *testing.T) {
	release := make(chan struct{})
	var late func(chat.State, string)
	var mu sync.Mutex
	r := runApp(t, nil, func(r *runRig, cfg ChatConfig) chat.Transport {
		run := stayUp
		if cfg.AppID == "cli_old" {
			run = func(ctx context.Context, _ int, onState func(chat.State, string)) error {
				onState(chat.StateConnected, "")
				mu.Lock()
				late = onState
				mu.Unlock()
				<-ctx.Done()
				<-release
				return ctx.Err()
			}
		}
		return scripted(run)(r, cfg)
	})
	r.enable("cli_old")
	r.waitState(chat.StateConnected)
	r.enable("cli_new")
	time.Sleep(50 * time.Millisecond)
	if n := len(r.transports()); n != 1 {
		t.Fatalf("旧的还没停就建了新的:%d", n)
	}
	if r.chatBotID(chat.Lark) != "cli_old" {
		t.Error("旧的停下之前渠道就撤了")
	}
	close(release)
	waitFor(t, "换上新渠道", func() bool { return r.chatBotID(chat.Lark) == "cli_new" })
	r.waitState(chat.StateConnected)
	// 旧渠道迟到的状态不算数
	mu.Lock()
	late(chat.StateConflict, "late")
	mu.Unlock()
	if st := r.ChatStatus()[chat.Lark]; st.State != chat.StateConnected || st.Detail != "" {
		t.Errorf("迟到的状态盖住了新的: %+v", st)
	}
}

// 旧连接不理取消:等到时限就照样起新的,并留一行日志;它之后的状态与结束都不影响新的。
func TestRunChat_StuckOldDoesNotBlock(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	logs := captureLogs(t, nil)
	r := runApp(t, func(c *chatTimings) { c.stop = 20 * time.Millisecond }, func(r *runRig, cfg ChatConfig) chat.Transport {
		run := stayUp
		if cfg.AppID == "cli_old" {
			run = func(_ context.Context, _ int, onState func(chat.State, string)) error {
				onState(chat.StateConnected, "")
				<-release
				return errors.New("finally")
			}
		}
		return scripted(run)(r, cfg)
	})
	r.enable("cli_old")
	r.waitState(chat.StateConnected)
	r.enable("cli_new")
	waitFor(t, "换上新渠道", func() bool { return r.chatBotID(chat.Lark) == "cli_new" })
	r.waitState(chat.StateConnected)
	if !strings.Contains(logs(), "旧连接") {
		t.Errorf("没有留下日志: %s", logs())
	}
}

// 断开、panic 都退避重连,状态看得到原因(截到 200 字、没有 panic 的内容),连上后恢复。
func TestRunChat_ReconnectsAfterErrorAndPanic(t *testing.T) {
	gate := make(chan struct{})
	logs := captureLogs(t, nil)
	r := runApp(t, func(c *chatTimings) { c.backoff, c.backoffMax = 30*time.Millisecond, 30*time.Millisecond }, scripted(
		func(ctx context.Context, nth int, onState func(chat.State, string)) error {
			onState(chat.StateConnecting, "")
			switch nth {
			case 1:
				return errors.New("dial\nfailed " + strings.Repeat("长", 300))
			case 2:
				panic("token=" + runSecret)
			case 3:
				return nil // 没有错误的断开也是断开
			}
			<-gate
			return stayUp(ctx, nth, onState)
		}))
	r.enable("cli_a")
	waitFor(t, "出错的原因", func() bool {
		st := r.ChatStatus()[chat.Lark]
		return st.State == chat.StateError && strings.HasPrefix(st.Detail, "dial failed 长")
	})
	if d := r.ChatStatus()[chat.Lark].Detail; strings.HasPrefix(d, "dial") && utf8.RuneCountInString(d) > 200 {
		t.Errorf("原因没截短:%d 字", utf8.RuneCountInString(d))
	}
	waitFor(t, "panic 的状态", func() bool {
		st := r.ChatStatus()[chat.Lark]
		return st.State == chat.StateError && strings.Contains(st.Detail, "panic")
	})
	if d := r.ChatStatus()[chat.Lark].Detail; strings.Contains(d, runSecret) {
		t.Errorf("panic 的内容进了状态: %s", d)
	}
	if r.chatTransport(chat.Lark) == nil {
		t.Error("断开期间渠道撤下了:发送本该照样试")
	}
	close(gate)
	r.waitState(chat.StateConnected)
	if n := len(r.transports()); n != 1 {
		t.Errorf("重连不该重建渠道:%d", n)
	}
	if strings.Contains(logs(), runSecret) {
		t.Errorf("密钥进了日志: %s", logs())
	}
}

// 冲突:Run 返回之后状态仍是冲突,不被「出错」盖掉;退避重试。
func TestRunChat_KeepsConflict(t *testing.T) {
	r := runApp(t, func(c *chatTimings) { c.backoff, c.backoffMax = time.Hour, time.Hour }, scripted(
		func(_ context.Context, _ int, onState func(chat.State, string)) error {
			onState(chat.StateConnecting, "")
			onState(chat.StateConflict, "lark: too many connections")
			return errors.New("lark: too many connections")
		}))
	r.enable("cli_a")
	waitFor(t, "第一次 Run 结束", func() bool { return len(r.transports()) == 1 && r.transports()[0].count() == 1 })
	time.Sleep(30 * time.Millisecond)
	if st := r.ChatStatus()[chat.Lark]; st.State != chat.StateConflict || st.Detail != "lark: too many connections" {
		t.Errorf("状态: %+v", st)
	}
}

// Run 已经返回之后才来的状态不算数:连接没了,不能还显示已连接。
func TestRunChat_IgnoresStateAfterReturn(t *testing.T) {
	ch := make(chan func(chat.State, string), 1)
	r := runApp(t, func(c *chatTimings) { c.backoff, c.backoffMax = time.Hour, time.Hour }, scripted(
		func(_ context.Context, _ int, onState func(chat.State, string)) error {
			ch <- onState
			return errors.New("closed")
		}))
	r.enable("cli_a")
	r.waitState(chat.StateError)
	(<-ch)(chat.StateConnected, "")
	if st := r.ChatStatus()[chat.Lark]; st.State != chat.StateError {
		t.Errorf("状态: %+v", st)
	}
}

// 工厂 panic:恢复、退避再试,别的照常。
func TestRunChat_FactoryPanicIsRetried(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	logs := captureLogs(t, nil)
	r := runApp(t, nil, func(r *runRig, cfg ChatConfig) chat.Transport {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		switch n {
		case 2:
			panic(cfg.AppSecret)
		case 3:
			return nil
		case 4:
			// 换的新渠道没建成,旧的不能还顶在那里
			if bot := r.chatBotID(chat.Lark); bot != "" {
				t.Errorf("还登记着 %s", bot)
			}
		}
		return scripted(stayUp)(r, cfg)
	})
	r.enable("cli_a")
	r.waitState(chat.StateConnected)
	r.enable("cli_b")
	waitFor(t, "换上新渠道", func() bool { return r.chatBotID(chat.Lark) == "cli_b" })
	r.waitState(chat.StateConnected)
	if strings.Contains(logs(), runSecret) {
		t.Errorf("密钥进了日志: %s", logs())
	}
}

// 旧配置的渠道建得太慢,建好时已经换了配置:不登记,不能盖掉新的。
func TestRunChat_LateFactoryDoesNotRegister(t *testing.T) {
	release, late := make(chan struct{}), make(chan struct{})
	r := runApp(t, func(c *chatTimings) { c.stop = 20 * time.Millisecond }, func(r *runRig, cfg ChatConfig) chat.Transport {
		if cfg.AppID == "cli_old" {
			<-release
			defer close(late)
		}
		return scripted(stayUp)(r, cfg)
	})
	r.enable("cli_old")
	r.waitState(chat.StateConnecting)
	r.enable("cli_new")
	r.waitState(chat.StateConnected)
	close(release)
	<-late
	time.Sleep(30 * time.Millisecond)
	if bot := r.chatBotID(chat.Lark); bot != "cli_new" {
		t.Errorf("登记着的是 %q", bot)
	}
	if st := r.ChatStatus()[chat.Lark]; st.State != chat.StateConnected {
		t.Errorf("状态: %+v", st)
	}
}

// 密钥解不开、缺必填项(直接改了库):不连,状态写明原因,不带细节;改好了就连上。
func TestRunChat_BadConfig(t *testing.T) {
	r := runApp(t, nil, scripted(stayUp))
	r.enable("cli_a")
	r.waitState(chat.StateConnected)
	if err := r.Repo.SetSetting("chat.lark.appSecret", `"enc:v2:bm90LXJlYWxseS1lbmNyeXB0ZWQtYXQtYWxsLXNvcnJ5"`); err != nil {
		t.Fatal(err)
	}
	r.ReloadChat()
	waitFor(t, "密钥解不开的状态", func() bool {
		st := r.ChatStatus()[chat.Lark]
		return st.State == chat.StateError && st.Detail == "密钥无法解密" && st.DetailRef != nil && st.Enabled
	})
	if r.chatTransport(chat.Lark) != nil {
		t.Error("密钥解不开,旧渠道还在")
	}
	r.set("chat.lark.appSecret", "")
	r.ReloadChat()
	waitFor(t, "缺密钥的状态", func() bool {
		st := r.ChatStatus()[chat.Lark]
		return st.State == chat.StateError && strings.Contains(st.Detail, "App Secret")
	})
	if n := len(r.transports()); n != 1 {
		t.Errorf("配置不能用却建了渠道:%d", n)
	}
	r.set("chat.lark.appSecret", runSecret)
	r.ReloadChat()
	r.waitState(chat.StateConnected)
}

// 进程收尾:ctx 取消,RunChat 返回,渠道撤下;再起一次照常。重复起的第二个立刻返回。
func TestRunChat_StopsAndRestarts(t *testing.T) {
	old := chatTiming
	chatTiming = chatTimings{poll: time.Hour, sweep: time.Hour, stop: time.Second, backoff: time.Millisecond, backoffMax: time.Millisecond, stable: time.Minute}
	t.Cleanup(func() { chatTiming = old })
	crypto.SetSecretKey("chat-runtime-test")
	s, _ := cardDB(t)
	r := &runRig{Services: s, t: t}
	s.SetChatFactory(func(_ chat.Channel, cfg ChatConfig) chat.Transport { return scripted(stayUp)(r, cfg) })
	r.enable("cli_a") // 没在跑:什么也不做,也不 panic
	for range 2 {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { defer close(done); s.RunChat(ctx) }()
		r.waitState(chat.StateConnected)
		s.RunChat(ctx) // 已经在跑
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("RunChat 没有返回")
		}
		if s.chatTransport(chat.Lark) != nil || r.ChatStatus()[chat.Lark].State != chat.StateOff {
			t.Errorf("停下之后: %+v", r.ChatStatus()[chat.Lark])
		}
	}
}

// 起着的时候每轮扫描一次,不等上一轮:一轮卡在渠道里,别的渠道照常。
func TestRunChat_Sweeps(t *testing.T) {
	r := runApp(t, func(c *chatTimings) { c.sweep = 5 * time.Millisecond }, scripted(stayUp))
	r.enable("cli_a")
	r.waitState(chat.StateConnected)
	// 单子不存在的卡片行,扫到就会收场
	row := model.ChatCard{ApprovalID: 999, UserID: 1, Channel: string(chat.Lark), BotID: "cli_a", State: model.ChatCardQueued, NextAt: time.Now()}
	if err := r.Repo.DB().Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	waitFor(t, "扫描收场", func() bool {
		var now model.ChatCard
		return r.Repo.DB().Limit(1).Find(&now, row.ID).Error == nil && now.State == model.ChatCardClosed
	})
}

// 退避 1s、2s、4s……60s 封顶;默认时限。
func TestChatReconnectBackoff(t *testing.T) {
	var got []time.Duration
	for n := range 9 {
		got = append(got, chatTiming.after(n)/time.Second)
	}
	if want := []time.Duration{1, 2, 4, 8, 16, 32, 60, 60, 60}; !slices.Equal(got, want) {
		t.Errorf("退避 %v", got)
	}
	if chatTiming.after(1000) != time.Minute {
		t.Error("次数很多时也是 60 秒")
	}
	if chatTiming.poll != 5*time.Second || chatTiming.sweep != 5*time.Second || chatTiming.stable != time.Minute {
		t.Errorf("时限: %+v", chatTiming)
	}
}

// 连着超过 stable 才把退避清零:刚连上就断的不算。
func TestRunChat_BackoffResetsOnlyWhenStable(t *testing.T) {
	for _, c := range []struct {
		name   string
		stable time.Duration
		reset  bool
	}{{"连得够久", time.Nanosecond, true}, {"刚连上就断", time.Hour, false}} {
		t.Run(c.name, func(t *testing.T) {
			r := runApp(t, func(ct *chatTimings) {
				ct.backoff, ct.backoffMax, ct.stable = time.Millisecond, time.Hour, c.stable
			}, scripted(func(_ context.Context, _ int, onState func(chat.State, string)) error {
				onState(chat.StateConnected, "")
				time.Sleep(time.Millisecond)
				return errors.New("closed")
			}))
			r.enable("cli_a")
			// 每次清零:间隔恒为 1ms;不清零:第 12 次之前就要等 1+2+…+1024 ms 以上
			time.Sleep(400 * time.Millisecond)
			n := r.transports()[0].count()
			if c.reset && n < 15 || !c.reset && n > 12 {
				t.Errorf("400ms 里重连了 %d 次", n)
			}
		})
	}
}

// 轮询的渠道每一轮都报一次「已连接」,连上的时长要从第一次算起;离开已连接再回来,几段加在一起。
func TestRunChat_UptimeAccumulates(t *testing.T) {
	for name, report := range map[string]func(func(chat.State, string)){
		"一直连着,反复报": func(on func(chat.State, string)) {
			for range 6 {
				on(chat.StateConnected, "")
				time.Sleep(10 * time.Millisecond)
			}
		},
		"断断续续": func(on func(chat.State, string)) {
			for range 6 {
				on(chat.StateConnected, "")
				time.Sleep(10 * time.Millisecond)
				on(chat.StateError, "too many requests")
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := runApp(t, func(ct *chatTimings) {
				ct.backoff, ct.backoffMax, ct.stable = time.Millisecond, time.Hour, 40*time.Millisecond
			}, scripted(func(_ context.Context, _ int, on func(chat.State, string)) error {
				report(on)
				return errors.New("closed")
			}))
			r.enable("cli_a")
			// 不清零的话,第 20 次之前光等就要 1+2+…+2^18 ms
			waitFor(t, "退避清零", func() bool { tr := r.transports(); return len(tr) == 1 && tr[0].count() >= 20 })
		})
	}
}

// 重连期间看得到上一次为什么断;连上就清掉,换了配置也清掉。
func TestRunChat_KeepsReasonWhileReconnecting(t *testing.T) {
	gate := make(chan struct{})
	r := runApp(t, nil, scripted(func(ctx context.Context, nth int, on func(chat.State, string)) error {
		on(chat.StateConnecting, "")
		if nth == 1 {
			on(chat.StateConnected, "")
			return errors.New("closed by peer")
		}
		select {
		case <-gate:
		case <-ctx.Done():
			return ctx.Err()
		}
		return stayUp(ctx, nth, on)
	}))
	r.enable("cli_a")
	waitFor(t, "第二次在连", func() bool { tr := r.transports(); return len(tr) == 1 && tr[0].count() == 2 })
	waitFor(t, "带着原因的「连接中」", func() bool {
		st := r.ChatStatus()[chat.Lark]
		return st.State == chat.StateConnecting && st.Detail == "closed by peer"
	})
	gate <- struct{}{}
	r.waitState(chat.StateConnected)
	if d := r.ChatStatus()[chat.Lark].Detail; d != "" {
		t.Errorf("连上之后还留着原因: %s", d)
	}
}

const runToken = "987654321:AAH_TOKEN-do-not-leak-0123456789xyz"

func (r *runRig) tgStatus() dto.ChatStatus { return r.ChatStatus()[chat.Telegram] }

// Telegram 的配置:机密解了密,代理空着就是没有;解不开、缺令牌、格式不对(直接改了库)不连,说明原因。
func TestRunChat_TelegramConfig(t *testing.T) {
	r := runApp(t, nil, scripted(stayUp))
	logs := captureLogs(t, nil)
	r.set("chat.telegram.botToken", runToken)
	r.set("chat.telegram.proxyToken", "socks5://u:px-do-not-leak@10.0.0.5:1080")
	r.set("chat.telegram.enabled", true)
	r.ReloadChat()
	waitFor(t, "连上", func() bool { return r.tgStatus().State == chat.StateConnected })
	if got := r.transports(); len(got) != 1 || got[0].cfg != (ChatConfig{Enabled: true, BotToken: runToken, Proxy: "socks5://u:px-do-not-leak@10.0.0.5:1080"}) {
		t.Fatalf("配置: %+v", got[0].cfg)
	}
	if st := r.ChatStatus()[chat.Lark]; st.State != chat.StateOff || st.Enabled {
		t.Errorf("另一个渠道: %+v", st)
	}
	problem := func(what string) {
		t.Helper()
		r.ReloadChat()
		waitFor(t, what, func() bool {
			st := r.tgStatus()
			return st.State == chat.StateError && strings.Contains(st.Detail, what) && st.DetailRef != nil && st.Enabled && !strings.Contains(st.Detail, "do-not-leak")
		})
		if r.chatTransport(chat.Telegram) != nil {
			t.Errorf("%s:渠道还在", what)
		}
	}
	const broken = `"enc:v2:bm90LXJlYWxseS1lbmNyeXB0ZWQtYXQtYWxsLXNvcnJ5"`
	if err := r.Repo.SetSetting("chat.telegram.proxyToken", broken); err != nil {
		t.Fatal(err)
	}
	problem("密钥无法解密")
	r.set("chat.telegram.proxyToken", "gopher://do-not-leak")
	problem("代理")
	r.set("chat.telegram.proxyToken", "")
	r.set("chat.telegram.botToken", "bad do-not-leak")
	problem("格式")
	r.set("chat.telegram.botToken", "")
	problem("没有填写 Bot Token")
	if err := r.Repo.SetSetting("chat.telegram.botToken", broken); err != nil {
		t.Fatal(err)
	}
	problem("密钥无法解密")
	r.set("chat.telegram.botToken", runToken)
	r.ReloadChat()
	waitFor(t, "改好了就连上", func() bool { return r.tgStatus().State == chat.StateConnected })
	if got := r.transports(); len(got) != 2 || got[1].cfg != (ChatConfig{Enabled: true, BotToken: runToken}) {
		t.Errorf("重建: %d", len(got))
	}
	if strings.Contains(logs(), "do-not-leak") {
		t.Errorf("机密进了日志: %s", logs())
	}
}

// 真的 Telegram 渠道接上本地的假平台:令牌不对时状态说人话;没认出机器人之前渠道在、标识为空,排着的
// 卡片等着;认出之后才发。日志与状态里没有令牌。
func TestRunChat_TelegramEndToEnd(t *testing.T) {
	var mu sync.Mutex
	ready := false
	var sent []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch method := strings.TrimPrefix(req.URL.Path, "/bot"+runToken+"/"); {
		case !ready:
			w.WriteHeader(401)
			fmt.Fprint(w, `{"ok":false,"error_code":401,"description":"Unauthorized"}`)
		case method == "getMe":
			fmt.Fprint(w, `{"ok":true,"result":{"id":987654321,"is_bot":true,"username":"aegis_bot"}}`)
		case method == "getWebhookInfo":
			fmt.Fprint(w, `{"ok":true,"result":{"url":""}}`)
		case method == "sendMessage":
			b, _ := io.ReadAll(req.Body)
			sent = append(sent, string(b))
			fmt.Fprint(w, `{"ok":true,"result":{"message_id":7,"chat":{"id":4242,"type":"private"}}}`)
		default:
			fmt.Fprint(w, `{"ok":true,"result":[]}`)
		}
	}))
	t.Cleanup(srv.Close)
	logs := captureLogs(t, nil)
	r := runApp(t, nil, func(r *runRig, cfg ChatConfig) chat.Transport {
		return r.newTelegram(cfg, telegram.WithBaseURL(srv.URL))
	})
	r.set("chat.telegram.botToken", runToken)
	r.set("chat.telegram.enabled", true)
	r.ReloadChat()
	waitFor(t, "令牌不对", func() bool {
		st := r.tgStatus()
		return strings.Contains(st.Detail, "令牌无效") && st.DetailRef != nil && st.BotName == ""
	})
	if r.chatTransport(chat.Telegram) == nil || r.chatBotID(chat.Telegram) != "" {
		t.Errorf("没认出之前:%q", r.chatBotID(chat.Telegram))
	}
	db := r.Repo.DB()
	ap := cardTicket(nil)
	row := &model.ChatCard{ApprovalID: 1, UserID: 3, Channel: "telegram", BotID: "987654321", State: model.ChatCardQueued, NextAt: time.Now().Add(-time.Second)}
	for _, v := range []any{ap, row, &model.User{ID: 3, Email: "a@b.c", Status: "active"},
		&model.ChatBinding{UserID: 3, Channel: "telegram", BotID: "987654321", AccountID: "4242", ChatID: "4242"}} {
		if err := db.Create(v).Error; err != nil {
			t.Fatal(err)
		}
	}
	state := func() (c model.ChatCard) { db.Limit(1).Find(&c, row.ID); return c }
	r.SweepChatCards(context.Background())
	if c := state(); c.State != model.ChatCardQueued || c.Attempts != 0 {
		t.Errorf("没就绪时动了: %+v", c)
	}
	mu.Lock()
	ready = true
	mu.Unlock()
	waitFor(t, "连上", func() bool {
		st := r.tgStatus()
		return st.State == chat.StateConnected && st.BotName == "aegis_bot" && st.Detail == ""
	})
	r.SweepChatCards(context.Background())
	mu.Lock()
	defer mu.Unlock()
	if c := state(); c.State != model.ChatCardSent || c.ChatID != "4242" || c.MessageID != "7" || len(sent) != 1 || strings.Contains(sent[0], "DROP") {
		t.Errorf("就绪之后: %+v %v", c, sent)
	}
	if out := logs(); strings.Contains(out, "do-not-leak") || strings.Contains(out, runToken) {
		t.Errorf("令牌进了日志: %s", out)
	}
}

// 同一个机器人前后两个连接(存设置、换代理)认得同一批 update;换了机器人不相干。
func TestNewTelegram_SharesSeen(t *testing.T) {
	var polls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case strings.HasSuffix(req.URL.Path, "getMe"):
			fmt.Fprint(w, `{"ok":true,"result":{"id":987654321,"username":"b"}}`)
		case strings.HasSuffix(req.URL.Path, "getWebhookInfo"):
			fmt.Fprint(w, `{"ok":true,"result":{"url":""}}`)
		case strings.HasSuffix(req.URL.Path, "getUpdates"):
			polls.Add(1)
			fmt.Fprint(w, `{"ok":true,"result":[{"update_id":5,"message":{"from":{"id":7},"chat":{"id":7,"type":"private"},"text":"AB2CD3EF4G"}}]}`)
		default:
			fmt.Fprint(w, `{"ok":true,"result":true}`)
		}
	}))
	t.Cleanup(srv.Close)
	s, _ := cardDB(t)
	var texts atomic.Int64
	run := func(token string) {
		t.Helper()
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		tr := s.newTelegram(ChatConfig{BotToken: token}, telegram.WithBaseURL(srv.URL))
		go func() {
			defer close(done)
			_ = tr.Run(ctx, countTexts{&texts}, func(chat.State, string) {})
		}()
		n := polls.Load()
		waitFor(t, "收了几次", func() bool { return polls.Load() >= n+2 })
		cancel()
		<-done
	}
	run(runToken)
	run(runToken + "x") // 换过令牌,还是同一个机器人
	if texts.Load() != 1 {
		t.Errorf("处理了 %d 次", texts.Load())
	}
	run("1234567:" + strings.Repeat("a", 35))
	if texts.Load() != 2 {
		t.Errorf("别的机器人不相干:%d", texts.Load())
	}
}

type countTexts struct{ n *atomic.Int64 }

func (c countTexts) OnText(context.Context, chat.Channel, string, string, string, string) (string, bool) {
	c.n.Add(1)
	return "", false
}
func (countTexts) OnClick(context.Context, chat.Click) chat.ClickResult { return chat.ClickResult{} }

// 令牌不对、机器人挂着 webhook 这两种,管理员改得了:状态里说人话,能随界面语言走。
func TestRunChat_ReasonsInPlainWords(t *testing.T) {
	for want, cause := range map[string]error{"令牌无效": chat.ErrCredentials, "webhook": chat.ErrWebhook} {
		r := runApp(t, func(c *chatTimings) { c.backoff, c.backoffMax = time.Hour, time.Hour }, scripted(
			func(context.Context, int, func(chat.State, string)) error {
				return fmt.Errorf("telegram: getMe: %w", cause)
			}))
		r.enable("cli_a")
		r.waitState(chat.StateError)
		if st := r.ChatStatus()[chat.Lark]; !strings.Contains(st.Detail, want) || st.DetailRef == nil {
			t.Errorf("%v → %+v", cause, st)
		}
	}
}

// 二次确认的应答拖得太久(库卡住),扫描等它的时间已经不够:不给按钮,请对方再点。
func TestHoldChatCard_TooSlow(t *testing.T) {
	s, db := cardDB(t)
	ap := &model.Approval{ApNo: "AP-1", Status: model.StatusPending}
	row := &model.ChatCard{UserID: 1, Channel: "telegram", State: model.ChatCardSent, NextAt: time.Now()}
	if err := s.Repo.CreateApproval(ap, nil); err != nil {
		t.Fatal(err)
	}
	row.ApprovalID = ap.ID
	if err := db.Create(row).Error; err != nil {
		t.Fatal(err)
	}
	if now, err := s.holdChatCard(row); err != nil || now.Status != model.StatusPending {
		t.Fatalf("%+v %v", now, err)
	}
	old := chatAnswerSlack
	chatAnswerSlack = 0
	t.Cleanup(func() { chatAnswerSlack = old })
	if _, err := s.holdChatCard(row); err == nil {
		t.Error("来不及了却照样放行")
	}
}

// 库挂住的时候,点击也要在 3 秒内得到「正在处理」的应答,且用的是设置的语言:这句话不能再去读库。
func TestLarkClick_AnswersWhileStoreIsStuck(t *testing.T) {
	conns := make(chan *websocket.Conn, 1)
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/callback/ws/endpoint", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"code":0,"data":{"URL":"%s/ws?service_id=7","ClientConfig":{"PingInterval":120}}}`, strings.Replace(srv.URL, "http", "ws", 1))
	})
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		if c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil); err == nil {
			conns <- c
		}
	})
	r := runApp(t, nil, func(r *runRig, cfg ChatConfig) chat.Transport { return r.newLark(cfg, lark.WithBaseURL(srv.URL)) })
	r.set("chat.cardLang", "en")
	r.enable("cli_a")
	r.waitState(chat.StateConnected)
	ws := <-conns
	t.Cleanup(func() { ws.Close() })

	event, _ := json.Marshal(map[string]any{"schema": "2.0",
		"header": map[string]any{"event_id": "c1", "event_type": "card.action.trigger", "app_id": "cli_a"},
		"event": map[string]any{"operator": map[string]any{"open_id": "ou_9"}, "host": "im_message",
			"action":  map[string]any{"tag": "button", "value": map[string]any{"t": strings.Repeat("A", 32), "a": "confirm"}},
			"context": map[string]any{"open_message_id": "om_1", "open_chat_id": "oc_1"}}})
	frame := larkws.Frame{Method: int32(larkws.FrameTypeData), Payload: event, Headers: []larkws.Header{
		{Key: "type", Value: "event"}, {Key: "message_id", Value: "f1"}, {Key: "sum", Value: "1"}, {Key: "seq", Value: "0"}}}
	raw, err := frame.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	tx := r.Repo.DB().Begin() // 占住唯一的连接,谁也读不了库
	defer tx.Rollback()
	began := time.Now()
	if err := ws.WriteMessage(websocket.BinaryMessage, raw); err != nil {
		t.Fatal(err)
	}
	_ = ws.SetReadDeadline(began.Add(3 * time.Second))
	for {
		_, msg, err := ws.ReadMessage()
		if err != nil {
			t.Fatalf("3 秒内没有应答:%v", err)
		}
		var got larkws.Frame
		if err := got.Unmarshal(msg); err != nil {
			t.Fatal(err)
		}
		if larkws.Headers(got.Headers).GetString("message_id") != "f1" {
			continue // 心跳
		}
		var resp larkws.Response
		if err := json.Unmarshal(got.Payload, &resp); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(resp.Data), chat.Tr("en", "click.processing")) {
			t.Errorf("应答: %s", resp.Data)
		}
		return
	}
}

// 老库里没有 chat.* 这些键:按默认值,且 GORM 不为它们每轮记一行 record not found。
func TestChatSettings_MissingKeysAreQuiet(t *testing.T) {
	s, _ := cardDB(t)
	logs := captureLogs(t, s)
	if s.cardLang() != "zh" {
		t.Errorf("默认语言 %q", s.cardLang())
	}
	if st := s.ChatStatus()[chat.Lark]; st.Enabled || st.State != chat.StateOff {
		t.Errorf("默认状态 %+v", st)
	}
	s.SetChatTransport(chat.Lark, &runTransport{cfg: ChatConfig{AppID: "cli_a"}})
	s.SweepChatCards(context.Background())
	if logs() != "" {
		t.Errorf("写了日志: %s", logs())
	}
	for raw, want := range map[string]string{`"en"`: "en", `"zh"`: "zh", `""`: "zh"} {
		if err := s.Repo.SetSetting("chat.cardLang", raw); err != nil {
			t.Fatal(err)
		}
		if got := s.cardLang(); got != want {
			t.Errorf("%s → %q", raw, got)
		}
	}
}
