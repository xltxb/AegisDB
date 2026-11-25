package bootstrap

// 卡片按钮的点击(.scratch/chat-approval-cards/PRD.md §2、§5.4)。
//
// 一次点击批的是生产变更,所以这里钉住的主要是「不能」:不是收件人的、没有资格的、过期的点击都不能
// 改动单子;一张单不能被决定两次;通过不等于执行。

import (
	"bytes"
	"context"
	"errors"
	"log"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"aegisdb/internal/chat"
	"aegisdb/internal/model"
	"aegisdb/internal/service"
)

const (
	toastInvalid = "卡片已失效,请到网关查看。"
	toastHandled = "这张审批单已经处理过了。"
	toastRetry   = "处理失败,请稍后再试。"
	toastUnbound = "这个聊天账号没有绑定网关账号,请先在网关里绑定。"
	toastChain   = "你不在这张审批单的审批链上。"
)

// larkClick 是 email 绑定的 Lark 账号点一下。
func (a *testApp) larkClick(email, token string, act chat.Action) chat.ClickResult {
	a.t.Helper()
	return a.svc.OnClick(context.Background(), chat.Click{Channel: chat.Lark, BotID: chatBot,
		AccountID: "lark:" + email, Token: token, Action: act})
}

// cardToken 是最近一次发给某个账号的、这张单的卡片上的令牌。
func cardToken(t *testing.T, f *fakeChat, apNo, account string) string {
	t.Helper()
	token := ""
	for _, s := range f.sends() {
		if cardNo(s.Card) == apNo && s.AccountID == account {
			token = s.Card.Token
		}
	}
	if token == "" {
		t.Fatalf("%s 没收到 %s 的卡片", account, apNo)
	}
	return token
}

// sentCommand 建一张高危命令单并发出卡片,返回它和两个收件人的令牌。
func (a *testApp) sentCommand(f *fakeChat) (id int64, no, token1, token2 string) {
	a.t.Helper()
	id, no = a.submitCommand()
	a.sweepCards()
	return id, no, cardToken(a.t, f, no, "lark:"+owner1), cardToken(a.t, f, no, "lark:"+owner2)
}

func (a *testApp) ticket(id int64) *model.Approval {
	a.t.Helper()
	ap, err := a.repo.GetApproval(id)
	if err != nil {
		a.t.Fatal(err)
	}
	return ap
}

// decisionAudits 是一张单上的决定留下的审计:只有决定才带操作人。
func (a *testApp) decisionAudits(apNo string) []model.AuditLog {
	a.t.Helper()
	var rows []model.AuditLog
	if err := a.repo.DB().Where("approval_no = ? AND operator <> ''", apNo).Order("id").Find(&rows).Error; err != nil {
		a.t.Fatal(err)
	}
	return rows
}

// untouched 断言单子与卡片行都没动过。
func (a *testApp) untouched(what string, id int64, no string, cards []model.ChatCard) {
	a.t.Helper()
	if ap := a.ticket(id); ap.Status != model.StatusPending || ap.DecidedVia != "" || ap.RejectReason != "" {
		a.t.Errorf("%s:单子被改了 %s / %q / %q", what, ap.Status, ap.DecidedVia, ap.RejectReason)
	}
	if rows := a.decisionAudits(no); len(rows) != 0 {
		a.t.Errorf("%s:多了审计 %+v", what, rows)
	}
	now := a.cardsOf(id)
	if len(now) != len(cards) {
		a.t.Fatalf("%s:卡片 %d → %d 张", what, len(cards), len(now))
	}
	for i := range cards {
		if now[i].State != cards[i].State || now[i].TokenHash != cards[i].TokenHash {
			a.t.Errorf("%s:卡片行被改了 %+v → %+v", what, cards[i], now[i])
		}
	}
}

