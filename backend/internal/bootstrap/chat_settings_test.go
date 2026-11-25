package bootstrap

// 聊天渠道的设置、状态与测试卡片(.scratch/chat-approval-cards/PRD.md §5.6、§5.7)。连接管理本身的
// 用例(退避、panic、换连接)在 internal/service/chat_runtime_test.go。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"

	"aegisdb/internal/chat"
	"aegisdb/internal/model"
	"aegisdb/internal/service"
	"aegisdb/pkg/crypto"
)

const (
	settingsURL   = "/api/v1/settings"
	chatStatusURL = "/api/v1/settings/chat-status"
	chatTestURL   = "/api/v1/settings/chat-test"
	larkSecret    = "lark-app-secret-0123456789"
	tgToken       = "123456789:AAtelegram-bot-token_0123456789abc"
	tgProxy       = "socks5://pxuser:px-pass-0123456789@proxy.corp.example:1080"
)

// runChat 是能观察 Run 的渠道替身。
type runChat struct {
	*fakeChat
	cfg     service.ChatConfig
	run     func(ctx context.Context, onState func(chat.State, string)) error // nil = 连上并一直连着
	stopped chan struct{}
	once    sync.Once
}

func (r *runChat) Run(ctx context.Context, h chat.Handler, onState func(chat.State, string)) error {
	if r.run != nil {
		return r.run(ctx, onState)
	}
	defer r.once.Do(func() { close(r.stopped) })
	return r.fakeChat.Run(ctx, h, onState)
}

