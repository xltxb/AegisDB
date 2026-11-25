package bootstrap

// 聊天账号绑定(.scratch/chat-approval-cards/PRD.md §5.1)。
//
// 绑定是比会话活得更久的凭据:绑上的人不登录网关也能批单。所以这里逐条钉住"绑不上"与"自动失效"
// 的那些情形,而不只是绑得上。

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"

	"aegisdb/internal/chat"
	"aegisdb/internal/model"
	"aegisdb/internal/service"
	"aegisdb/pkg/resp"
	"aegisdb/pkg/totp"
)

const (
	chatBot  = "cli_test"
	bindURL  = "/api/v1/auth/chat-bindings"
	larkCode = bindURL + "/lark/code"
	// 绑定成功的回复;「已绑定另一个网关账号」那句是被拒,不能混
	boundReply = "已绑定网关账号"
)

// chatApp 起一个开着 Lark 渠道的网关。
func chatApp(t *testing.T) (*testApp, *fakeChat) {
	t.Helper()
	app := newTestApp(t)
	fc := newFakeChat(chatBot, "AegisBot")
	app.svc.SetChatTransport(chat.Lark, fc)
	return app, fc
}

// bindCode 经接口生成一个绑定码。
func (a *testApp) bindCode(token string) string {
	a.t.Helper()
	r := a.do(http.MethodPost, larkCode, token, map[string]any{})
	var d struct {
		Code      string    `json:"code"`
		ExpiresAt time.Time `json:"expiresAt"`
		BotName   string    `json:"botName"`
	}
	_ = json.Unmarshal(r.Data, &d)
	if r.Code != 0 || d.Code == "" {
		a.t.Fatalf("生成绑定码: code=%d msg=%s data=%s", r.Code, r.Msg, r.Data)
	}
	if left := time.Until(d.ExpiresAt); left < 9*time.Minute || left > 10*time.Minute {
		a.t.Errorf("绑定码应 10 分钟有效,实际还剩 %s", left)
	}
	eq(a.t, d.BotName, "AegisBot", "机器人名")
	return d.Code
}

func (a *testApp) redeem(account, text string) string {
	return a.svc.RedeemChatBindCode(chat.Lark, chatBot, account, "oc_"+account, text)
}

// bindAs 让 email 走完整流程绑上一个 Lark 账号。
func (a *testApp) bindAs(email, account string) (token string) {
	a.t.Helper()
	token = a.login(email, "aegisdb123")
	if reply := a.redeem(account, a.bindCode(token)); !strings.Contains(reply, boundReply) {
		a.t.Fatalf("%s 绑定失败:%s", email, reply)
	}
	return token
}

func (a *testApp) chatRows(userID int64) []model.ChatBinding {
	a.t.Helper()
	var rows []model.ChatBinding
	if err := a.repo.DB().Where("user_id = ?", userID).Order("id").Find(&rows).Error; err != nil {
		a.t.Fatal(err)
	}
	return rows
}

func (a *testApp) chatCodes(userID int64) []model.ChatBindCode {
	a.t.Helper()
	var rows []model.ChatBindCode
	if err := a.repo.DB().Where("user_id = ?", userID).Find(&rows).Error; err != nil {
		a.t.Fatal(err)
	}
	return rows
}

func (a *testApp) notifs(userID int64, typ string) []model.Notification {
	a.t.Helper()
	var rows []model.Notification
	if err := a.repo.DB().Where("user_id = ? AND type = ?", userID, typ).Order("id").Find(&rows).Error; err != nil {
		a.t.Fatal(err)
	}
	return rows
}

func (a *testApp) auditLike(pattern string) []model.AuditLog {
	a.t.Helper()
	var rows []model.AuditLog
	if err := a.repo.DB().Where("command LIKE ?", pattern).Order("id").Find(&rows).Error; err != nil {
		a.t.Fatal(err)
	}
	return rows
}

type chatBindingView struct {
	Channel string     `json:"channel"`
	BotName string     `json:"botName"`
	Bound   bool       `json:"bound"`
	BoundAt *time.Time `json:"boundAt"`
}

func (a *testApp) chatBindings(token string) (string, []chatBindingView) {
	a.t.Helper()
	r := a.do(http.MethodGet, bindURL, token, nil)
	eq(a.t, r.Code, 0, "列出绑定: "+r.Msg)
	var out []chatBindingView
	if err := json.Unmarshal(r.Data, &out); err != nil {
		a.t.Fatalf("%v: %s", err, r.Data)
	}
	return string(r.Data), out
}