// 通过:记在点的那个人名下,命令不执行(ADR 0010),应答里带着结论卡片;别人的卡片由扫描收敛。
func TestChatClick_Approve(t *testing.T) {
	app, lark, _ := cardApp(t)
	admin := app.login(cardAdmin, "aegisdb123")
	dev, prod := app.sameFileConns(admin, "chat-click")
	eq(t, app.execSQL(admin, dev, `CREATE TABLE t_victim (id INTEGER PRIMARY KEY)`).Code, 0, "建表")
	view := app.interceptedTicket(admin, prod, `DROP TABLE t_victim`)
	app.sweepCards()

	// owner2 不是链上第一个:步骤要记在真正点的人那一步上
	res := app.larkClick(owner2, cardToken(t, lark, view.ApNo, "lark:"+owner2), chat.ActConfirm)
	if !res.OK || res.Toast != "已通过" {
		t.Errorf("应答: %+v", res)
	}
	if res.Card == nil || !strings.HasPrefix(res.Card.Outcome, "已由 Li Na 通过 · ") || res.Card.Token != "" || res.Card.Confirming ||
		cardNo(*res.Card) != view.ApNo || !strings.Contains(res.Card.Statement, "t_victim") {
		t.Errorf("结论卡片: %+v", res.Card)
	}
	ap := app.ticket(view.ID)
	eq(t, ap.Status, model.StatusApproved, "状态")
	eq(t, ap.DecidedVia, "lark", "渠道")
	if ap.ExecutedAt != nil || ap.ExecStatus != "" {
		t.Errorf("通过不等于执行: executed_at=%v exec_status=%q", ap.ExecutedAt, ap.ExecStatus)
	}
	if !app.tableExists(admin, dev, "t_victim") {
		t.Fatal("卡片上一通过命令就跑了")
	}
	steps, err := app.repo.StepsOf(view.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range steps {
		if mine := st.ApproverID == app.storedUser(owner2).ID; mine != (st.Status == model.StatusApproved) || mine != (st.ActedAt != nil) {
			t.Errorf("步骤: %+v", st)
		}
	}
	rows := app.decisionAudits(view.ApNo)
	if len(rows) != 1 || rows[0].Operator != "Li Na (Lark)" || rows[0].Source != model.AuditSourceApproval || rows[0].ClientIP != "" ||
		rows[0].EventType != "approve" || rows[0].ActorID != app.storedUser(cardAdmin).ID {
		t.Errorf("审计: %+v", rows)
	}
	if body := app.decisionNotif(admin, view.ApNo).Body; !strings.HasPrefix(body, "Li Na 通过了") {
		t.Errorf("通知: %q", body)
	}

	// 别人的卡片由扫描收敛;点过的这一张再更新一次也是同样的结论
	app.sweepCards()
	ups := lark.updates()
	if len(ups) != 2 || ups[0].Card.Outcome != res.Card.Outcome || ups[1].Card.Outcome != res.Card.Outcome {
		t.Errorf("收敛: %+v", ups)
	}
	app.dueCards()
	app.sweepCards()
	eq(t, len(lark.updates()), 2, "收敛一次就够")
}

// 四类单子都能在卡片上通过。
func TestChatClick_FourKinds(t *testing.T) {
	app, lark, _ := cardApp(t)
	for kind, source := range map[string]string{"command": model.AuditSourceApproval, "release": model.AuditSourceRelease,
		"window": model.AuditSourceApproval, "export": model.AuditSourceApproval} {
		t.Run(kind, func(t *testing.T) {
			id, no := app.raise(kind)
			app.sweepCards()
			res := app.larkClick(owner1, cardToken(t, lark, no, "lark:"+owner1), chat.ActConfirm)
			if !res.OK || res.Card == nil || !strings.HasPrefix(res.Card.Outcome, "已由 Zhang Wei 通过 · ") {
				t.Errorf("应答: %+v", res)
			}
			eq(t, app.ticket(id).Status, model.StatusApproved, "状态")
			rows := app.decisionAudits(no)
			if len(rows) != 1 || rows[0].Source != source || rows[0].Operator != "Zhang Wei (Lark)" {
				t.Errorf("审计: %+v", rows)
			}
		})
	}
}

func TestChatClick_Reject(t *testing.T) {
	app, lark, _ := cardApp(t)
	id, no, token, _ := app.sentCommand(lark)
	res := app.svc.OnClick(context.Background(), chat.Click{Channel: chat.Lark, BotID: chatBot, AccountID: "lark:" + owner1,
		Token: token, Action: chat.ActReject, Reason: "  影响大促\n"})
	if !res.OK || res.Toast != "已拒绝" || res.Card == nil || !strings.HasPrefix(res.Card.Outcome, "已由 Zhang Wei 拒绝 · ") || res.Card.Token != "" {
		t.Errorf("应答: %+v %+v", res, res.Card)
	}
	ap := app.ticket(id)
	eq(t, ap.Status, model.StatusRejected, "状态")
	eq(t, ap.RejectReason, "影响大促", "原因")
	eq(t, ap.DecidedVia, "lark", "渠道")
	if rows := app.decisionAudits(no); len(rows) != 1 || rows[0].Result != model.ResultRejected {
		t.Errorf("审计: %+v", rows)
	}

	// 通过时带来的原因不算数
	id, _, token, _ = app.sentCommand(lark)
	app.svc.OnClick(context.Background(), chat.Click{Channel: chat.Lark, BotID: chatBot, AccountID: "lark:" + owner1,
		Token: token, Action: chat.ActConfirm, Reason: "不该留下"})
	if ap := app.ticket(id); ap.Status != model.StatusApproved || ap.RejectReason != "" {
		t.Errorf("通过: %s %q", ap.Status, ap.RejectReason)
	}
}

// 二次确认:点「通过」只换卡片,不改状态;「返回」换回来;换过的卡片上按钮照样有效。
func TestChatClick_Confirming(t *testing.T) {
	app, lark, _ := cardApp(t)
	app.setting("notify.consoleURL", `"https://db.example.com"`)
	id, no, token, _ := app.sentCommand(lark)
	cards := app.cardsOf(id)
	var original chat.Card
	for _, s := range lark.sends() {
		if s.Card.Token == token {
			original = s.Card
		}
	}
	for round := 0; round < 2; round++ {
		res := app.larkClick(owner1, token, chat.ActApprove)
		if !res.OK || res.Card == nil || !res.Card.Confirming || res.Card.Token != token || res.Card.Outcome != "" || res.Card.Statement != original.Statement {
			t.Fatalf("通过: %+v %+v", res, res.Card)
		}
		app.untouched("通过(未确认)", id, no, cards)
		res = app.larkClick(owner1, token, chat.ActBack)
		if !res.OK || res.Card == nil {
			t.Fatalf("返回: %+v", res)
		}
		if !reflect.DeepEqual(*res.Card, original) {
			t.Errorf("返回后与发出时不一致: %+v / %+v", *res.Card, original)
		}
		app.untouched("返回", id, no, cards)
	}
	app.larkClick(owner1, token, chat.ActApprove)
	if res := app.larkClick(owner1, token, chat.ActConfirm); !res.OK {
		t.Errorf("确认: %+v", res)
	}
	eq(t, app.ticket(id).Status, model.StatusApproved, "状态")
}

// 没有资格的点击:单子不动,告诉对方卡在哪一条。四个动作一个都不放行。
func TestChatClick_Refused(t *testing.T) {
	actions := []chat.Action{chat.ActConfirm, chat.ActReject, chat.ActApprove, chat.ActBack}
	user := func(app *testApp, email string, fields map[string]any) {
		app.t.Helper()
		if err := app.repo.DB().Model(&model.User{}).Where("id = ?", app.storedUser(email).ID).UpdateColumns(fields).Error; err != nil {
			app.t.Fatal(err)
		}
	}
	for _, c := range []struct {
		name  string
		setup func(app *testApp, lark *fakeChat, id int64, k *chat.Click)
		toast string
	}{
		{"令牌不存在", func(_ *testApp, _ *fakeChat, _ int64, k *chat.Click) { k.Token = strings.Repeat("A", 32) }, toastInvalid},
		{"空令牌", func(_ *testApp, _ *fakeChat, _ int64, k *chat.Click) { k.Token = "" }, toastInvalid},
		{"令牌长得不像", func(_ *testApp, _ *fakeChat, _ int64, k *chat.Click) { k.Token += "x" }, toastInvalid},
		{"拿库里的摘要当令牌", func(app *testApp, _ *fakeChat, id int64, k *chat.Click) { k.Token = app.cardOf(id, owner1).TokenHash }, toastInvalid},
		{"令牌属于别人的卡片", func(_ *testApp, _ *fakeChat, _ int64, k *chat.Click) { k.AccountID = "lark:" + owner2 }, toastInvalid},
		{"没绑定的账号", func(_ *testApp, _ *fakeChat, _ int64, k *chat.Click) { k.AccountID = "ou_stranger" }, toastUnbound},
		{"空账号", func(_ *testApp, _ *fakeChat, _ int64, k *chat.Click) { k.AccountID = "" }, toastUnbound},
		{"账号大小写不同", func(_ *testApp, _ *fakeChat, _ int64, k *chat.Click) { k.AccountID = strings.ToUpper(k.AccountID) }, toastUnbound},
		{"解绑后", func(app *testApp, _ *fakeChat, _ int64, _ *chat.Click) {
			u := app.storedUser(owner1)
			if err := app.svc.UnbindChat(u, u.ID, "", "x"); err != nil {
				t.Fatal(err)
			}
		}, toastUnbound},
		{"聊天账号改绑给了别人", func(app *testApp, _ *fakeChat, _ int64, _ *chat.Click) {
			app.repo.DB().Where("user_id = ?", app.storedUser(owner1).ID).Delete(&model.ChatBinding{})
			app.repo.DB().Model(&model.ChatBinding{}).Where("user_id = ?", app.storedUser(owner2).ID).Update("account_id", "lark:"+owner1)
		}, toastInvalid},
		{"停用后", func(app *testApp, _ *fakeChat, _ int64, _ *chat.Click) {
			user(app, owner1, map[string]any{"status": "disabled"})
		}, "你的网关账号不可用,不能审批。"},
		{"未激活", func(app *testApp, _ *fakeChat, _ int64, _ *chat.Click) {
			user(app, owner1, map[string]any{"status": "invited"})
		}, "你的网关账号不可用,不能审批。"},
		{"成了服务账号", func(app *testApp, _ *fakeChat, _ int64, _ *chat.Click) {
			user(app, owner1, map[string]any{"kind": model.UserKindService})
		}, "服务账号不能审批。"},
		{"去掉审批菜单", func(app *testApp, _ *fakeChat, _ int64, _ *chat.Click) {
			for _, rid := range app.repo.EffectiveRoleIDs(app.storedUser(owner1)) {
				if err := app.repo.SetMenus(rid, map[string]bool{"approve": false}); err != nil {
					t.Fatal(err)
				}
			}
		}, "你没有「审批」菜单的权限,请联系管理员。"},
		{"被移出链", func(app *testApp, _ *fakeChat, id int64, _ *chat.Click) {
			app.repo.DB().Where("approval_id = ? AND approver_id = ?", id, app.storedUser(owner1).ID).Delete(&model.ApprovalStep{})
		}, toastChain},
		{"发起人自己", func(app *testApp, _ *fakeChat, id int64, _ *chat.Click) {
			app.repo.DB().Model(&model.Approval{}).Where("id = ?", id).Update("initiator_id", app.storedUser(owner1).ID)
		}, "不能审批自己发起的单子,请审批链上的其他人处理。"},
		{"来自别的机器人", func(_ *testApp, _ *fakeChat, _ int64, k *chat.Click) { k.BotID = "cli_other" }, "这个审批机器人没有开启,请到网关处理。"},
		{"没带机器人标识", func(_ *testApp, lark *fakeChat, _ int64, k *chat.Click) { lark.setBot("", ""); k.BotID = "" }, "这个审批机器人没有开启,请到网关处理。"},
		{"渠道关着", func(app *testApp, _ *fakeChat, _ int64, _ *chat.Click) { app.svc.SetChatTransport(chat.Lark, nil) }, "这个审批机器人没有开启,请到网关处理。"},
		{"没有这个渠道", func(_ *testApp, _ *fakeChat, _ int64, k *chat.Click) { k.Channel = "console" }, "这个审批机器人没有开启,请到网关处理。"},
		{"卡片是旧机器人发的", func(app *testApp, lark *fakeChat, _ int64, k *chat.Click) {
			lark.setBot("cli_new", "AegisBot")
			app.repo.DB().Model(&model.ChatBinding{}).Where("1 = 1").Update("bot_id", "cli_new")
			k.BotID = "cli_new"
		}, toastInvalid},
		{"另一个渠道的令牌", func(app *testApp, _ *fakeChat, _ int64, k *chat.Click) {
			app.bindRow(owner1, chat.Telegram, tgBot)
			k.Channel, k.BotID, k.AccountID = chat.Telegram, tgBot, "telegram:"+owner1
		}, toastInvalid},
		{"另一个渠道的令牌,机器人标识恰好相同", func(app *testApp, _ *fakeChat, _ int64, k *chat.Click) {
			app.svc.SetChatTransport(chat.Telegram, newFakeChat(chatBot, "same"))
			app.bindRow(owner1, chat.Telegram, chatBot)
			k.Channel, k.AccountID = chat.Telegram, "telegram:"+owner1
		}, toastInvalid},
		{"行的机器人对不上", func(app *testApp, _ *fakeChat, id int64, _ *chat.Click) {
			app.repo.DB().Model(&model.ChatCard{}).Where("approval_id = ?", id).Update("bot_id", "cli_old")
		}, toastInvalid},
		{"卡片已收场而单子还在等", func(app *testApp, _ *fakeChat, id int64, _ *chat.Click) {
			app.repo.DB().Model(&model.ChatCard{}).Where("approval_id = ?", id).Update("state", model.ChatCardClosed)
		}, toastInvalid},
	} {
		t.Run(c.name, func(t *testing.T) {
			app, lark, _ := cardApp(t)
			id, no, token, _ := app.sentCommand(lark)
			click := chat.Click{Channel: chat.Lark, BotID: chatBot, AccountID: "lark:" + owner1, Token: token}
			c.setup(app, lark, id, &click)
			cards := app.cardsOf(id)
			for _, act := range actions {
				click.Action = act
				res := app.svc.OnClick(context.Background(), click)
				if res.OK || res.Card != nil || res.Toast != c.toast {
					t.Errorf("%s: %+v,提示应为 %q", act, res, c.toast)
				}
			}
			app.untouched(c.name, id, no, cards)
		})
	}

	// 上面每一条都只差那一处:什么都不改时,同一个点击是放行的
	app, lark, _ := cardApp(t)
	id, _, token, _ := app.sentCommand(lark)
	if res := app.larkClick(owner1, token, "approve_all"); res.OK || res.Toast != toastInvalid || app.ticket(id).Status != model.StatusPending {
		t.Errorf("不认识的动作: %+v", res)
	}
	if res := app.larkClick(owner1, token, chat.ActConfirm); !res.OK {
		t.Errorf("对照组: %+v", res)
	}
}

// 开了自审时发起人能在卡片上批自己的单,与站内一致;之后关掉就不能。
func TestChatClick_SelfApprove(t *testing.T) {
	app, lark, _ := cardApp(t)
	app.setting("approval.allowSelfApprove", "true")
	id, no := app.submitCommand()
	app.sweepCards()
	token := cardToken(t, lark, no, "lark:"+cardAdmin)
	app.setting("approval.allowSelfApprove", "false")
	if res := app.larkClick(cardAdmin, token, chat.ActConfirm); res.OK || app.ticket(id).Status != model.StatusPending {
		t.Errorf("关了自审: %+v", res)
	}
	app.setting("approval.allowSelfApprove", "true")
	if res := app.larkClick(cardAdmin, token, chat.ActConfirm); !res.OK || app.ticket(id).Status != model.StatusApproved {
		t.Errorf("开了自审: %+v", res)
	}
}

// 没认出来的人从提示里看不出某个令牌对应的单子在不在、是什么样。
func TestChatClick_NoOracle(t *testing.T) {
	app, lark, _ := cardApp(t)
	pending, _, t1, _ := app.sentCommand(lark)
	done, _, t2, _ := app.sentCommand(lark)
	eq(t, app.cancel(app.login(cardAdmin, "aegisdb123"), done).Code, 0, "撤回")
	gone, _, t3, _ := app.sentCommand(lark)
	app.repo.DB().Delete(&model.Approval{}, gone)
	for _, account := range []string{"ou_stranger", "lark:" + owner2} {
		seen := map[chat.ClickResult]bool{}
		for _, token := range []string{strings.Repeat("A", 32), t1, t2, t3} {
			res := app.svc.OnClick(context.Background(), chat.Click{Channel: chat.Lark, BotID: chatBot, AccountID: account, Token: token, Action: chat.ActConfirm})
			seen[res] = true
		}
		if len(seen) != 1 || seen[chat.ClickResult{}] {
			t.Errorf("%s 看得出区别: %v", account, seen)
		}
	}
	eq(t, app.ticket(pending).Status, model.StatusPending, "状态")
	// 收件人自己的单子没了
	if res := app.larkClick(owner1, t3, chat.ActConfirm); res.OK || res.Toast != toastInvalid {
		t.Errorf("单子没了: %+v", res)
	}
}

// 已有结论的单:说已处理、换成结论卡片,不再审计,也不改写已有的决定。
func TestChatClick_AlreadyHandled(t *testing.T) {
	app, lark, _ := cardApp(t)
	admin := app.login(cardAdmin, "aegisdb123")
	for _, c := range []struct {
		name  string
		leave func(id int64, token1 string)
		want  string
	}{
		{"别人在站内通过", func(id int64, _ string) {
			eq(t, app.do(http.MethodPost, "/api/v1/approvals/"+itoa(id)+"/approve", app.login(owner2, "aegisdb123"), nil).Code, 0, "通过")
		}, "已由 Li Na 通过 · "},
		{"自己在卡片上拒绝过", func(_ int64, token string) { app.larkClick(owner1, token, chat.ActReject) }, "已由 Zhang Wei 拒绝 · "},
		{"发起人撤回", func(id int64, _ string) { eq(t, app.cancel(admin, id).Code, 0, "撤回") }, "已撤回"},
		{"作废", func(id int64, _ string) {
			if err := app.repo.UpdateApprovalStatus(id, model.StatusExpired); err != nil {
				t.Fatal(err)
			}
		}, "已作废"},
	} {
		t.Run(c.name, func(t *testing.T) {
			id, no, token, _ := app.sentCommand(lark)
			c.leave(id, token)
			before, audits := *app.ticket(id), len(app.decisionAudits(no))
			check := func(what string) {
				t.Helper()
				for _, act := range []chat.Action{chat.ActConfirm, chat.ActReject, chat.ActApprove, chat.ActBack} {
					res := app.larkClick(owner1, token, act)
					if res.OK || res.Toast != toastHandled {
						t.Errorf("%s %s: %+v", what, act, res)
					}
					if res.Card == nil || !strings.HasPrefix(res.Card.Outcome, c.want) || res.Card.Token != "" || res.Card.Confirming {
						t.Errorf("%s %s 的卡片: %+v", what, act, res.Card)
					}
				}
			}
			check("收敛之前")
			app.sweepCards()
			eq(t, app.cardOf(id, owner1).State, model.ChatCardClosed, "收敛")
			check("收敛之后")
			after := app.ticket(id)
			if after.Status != before.Status || after.DecidedVia != before.DecidedVia || after.RejectReason != before.RejectReason {
				t.Errorf("决定被改写: %+v", after)
			}
			eq(t, len(app.decisionAudits(no)), audits, "审计")
		})
	}
}

// 窗口改过之后的旧单:即便还挂着 pending,点下去也只是把它撤掉,不算一次决定。
func TestChatClick_StaleWindowTicket(t *testing.T) {
	app, lark, _ := cardApp(t)
	id, no := app.raise("window")
	app.sweepCards()
	token := cardToken(t, lark, no, "lark:"+owner1)
	app.repo.DB().Model(&model.ExecWindow{}).Where("id = ?", app.ticket(id).WindowID).UpdateColumn("approval_id", 999999)
	res := app.larkClick(owner1, token, chat.ActConfirm)
	if res.OK || res.Toast != toastHandled || res.Card == nil || res.Card.Outcome != "已作废" {
		t.Errorf("应答: %+v %+v", res, res.Card)
	}
	if ap := app.ticket(id); ap.Status != model.StatusCancelled || ap.DecidedVia != "" {
		t.Errorf("单子: %s %q", ap.Status, ap.DecidedVia)
	}
}

// 已经送到、库里却还没记成已发的卡片,按钮照样有效;决定之后扫描不会再发一张。
func TestChatClick_DeliveredButNotMarked(t *testing.T) {
	for _, state := range []string{model.ChatCardQueued, model.ChatCardFailed} {
		t.Run(state, func(t *testing.T) {
			app, lark, _ := cardApp(t)
			id, no := app.submitCommand()
			app.repo.DB().Where("approval_id = ? AND user_id <> ?", id, app.storedUser(owner1).ID).Delete(&model.ChatCard{})
			token := ""
			// 消息到了对面,应答却丢了
			lark.setHooks(func(_ context.Context, _ string, c chat.Card) error { token = c.Token; return errors.New("timeout") }, nil)
			app.sweepCards()
			row := app.cardOf(id, owner1)
			if token == "" || row.State != model.ChatCardQueued {
				t.Fatalf("前置条件: %q %+v", token, row)
			}
			app.repo.DB().Model(&model.ChatCard{}).Where("id = ?", row.ID).UpdateColumn("state", state)
			lark.setHooks(nil, nil)
			if res := app.larkClick(owner1, token, chat.ActConfirm); !res.OK || res.Card == nil {
				t.Errorf("应答: %+v", res)
			}
			eq(t, app.ticket(id).Status, model.StatusApproved, "状态")
			for i := 0; i < 3; i++ {
				app.dueCards()
				app.sweepCards()
			}
			if got := sentTo(lark, no); len(got) != 0 {
				t.Errorf("决定之后又发了: %v", got)
			}
			if len(app.decisionAudits(no)) != 1 {
				t.Error("审计应恰好一条")
			}
		})
	}
}

// 重发换了令牌:旧卡片的按钮作废。
func TestChatClick_ResentCardReplacesToken(t *testing.T) {
	app, lark, _ := cardApp(t)
	id, no := app.submitCommand()
	old := map[string]string{}
	lark.setHooks(func(_ context.Context, account string, c chat.Card) error {
		old[account] = c.Token
		return errors.New("timeout")
	}, nil)
	app.sweepCards()
	lark.setHooks(nil, nil)
	app.dueCards()
	app.sweepCards()
	if res := app.larkClick(owner1, old["lark:"+owner1], chat.ActConfirm); res.OK || res.Toast != toastInvalid || app.ticket(id).Status != model.StatusPending {
		t.Errorf("旧卡片: %+v", res)
	}
	if res := app.larkClick(owner1, cardToken(t, lark, no, "lark:"+owner1), chat.ActConfirm); !res.OK {
		t.Errorf("新卡片: %+v", res)
	}
}

// 两个收件人同时点:恰好一个成功,审计恰好一条;输的一方得到已处理与结论卡片。
func TestChatClick_ConcurrentRecipients(t *testing.T) {
	app, lark, _ := cardApp(t)
	admin := app.login(cardAdmin, "aegisdb123")
	for i := 0; i < 20; i++ {
		ap := app.submitProdHighRisk(admin)
		app.sweepCards()
		t1, t2 := cardToken(t, lark, ap.ApNo, "lark:"+owner1), cardToken(t, lark, ap.ApNo, "lark:"+owner2)
		second, secondStatus := chat.ActConfirm, model.StatusApproved
		if i%2 == 1 {
			second, secondStatus = chat.ActReject, model.StatusRejected
		}
		start, r1, r2 := make(chan struct{}), make(chan chat.ClickResult, 1), make(chan chat.ClickResult, 1)
		go func() { <-start; r1 <- app.larkClick(owner1, t1, chat.ActConfirm) }()
		go func() { <-start; r2 <- app.larkClick(owner2, t2, second) }()
		close(start)
		a, b := <-r1, <-r2
		if a.OK == b.OK {
			t.Fatalf("第 %d 轮:应恰好一个成功 %+v %+v", i, a, b)
		}
		want, loser := model.StatusApproved, b
		if b.OK {
			want, loser = secondStatus, a
		}
		eq(t, app.ticket(ap.ID).Status, want, "状态")
		if loser.Toast != toastHandled || loser.Card == nil || loser.Card.Outcome == "" {
			t.Errorf("输的一方: %+v", loser)
		}
		eq(t, len(app.decisionAudits(ap.ApNo)), 1, "审计")
	}
}

// 卡片与站内同时决定同理。
func TestChatClick_ConcurrentWithConsole(t *testing.T) {
	app, lark, _ := cardApp(t)
	admin, peer := app.login(cardAdmin, "aegisdb123"), app.login(owner2, "aegisdb123")
	for i := 0; i < 20; i++ {
		ap := app.submitProdHighRisk(admin)
		app.sweepCards()
		token := cardToken(t, lark, ap.ApNo, "lark:"+owner1)
		start, r1, r2 := make(chan struct{}), make(chan chat.ClickResult, 1), make(chan int, 1)
		go func() { <-start; r1 <- app.larkClick(owner1, token, chat.ActConfirm) }()
		go func() {
			<-start
			r2 <- app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/reject", peer, nil).Code
		}()
		close(start)
		card, console := <-r1, <-r2
		if card.OK == (console == 0) {
			t.Fatalf("第 %d 轮:应恰好一个成功 %+v / %d", i, card, console)
		}
		got := app.ticket(ap.ID)
		if card.OK && (got.Status != model.StatusApproved || got.DecidedVia != "lark") ||
			!card.OK && (got.Status != model.StatusRejected || got.DecidedVia != "console") {
			t.Errorf("两次决定混在了一起: %s / %s", got.Status, got.DecidedVia)
		}
		if !card.OK && (card.Toast != toastHandled || card.Card == nil || card.Card.Outcome == "") {
			t.Errorf("卡片输了: %+v", card)
		}
		eq(t, len(app.decisionAudits(ap.ApNo)), 1, "审计")
	}
}

// 测试卡片的按钮只回一句话:不查卡片、不查单子,带着真令牌也一样。
func TestChatClick_Test(t *testing.T) {
	app, lark, _ := cardApp(t)
	id, no, token, _ := app.sentCommand(lark)
	cards := app.cardsOf(id)
	var queries int64
	app.beforeQuery(t, func(tx *gorm.DB) {
		if tx.Statement.Table != "tbl_setting" {
			atomic.AddInt64(&queries, 1)
		}
	})
	for _, tok := range []string{"", token} {
		res := app.larkClick(owner1, tok, chat.ActTest)
		if !res.OK || res.Card != nil || res.Toast != "测试成功。这是测试卡片,没有审批任何单子。" {
			t.Errorf("应答: %+v", res)
		}
	}
	eq(t, atomic.LoadInt64(&queries), int64(0), "除设置外不查任何表")
	app.untouched("测试卡片", id, no, cards)
}

// beforeQuery 在每次查询之前跑 fn。
func (a *testApp) beforeQuery(t *testing.T, fn func(tx *gorm.DB)) {
	t.Helper()
	const name = "test:chat_click_query"
	if err := a.repo.DB().Callback().Query().Before("gorm:query").Register(name, fn); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.repo.DB().Callback().Query().Remove(name) })
}