// runChatApp 起一个管着聊天渠道的网关;shape 可以改每个建出来的渠道。返回的函数给出到目前为止建过的渠道。
func runChatApp(t *testing.T, shape func(*runChat)) (*testApp, func() []*runChat) {
	t.Helper()
	app := newTestApp(t)
	var mu sync.Mutex
	var built []*runChat
	app.svc.SetChatFactory(func(ch chat.Channel, cfg service.ChatConfig) chat.Transport {
		bot := cfg.AppID
		if ch == chat.Telegram {
			bot, _, _ = strings.Cut(cfg.BotToken, ":")
		}
		f := &runChat{fakeChat: newFakeChat(bot, "AegisBot"), cfg: cfg, stopped: make(chan struct{})}
		if shape != nil {
			shape(f)
		}
		mu.Lock()
		defer mu.Unlock()
		built = append(built, f)
		return f
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); app.svc.RunChat(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	return app, func() []*runChat {
		mu.Lock()
		defer mu.Unlock()
		return append([]*runChat(nil), built...)
	}
}

func larkOn(appID, secret string) map[string]any {
	return map[string]any{"chat.lark.enabled": true, "chat.lark.region": "lark", "chat.lark.appId": appID, "chat.lark.appSecret": secret}
}

func tgOn(token string) map[string]any {
	return map[string]any{"chat.telegram.enabled": true, "chat.telegram.botToken": token}
}

func (a *testApp) saveSettings(token string, body map[string]any) apiResp {
	a.t.Helper()
	return a.do(http.MethodPut, settingsURL, token, body)
}

type chatStatusView struct {
	Enabled   bool           `json:"enabled"`
	State     string         `json:"state"`
	Detail    string         `json:"detail"`
	DetailRef *model.RuleRef `json:"detailRef"`
	BotName   string         `json:"botName"`
}

func (a *testApp) chatStatus(token string) map[string]chatStatusView {
	a.t.Helper()
	r := a.do(http.MethodGet, chatStatusURL, token, nil)
	eq(a.t, r.Code, 0, "渠道状态: "+r.Msg)
	var out map[string]chatStatusView
	if err := json.Unmarshal(r.Data, &out); err != nil {
		a.t.Fatalf("%v: %s", err, r.Data)
	}
	return out
}

// waitChat 等 Lark 变成某个状态。时限短于轮询设置的 5 秒:等到的是保存时那一声,不是轮询。
func (a *testApp) waitChat(token, state string) chatStatusView {
	a.t.Helper()
	return a.waitChannel(token, chat.Lark, state)
}

func (a *testApp) waitChannel(token string, ch chat.Channel, state string) chatStatusView {
	a.t.Helper()
	var st chatStatusView
	for end := time.Now().Add(3 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if st = a.chatStatus(token)[string(ch)]; st.State == state {
			return st
		}
	}
	a.t.Fatalf("没等到 %s,停在 %+v", state, st)
	return st
}

// chatRowsRaw 是 chat.* 设置此刻的原值,比较「一项都没存」用。
func (a *testApp) chatRowsRaw() string {
	a.t.Helper()
	var rows []model.Setting
	if err := a.repo.DB().Where("k LIKE 'chat.%'").Order("k").Find(&rows).Error; err != nil {
		a.t.Fatal(err)
	}
	b, _ := json.Marshal(rows)
	return string(b)
}

// 默认值:都关着。
func TestChatSettings_Seeds(t *testing.T) {
	app := newTestApp(t)
	for k, want := range map[string]string{
		"chat.cardLang": `"zh"`, "chat.lark.enabled": "false", "chat.lark.region": `"feishu"`,
		"chat.lark.appId": `""`, "chat.lark.appSecret": `""`,
		"chat.telegram.enabled": "false", "chat.telegram.botToken": `""`, "chat.telegram.includeSql": "false", "chat.telegram.proxyToken": `""`,
	} {
		eq(t, app.settingRaw(k), want, k)
	}
}

// 启用时缺必填项、值不合法:整次拒绝,一项不存。按「存进去之后」的样子判。
func TestChatSettings_RefusedSavesStoreNothing(t *testing.T) {
	app := newTestApp(t)
	admin := app.login(cardAdmin, "aegisdb123")
	before := app.chatRowsRaw()
	for _, c := range []struct {
		what, msg string
		body      map[string]any
	}{
		{"启用缺 appId", "App ID", map[string]any{"chat.lark.enabled": true, "chat.lark.appSecret": larkSecret}},
		{"启用、appId 全是空白", "App ID", larkOn("   ", larkSecret)},
		{"启用、从没存过密钥", "App Secret", map[string]any{"chat.lark.enabled": true, "chat.lark.appId": "cli_a"}},
		{"启用、密钥为空串", "App Secret", larkOn("cli_a", "")},
		{"启用、密钥全是空白", "App Secret", larkOn("cli_a", "  \n")},
		{"启用、同时清除密钥", "App Secret", map[string]any{"chat.lark.enabled": true, "chat.lark.appId": "cli_a", "chat.lark.appSecret": nil}},
		{"地区不认识", "chat.lark.region", map[string]any{"chat.lark.region": "xx"}},
		{"语言不认识", "chat.cardLang", map[string]any{"chat.cardLang": "fr"}},
		{"开关不是布尔", "chat.lark.enabled", map[string]any{"chat.lark.enabled": "true"}},
		{"appId 有怪字符", "chat.lark.appId", map[string]any{"chat.lark.appId": "cli_a?x=1"}},
		{"appId 太长", "chat.lark.appId", map[string]any{"chat.lark.appId": strings.Repeat("a", 65)}},
		{"Telegram 启用、从没存过令牌", "Bot Token", map[string]any{"chat.telegram.enabled": true}},
		{"Telegram 启用、令牌为空串", "Bot Token", tgOn("")},
		{"Telegram 启用、令牌全是空白", "Bot Token", tgOn(" \n")},
		{"Telegram 启用、同时清除令牌", "Bot Token", map[string]any{"chat.telegram.enabled": true, "chat.telegram.botToken": nil}},
		{"Telegram 令牌不像", "chat.telegram.botToken", tgOn("not-a-token-do-not-echo")},
		{"Telegram 令牌能拼坏地址", "chat.telegram.botToken", tgOn(tgToken + "/../do-not-echo")},
		{"Telegram 带 SQL 不是布尔", "chat.telegram.includeSql", map[string]any{"chat.telegram.includeSql": "yes"}},
		{"Telegram 开关不是布尔", "chat.telegram.enabled", map[string]any{"chat.telegram.enabled": 1}},
		{"代理的协议不认识", "chat.telegram.proxyToken", map[string]any{"chat.telegram.proxyToken": "ftp://do-not-echo:1"}},
		{"代理用 socks4", "chat.telegram.proxyToken", map[string]any{"chat.telegram.proxyToken": "socks4://do-not-echo:1"}},
		{"代理没有协议", "chat.telegram.proxyToken", map[string]any{"chat.telegram.proxyToken": "do-not-echo:3128"}},
		{"代理没有主机", "chat.telegram.proxyToken", map[string]any{"chat.telegram.proxyToken": "http://"}},
		{"代理不是字符串", "chat.telegram.proxyToken", map[string]any{"chat.telegram.proxyToken": 7}},
	} {
		body := map[string]any{"notify.larkChannel": "x-" + c.what}
		for k, v := range c.body {
			body[k] = v
		}
		r := app.saveSettings(admin, body)
		if r.Code != 40001 || !strings.Contains(r.Msg, c.msg) || strings.Contains(r.Msg, "do-not-echo") {
			t.Errorf("%s: code=%d msg=%s", c.what, r.Code, r.Msg)
		}
		if now := app.chatRowsRaw(); now != before {
			t.Errorf("%s:存了东西 %s", c.what, now)
		}
		if got := app.settingRaw("notify.larkChannel"); got != `""` {
			t.Errorf("%s:同一次的别的项也存了 %s", c.what, got)
		}
	}
}

// 密钥落库是密文、接口不回显、审计只有「已修改」。开着时不许清掉;关掉随时可以。
func TestChatSettings_SecretAndMergedChecks(t *testing.T) {
	app := newTestApp(t)
	admin := app.login(cardAdmin, "aegisdb123")
	// 先只存密钥、再补其余:「已存的 + 这次提交的」齐了就放行;首尾的空白是粘贴带进来的。
	eq(t, app.saveSettings(admin, map[string]any{"chat.lark.appSecret": " " + larkSecret + "\n"}).Code, 0, "存密钥")
	eq(t, app.saveSettings(admin, map[string]any{"chat.lark.enabled": true, "chat.lark.appId": " cli_a ", "chat.lark.appSecret": ""}).Code, 0, "启用")
	eq(t, app.settingRaw("chat.lark.appId"), `"cli_a"`, "appId 去掉首尾空白")

	var stored string
	if err := json.Unmarshal([]byte(app.settingRaw("chat.lark.appSecret")), &stored); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(stored, "enc:") || strings.Contains(stored, larkSecret) {
		t.Errorf("密钥不是密文: %s", stored)
	}
	plain, err := crypto.DecryptSecret(stored)
	if err != nil || plain != larkSecret {
		t.Errorf("解出来 %q %v", plain, err)
	}
	get := app.do(http.MethodGet, settingsURL, admin, nil)
	var view struct {
		Settings   map[string]string `json:"settings"`
		SecretsSet map[string]bool   `json:"secretsSet"`
	}
	if err := json.Unmarshal(get.Data, &view); err != nil {
		t.Fatal(err)
	}
	if _, leaked := view.Settings["chat.lark.appSecret"]; leaked || !view.SecretsSet["chat.lark.appSecret"] ||
		strings.Contains(string(get.Data), larkSecret) || strings.Contains(string(get.Data), stored) {
		t.Errorf("GET /settings: %s", get.Data)
	}
	if rows := app.auditCommandsLike("chat.lark.appSecret=已修改"); len(rows) != 1 {
		t.Errorf("审计: %v", rows)
	}
	for _, sub := range []string{larkSecret, stored} {
		if rows := app.auditCommandsLike(sub); len(rows) != 0 {
			t.Errorf("密钥进了审计: %v", rows)
		}
	}
	if r := app.do(http.MethodGet, chatStatusURL, admin, nil); strings.Contains(string(r.Data), larkSecret) || strings.Contains(string(r.Data), "enc:") {
		t.Errorf("状态里有密钥: %s", r.Data)
	}

	before := app.chatRowsRaw()
	for what, body := range map[string]map[string]any{
		"开着时清密钥":     {"chat.lark.appSecret": nil},
		"开着时清 appId": {"chat.lark.appId": ""},
	} {
		if r := app.saveSettings(admin, body); r.Code != 40001 {
			t.Errorf("%s: code=%d %s", what, r.Code, r.Msg)
		}
	}
	eq(t, app.chatRowsRaw(), before, "没存")
	// 关掉、同时清空:放行
	eq(t, app.saveSettings(admin, map[string]any{"chat.lark.enabled": false, "chat.lark.appId": "", "chat.lark.appSecret": nil}).Code, 0, "关掉")
	eq(t, app.settingRaw("chat.lark.appSecret"), `""`, "清掉了")
	// 存着坏配置(直接改了库)也不妨碍改别的设置与关掉
	app.setting("chat.lark.enabled", "true")
	eq(t, app.saveSettings(admin, map[string]any{"notify.larkChannel": "ops"}).Code, 0, "改别的")
	eq(t, app.saveSettings(admin, map[string]any{"chat.cardLang": "en"}).Code, 0, "改语言")
	eq(t, app.saveSettings(admin, map[string]any{"chat.lark.enabled": false}).Code, 0, "关掉")
}

// Telegram 的令牌与代理都是机密:落库是密文,接口、审计、状态里都没有。代理空 = 没有,null 才清掉。
func TestChatSettings_TelegramSecrets(t *testing.T) {
	app, built := runChatApp(t, nil)
	admin := app.login(cardAdmin, "aegisdb123")
	eq(t, app.saveSettings(admin, map[string]any{"chat.telegram.botToken": " " + tgToken + "\n", "chat.telegram.proxyToken": tgProxy + " "}).Code, 0, "先存机密")
	eq(t, app.saveSettings(admin, map[string]any{"chat.telegram.enabled": true, "chat.telegram.botToken": "", "chat.telegram.proxyToken": ""}).Code, 0, "启用")
	st := app.waitChannel(admin, chat.Telegram, "connected")
	if !st.Enabled || st.BotName != "AegisBot" {
		t.Errorf("状态: %+v", st)
	}
	if got := built(); len(got) != 1 || got[0].cfg != (service.ChatConfig{Enabled: true, BotToken: tgToken, Proxy: tgProxy}) {
		t.Fatalf("渠道配置: %+v", got[0].cfg)
	}
	get := app.do(http.MethodGet, settingsURL, admin, nil)
	var view struct {
		Settings   map[string]string `json:"settings"`
		SecretsSet map[string]bool   `json:"secretsSet"`
	}
	if err := json.Unmarshal(get.Data, &view); err != nil {
		t.Fatal(err)
	}
	var audits []string
	for _, like := range []string{"chat.", "telegram", "px-pass", "AAtelegram", "proxy.corp"} {
		audits = append(audits, app.auditCommandsLike(like)...)
	}
	status := app.do(http.MethodGet, chatStatusURL, admin, nil)
	for k, want := range map[string]string{"chat.telegram.botToken": tgToken, "chat.telegram.proxyToken": tgProxy} {
		var stored string
		if err := json.Unmarshal([]byte(app.settingRaw(k)), &stored); err != nil {
			t.Fatal(err)
		}
		if plain, err := crypto.DecryptSecret(stored); !strings.HasPrefix(stored, "enc:") || err != nil || plain != want {
			t.Errorf("%s 存成 %s(%v)", k, stored, err)
		}
		if _, leaked := view.Settings[k]; leaked || !view.SecretsSet[k] {
			t.Errorf("GET /settings: %s", get.Data)
		}
		for _, where := range []string{string(get.Data), string(status.Data), strings.Join(audits, "\n")} {
			for _, bad := range []string{want, stored, "px-pass", "proxy.corp", "AAtelegram"} {
				if strings.Contains(where, bad) {
					t.Errorf("%q 漏在 %s", bad, where)
				}
			}
		}
		if rows := app.auditCommandsLike(k + "=已修改"); len(rows) != 1 {
			t.Errorf("审计: %v", rows)
		}
	}

	before := app.chatRowsRaw()
	r := app.saveSettings(admin, map[string]any{"chat.telegram.botToken": nil})
	if r.Code != 40001 || !strings.Contains(r.Msg, "Bot Token") || r.MsgRef == nil {
		t.Errorf("开着时清令牌: %d %s", r.Code, r.Msg)
	}
	eq(t, app.chatRowsRaw(), before, "没存")
	// 清掉代理:换连接,直连
	eq(t, app.saveSettings(admin, map[string]any{"chat.telegram.proxyToken": nil}).Code, 0, "清代理")
	eq(t, app.settingRaw("chat.telegram.proxyToken"), `""`, "清掉了")
	for end := time.Now().Add(3 * time.Second); len(built()) < 2; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatal("没有换连接")
		}
	}
	eq(t, built()[1].cfg, service.ChatConfig{Enabled: true, BotToken: tgToken}, "直连")
	// 另一个渠道的坏配置互不牵连;关掉、同时清掉放行
	app.setting("chat.lark.enabled", "true")
	eq(t, app.saveSettings(admin, map[string]any{"chat.telegram.enabled": false, "chat.telegram.botToken": nil}).Code, 0, "关掉")
	app.waitChannel(admin, chat.Telegram, "off")
}