func TestChatBind_HappyPath(t *testing.T) {
	app, _ := chatApp(t)
	token := app.login("chenhao@aegisdb.io", "aegisdb123")
	u := app.storedUser("chenhao@aegisdb.io")
	_, list := app.chatBindings(token)
	if len(list) != 1 || list[0].Channel != "lark" || list[0].BotName != "AegisBot" || list[0].Bound || list[0].BoundAt != nil {
		t.Errorf("绑定前的列表: %+v", list)
	}

	code := app.bindCode(token)
	if !regexp.MustCompile(`^[2-9A-HJKMNP-Z]{10}$`).MatchString(code) {
		t.Errorf("绑定码应是 10 位无歧义大写字母数字: %q", code)
	}
	// 库里只有摘要
	sum := sha256.Sum256([]byte(code))
	rows := app.chatCodes(u.ID)
	if len(rows) != 1 || rows[0].CodeHash != hex.EncodeToString(sum[:]) || rows[0].UsedAt != nil {
		t.Fatalf("绑定码行: %+v", rows)
	}

	// 大小写、首尾空白、/start 前缀都认
	reply := app.redeem("ou_1", "  /start "+strings.ToLower(code)+" \n")
	if !strings.Contains(reply, boundReply) || !strings.Contains(reply, u.Name) {
		t.Errorf("绑定成功的回复: %q", reply)
	}
	b := app.chatRows(u.ID)
	if len(b) != 1 || b[0].Channel != "lark" || b[0].BotID != chatBot || b[0].AccountID != "ou_1" || b[0].ChatID != "oc_ou_1" {
		t.Fatalf("绑定行: %+v", b)
	}
	ns := app.notifs(u.ID, model.NotifChatBound)
	if len(ns) != 1 || !strings.Contains(ns[0].Body, "Lark") {
		t.Errorf("应有一条带渠道名的站内通知: %+v", ns)
	}
	as := app.auditLike("chat.bind %")
	if len(as) != 1 || as[0].ActorID != u.ID || as[0].Source != model.AuditSourceAuth || !strings.Contains(as[0].Command, "Lark") {
		t.Errorf("应有一条本人的 auth 审计: %+v", as)
	}
	raw, list := app.chatBindings(token)
	if len(list) != 1 || !list[0].Bound || list[0].BoundAt == nil {
		t.Errorf("绑定后的列表: %s", raw)
	}
	for _, secret := range []string{"ou_1", chatBot, code} {
		if strings.Contains(raw, secret) {
			t.Errorf("列表不该带 %q: %s", secret, raw)
		}
	}
	if got := app.svc.ChatUserOf(chat.Lark, chatBot, "ou_1"); got == nil || got.ID != u.ID {
		t.Errorf("ChatUserOf = %+v", got)
	}
	for what, got := range map[string]*model.User{
		"别的账号":  app.svc.ChatUserOf(chat.Lark, chatBot, "ou_2"),
		"别的机器人": app.svc.ChatUserOf(chat.Lark, "cli_other", "ou_1"),
		"别的渠道":  app.svc.ChatUserOf(chat.Telegram, chatBot, "ou_1"),
		"大小写不同": app.svc.ChatUserOf(chat.Lark, chatBot, "OU_1"),
	} {
		if got != nil {
			t.Errorf("ChatUserOf %s 应为 nil", what)
		}
	}
	// 绑定码不进审计、不进通知
	for _, r := range app.auditLike("%") {
		if strings.Contains(strings.ToUpper(r.Command), code) {
			t.Errorf("绑定码进了审计: %s", r.Command)
		}
	}
}

// 没有渠道开着:列表是空数组(界面据此藏起入口),也生成不了码。
func TestChatBind_NoChannel(t *testing.T) {
	app := newTestApp(t)
	token := app.login("chenhao@aegisdb.io", "aegisdb123")
	raw, _ := app.chatBindings(token)
	eq(t, raw, "[]", "没有渠道时的列表")
	r := app.do(http.MethodPost, larkCode, token, map[string]any{})
	if r.Code != resp.CodeBadRequest || !strings.Contains(r.Msg, "没有开启") {
		t.Errorf("渠道没开应拒绝: %d %s", r.Code, r.Msg)
	}
	if r := app.do(http.MethodPost, bindURL+"/slack/code", token, map[string]any{}); r.Code != resp.CodeBadRequest {
		t.Errorf("不认识的渠道: %d %s", r.Code, r.Msg)
	}
	if reply := app.redeem("ou_1", "ABCDEFG234"); !strings.Contains(reply, "没有开启") {
		t.Errorf("渠道没开时的回复: %q", reply)
	}
}

func TestChatBind_NotReadyOrNotHuman(t *testing.T) {
	app, fc := chatApp(t)
	token := app.login("chenhao@aegisdb.io", "aegisdb123")
	fc.setBot("", "")
	r := app.do(http.MethodPost, larkCode, token, map[string]any{})
	if r.Code != resp.CodeBadRequest || !strings.Contains(r.Msg, "还没连上") {
		t.Errorf("机器人没就绪应拒绝: %d %s", r.Code, r.Msg)
	}
	fc.setBot(chatBot, "AegisBot")

	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	sa := app.createServiceAccount(admin, "chat-bot", []int64{app.roleIDByCode(admin, "l2")}, nil)
	if _, _, err := app.svc.NewChatBindCode(mustUser(t, app, sa.ID), chat.Lark, ""); err != service.ErrChatHumanOnly {
		t.Errorf("服务账号应拒绝: %v", err)
	}
	if n := len(app.chatCodes(sa.ID)); n != 0 {
		t.Errorf("服务账号不该有绑定码: %d", n)
	}
	// 关掉渠道之后,先前生成的码也兑不了,且不消耗
	code := app.bindCode(token)
	app.svc.SetChatTransport(chat.Lark, nil)
	if reply := app.redeem("ou_1", code); !strings.Contains(reply, "没有开启") {
		t.Errorf("渠道关掉后: %q", reply)
	}
	app.svc.SetChatTransport(chat.Lark, fc)
	// 来自另一个机器人的消息不认
	if reply := app.svc.RedeemChatBindCode(chat.Lark, "cli_other", "ou_1", "oc", code); strings.Contains(reply, boundReply) {
		t.Errorf("别的机器人转来的码: %q", reply)
	}
	if reply := app.svc.RedeemChatBindCode(chat.Lark, chatBot, "", "oc", code); strings.Contains(reply, boundReply) {
		t.Errorf("没有账号标识: %q", reply)
	}
	// 码是给 Lark 的,在 Telegram 里不认
	app.svc.SetChatTransport(chat.Telegram, newFakeChat("42", "tg"))
	if reply := app.svc.RedeemChatBindCode(chat.Telegram, "42", "100", "100", code); !strings.Contains(reply, "无效或已过期") {
		t.Errorf("跨渠道的码: %q", reply)
	}
	if reply := app.redeem("ou_1", code); !strings.Contains(reply, boundReply) {
		t.Errorf("前面几次都不该消耗它: %q", reply)
	}
}