// 库出错:请对方再试,什么都不改 —— 尤其不能把读不出来说成「不在链上」「没有权限」,也不能把一张
// 窗口单当成旧单撤掉。
func TestChatClick_DatabaseErrors(t *testing.T) {
	app, lark, _ := cardApp(t)
	var broken atomic.Value
	broken.Store("")
	app.beforeQuery(t, func(tx *gorm.DB) {
		if tx.Statement.Table == broken.Load() {
			_ = tx.AddError(errors.New("injected"))
		}
	})
	for _, c := range []struct{ table, kind string }{
		{"tbl_chat_card", "command"}, {"tbl_chat_binding", "command"}, {"tbl_user", "command"}, {"tbl_role_member", "command"}, {"tbl_role", "command"},
		{"tbl_role_menu", "command"}, {"tbl_approval", "command"}, {"tbl_approval_step", "command"}, {"tbl_exec_window", "window"},
	} {
		t.Run(c.table, func(t *testing.T) {
			id, no := app.raise(c.kind)
			app.sweepCards()
			token := cardToken(t, lark, no, "lark:"+owner1)
			cards := app.cardsOf(id)
			for _, act := range []chat.Action{chat.ActConfirm, chat.ActReject} {
				broken.Store(c.table)
				res := app.larkClick(owner1, token, act)
				broken.Store("")
				if res.OK || res.Card != nil || res.Toast != toastRetry {
					t.Errorf("%s: %+v", act, res)
				}
				app.untouched(c.table, id, no, cards)
			}
			if res := app.larkClick(owner1, token, chat.ActConfirm); !res.OK {
				t.Errorf("恢复之后: %+v", res)
			}
		})
	}

	t.Run("写不进去", func(t *testing.T) {
		id, no, token, _ := app.sentCommand(lark)
		cards := app.cardsOf(id)
		const name = "test:chat_click_update"
		if err := app.repo.DB().Callback().Update().Before("gorm:update").Register(name, func(tx *gorm.DB) {
			if tx.Statement.Table == "tbl_approval" {
				_ = tx.AddError(errors.New("injected"))
			}
		}); err != nil {
			t.Fatal(err)
		}
		res := app.larkClick(owner1, token, chat.ActConfirm)
		if err := app.repo.DB().Callback().Update().Remove(name); err != nil {
			t.Fatal(err)
		}
		if res.OK || res.Card != nil || res.Toast != toastRetry {
			t.Errorf("应答: %+v", res)
		}
		app.untouched("写不进去", id, no, cards)
	})

	// 旧窗口单要撤、却没撤成:单子还在等,不能说成已处理,更不能画上「已作废」。
	t.Run("旧窗口单撤不掉", func(t *testing.T) {
		id, no := app.raise("window")
		app.sweepCards()
		token := cardToken(t, lark, no, "lark:"+owner1)
		app.repo.DB().Model(&model.ExecWindow{}).Where("id = ?", app.ticket(id).WindowID).UpdateColumn("approval_id", 999999)
		cards := app.cardsOf(id)
		const name = "test:chat_click_stale"
		if err := app.repo.DB().Callback().Update().Before("gorm:update").Register(name, func(tx *gorm.DB) {
			if tx.Statement.Table == "tbl_approval" {
				_ = tx.AddError(errors.New("injected"))
			}
		}); err != nil {
			t.Fatal(err)
		}
		res := app.larkClick(owner1, token, chat.ActConfirm)
		_ = app.repo.DB().Callback().Update().Remove(name)
		if res.OK || res.Card != nil || res.Toast != toastRetry {
			t.Errorf("应答: %+v %+v", res, res.Card)
		}
		app.untouched("旧窗口单撤不掉", id, no, cards)
	})

	// 决定已经写下,只是画不出结论卡片:决定照样算数,也照样说成功。
	t.Run("决定之后读不出来", func(t *testing.T) {
		id, _, token, _ := app.sentCommand(lark)
		const name = "test:chat_click_after"
		if err := app.repo.DB().Callback().Update().After("gorm:update").Register(name, func(tx *gorm.DB) {
			if tx.Statement.Table == "tbl_approval" {
				broken.Store("tbl_approval")
			}
		}); err != nil {
			t.Fatal(err)
		}
		res := app.larkClick(owner1, token, chat.ActConfirm)
		_ = app.repo.DB().Callback().Update().Remove(name)
		broken.Store("")
		if !res.OK || res.Card != nil || res.Toast != "已通过" {
			t.Errorf("应答: %+v", res)
		}
		eq(t, app.ticket(id).Status, model.StatusApproved, "状态")
	})
}