// 从设置页打开「带 SQL」:只影响之后发的卡片,连接不动。
func TestChatSettings_IncludeSQLAffectsLaterCardsOnly(t *testing.T) {
	app, built := runChatApp(t, nil)
	admin := app.login(cardAdmin, "aegisdb123")
	bot, _, _ := strings.Cut(tgToken, ":")
	app.bindRow(owner1, chat.Telegram, bot)
	eq(t, app.saveSettings(admin, tgOn(tgToken)).Code, 0, "启用")
	app.waitChannel(admin, chat.Telegram, "connected")
	tg := built()[0]
	early, _ := app.submitCommand()
	app.sweepCards()
	eq(t, app.saveSettings(admin, map[string]any{"chat.telegram.includeSql": true}).Code, 0, "打开")
	app.submitCommand()
	app.sweepCards()
	sent := tg.sends()
	if len(sent) != 2 || sent[0].Card.Statement != "" || !sent[0].Card.Partial || !strings.Contains(sent[1].Card.Statement, "DROP TABLE") || sent[1].Card.Reason == "" {
		t.Fatalf("发出的: %+v", sent)
	}
	eq(t, app.cancel(admin, early).Code, 0, "撤回")
	app.dueCards()
	app.sweepCards()
	ups := tg.updates()
	if len(ups) != 1 || ups[0].Card.Outcome != "⚪ 已撤回" || ups[0].Card.Statement != "" || ups[0].Card.Reason != "" {
		t.Errorf("先前那张的更新: %+v", ups)
	}
	if rows := app.auditCommandsLike("chat.telegram.includeSql=true"); len(rows) != 1 {
		t.Errorf("审计: %v", rows)
	}
	if n := len(built()); n != 1 {
		t.Errorf("换了连接:%d", n)
	}
}