func TestChatBind_DeadCodes(t *testing.T) {
	const dead = "无效或已过期"
	app, _ := chatApp(t)
	token := app.login("chenhao@aegisdb.io", "aegisdb123")
	u := app.storedUser("chenhao@aegisdb.io")
	check := func(what, code string, bindings int) {
		t.Helper()
		if reply := app.redeem("ou_x", code); !strings.Contains(reply, dead) {
			t.Errorf("%s: %q", what, reply)
		}
		if rows := app.chatRows(u.ID); len(rows) != bindings {
			t.Errorf("%s:绑定不该变,现有 %+v", what, rows)
		}
	}
	code := app.bindCode(token)
	app.repo.DB().Model(&model.ChatBindCode{}).Where("user_id = ?", u.ID).Update("expires_at", time.Now().Add(-time.Second))
	check("过期", code, 0)

	old := app.bindCode(token)
	code = app.bindCode(token)
	check("重新生成后的旧码", old, 0)
	if n := len(app.chatCodes(u.ID)); n != 1 {
		t.Errorf("旧码不该留着:%d 行", n)
	}
	if reply := app.redeem("ou_1", code); !strings.Contains(reply, boundReply) {
		t.Fatalf("新码: %q", reply)
	}
	check("用过的", code, 1)
	eq(t, app.chatRows(u.ID)[0].AccountID, "ou_1", "绑定的账号")
	check("从没有过的", "ABCDEFG234", 1)

	// 账号被停用(绕过自动解绑,直接改库):码对也不认
	code = app.bindCode(token)
	app.repo.DB().Model(&model.User{}).Where("id = ?", u.ID).Update("status", "disabled")
	check("账号已停用", code, 1)
	app.repo.DB().Model(&model.User{}).Where("id = ?", u.ID).Updates(map[string]any{"status": "active", "kind": model.UserKindService})
	check("变成了服务账号", code, 1)
}

func TestChatBind_AccountTakenAndReplace(t *testing.T) {
	app, _ := chatApp(t)
	app.bindAs("chenhao@aegisdb.io", "ou_1")
	chen, zhang := app.storedUser("chenhao@aegisdb.io"), app.storedUser("zhangwei@aegisdb.io")

	ztok := app.login("zhangwei@aegisdb.io", "aegisdb123")
	code := app.bindCode(ztok)
	if reply := app.redeem("ou_1", code); !strings.Contains(reply, "另一个网关账号") {
		t.Errorf("聊天账号已被别人绑走: %q", reply)
	}
	if rows := app.chatRows(zhang.ID); len(rows) != 0 {
		t.Errorf("不该绑上: %+v", rows)
	}
	if rows := app.chatRows(chen.ID); len(rows) != 1 || rows[0].AccountID != "ou_1" {
		t.Errorf("原绑定不该变: %+v", rows)
	}
	if n := len(app.notifs(zhang.ID, model.NotifChatBound)); n != 0 {
		t.Errorf("没绑上不该通知: %d", n)
	}

	// 同一用户换聊天账号:替换,旧账号立刻不算数
	app.bindAs("chenhao@aegisdb.io", "ou_9")
	if rows := app.chatRows(chen.ID); len(rows) != 1 || rows[0].AccountID != "ou_9" || rows[0].ChatID != "oc_ou_9" {
		t.Errorf("应替换成新账号: %+v", rows)
	}
	if app.svc.ChatUserOf(chat.Lark, chatBot, "ou_1") != nil {
		t.Error("旧聊天账号应不再算数")
	}
	// 腾出来的旧账号可以给别人
	if reply := app.redeem("ou_1", code); !strings.Contains(reply, boundReply) {
		t.Errorf("旧账号腾出来之后: %q", reply)
	}
}

func TestChatBind_MFA(t *testing.T) {
	app, _ := chatApp(t)
	token := app.login("linwei@aegisdb.io", "aegisdb123")
	u := app.storedUser("linwei@aegisdb.io")
	secret := app.setupMFA(token)
	gen := func(code string) apiResp {
		return app.do(http.MethodPost, larkCode, token, map[string]any{"mfaCode": code})
	}
	for i := 0; i < 6; i++ { // 不带码不算输错
		eq(t, gen(" ").Code, resp.CodeMFARequired, "不带码")
	}
	eq(t, gen(wrongCode(secret)).Code, resp.CodeBadRequest, "错码")
	if n := len(app.chatCodes(u.ID)); n != 0 {
		t.Errorf("没过 MFA 不该有绑定码: %d", n)
	}
	now := totp.Code(secret, time.Now())
	eq(t, gen(now).Code, 0, "正确的码")
	eq(t, gen(now).Code, resp.CodeBadRequest, "同一个码不能再用一次")

	// 与 MFADisable 同一本失败账:错够 5 次锁住,锁定期间关 MFA 也不行
	for i := 0; i < 5; i++ {
		gen(wrongCode(secret))
	}
	eq(t, gen(totp.Code(secret, time.Now())).Code, resp.CodeTooManyReqs, "锁定")
	eq(t, app.do(http.MethodPost, "/api/v1/auth/mfa/disable", token, map[string]any{"code": "000000"}).Code, resp.CodeTooManyReqs, "共用锁定")
	if len(app.auditLike("mfa.locked 生成审批机器人绑定码%")) != 1 {
		t.Error("触发锁定应写审计并说明是哪一步")
	}
}