// 每次都读库里此刻的用户,不看谁手里的旧副本。
func TestChatClick_RoleChangesTakeEffect(t *testing.T) {
	app, lark, _ := cardApp(t)
	id, _, token, _ := app.sentCommand(lark)
	u := app.storedUser(owner1)
	roles := app.repo.EffectiveRoleIDs(u)
	if err := app.repo.ClearUserRoles(u.ID); err != nil {
		t.Fatal(err)
	}
	if res := app.larkClick(owner1, token, chat.ActConfirm); res.OK || !strings.Contains(res.Toast, "审批") || app.ticket(id).Status != model.StatusPending {
		t.Errorf("没有角色: %+v", res)
	}
	if err := app.repo.SetUserRoles(u.ID, roles); err != nil {
		t.Fatal(err)
	}
	if res := app.larkClick(owner1, token, chat.ActConfirm); !res.OK {
		t.Errorf("角色恢复: %+v", res)
	}
}

// Telegram:渠道如实记下;没带 SQL 的卡片,重画(二次确认、结论)也不带。
func TestChatClick_Telegram(t *testing.T) {
	app, lark, tg := cardApp(t)
	app.bindRow(owner1, chat.Telegram, tgBot)
	id, no := app.submitCommand()
	app.sweepCards()
	token := cardToken(t, tg, no, "telegram:"+owner1)
	app.setting("chat.telegram.includeSql", "true") // 之后才打开
	click := func(act chat.Action) chat.ClickResult {
		return app.svc.OnClick(context.Background(), chat.Click{Channel: chat.Telegram, BotID: tgBot, AccountID: "telegram:" + owner1, Token: token, Action: act})
	}
	for _, act := range []chat.Action{chat.ActApprove, chat.ActBack, chat.ActConfirm} {
		res := click(act)
		if !res.OK || res.Card == nil || res.Card.Statement != "" || res.Card.Reason != "" || !res.Card.Partial {
			t.Fatalf("%s: %+v %+v", act, res, res.Card)
		}
	}
	eq(t, app.ticket(id).DecidedVia, "telegram", "渠道")
	if rows := app.decisionAudits(no); len(rows) != 1 || rows[0].Operator != "Zhang Wei (Telegram)" {
		t.Errorf("审计: %+v", rows)
	}
	// 同一张单在 Lark 上的卡片再点,是已处理
	res := app.larkClick(owner1, cardToken(t, lark, no, "lark:"+owner1), chat.ActConfirm)
	if res.OK || res.Toast != chat.Tr("zh", "click.handled") || res.Card == nil || res.Card.Token != "" ||
		!strings.HasPrefix(res.Card.Outcome, "已由 Zhang Wei 通过 · ") {
		t.Errorf("已处理: %+v %+v", res, res.Card)
	}
}