// 绑定页要机器人的用户名去拼 t.me 链接:列表与绑定码的应答里都有。
func TestChatBind_TelegramBotName(t *testing.T) {
	app := newTestApp(t)
	app.svc.SetChatTransport(chat.Telegram, newFakeChat("42", "aegis_bot"))
	token := app.login(cardAdmin, "aegisdb123")
	if _, list := app.chatBindings(token); len(list) != 1 || list[0].Channel != "telegram" || list[0].BotName != "aegis_bot" {
		t.Errorf("列表: %+v", list)
	}
	r := app.do(http.MethodPost, "/api/v1/auth/chat-bindings/telegram/code", token, map[string]any{})
	if r.Code != 0 || !strings.Contains(string(r.Data), `"botName":"aegis_bot"`) {
		t.Errorf("绑定码: %d %s", r.Code, r.Data)
	}
}

// 保存后数秒内生效:启用就连上,换应用就取消旧连接、换上新的,关掉就撤下。改无关的设置不动它。
func TestChatSettings_SaveReloads(t *testing.T) {
	app, built := runChatApp(t, nil)
	admin := app.login(cardAdmin, "aegisdb123")
	eq(t, app.chatStatus(admin)["lark"].State, "off", "默认关着")

	eq(t, app.saveSettings(admin, larkOn("cli_a", larkSecret)).Code, 0, "启用")
	st := app.waitChat(admin, "connected")
	if !st.Enabled || st.BotName != "AegisBot" {
		t.Errorf("状态: %+v", st)
	}
	all := app.chatStatus(admin)
	if tg, ok := all["telegram"]; len(all) != 2 || !ok || tg.Enabled || tg.State != "off" {
		t.Errorf("各渠道: %+v", all)
	}
	first := built()[0]
	if first.cfg != (service.ChatConfig{Enabled: true, Region: "lark", AppID: "cli_a", AppSecret: larkSecret}) {
		t.Errorf("渠道配置: %+v", first.cfg)
	}

	eq(t, app.saveSettings(admin, map[string]any{"chat.cardLang": "en", "notify.larkChannel": "ops"}).Code, 0, "改语言")
	eq(t, app.saveSettings(admin, map[string]any{"chat.lark.appId": "cli_b"}).Code, 0, "换应用")
	select {
	case <-first.stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("旧连接没有被取消")
	}
	app.waitChat(admin, "connected")
	if got := built(); len(got) != 2 || got[1].cfg.AppID != "cli_b" {
		t.Fatalf("建过的渠道: %d", len(got))
	}
	eq(t, app.saveSettings(admin, map[string]any{"chat.lark.enabled": false}).Code, 0, "关掉")
	app.waitChat(admin, "off")
	if _, list := app.chatBindings(admin); len(list) != 0 {
		t.Errorf("关掉之后还列着: %+v", list)
	}
}