// 同一聊天账号 15 分钟内错 10 次锁住,锁定期间正确的码也拒;别的聊天账号不受影响。
func TestChatBind_WrongCodeLock(t *testing.T) {
	app, _ := chatApp(t)
	token := app.login("chenhao@aegisdb.io", "aegisdb123")
	code := app.bindCode(token)

	// 闲聊不算输错:只有"像绑定码"的才计数
	for _, chatter := range []string{"", "你好", "/start", "/help", "hello", "helloworld", "DEPARTMENT", "/start DEPARTMENT",
		"1234567890", "23456789", "ABCDEFG2345", "ABC DEF 23", "ABCDE-2345", "ABCDEF0123", "２３４５ＡＢＣＤＥＦ"} {
		for i := 0; i < 3; i++ {
			if reply := app.redeem("ou_1", chatter); !strings.Contains(reply, "生成绑定码") {
				t.Errorf("%q 应回用法说明: %q", chatter, reply)
			}
		}
	}
	for i := 0; i < 9; i++ {
		if reply := app.redeem("ou_1", fmt.Sprintf("/start ABCDEFGH2%d", i%8+2)); !strings.Contains(reply, "无效或已过期") {
			t.Fatalf("第 %d 次错码: %q", i+1, reply)
		}
	}
	// 夹一次成功不清零(口径就是 15 分钟内错 10 次)
	if reply := app.redeem("ou_1", code); !strings.Contains(reply, boundReply) {
		t.Fatalf("第 10 次之前还没锁: %q", reply)
	}
	code = app.bindCode(token)
	app.redeem("ou_1", "ABCDEFGH23")
	if reply := app.redeem("ou_1", code); !strings.Contains(reply, "15 分钟后") {
		t.Errorf("锁定后正确的码也应拒: %q", reply)
	}
	if reply := app.redeem("ou_2", code); !strings.Contains(reply, boundReply) {
		t.Errorf("别的聊天账号不受影响: %q", reply)
	}
}

func TestChatBind_GenerateLimit(t *testing.T) {
	app, _ := chatApp(t)
	token := app.login("chenhao@aegisdb.io", "aegisdb123")
	for i := 0; i < 10; i++ {
		app.bindCode(token)
	}
	r := app.do(http.MethodPost, larkCode, token, map[string]any{})
	eq(t, r.Code, resp.CodeTooManyReqs, "一个人 15 分钟内生成太多次: "+r.Msg)
	app.bindCode(app.login("zhangwei@aegisdb.io", "aegisdb123")) // 别人不受影响
}

func TestChatBind_ConcurrentRedeem(t *testing.T) {
	app, _ := chatApp(t)
	race := func(texts map[string]string) (won int, replies []string) {
		var wg sync.WaitGroup
		var mu sync.Mutex
		for account, code := range texts {
			wg.Add(1)
			go func() {
				defer wg.Done()
				reply := app.redeem(strings.TrimSuffix(account, "#2"), code)
				mu.Lock()
				defer mu.Unlock()
				replies = append(replies, reply)
				if strings.Contains(reply, boundReply) {
					won++
				}
			}()
		}
		wg.Wait()
		return
	}
	ctok, ztok := app.login("chenhao@aegisdb.io", "aegisdb123"), app.login("zhangwei@aegisdb.io", "aegisdb123")
	chen, zhang := app.storedUser("chenhao@aegisdb.io"), app.storedUser("zhangwei@aegisdb.io")
	for i := 0; i < 5; i++ { // 两个聊天账号抢同一个码
		code := app.bindCode(ctok)
		a, b := fmt.Sprintf("ou_a%d", i), fmt.Sprintf("ou_b%d", i)
		if won, replies := race(map[string]string{a: code, b: code}); won != 1 {
			t.Errorf("同一个码应恰好兑一次: %d %q", won, replies)
		}
		if n := len(app.chatRows(chen.ID)); n != 1 {
			t.Errorf("绑定行: %d", n)
		}
	}
	for i := 0; i < 5; i++ { // 两个用户抢同一个聊天账号
		acct := fmt.Sprintf("ou_c%d", i)
		won, replies := race(map[string]string{acct: app.bindCode(ctok), acct + "#2": app.bindCode(ztok)})
		if won != 1 || !strings.Contains(strings.Join(replies, "|"), "另一个网关账号") {
			t.Errorf("同一个聊天账号应恰好归一个人: %d %q", won, replies)
		}
	}
	if len(app.chatRows(chen.ID)) > 1 || len(app.chatRows(zhang.ID)) > 1 {
		t.Error("每人至多一个绑定")
	}
}

// 换了机器人:旧绑定当作没绑,重新绑定时替换掉。
func TestChatBind_BotChanged(t *testing.T) {
	app, fc := chatApp(t)
	token := app.bindAs("chenhao@aegisdb.io", "ou_1")
	u := app.storedUser("chenhao@aegisdb.io")
	if app.svc.ChatBindingOf(u.ID, chat.Lark) == nil {
		t.Fatal("应有绑定")
	}
	for _, bot := range []string{"cli_new", ""} { // 空 = 机器人还没连上
		fc.setBot(bot, "New")
		if app.svc.ChatBindingOf(u.ID, chat.Lark) != nil || app.svc.ChatUserOf(chat.Lark, chatBot, "ou_1") != nil {
			t.Errorf("机器人是 %q 时旧绑定不该算数", bot)
		}
		if _, list := app.chatBindings(token); len(list) != 1 || list[0].Bound || list[0].BoundAt != nil {
			t.Errorf("列表应显示未绑定: %+v", list)
		}
	}
	fc.setBot("cli_new", "AegisBot")
	if reply := app.svc.RedeemChatBindCode(chat.Lark, "cli_new", "ou_7", "oc_7", app.bindCode(token)); !strings.Contains(reply, boundReply) {
		t.Fatalf("重新绑定: %q", reply)
	}
	if rows := app.chatRows(u.ID); len(rows) != 1 || rows[0].BotID != "cli_new" {
		t.Errorf("应替换旧绑定: %+v", rows)
	}
	app.svc.SetChatTransport(chat.Lark, nil)
	if app.svc.ChatBindingOf(u.ID, chat.Lark) != nil || app.svc.ChatUserOf(chat.Lark, "cli_new", "ou_7") != nil {
		t.Error("渠道关掉后不该算数")
	}
}

