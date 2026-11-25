package bootstrap

// 聊天渠道的设置、状态与测试卡片(.scratch/chat-approval-cards/PRD.md §5.6、§5.7)。连接管理本身的
// 用例(退避、panic、换连接)在 internal/service/chat_runtime_test.go。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

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
	app.svc.SetChatFactory(func(_ chat.Channel, cfg service.ChatConfig) chat.Transport {
		f := &runChat{fakeChat: newFakeChat(cfg.AppID, "AegisBot"), cfg: cfg, stopped: make(chan struct{})}
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
	var st chatStatusView
	for end := time.Now().Add(3 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if st = a.chatStatus(token)["lark"]; st.State == state {
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
		{"Telegram 的键还没有", "chat.telegram.enabled", map[string]any{"chat.telegram.enabled": true}},
	} {
		body := map[string]any{"notify.larkChannel": "x-" + c.what}
		for k, v := range c.body {
			body[k] = v
		}
		r := app.saveSettings(admin, body)
		if r.Code != 40001 || !strings.Contains(r.Msg, c.msg) {
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