// 换了应用,旧绑定自动失效。
func TestChatSettings_NewAppMakesBindingsStale(t *testing.T) {
	app, _ := runChatApp(t, nil)
	admin := app.login(cardAdmin, "aegisdb123")
	app.bindRow(cardAdmin, chat.Lark, "cli_a")
	id := app.storedUser(cardAdmin).ID
	eq(t, app.saveSettings(admin, larkOn("cli_a", larkSecret)).Code, 0, "启用")
	app.waitChat(admin, "connected")
	if app.svc.ChatBindingOf(id, chat.Lark) == nil {
		t.Fatal("绑定应当有效")
	}
	eq(t, app.saveSettings(admin, map[string]any{"chat.lark.appId": "cli_b"}).Code, 0, "换应用")
	for end := time.Now().Add(5 * time.Second); app.svc.ChatBindingOf(id, chat.Lark) != nil; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatal("换了应用,旧绑定还有效")
		}
	}
}

// 渠道断开、panic、挂住,进程照常:建单、站内审批、超时扫描都不受影响;状态看得到原因。
func TestChatSettings_BrokenChannelHurtsNothing(t *testing.T) {
	for name, run := range map[string]func(context.Context, func(chat.State, string)) error{
		"连不上":   func(context.Context, func(chat.State, string)) error { return errors.New("lark: connection: refused") },
		"panic": func(context.Context, func(chat.State, string)) error { panic(larkSecret) },
		"挂住": func(ctx context.Context, onState func(chat.State, string)) error {
			onState(chat.StateConnecting, "")
			<-ctx.Done()
			return ctx.Err()
		},
	} {
		t.Run(name, func(t *testing.T) {
			app, _ := runChatApp(t, func(f *runChat) {
				f.run = run
				f.setErrs(errors.New("send down"), errors.New("update down"))
			})
			admin := app.login(cardAdmin, "aegisdb123")
			for _, email := range []string{owner1, owner2} {
				app.bindRow(email, chat.Lark, "cli_a")
			}
			eq(t, app.saveSettings(admin, larkOn("cli_a", larkSecret)).Code, 0, "启用")
			want := "error"
			if name == "挂住" {
				want = "connecting"
			}
			st := app.waitChat(admin, want)
			if strings.Contains(st.Detail, larkSecret) || want == "error" && st.Detail == "" {
				t.Errorf("状态: %+v", st)
			}

			id, _ := app.submitCommand()
			if len(app.cardsOf(id)) != 2 {
				t.Errorf("断开期间建的单也要排卡片: %+v", app.cardsOf(id))
			}
			app.sweepCards()
			owner := app.login(owner1, "aegisdb123")
			r := app.do(http.MethodPost, "/api/v1/approvals/"+itoa(id)+"/approve", owner, map[string]any{})
			eq(t, r.Code, 0, "站内审批: "+r.Msg)
			eq(t, app.ticket(id).Status, model.StatusApproved, "状态")

			late, _ := app.submitCommand()
			app.setting("approval.onTimeout", `"auto-reject"`)
			app.setting("approval.timeoutMinutes", "1")
			if err := app.repo.DB().Model(&model.Approval{}).Where("id = ?", late).UpdateColumn("created_at", time.Now().Add(-time.Hour)).Error; err != nil {
				t.Fatal(err)
			}
			app.svc.SweepApprovalTimeouts()
			eq(t, app.ticket(late).Status, model.StatusExpired, "超时作废")
		})
	}
}