func TestChatBind_ReplyLanguage(t *testing.T) {
	app, _ := chatApp(t)
	if err := app.repo.SetSetting("chat.cardLang", `"en"`); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"hi", "ABCDEFG234", app.bindCode(app.login("chenhao@aegisdb.io", "aegisdb123"))} {
		reply := app.redeem("ou_1", text)
		if reply == "" || strings.Contains(reply, "绑定") || strings.HasPrefix(reply, "bind.") {
			t.Errorf("%q 应回英文: %q", text, reply)
		}
	}
}

// 生成请求与口令重置 / 停用等撞在一起:请求里那份用户已经过时,不发码。
func TestChatBind_StaleSessionGetsNoCode(t *testing.T) {
	app, _ := chatApp(t)
	u := app.storedUser("chenhao@aegisdb.io")
	for what, col := range map[string]map[string]any{
		"口令改了":   {"password_hash": "x"},
		"停用了":    {"status": "disabled"},
		"会话作废了":  {"token_version": 99},
		"MFA 换了": {"mfa_enabled": true, "mfa_secret": "JBSWY3DPEHPK3PXP"},
	} {
		before := mustUser(t, app, u.ID)
		app.repo.DB().Model(&model.User{}).Where("id = ?", u.ID).Updates(col)
		if _, _, err := app.svc.NewChatBindCode(before, chat.Lark, ""); err != service.ErrChatUserChanged {
			t.Errorf("%s: %v", what, err)
		}
		if n := len(app.chatCodes(u.ID)); n != 0 {
			t.Errorf("%s:不该留下绑定码", what)
		}
		app.repo.DB().Model(&model.User{}).Where("id = ?", u.ID).Updates(map[string]any{
			"password_hash": u.PasswordHash, "status": "active", "mfa_enabled": false, "mfa_secret": "", "token_version": u.TokenVersion})
	}
}

// 这些事之后,聊天账号不该还能批单:绑定没了、没用掉的码作废、本人收到通知、审计记在动手的人名下。
func TestChatUnbind_Automatic(t *testing.T) {
	cases := []struct {
		name, why, source string
		self              bool
		do                func(app *testApp, admin, self string, id int64) apiResp
	}{
		{"停用账号", "停用", model.AuditSourceAdmin, false, func(app *testApp, admin, _ string, id int64) apiResp {
			return app.do(http.MethodPatch, fmt.Sprintf("/api/v1/users/%d", id), admin, map[string]any{"status": "disabled"})
		}},
		{"改成受邀", "账号状态变更", model.AuditSourceAdmin, false, func(app *testApp, admin, _ string, id int64) apiResp {
			return app.do(http.MethodPatch, fmt.Sprintf("/api/v1/users/%d", id), admin, map[string]any{"status": "invited"})
		}},
		{"管理员重置口令", "重置了登录口令", model.AuditSourceAdmin, false, func(app *testApp, admin, _ string, id int64) apiResp {
			return app.do(http.MethodPost, fmt.Sprintf("/api/v1/users/%d/password", id), admin, map[string]any{"password": "newpass99"})
		}},
		{"本人改口令", "修改了登录口令", model.AuditSourceAuth, true, func(app *testApp, _, self string, _ int64) apiResp {
			return app.do(http.MethodPost, "/api/v1/auth/password", self, map[string]any{"oldPassword": "aegisdb123", "newPassword": "newpass99"})
		}},
		{"重置 MFA", "重置了 MFA", model.AuditSourceAdmin, false, func(app *testApp, admin, _ string, id int64) apiResp {
			return app.do(http.MethodPost, fmt.Sprintf("/api/v1/users/%d/mfa/reset", id), admin, nil)
		}},
		{"管理员绑 MFA", "重新绑定了 MFA", model.AuditSourceAdmin, false, func(app *testApp, admin, _ string, id int64) apiResp {
			return app.do(http.MethodPost, fmt.Sprintf("/api/v1/users/%d/mfa/bind", id), admin, nil)
		}},
		{"管理员解绑", "管理员", model.AuditSourceAdmin, false, func(app *testApp, admin, _ string, id int64) apiResp {
			return app.do(http.MethodDelete, fmt.Sprintf("/api/v1/users/%d/chat-bindings", id), admin, nil)
		}},
		{"本人解绑", "你在网关里", model.AuditSourceAuth, true, func(app *testApp, _, self string, _ int64) apiResp {
			return app.do(http.MethodDelete, bindURL+"/lark", self, nil)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			app, _ := chatApp(t)
			admin := app.login("linwei@aegisdb.io", "aegisdb123")
			app.bindAs("linwei@aegisdb.io", "ou_admin")
			self := app.bindAs("chenhao@aegisdb.io", "ou_1")
			code := app.bindCode(self) // 还没用掉的码
			u, adm := app.storedUser("chenhao@aegisdb.io"), app.storedUser("linwei@aegisdb.io")

			if r := c.do(app, admin, self, u.ID); r.Code != 0 {
				t.Fatalf("code=%d msg=%s", r.Code, r.Msg)
			}
			if rows := app.chatRows(u.ID); len(rows) != 0 {
				t.Errorf("绑定应没了: %+v", rows)
			}
			if app.svc.ChatUserOf(chat.Lark, chatBot, "ou_1") != nil {
				t.Error("聊天账号应不再算数")
			}
			// 即使账号此刻是在职的(停用后又启用),旧码也兑不了
			app.repo.DB().Model(&model.User{}).Where("id = ?", u.ID).Update("status", "active")
			if reply := app.redeem("ou_1", code); !strings.Contains(reply, "无效或已过期") {
				t.Errorf("先前的码应作废: %q", reply)
			}
			ns := app.notifs(u.ID, model.NotifChatUnbound)
			if len(ns) != 1 || !strings.Contains(ns[0].Body, "Lark") || !strings.Contains(ns[0].Body, c.why) || ns[0].BodyRef == "" {
				t.Errorf("应通知本人渠道与原因: %+v", ns)
			}
			actor := adm.ID
			if c.self {
				actor = u.ID
			}
			as := app.auditLike("chat.unbind %")
			if len(as) != 1 || as[0].ActorID != actor || as[0].Source != c.source ||
				!strings.Contains(as[0].Command, "user=chenhao@aegisdb.io") || !strings.Contains(as[0].Command, "Lark") {
				t.Errorf("审计: %+v", as)
			}
			if n := len(app.chatRows(adm.ID)); n != 1 {
				t.Errorf("别人的绑定不该受影响: %d", n)
			}
		})
	}
}