// 提示与卡片用卡片语言。
func TestChatClick_English(t *testing.T) {
	app, lark, _ := cardApp(t)
	app.setting("chat.cardLang", `"en"`)
	_, _, token, peer := app.sentCommand(lark)
	res := app.larkClick(owner2, token, chat.ActConfirm)
	eq(t, res.Toast, "This card is no longer valid. Check the gateway.", "失效")
	res = app.larkClick(owner1, token, chat.ActConfirm)
	if res.Toast != "Approved" || res.Card == nil || !strings.HasPrefix(res.Card.Outcome, "Approved by Zhang Wei · ") {
		t.Errorf("通过: %+v %+v", res, res.Card)
	}
	eq(t, app.larkClick(owner2, peer, chat.ActReject).Toast, "This request has already been handled.", "已处理")
}

// 令牌不进日志;认不出来的点击一行日志都不写(不然谁都能刷),有身份却没资格的记一行。
func TestChatClick_Logs(t *testing.T) {
	app, lark, _ := cardApp(t)
	app.repo.DB().Delete(&model.Setting{}, "k LIKE 'chat.%'") // 老库里没有这些键,也不该每点一次记一行
	_, _, token, _ := app.sentCommand(lark)
	var buf bytes.Buffer
	old, oldDB := slog.Default(), app.repo.DB().Logger
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	app.repo.DB().Logger = gormlogger.New(log.New(&buf, "", 0), gormlogger.Config{LogLevel: gormlogger.Warn, SlowThreshold: time.Minute})
	t.Cleanup(func() { slog.SetDefault(old); app.repo.DB().Logger = oldDB })

	app.larkClick(owner1, "junk", chat.ActConfirm)
	app.larkClick("stranger", token, chat.ActConfirm)
	app.larkClick("stranger", strings.Repeat("A", 32), chat.ActConfirm)
	if buf.Len() != 0 {
		t.Errorf("认不出来的点击写了日志: %s", buf.String())
	}
	app.larkClick(owner2, token, chat.ActConfirm)
	app.repo.DB().Where("approver_id = ?", app.storedUser(owner1).ID).Delete(&model.ApprovalStep{})
	app.larkClick(owner1, token, chat.ActConfirm)
	if n := strings.Count(buf.String(), "level=WARN"); n != 2 || strings.Contains(buf.String(), token) {
		t.Errorf("有身份没资格的各记一行、不带令牌,实际 %d 行: %s", n, buf.String())
	}
}