// 测试卡片只发给本人,按钮只有测试:令牌形状对、库里没有,点了只回提示。
func TestChatTest_SendsToSelf(t *testing.T) {
	app, lark, _ := cardApp(t)
	admin := app.login(cardAdmin, "aegisdb123")
	r := app.do(http.MethodPost, chatTestURL, admin, map[string]any{"channel": "lark"})
	eq(t, r.Code, 0, "发测试卡片: "+r.Msg)
	sent := lark.sends()
	if len(sent) != 1 || sent[0].AccountID != "lark:"+cardAdmin {
		t.Fatalf("发给了: %+v", sent)
	}
	card := sent[0].Card
	if !card.Test || card.Title != "测试卡片" || len(card.Token) != 32 || card.DetailURL != "" || card.Outcome != "" || card.Statement != "" {
		t.Errorf("卡片: %+v", card)
	}
	var n int64
	app.repo.DB().Model(&model.ChatCard{}).Count(&n)
	eq(t, n, int64(0), "不落卡片行")

	res := app.larkClick(cardAdmin, card.Token, chat.ActTest)
	if !res.OK || !strings.Contains(res.Toast, "测试成功") || res.Card != nil {
		t.Errorf("点测试: %+v", res)
	}
	// 拿测试卡片的令牌去批:不认
	if res := app.larkClick(cardAdmin, card.Token, chat.ActConfirm); res.OK || !strings.Contains(res.Toast, "卡片已失效") {
		t.Errorf("拿测试令牌去批: %+v", res)
	}

	// 紧接着再发:太频繁;别人不受影响
	eq(t, app.do(http.MethodPost, chatTestURL, admin, map[string]any{"channel": "lark"}).Code, 42900, "太频繁")
	if n := len(lark.sends()); n != 1 {
		t.Errorf("发了 %d 张", n)
	}
}