func TestChatUnbind_NotTriggered(t *testing.T) {
	app, _ := chatApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	token := app.bindAs("chenhao@aegisdb.io", "ou_1")
	u := app.storedUser("chenhao@aegisdb.io")
	still := func(what string, r apiResp) {
		t.Helper()
		if r.Code != 0 {
			t.Errorf("%s: code=%d msg=%s", what, r.Code, r.Msg)
		}
		if len(app.chatRows(u.ID)) != 1 || len(app.notifs(u.ID, model.NotifChatUnbound)) != 0 {
			t.Errorf("%s 不该解绑", what)
		}
	}
	secret := app.setupMFA(token)
	still("停用 MFA", app.do(http.MethodPost, "/api/v1/auth/mfa/disable", token, map[string]any{"code": totp.Code(secret, time.Now())}))
	still("改角色", app.do(http.MethodPatch, fmt.Sprintf("/api/v1/users/%d", u.ID), admin, map[string]any{"roleIds": []int64{app.roleIDByCode(admin, "owner")}}))
	still("状态没变", app.do(http.MethodPatch, fmt.Sprintf("/api/v1/users/%d", u.ID), admin, map[string]any{"status": "active"}))
	still("登出", app.do(http.MethodPost, "/api/v1/auth/logout", token, nil))
	// 没成功的操作不解绑
	app.do(http.MethodPost, fmt.Sprintf("/api/v1/users/%d/password", u.ID), admin, map[string]any{"password": "short"})
	app.do(http.MethodPost, "/api/v1/auth/password", app.login("chenhao@aegisdb.io", "aegisdb123"), map[string]any{"oldPassword": "wrong-one", "newPassword": "newpass99"})
	still("被拒的改口令", apiResp{})
}

func TestChatUnbind_Routes(t *testing.T) {
	app, _ := chatApp(t)
	app.svc.SetChatTransport(chat.Telegram, newFakeChat("42", "tg_bot"))
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	token := app.bindAs("chenhao@aegisdb.io", "ou_1")
	u, adm := app.storedUser("chenhao@aegisdb.io"), app.storedUser("linwei@aegisdb.io")
	tgBind := func() {
		code, _, err := app.svc.NewChatBindCode(mustUser(t, app, u.ID), chat.Telegram, "")
		if err != nil {
			t.Fatal(err)
		}
		if reply := app.svc.RedeemChatBindCode(chat.Telegram, "42", "100", "100", "/start "+code); !strings.Contains(reply, boundReply) {
			t.Fatal(reply)
		}
	}
	tgBind()
	if _, list := app.chatBindings(token); len(list) != 2 || list[0].Channel != "lark" || list[1].Channel != "telegram" || !list[1].Bound {
		t.Errorf("两个渠道: %+v", list)
	}

	// 管理员解绑只给管理员,哪怕菜单全勾
	grantEveryMenu(t, app, "l2")
	ztok := app.login("chenhao@aegisdb.io", "aegisdb123")
	eq(t, app.do(http.MethodDelete, fmt.Sprintf("/api/v1/users/%d/chat-bindings", adm.ID), ztok, nil).Code, resp.CodeForbidden, "普通用户调管理员解绑")
	eq(t, app.do(http.MethodDelete, "/api/v1/users/999999/chat-bindings", admin, nil).Code, resp.CodeBadRequest, "用户不存在")
	eq(t, app.do(http.MethodDelete, bindURL+"/slack", token, nil).Code, resp.CodeBadRequest, "不认识的渠道")

	// 本人解绑只动那一个渠道;没绑也算成功、不重复通知
	eq(t, app.do(http.MethodDelete, bindURL+"/telegram", token, nil).Code, 0, "解绑 Telegram")
	eq(t, app.do(http.MethodDelete, bindURL+"/telegram", token, nil).Code, 0, "再解一次")
	if rows := app.chatRows(u.ID); len(rows) != 1 || rows[0].Channel != "lark" {
		t.Errorf("应只剩 Lark: %+v", rows)
	}
	if ns := app.notifs(u.ID, model.NotifChatUnbound); len(ns) != 1 || !strings.Contains(ns[0].Body, "Telegram") {
		t.Errorf("通知: %+v", ns)
	}
	// 管理员解绑:全部渠道;渠道关着、机器人换过也要解得掉
	tgBind()
	app.svc.SetChatTransport(chat.Lark, nil)
	eq(t, app.do(http.MethodDelete, fmt.Sprintf("/api/v1/users/%d/chat-bindings", u.ID), admin, nil).Code, 0, "管理员解绑")
	if rows := app.chatRows(u.ID); len(rows) != 0 {
		t.Errorf("应全部解绑: %+v", rows)
	}
	eq(t, len(app.notifs(u.ID, model.NotifChatUnbound)), 3, "每个渠道各一条通知")
}