// 认得出是谁、卡片却对不上的(令牌不存在、旧机器人的卡片、已收场的卡片)也记一行,带用户不带令牌;同一个人
// 同一种一分钟只记一次,别人的照记 —— 不然一个绑定了的人能刷满日志。
func TestChatClick_LogsStaleCards(t *testing.T) {
	app, lark, _ := cardApp(t)
	id, _, token, peer := app.sentCommand(lark)
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	lines := func(why string) int { return strings.Count(buf.String(), "why=\""+why+"\"") }
	click := func(email, tok string) {
		t.Helper()
		if res := app.larkClick(email, tok, chat.ActConfirm); res.OK || res.Toast != toastInvalid {
			t.Errorf("应答: %+v", res)
		}
	}
	for range 3 {
		click(owner1, strings.Repeat("A", 32))
	}
	eq(t, lines("unknown token"), 1, "令牌不存在")
	click(owner2, strings.Repeat("A", 32))
	eq(t, lines("unknown token"), 2, "换一个人")
	app.repo.DB().Model(&model.ChatCard{}).Where("approval_id = ?", id).Update("state", model.ChatCardClosed)
	click(owner1, token)
	click(owner1, token)
	eq(t, lines("closed card"), 1, "已收场")
	app.repo.DB().Model(&model.ChatCard{}).Where("approval_id = ?", id).Update("bot_id", "cli_old")
	click(owner2, peer)
	eq(t, lines("other bot"), 1, "旧机器人")
	out := buf.String()
	if strings.Contains(out, token) || strings.Contains(out, peer) || strings.Contains(out, strings.Repeat("A", 32)) ||
		strings.Count(out, "level=WARN") != strings.Count(out, "user=") || !strings.Contains(out, "user="+itoa(app.storedUser(owner1).ID)) {
		t.Errorf("日志: %s", out)
	}
}