// 频控按人、按渠道分开算;同时来的几次只放行一次。
func TestChatTest_LimitIsPerChannelAndAtomic(t *testing.T) {
	app, lark, tg := cardApp(t)
	app.bindRow(cardAdmin, chat.Telegram, tgBot)
	u := app.storedUser(cardAdmin)
	var ok atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			switch err := app.svc.SendChatTest(context.Background(), u, chat.Lark); {
			case err == nil:
				ok.Add(1)
			case !errors.Is(err, service.ErrChatTestTooOften):
				t.Error(err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if ok.Load() != 1 || len(lark.sends()) != 1 {
		t.Errorf("放行了 %d 次,发了 %d 张", ok.Load(), len(lark.sends()))
	}
	if err := app.svc.SendChatTest(context.Background(), u, chat.Telegram); err != nil || len(tg.sends()) != 1 {
		t.Errorf("另一个渠道不受影响: %v", err)
	}
}

// 发不了的各种情形,各有各的话;没发出去的不占频次。
func TestChatTest_Refusals(t *testing.T) {
	app, lark, tg := cardApp(t)
	admin := app.login(cardAdmin, "aegisdb123")
	post := func(ch any) apiResp {
		return app.do(http.MethodPost, chatTestURL, admin, map[string]any{"channel": ch})
	}
	refused := func(what string, r apiResp, want string) {
		t.Helper()
		if r.Code != 40001 || !strings.Contains(r.Msg, want) || r.MsgRef == nil {
			t.Errorf("%s: code=%d msg=%s ref=%v", what, r.Code, r.Msg, r.MsgRef)
		}
	}
	refused("没绑定", post("telegram"), "还没有绑定")
	refused("不认识的渠道", post("wechat"), "没有这个审批机器人渠道")
	refused("没给渠道", post(""), "没有这个审批机器人渠道")
	refused("类型不对", post(1), "参数错误")
	app.svc.SetChatTransport(chat.Telegram, nil)
	refused("渠道关着", post("telegram"), "没有开启")
	lark.setBot("", "")
	refused("还没连上", post("lark"), "还没连上")
	lark.setBot("cli_other", "AegisBot")
	refused("换过机器人", post("lark"), "还没有绑定")
	lark.setBot(chatBot, "AegisBot")

	lark.setErrs(errors.New("lark: code 230013:\n"+strings.Repeat("长", 300)), nil)
	r := post("lark")
	refused("平台报错", r, "测试卡片发送失败:lark: code 230013: 长")
	if n := len([]rune(r.Msg)); n > 220 {
		t.Errorf("原因没截短:%d", n)
	}
	if len(tg.sends())+len(lark.sends()) != 0 {
		t.Error("不该发出去")
	}
}

// 渠道在发送里 panic:只说发不出去,不带 panic 的内容。
func TestChatTest_PanicIsContained(t *testing.T) {
	app, lark, _ := cardApp(t)
	lark.setHooks(func(context.Context, string, chat.Card) error { panic(larkSecret) }, nil)
	r := app.do(http.MethodPost, chatTestURL, app.login(cardAdmin, "aegisdb123"), map[string]any{"channel": "lark"})
	if r.Code != 40001 || strings.Contains(r.Msg, larkSecret) {
		t.Errorf("code=%d msg=%s", r.Code, r.Msg)
	}
}

// 只有平台管理员:有设置菜单的角色也不行。
func TestChatSettings_AdminOnly(t *testing.T) {
	app, _, _ := cardApp(t)
	for _, email := range []string{owner1, "chenhao@aegisdb.io"} {
		token := app.login(email, "aegisdb123")
		eq(t, app.do(http.MethodGet, chatStatusURL, token, nil).Code, 40300, email+" 看状态")
		eq(t, app.do(http.MethodPost, chatTestURL, token, map[string]any{"channel": "lark"}).Code, 40300, email+" 发测试卡片")
		eq(t, app.saveSettings(token, map[string]any{"chat.cardLang": "en"}).Code, 40300, email+" 改设置")
	}
}

// 写库失败(第几个键都一样):整次一项都不存、不记审计、渠道不动,回内部错误。
func TestSettings_FailedWriteStoresNothing(t *testing.T) {
	app, built := runChatApp(t, nil)
	admin := app.login(cardAdmin, "aegisdb123")
	all := func() string {
		var rows []model.Setting
		if err := app.repo.DB().Order("k").Find(&rows).Error; err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(rows)
		return string(b)
	}
	before := all()
	// 按键排序写,region 在 enabled、appId、appSecret 之后:只有它写不进去,前面的也不能留下。
	fail := func(tx *gorm.DB) {
		if s, ok := tx.Statement.Dest.(*model.Setting); ok && s.K == "chat.lark.region" {
			_ = tx.AddError(errors.New("disk full"))
		}
	}
	db := app.repo.DB()
	if err := errors.Join(db.Callback().Update().Before("gorm:update").Register("test:fail", fail),
		db.Callback().Create().Before("gorm:create").Register("test:fail", fail)); err != nil {
		t.Fatal(err)
	}
	r := app.saveSettings(admin, larkOn("cli_a", larkSecret))
	if r.Code != 50000 || strings.Contains(r.Msg, "disk full") {
		t.Errorf("code=%d msg=%s", r.Code, r.Msg)
	}
	if now := all(); now != before {
		t.Errorf("存下了一部分: %s", now)
	}
	if rows := app.auditCommandsLike("admin.settings.update"); len(rows) != 0 {
		t.Errorf("没存成也记了审计: %v", rows)
	}
	if st := app.chatStatus(admin)["lark"]; st.State != "off" || len(built()) != 0 {
		t.Errorf("渠道动了: %+v", st)
	}
	// 恢复之后照常
	if err := errors.Join(db.Callback().Update().Remove("test:fail"), db.Callback().Create().Remove("test:fail")); err != nil {
		t.Fatal(err)
	}
	eq(t, app.saveSettings(admin, larkOn("cli_a", larkSecret)).Code, 0, "恢复之后")
	app.waitChat(admin, "connected")
}

// 核对时读不出库里的设置:是网关的问题,不是提交的值不对。
func TestChatSettings_ReadFailureIsInternal(t *testing.T) {
	app := newTestApp(t)
	admin := app.login(cardAdmin, "aegisdb123")
	if err := app.repo.DB().Callback().Query().Before("gorm:query").Register("test:fail", func(tx *gorm.DB) {
		if _, ok := tx.Statement.Dest.(*[]model.Setting); ok {
			_ = tx.AddError(errors.New("disk full"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	r := app.saveSettings(admin, map[string]any{"chat.cardLang": "en"})
	if r.Code != 50000 || strings.Contains(r.Msg, "disk full") {
		t.Errorf("code=%d msg=%s", r.Code, r.Msg)
	}
	eq(t, app.settingRaw("chat.cardLang"), `"zh"`, "没存")
}

// 两次保存同时写一组库里还没有的键:都成功。先 UPDATE、落空再 INSERT 的写法在 MySQL 上会互相等死(拿真库当
// 存储库时才看得出来,SQLite 只有一个写者)。
func TestSettings_ConcurrentFirstSaves(t *testing.T) {
	app := newTestApp(t)
	for round := range 20 {
		// 每轮一组从没有过的键:删过的键在 MySQL 上会留下痕迹,锁法就不一样了
		values := map[string]string{}
		for _, k := range []string{"a", "b", "c", "d"} {
			values[fmt.Sprintf("test.round%d.%s", round, k)] = "true"
		}
		errs := make(chan error, 4)
		start := make(chan struct{})
		for range 4 {
			go func() {
				<-start
				errs <- app.repo.SetSettings(values)
			}()
		}
		close(start)
		for range 4 {
			if err := <-errs; err != nil {
				t.Fatalf("第 %d 轮: %v", round, err)
			}
		}
		for k := range values {
			eq(t, app.settingRaw(k), "true", k)
		}
	}
}