// 解绑出错只记日志,主操作照常成功。
func TestChatUnbind_FailureDoesNotBlockPrimary(t *testing.T) {
	app, _ := chatApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	token := app.login("chenhao@aegisdb.io", "aegisdb123")
	u := app.storedUser("chenhao@aegisdb.io")
	if err := app.repo.DB().Exec("DROP TABLE tbl_chat_binding").Error; err != nil {
		t.Fatal(err)
	}
	eq(t, app.do(http.MethodPost, "/api/v1/auth/password", token, map[string]any{"oldPassword": "aegisdb123", "newPassword": "newpass99"}).Code, 0, "本人改口令")
	path := fmt.Sprintf("/api/v1/users/%d", u.ID)
	eq(t, app.do(http.MethodPost, path+"/password", admin, map[string]any{"password": "newpass88"}).Code, 0, "重置口令")
	eq(t, app.do(http.MethodPost, path+"/mfa/reset", admin, nil).Code, 0, "重置 MFA")
	eq(t, app.do(http.MethodPost, path+"/mfa/bind", admin, nil).Code, 0, "绑 MFA")
	eq(t, app.do(http.MethodPatch, path, admin, map[string]any{"status": "disabled"}).Code, 0, "停用")
	// 专门来解绑的则要如实报错
	if r := app.do(http.MethodDelete, path+"/chat-bindings", admin, nil); r.Code != resp.CodeInternalError {
		t.Errorf("解绑失败应报错: %d %s", r.Code, r.Msg)
	}
}

// 兑换与重置口令撞在一起:不论谁先谁后,结束时都不该留下绑定。
func TestChatUnbind_RacesWithRedeem(t *testing.T) {
	app, _ := chatApp(t)
	admin := app.storedUser("linwei@aegisdb.io")
	u := app.storedUser("chenhao@aegisdb.io")
	for i := 0; i < 10; i++ {
		code, _, err := app.svc.NewChatBindCode(mustUser(t, app, u.ID), chat.Lark, "")
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); app.redeem("ou_1", code) }()
		go func() {
			defer wg.Done()
			if err := app.svc.AdminSetPassword(admin, u.ID, fmt.Sprintf("newpass9%d", i)); err != nil {
				t.Error(err)
			}
		}()
		wg.Wait()
		if rows := app.chatRows(u.ID); len(rows) != 0 {
			t.Fatalf("第 %d 轮:重置口令之后还留着绑定 %+v", i, rows)
		}
	}
}