// 二次确认的应答由渠道去换卡片,可能晚到:其间单子被别人处理、扫描写下结论并收场的话,带按钮的卡片会盖在
// 结论上,再也没人去改。所以这之后扫描要等应答换完,再收敛一次。
func TestChatClick_OutcomeFollowsTwoStepAnswer(t *testing.T) {
	for _, act := range []chat.Action{chat.ActApprove, chat.ActBack} {
		t.Run(string(act), func(t *testing.T) {
			app, lark, _ := cardApp(t)
			id, _, token, _ := app.sentCommand(lark)
			ref := chat.Ref{ChatID: app.cardOf(id, owner1).ChatID, MessageID: app.cardOf(id, owner1).MessageID}
			last := func() *chat.Card {
				var c *chat.Card
				for _, u := range lark.updates() {
					if u.Ref == ref {
						c = &u.Card
					}
				}
				return c
			}
			before := time.Now()
			res := app.larkClick(owner1, token, act)
			if !res.OK || res.Card == nil {
				t.Fatalf("应答: %+v", res)
			}
			// 应答还在路上,别人在站内通过了
			eq(t, app.do(http.MethodPost, "/api/v1/approvals/"+itoa(id)+"/approve", app.login(owner2, "aegisdb123"), nil).Code, 0, "通过")
			app.sweepCards()
			eq(t, app.cardOf(id, owner2).State, model.ChatCardClosed, "没有应答在路上的照常收敛")
			if c := last(); c != nil {
				t.Errorf("应答还在路上就写了结论: %+v", c)
			}
			row := app.cardOf(id, owner1)
			eq(t, row.State, model.ChatCardSent, "还等着收敛")
			if at := row.NextAt.Sub(before); at < chat.CardWithin || at > chat.CardWithin+10*time.Second {
				t.Errorf("下次收敛在 %s 之后", at)
			}
			// 晚到的应答把按钮换了上去,到点再收敛
			if err := lark.Update(context.Background(), ref, *res.Card); err != nil {
				t.Fatal(err)
			}
			app.dueCards()
			app.sweepCards()
			if c := last(); c == nil || !strings.HasPrefix(c.Outcome, "已由 Li Na 通过") || c.Token != "" {
				t.Errorf("最后停在: %+v", c)
			}
			eq(t, app.cardOf(id, owner1).State, model.ChatCardClosed, "收场")
		})
	}
}