// beforeTokenBump 在「会话代次即将提升」的那一刻(旧 token 还认得)跑一次 fn。
func (a *testApp) beforeTokenBump(t *testing.T, fn func(tx *gorm.DB)) {
	t.Helper()
	const name = "test:before_bump"
	fired := false
	err := a.repo.DB().Callback().Update().Before("gorm:begin_transaction").Register(name, func(tx *gorm.DB) {
		if cols, ok := tx.Statement.Dest.(map[string]any); ok && !fired {
			if _, bump := cols["token_version"]; bump {
				fired = true
				fn(tx)
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = a.repo.DB().Callback().Update().Remove(name)
		if !fired {
			t.Error("没有等到提升会话代次的那一步,用例没有生效")
		}
	})
}

// chatResets 是会作废会话的三件事;做之前旧会话都还活着。
var chatResets = map[string]func(app *testApp, admin, self string, id int64) apiResp{
	"管理员重置口令": func(app *testApp, admin, _ string, id int64) apiResp {
		return app.do(http.MethodPost, fmt.Sprintf("/api/v1/users/%d/password", id), admin, map[string]any{"password": "newpass99"})
	},
	"管理员重置 MFA": func(app *testApp, admin, _ string, id int64) apiResp {
		return app.do(http.MethodPost, fmt.Sprintf("/api/v1/users/%d/mfa/reset", id), admin, nil)
	},
	"本人改口令": func(app *testApp, _, self string, _ int64) apiResp {
		return app.do(http.MethodPost, "/api/v1/auth/password", self, map[string]any{"oldPassword": "aegisdb123", "newPassword": "newpass99"})
	},
}

// 凭据已经写下、旧会话还没作废的空当里,旧会话看到的就是新凭据 —— 光比凭据发现不了它。
// 它在空当里拿到的码、以及空当里过了鉴权而晚些才存的码,重置之后都不能留着。
func TestChatUnbind_OldSessionInResetWindow(t *testing.T) {
	for name, reset := range chatResets {
		t.Run(name, func(t *testing.T) {
			app, _ := chatApp(t)
			admin := app.login("linwei@aegisdb.io", "aegisdb123")
			old := app.login("chenhao@aegisdb.io", "aegisdb123")  // 被盗的会话
			self := app.login("chenhao@aegisdb.io", "aegisdb123") // 本人的会话
			u := app.storedUser("chenhao@aegisdb.io")
			if name == "管理员重置 MFA" {
				app.setupMFA(old) // 重置之后 MFA 是关的,空当里不再要码
			}
			var leaked apiResp
			var authed *model.User
			app.beforeTokenBump(t, func(*gorm.DB) {
				leaked = app.do(http.MethodPost, larkCode, old, map[string]any{})
				authed = mustUser(t, app, u.ID) // 空当里过了鉴权、还没存码的另一个请求
			})
			if r := reset(app, admin, self, u.ID); r.Code != 0 {
				t.Fatalf("code=%d msg=%s", r.Code, r.Msg)
			}
			if rows := app.chatCodes(u.ID); len(rows) != 0 {
				t.Errorf("重置之后还留着绑定码: %+v", rows)
			}
			var d struct {
				Code string `json:"code"`
			}
			_ = json.Unmarshal(leaked.Data, &d)
			if reply := app.redeem("ou_evil", d.Code); strings.Contains(reply, boundReply) {
				t.Errorf("已作废的会话在空当里拿到的码兑成了: %q", reply)
			}
			if _, _, err := app.svc.NewChatBindCode(authed, chat.Lark, ""); err != service.ErrChatUserChanged {
				t.Errorf("空当里过了鉴权的请求晚些才存码: %v", err)
			}
			if rows := app.chatCodes(u.ID); len(rows) != 0 {
				t.Errorf("不该留下绑定码: %+v", rows)
			}
			if rows := app.chatRows(u.ID); len(rows) != 0 {
				t.Errorf("不该绑上: %+v", rows)
			}
		})
	}
}

// 作废会话失败:照旧报错,但绑定要解掉 —— 凭据已经换了。停用同理。
func TestChatUnbind_EvenIfTokenBumpFails(t *testing.T) {
	resets := map[string]func(app *testApp, admin, self string, id int64) apiResp{
		"停用": func(app *testApp, admin, _ string, id int64) apiResp {
			return app.do(http.MethodPatch, fmt.Sprintf("/api/v1/users/%d", id), admin, map[string]any{"status": "disabled"})
		},
	}
	for k, v := range chatResets {
		resets[k] = v
	}
	for name, reset := range resets {
		t.Run(name, func(t *testing.T) {
			app, _ := chatApp(t)
			admin := app.login("linwei@aegisdb.io", "aegisdb123")
			self := app.bindAs("chenhao@aegisdb.io", "ou_1")
			u := app.storedUser("chenhao@aegisdb.io")
			app.beforeTokenBump(t, func(tx *gorm.DB) { _ = tx.AddError(errors.New("bump failed")) })
			if r := reset(app, admin, self, u.ID); r.Code == 0 {
				t.Error("作废会话失败应如实报错")
			}
			if rows := app.chatRows(u.ID); len(rows) != 0 {
				t.Errorf("绑定应解掉: %+v", rows)
			}
		})
	}
}

// 改写已有绑定时那一行已经没了:当作失败,不能回「已绑定」而库里什么都没有;码也没用掉。
func TestChatBind_ReplaceLosesRow(t *testing.T) {
	app, _ := chatApp(t)
	token := app.bindAs("chenhao@aegisdb.io", "ou_1")
	u := app.storedUser("chenhao@aegisdb.io")
	code := app.bindCode(token)
	const name = "test:lose_row"
	_ = app.repo.DB().Callback().Update().Before("gorm:update").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "tbl_chat_binding" {
			tx.Session(&gorm.Session{NewDB: true}).Exec("DELETE FROM tbl_chat_binding")
		}
	})
	if reply := app.redeem("ou_2", code); !strings.Contains(reply, "绑定失败") {
		t.Errorf("回复: %q", reply)
	}
	_ = app.repo.DB().Callback().Update().Remove(name)
	if rows := app.chatRows(u.ID); len(rows) != 1 || rows[0].AccountID != "ou_1" {
		t.Errorf("应整体回滚: %+v", rows)
	}
	if reply := app.redeem("ou_2", code); !strings.Contains(reply, boundReply) {
		t.Errorf("码不该被用掉: %q", reply)
	}
}

// 绑定与解绑的审计能对上是同一个聊天账号,但不写出账号标识。
func TestChatAudit_AccountFingerprint(t *testing.T) {
	app, _ := chatApp(t)
	token := app.bindAs("chenhao@aegisdb.io", "ou_fingerprint")
	eq(t, app.do(http.MethodDelete, bindURL+"/lark", token, nil).Code, 0, "解绑")
	sum := sha256.Sum256([]byte("ou_fingerprint"))
	for _, p := range []string{"chat.bind %", "chat.unbind %"} {
		rows := app.auditLike(p)
		if len(rows) != 1 || !strings.Contains(rows[0].Command, "account="+hex.EncodeToString(sum[:])[:12]+" ") || strings.Contains(rows[0].Command, "ou_fingerprint") {
			t.Errorf("%s: %+v", p, rows)
		}
	}
}

// 带类型的 nil 也是「关闭」,不能让后面的调用崩掉。
func TestChatTransport_TypedNil(t *testing.T) {
	app, _ := chatApp(t)
	var none *fakeChat
	app.svc.SetChatTransport(chat.Lark, none)
	raw, _ := app.chatBindings(app.login("chenhao@aegisdb.io", "aegisdb123"))
	eq(t, raw, "[]", "带类型的 nil")
}