// 单子恰好在二次确认的应答成形之前被处理掉:不再给按钮,直接给结论。
func TestChatClick_DecidedWhileAnsweringTwoStep(t *testing.T) {
	app, lark, _ := cardApp(t)
	id, _, token, _ := app.sentCommand(lark)
	const name = "test:decided_meanwhile"
	if err := app.repo.DB().Callback().Update().After("gorm:update").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "tbl_chat_card" {
			tx.Session(&gorm.Session{NewDB: true}).Exec("UPDATE tbl_approval SET status = ? WHERE id = ?", model.StatusCancelled, id)
		}
	}); err != nil {
		t.Fatal(err)
	}
	res := app.larkClick(owner1, token, chat.ActApprove)
	_ = app.repo.DB().Callback().Update().Remove(name)
	if res.OK || res.Toast != toastHandled || res.Card == nil || res.Card.Outcome != "已撤回" || res.Card.Token != "" {
		t.Errorf("应答: %+v %+v", res, res.Card)
	}
}

// 私聊文字就是兑换绑定码。
func TestChatClick_OnText(t *testing.T) {
	app, _ := chatApp(t)
	var h chat.Handler = app.svc
	code := app.bindCode(app.login("chenhao@aegisdb.io", "aegisdb123"))
	if got := h.OnText(context.Background(), chat.Lark, chatBot, "ou_1", "oc_1", "/start "+code); !strings.Contains(got, boundReply) {
		t.Errorf("回复: %q", got)
	}
	if rows := app.chatRows(app.storedUser("chenhao@aegisdb.io").ID); len(rows) != 1 || rows[0].AccountID != "ou_1" || rows[0].ChatID != "oc_1" {
		t.Errorf("绑定: %+v", rows)
	}
}

// 决定的渠道只认已知的几个:它会写进库里、决定审计上的操作人。
func TestDecidedVia_UnknownChannelRefused(t *testing.T) {
	app := newTestApp(t)
	no := app.submitProdHighRisk(app.login(cardAdmin, "aegisdb123")).ApNo
	id := app.approvalIDByNo(no)
	for _, via := range []string{"", "Lark", "slack", "lark "} {
		for _, approve := range []bool{true, false} {
			if _, err := app.svc.DecideApprovalVia(app.storedUser(owner1), id, approve, via, ""); err != service.ErrBadRequest {
				t.Errorf("%q: %v", via, err)
			}
		}
	}
	app.untouched("不认识的渠道", id, no, nil)
	if _, err := app.svc.DecideApprovalVia(app.storedUser(owner1), id, true, "api", ""); err != nil {
		t.Errorf("api: %v", err)
	}
}
