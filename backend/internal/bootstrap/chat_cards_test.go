package bootstrap

// 审批卡片的排队、发送与收敛(.scratch/chat-approval-cards/PRD.md §5.2、§5.5)。
//
// 先落库、后台发:建单不等渠道,也不因它失败。卡片的内容在 internal/service/chat_cards_test.go。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"aegisdb/internal/chat"
	"aegisdb/internal/model"
	"aegisdb/internal/service"
)

const (
	tgBot     = "tg_test"
	cardAdmin = "linwei@aegisdb.io" // 建单的人
	owner1    = "zhangwei@aegisdb.io"
	owner2    = "lina@aegisdb.io"
	owner3    = "sunqi@aegisdb.io" // cardApp 里也进了默认审批链,但不绑定
)

// cardApp 是一个开着 Lark 与 Telegram 的网关。默认审批链上有四个人:owner1、owner2、owner3 与发起人
// 自己;owner3 没绑定,其余的在 Lark 上都绑了,Telegram 上谁都没绑。
func cardApp(t *testing.T) (app *testApp, lark, tg *fakeChat) {
	t.Helper()
	app = newTestApp(t)
	lark, tg = newFakeChat(chatBot, "AegisBot"), newFakeChat(tgBot, "aegis_bot")
	app.svc.SetChatTransport(chat.Lark, lark)
	app.svc.SetChatTransport(chat.Telegram, tg)
	role, err := app.repo.GetRoleByCode("owner")
	if err != nil {
		t.Fatal(err)
	}
	for _, email := range []string{cardAdmin, owner3} {
		if err := app.repo.AddMember(role.ID, app.storedUser(email).ID); err != nil {
			t.Fatal(err)
		}
	}
	for _, email := range []string{cardAdmin, owner1, owner2} {
		app.bindRow(email, chat.Lark, chatBot)
	}
	return app, lark, tg
}

// bindRow 直接写一条绑定;绑定流程自己的用例在 chat_binding_test.go。
func (a *testApp) bindRow(email string, ch chat.Channel, bot string) {
	a.t.Helper()
	mustCreate(a.t, a.repo.DB(), &model.ChatBinding{UserID: a.storedUser(email).ID, Channel: string(ch), BotID: bot,
		AccountID: string(ch) + ":" + email, ChatID: "c:" + email})
}

func (a *testApp) sweepCards() { a.svc.SweepChatCards(context.Background()) }

// dueCards 让所有卡片立刻到期,免得等退避。
func (a *testApp) dueCards() {
	a.t.Helper()
	if err := a.repo.DB().Model(&model.ChatCard{}).Where("1 = 1").UpdateColumn("next_at", time.Now().Add(-time.Second)).Error; err != nil {
		a.t.Fatal(err)
	}
}

func (a *testApp) cardsOf(approvalID int64) []model.ChatCard {
	a.t.Helper()
	var rows []model.ChatCard
	if err := a.repo.DB().Where("approval_id = ?", approvalID).Order("id").Find(&rows).Error; err != nil {
		a.t.Fatal(err)
	}
	return rows
}

// cardOf 是某人在某张单上的那一行(Lark)。
func (a *testApp) cardOf(approvalID int64, email string) model.ChatCard {
	a.t.Helper()
	id := a.storedUser(email).ID
	for _, c := range a.cardsOf(approvalID) {
		if c.UserID == id && c.Channel == string(chat.Lark) {
			return c
		}
	}
	a.t.Fatalf("单 %d 上没有 %s 的卡片", approvalID, email)
	return model.ChatCard{}
}

func cardNo(c chat.Card) string {
	for _, f := range c.Fields {
		if f.Label == "单号" || f.Label == "Ticket" {
			return f.Value
		}
	}
	return ""
}

// sentTo 是一张单的卡片都发给了哪些账号。
func sentTo(f *fakeChat, apNo string) []string {
	var out []string
	for _, s := range f.sends() {
		if cardNo(s.Card) == apNo {
			out = append(out, s.AccountID)
		}
	}
	return out
}

func (a *testApp) submitCommand() (int64, string) {
	a.t.Helper()
	no := a.submitProdHighRisk(a.login(cardAdmin, "aegisdb123")).ApNo
	return a.approvalIDByNo(no), no
}

func (a *testApp) setting(key, json string) {
	a.t.Helper()
	if err := a.repo.SetSetting(key, json); err != nil {
		a.t.Fatal(err)
	}
}

// 四类单子各一张,返回单号。
func (a *testApp) raise(kind string) (int64, string) {
	a.t.Helper()
	token := a.login(cardAdmin, "aegisdb123")
	switch kind {
	case "command":
		return a.submitCommand()
	case "release":
		pid := a.createPipeline(token, "卡片流程"+fmt.Sprint(time.Now().UnixNano()), "", []map[string]any{
			{"name": "人工审批", "type": "approve"}, {"name": "执行变更", "type": "execute"},
		})
		r := a.submitRelease(token, map[string]any{"title": "改价上线", "pipelineId": pid,
			"connectionId": a.connIDByEnv(token, "staging"), "sql": "SELECT 1;", "reason": "紧急"})
		eq(a.t, r.Code, 0, "提交发布单 "+r.Msg)
		var rel releaseView
		_ = json.Unmarshal(r.Data, &rel)
		a.waitRelease(token, rel.ID, "waiting", "failed", "success")
		var ap model.Approval
		if err := a.repo.DB().First(&ap, "release_id = ?", rel.ID).Error; err != nil {
			a.t.Fatal(err)
		}
		return ap.ID, ap.ApNo
	case "window":
		_, no := a.applyWindow(token, a.connIDByEnv(token, "prod"), "orders_db", "夜间班车")
		return a.approvalIDByNo(no), no
	case "export":
		conn := a.exportSetup(token)
		eq(a.t, a.do(http.MethodPost, "/api/v1/export", token, map[string]any{
			"connectionId": conn, "sql": "SELECT 1", "name": "raw", "includeSensitive": true}).Code, 0, "提交导出")
		job := a.lastExportJob(conn)
		return job.ApprovalID, job.ApNo
	}
	a.t.Fatalf("没有这类单: %s", kind)
	return 0, ""
}

func TestChatCards_FourKinds(t *testing.T) {
	app, lark, tg := cardApp(t)
	want := []string{"lark:" + owner1, "lark:" + owner2}
	titles := map[string]string{"command": "高危命令待审批", "release": "发布待审批", "window": "执行窗口待审批", "export": "敏感导出待审批"}
	for _, kind := range []string{"command", "release", "window", "export"} {
		t.Run(kind, func(t *testing.T) {
			id, no := app.raise(kind)
			for _, c := range app.cardsOf(id) {
				if c.State != model.ChatCardQueued || c.TokenHash != "" {
					t.Errorf("建单时只排队,不发: %+v", c)
				}
			}
			if n := len(sentTo(lark, no)); n != 0 {
				t.Errorf("建单途中就发了 %d 张", n)
			}
			app.sweepCards()
			// 链上绑定了的两人各一张;没绑定的、发起人自己(没开自审)没有
			if got := sentTo(lark, no); strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("收到卡片的: %v", got)
			}
			for _, s := range lark.sends() {
				if cardNo(s.Card) == no && !strings.HasPrefix(s.Card.Title, titles[kind]) {
					t.Errorf("标题 %q", s.Card.Title)
				}
			}
			row := app.cardOf(id, owner1)
			if row.State != model.ChatCardSent || row.MessageID == "" || row.ChatID != "chat-lark:"+owner1 || row.BotID != chatBot || row.Attempts != 0 || row.LastError != "" {
				t.Errorf("发出后的行: %+v", row)
			}
			app.sweepCards() // 再扫不重发,单子还等着也不更新
			if got := sentTo(lark, no); len(got) != 2 {
				t.Errorf("重复发送: %v", got)
			}
		})
	}
	if len(tg.sends()) != 0 || len(lark.updates()) != 0 {
		t.Errorf("没绑 Telegram 不该发;单子还在等不该更新")
	}
}

func TestChatCards_SelfApprove(t *testing.T) {
	app, lark, _ := cardApp(t)
	app.setting("approval.allowSelfApprove", "true")
	_, no := app.submitCommand()
	app.sweepCards()
	if got := strings.Join(sentTo(lark, no), ","); !strings.Contains(got, "lark:"+cardAdmin) || len(sentTo(lark, no)) != 3 {
		t.Errorf("开了自审,发起人也该收到: %s", got)
	}
	// 排队之后又关掉自审:发送时重新判
	_, no = app.submitCommand()
	app.setting("approval.allowSelfApprove", "false")
	app.sweepCards()
	if got := strings.Join(sentTo(lark, no), ","); strings.Contains(got, cardAdmin) {
		t.Errorf("发送时已不许自审: %s", got)
	}
}

// 每张卡片一个令牌,≥128 位,库里只有摘要;一人绑两个渠道就是两张、两个令牌。
func TestChatCards_Token(t *testing.T) {
	app, lark, tg := cardApp(t)
	app.bindRow(owner1, chat.Telegram, tgBot)
	id, _ := app.submitCommand()
	app.sweepCards()
	if len(lark.sends()) != 2 || len(tg.sends()) != 1 {
		t.Fatalf("发出 %d / %d 张", len(lark.sends()), len(tg.sends()))
	}
	seen := map[string]bool{}
	for _, s := range append(lark.sends(), tg.sends()...) {
		tok := s.Card.Token
		// Telegram 的 callback_data 只有 64 字节,还要放下动作
		if !regexp.MustCompile(`^[A-Za-z0-9_-]{22,48}$`).MatchString(tok) || seen[tok] {
			t.Errorf("令牌 %q", tok)
		}
		seen[tok] = true
		sum := sha256.Sum256([]byte(tok))
		row, err := app.repo.ChatCardByTokenHash(hex.EncodeToString(sum[:]))
		if err != nil || row.ApprovalID != id {
			t.Errorf("令牌摘要对不上卡片: %v", err)
		}
	}
	for _, c := range app.cardsOf(id) {
		if seen[c.TokenHash] {
			t.Error("库里存了令牌本身")
		}
	}
}

// 渠道关着时建的单一张都不排(也不补发);开着但机器人还没连上,先排着。
func TestChatCards_ChannelOff(t *testing.T) {
	app, lark, _ := cardApp(t)
	app.svc.SetChatTransport(chat.Lark, nil)
	id, _ := app.submitCommand()
	if n := len(app.cardsOf(id)); n != 0 {
		t.Errorf("渠道关着排了 %d 张", n)
	}
	app.sweepCards()

	app.svc.SetChatTransport(chat.Lark, lark)
	lark.setBot("", "")
	id, no := app.submitCommand()
	app.sweepCards()
	if row := app.cardOf(id, owner1); row.State != model.ChatCardQueued || row.Attempts != 0 {
		t.Errorf("没连上时只排着,不计失败: %+v", row)
	}
	lark.setBot(chatBot, "AegisBot")
	app.sweepCards()
	if len(sentTo(lark, no)) != 2 {
		t.Error("连上之后应发出")
	}
}

// Send 恒失败:建单照常;退避 5s、15s、45s、135s,第 5 次之后 failed;错误里没有换行与控制字符。
func TestChatCards_SendFails(t *testing.T) {
	app, lark, _ := cardApp(t)
	lark.setErrs(errors.New("lark\n99991\x00: rate limited"), nil)
	id, no := app.submitCommand()
	if ap, err := app.repo.GetApproval(id); err != nil || ap.Status != model.StatusPending {
		t.Fatalf("建单应照常成功: %v", err)
	}
	for i, wait := range []time.Duration{5 * time.Second, 15 * time.Second, 45 * time.Second, 135 * time.Second} {
		app.dueCards()
		before := time.Now()
		app.sweepCards()
		row := app.cardOf(id, owner1)
		if row.State != model.ChatCardQueued || row.Attempts != i+1 {
			t.Fatalf("第 %d 次之后: %+v", i+1, row)
		}
		if got := row.NextAt.Sub(before); got < wait-time.Second || got > wait+3*time.Second {
			t.Errorf("第 %d 次失败之后应等 %s,实际 %s", i+1, wait, got)
		}
		n := app.cardOf(id, owner1).Attempts
		app.sweepCards() // 没到时间不重试
		if app.cardOf(id, owner1).Attempts != n {
			t.Error("没到时间就重试了")
		}
	}
	app.dueCards()
	app.sweepCards()
	row := app.cardOf(id, owner1)
	if row.State != model.ChatCardFailed || row.LastError != "lark 99991 : rate limited" {
		t.Errorf("第 5 次之后: %+v", row)
	}
	lark.setErrs(nil, nil)
	app.dueCards()
	app.sweepCards()
	if len(sentTo(lark, no)) != 0 {
		t.Error("failed 之后不再发")
	}
}

// 发送之前单子已被决定:不发,直接收场。
func TestChatCards_DecidedBeforeSend(t *testing.T) {
	app, lark, _ := cardApp(t)
	id, no := app.submitCommand()
	eq(t, app.do(http.MethodPost, "/api/v1/approvals/"+itoa(id)+"/approve", app.login(owner1, "aegisdb123"), nil).Code, 0, "通过")
	app.sweepCards()
	if len(sentTo(lark, no)) != 0 {
		t.Error("已决定的单还在发卡片")
	}
	eq(t, len(app.cardsOf(id)), 2, "排了两张")
	for _, c := range app.cardsOf(id) {
		if c.State != model.ChatCardClosed || c.TokenHash != "" {
			t.Errorf("应直接 closed: %+v", c)
		}
	}
}

// 发送之前收件人的情况变了:停用、改成服务账号、解绑、换了机器人,都不发。
func TestChatCards_RecipientRecheckedAtSend(t *testing.T) {
	for name, change := range map[string]func(*testApp, *model.User){
		"停用":   func(a *testApp, u *model.User) { a.repo.DB().Model(u).Update("status", "disabled") },
		"服务账号": func(a *testApp, u *model.User) { a.repo.DB().Model(u).Update("kind", model.UserKindService) },
		"账号已删": func(a *testApp, u *model.User) { a.repo.DB().Delete(&model.User{}, u.ID) },
		"解绑":   func(a *testApp, u *model.User) { _, _ = a.repo.DeleteChatBindings(u.ID, "") },
		"换了机器人": func(a *testApp, u *model.User) {
			a.repo.DB().Model(&model.ChatBinding{}).Where("user_id = ?", u.ID).Update("bot_id", "cli_old")
		},
	} {
		t.Run(name, func(t *testing.T) {
			app, lark, _ := cardApp(t)
			id, no := app.submitCommand()
			u := app.storedUser(owner1)
			change(app, u)
			app.sweepCards()
			if got := strings.Join(sentTo(lark, no), ","); got != "lark:"+owner2 {
				t.Errorf("只该发给另一个人: %s", got)
			}
			for _, c := range app.cardsOf(id) {
				if c.UserID == u.ID && c.State != model.ChatCardClosed {
					t.Errorf("应收场: %+v", c)
				}
			}
		})
	}
}

// 机器人换了、本人也重新绑了:按新绑定发,行上记的是新机器人。
func TestChatCards_ReboundBeforeSend(t *testing.T) {
	app, lark, _ := cardApp(t)
	id, no := app.submitCommand()
	lark.setBot("cli_new", "AegisBot")
	app.repo.DB().Model(&model.ChatBinding{}).Where("user_id = ?", app.storedUser(owner1).ID).
		Updates(map[string]any{"bot_id": "cli_new", "account_id": "ou_new"})
	app.sweepCards()
	if got := strings.Join(sentTo(lark, no), ","); got != "ou_new" {
		t.Errorf("发给了 %s", got)
	}
	eq(t, app.cardOf(id, owner1).BotID, "cli_new", "行上的机器人")
	eq(t, app.cardOf(id, owner2).State, model.ChatCardClosed, "没重新绑的收场")
}

// 崩溃在「令牌已写、还没记成已发出」之间:再扫重新发一张,旧令牌作废。
func TestChatCards_CrashBetweenTokenAndSent(t *testing.T) {
	app, lark, _ := cardApp(t)
	id, no := app.submitCommand()
	row := app.cardOf(id, owner1)
	old := strings.Repeat("ab", 32)
	app.repo.DB().Model(&model.ChatCard{}).Where("id = ?", row.ID).
		Updates(map[string]any{"token_hash": old, "bot_id": chatBot, "attempts": 1})
	app.sweepCards()
	if len(sentTo(lark, no)) != 2 {
		t.Fatalf("应重新发出: %v", sentTo(lark, no))
	}
	if _, err := app.repo.ChatCardByTokenHash(old); err == nil {
		t.Error("旧令牌还查得到卡片")
	}
	row = app.cardOf(id, owner1)
	if row.State != model.ChatCardSent || len(row.TokenHash) != 64 || row.TokenHash == old {
		t.Errorf("重发后的行: %+v", row)
	}
}

// 令牌先落库再发:Send 进行时按令牌就查得到那一行,而且这时没有占着数据库或事务。
func TestChatCards_TokenStoredBeforeSend(t *testing.T) {
	app, lark, _ := cardApp(t)
	var found, blocked atomic.Int32
	lark.setHooks(func(_ context.Context, _ string, c chat.Card) error {
		sum := sha256.Sum256([]byte(c.Token))
		done := make(chan bool, 1)
		go func() {
			row, err := app.repo.ChatCardByTokenHash(hex.EncodeToString(sum[:]))
			done <- err == nil && row.State == model.ChatCardQueued
		}()
		select {
		case ok := <-done:
			if ok {
				found.Add(1)
			}
		case <-time.After(3 * time.Second):
			blocked.Add(1)
		}
		return nil
	}, nil)
	app.submitCommand()
	app.sweepCards()
	if found.Load() != 2 || blocked.Load() != 0 {
		t.Errorf("查得到 %d、卡住 %d", found.Load(), blocked.Load())
	}
}

// 第 5 次发到一半崩了:不无限重发。
func TestChatCards_CrashOnLastAttempt(t *testing.T) {
	app, lark, _ := cardApp(t)
	id, no := app.submitCommand()
	app.repo.DB().Model(&model.ChatCard{}).Where("approval_id = ?", id).Update("attempts", 5)
	app.sweepCards()
	if row := app.cardOf(id, owner1); row.State != model.ChatCardFailed || row.LastError == "" {
		t.Errorf("次数用完应 failed 并写明: %+v", row)
	}
	if len(sentTo(lark, no)) != 0 {
		t.Error("次数用完还在发")
	}
}

var outcomeTime = regexp.MustCompile(` · \d\d-\d\d \d\d:\d\d$`)

// 离开待审批的 7 条路都收敛:卡片更新成结论,行收场。
func TestChatCards_ConvergeOnEveryExit(t *testing.T) {
	app, lark, _ := cardApp(t)
	admin := app.login(cardAdmin, "aegisdb123")
	approver := app.login(owner1, "aegisdb123")
	decide := func(verb string) func(int64) {
		return func(id int64) {
			eq(t, app.do(http.MethodPost, "/api/v1/approvals/"+itoa(id)+"/"+verb, approver, nil).Code, 0, verb)
		}
	}
	windowOf := func(id int64) int64 {
		ap, err := app.repo.GetApproval(id)
		if err != nil {
			t.Fatal(err)
		}
		return ap.WindowID
	}
	for _, c := range []struct {
		name, kind string
		leave      func(id int64)
		want       string
		timed      bool
	}{
		{"通过", "command", decide("approve"), "已由 Zhang Wei 通过", true},
		{"驳回", "command", decide("reject"), "已由 Zhang Wei 拒绝", true},
		{"发起人撤回", "command", func(id int64) { eq(t, app.cancel(admin, id).Code, 0, "撤回") }, "已撤回", false},
		{"窗口改动", "window", func(id int64) {
			now := time.Now()
			eq(t, app.do(http.MethodPut, "/api/v1/exec-windows/"+itoa(windowOf(id)), admin,
				windowBody(app.connIDByEnv(admin, "prod"), "orders_db", "改后", now.Add(-time.Minute), now.Add(2*time.Hour))).Code, 0, "改窗口")
		}, "已作废", false},
		{"窗口删除", "window", func(id int64) {
			eq(t, app.do(http.MethodDelete, "/api/v1/exec-windows/"+itoa(windowOf(id)), admin, nil).Code, 0, "删窗口")
		}, "已撤回", false},
		{"窗口单由发起人撤回", "window", func(id int64) { eq(t, app.cancel(admin, id).Code, 0, "撤回") }, "已撤回", false},
		{"超时作废", "command", func(int64) {
			app.setting("approval.onTimeout", `"auto-reject"`)
			app.setting("approval.timeoutMinutes", "0")
			app.svc.SweepApprovalTimeouts()
			app.setting("approval.onTimeout", `"keep-waiting"`)
		}, "已作废", false},
		{"发布单终止", "release", func(id int64) {
			ap, _ := app.repo.GetApproval(id)
			eq(t, app.do(http.MethodPost, "/api/v1/releases/"+itoa(ap.ReleaseID)+"/abort", admin, nil).Code, 0, "终止")
		}, "已作废", false},
		{"启动清理孤儿单", "command", func(id int64) {
			app.repo.DB().Model(&model.Approval{}).Where("id = ?", id).Update("release_id", 987654)
			if _, err := app.repo.VoidOrphanReleaseTickets(); err != nil {
				t.Fatal(err)
			}
		}, "已作废", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			id, no := app.raise(c.kind)
			app.sweepCards()
			var refs []chat.Ref
			for _, s := range lark.sends() {
				if cardNo(s.Card) == no {
					refs = append(refs, s.Ref)
				}
			}
			if len(refs) != 2 {
				t.Fatalf("发出 %d 张", len(refs))
			}
			c.leave(id)
			seen := len(lark.updates())
			app.sweepCards()
			ups := map[chat.Ref]chat.Card{}
			for _, u := range lark.updates()[seen:] {
				ups[u.Ref] = u.Card
			}
			for _, ref := range refs {
				card, ok := ups[ref]
				if !ok {
					t.Errorf("消息 %v 没有更新", ref)
					continue
				}
				got := card.Outcome
				if c.timed {
					if !outcomeTime.MatchString(got) {
						t.Errorf("结论没带时间: %q", got)
					}
					got = outcomeTime.ReplaceAllString(got, "")
				}
				eq(t, got, c.want, "结论")
				if card.Token != "" || card.Confirming {
					t.Error("已结束的卡片不该有按钮")
				}
				if cardNo(card) != no || card.Statement == "" {
					t.Errorf("更新后的卡片内容没了: %+v", card)
				}
			}
			for _, row := range app.cardsOf(id) {
				if row.State != model.ChatCardClosed {
					t.Errorf("应 closed: %+v", row)
				}
			}
			n := len(lark.updates())
			app.dueCards()
			app.sweepCards()
			if len(lark.updates()) != n {
				t.Error("收场之后还在更新")
			}
		})
	}
}

// 重启之后接着发、接着收敛:状态全在库里。
func TestChatCards_SurviveRestart(t *testing.T) {
	app, lark, _ := cardApp(t)
	id, no := app.submitCommand()
	id2, no2 := app.submitCommand()
	app.repo.DB().Model(&model.ChatCard{}).Where("approval_id = ?", id2).UpdateColumn("next_at", time.Now().Add(time.Hour))
	app.sweepCards()
	app.dueCards()
	eq(t, app.do(http.MethodPost, "/api/v1/approvals/"+itoa(id)+"/approve", app.login(owner2, "aegisdb123"), nil).Code, 0, "通过")

	next := service.New(app.repo, app.svc.Engine, app.svc.JWT)
	next.SetChatTransport(chat.Lark, lark)
	next.SweepChatCards(context.Background())
	if len(sentTo(lark, no)) != 2 || len(sentTo(lark, no2)) != 2 || len(lark.updates()) != 2 {
		t.Errorf("重启后:%v %v 更新 %d", sentTo(lark, no), sentTo(lark, no2), len(lark.updates()))
	}
}

// 结论只看单子的状态:通过之后又撤回的单,decided_via 还留着那次通过的渠道。
func TestChatCards_OutcomeFromStatus(t *testing.T) {
	app, lark, _ := cardApp(t)
	id, _ := app.submitCommand()
	app.sweepCards()
	if _, err := app.svc.DecideApprovalVia(app.storedUser(owner1), id, true, "lark", ""); err != nil {
		t.Fatal(err)
	}
	eq(t, app.cancel(app.login(cardAdmin, "aegisdb123"), id).Code, 0, "撤回")
	app.sweepCards()
	ups := lark.updates()
	if len(ups) != 2 || ups[0].Card.Outcome != "已撤回" {
		t.Errorf("更新: %+v", ups)
	}
}

// 状态先改、步骤后记:夹在中间的扫描不该写出没有名字的结论,等一轮;一直等不到才退而求其次。
func TestChatCards_DeciderNotRecordedYet(t *testing.T) {
	app, lark, _ := cardApp(t)
	id, _ := app.submitCommand()
	app.sweepCards()
	if ok, err := app.repo.ClaimApprovalDecision(id, model.StatusApproved, "console", ""); err != nil || !ok {
		t.Fatal(err)
	}
	app.sweepCards()
	if len(lark.updates()) != 0 {
		t.Fatalf("谁通过的还没记下就更新了: %+v", lark.updates()[0].Card.Outcome)
	}
	app.dueCards()
	app.sweepCards()
	if ups := lark.updates(); len(ups) != 2 || ups[0].Card.Outcome != "已通过" {
		t.Errorf("一直等不到时: %+v", ups)
	}

	id, _ = app.submitCommand()
	app.sweepCards()
	_, _ = app.repo.ClaimApprovalDecision(id, model.StatusRejected, "console", "")
	app.sweepCards()
	_ = app.repo.DecideStepBy(id, app.storedUser(owner2).ID, model.StatusRejected, time.Now())
	app.dueCards()
	app.sweepCards()
	if ups := lark.updates(); len(ups) != 4 || !strings.HasPrefix(ups[3].Card.Outcome, "已由 Li Na 拒绝 · ") {
		t.Errorf("等到之后: %+v", ups)
	}
}

func TestChatCards_UpdateGone(t *testing.T) {
	app, lark, _ := cardApp(t)
	id, _ := app.submitCommand()
	app.sweepCards()
	eq(t, app.cancel(app.login(cardAdmin, "aegisdb123"), id).Code, 0, "撤回")
	lark.setErrs(nil, fmt.Errorf("lark 230031: %w", chat.ErrGone))
	app.sweepCards()
	eq(t, len(app.cardsOf(id)), 2, "两张")
	for _, row := range app.cardsOf(id) {
		if row.State != model.ChatCardClosed {
			t.Errorf("ErrGone 应直接收场: %+v", row)
		}
	}
}

func TestChatCards_UpdateFails(t *testing.T) {
	app, lark, _ := cardApp(t)
	id, _ := app.submitCommand()
	app.sweepCards()
	eq(t, app.cancel(app.login(cardAdmin, "aegisdb123"), id).Code, 0, "撤回")
	lark.setErrs(nil, errors.New("boom"))
	for i, wait := range []time.Duration{5 * time.Second, 15 * time.Second, 45 * time.Second, 135 * time.Second} {
		app.dueCards()
		before := time.Now()
		app.sweepCards()
		row := app.cardOf(id, owner1)
		if row.State != model.ChatCardSent || row.Attempts != i+1 || row.LastError != "boom" {
			t.Fatalf("第 %d 次之后: %+v", i+1, row)
		}
		if got := row.NextAt.Sub(before); got < wait-time.Second || got > wait+3*time.Second {
			t.Errorf("第 %d 次失败之后应等 %s,实际 %s", i+1, wait, got)
		}
	}
	app.dueCards()
	app.sweepCards()
	eq(t, app.cardOf(id, owner1).State, model.ChatCardFailed, "第 5 次之后")
}

// 别的机器人发出的消息更新不了:不调 Update,直接收场。
func TestChatCards_OtherBotClosedWithoutUpdate(t *testing.T) {
	app, lark, _ := cardApp(t)
	id, _ := app.submitCommand()
	app.sweepCards()
	eq(t, app.cancel(app.login(cardAdmin, "aegisdb123"), id).Code, 0, "撤回")
	lark.setBot("cli_new", "AegisBot")
	app.sweepCards()
	if len(lark.updates()) != 0 {
		t.Error("拿新机器人去更新旧机器人的消息")
	}
	eq(t, app.cardOf(id, owner1).State, model.ChatCardClosed, "收场")
}

// 超时升级不另发私聊。
func TestChatCards_EscalationAddsNone(t *testing.T) {
	app, lark, _ := cardApp(t)
	id, no := app.submitCommand()
	app.sweepCards()
	app.bindRow(owner3, chat.Lark, chatBot) // 这时才绑上的人:不补发
	app.setting("approval.onTimeout", `"auto-escalate"`)
	app.setting("approval.timeoutMinutes", "0")
	app.svc.SweepApprovalTimeouts()
	if ap, _ := app.repo.GetApproval(id); !ap.Escalated || ap.Status != model.StatusPending {
		t.Fatalf("前提:单子应已升级且仍在等: %+v", ap)
	}
	app.sweepCards()
	if len(app.cardsOf(id)) != 2 || len(sentTo(lark, no)) != 2 || len(lark.updates()) != 0 {
		t.Errorf("升级之后 %d 行、发出 %d、更新 %d", len(app.cardsOf(id)), len(sentTo(lark, no)), len(lark.updates()))
	}
}

// 一个渠道卡住:另一个渠道照发;重叠的扫描不会在卡住的渠道上再开一轮。
func TestChatCards_SlowChannelDoesNotStarveTheOther(t *testing.T) {
	app, lark, tg := cardApp(t)
	app.bindRow(owner1, chat.Telegram, tgBot)
	app.submitCommand()
	release, entered := make(chan struct{}), make(chan struct{}, 10)
	tg.setHooks(func(ctx context.Context, _ string, _ chat.Card) error {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("渠道调用没有超时")
		}
		entered <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
		return nil
	}, nil)
	first := make(chan struct{})
	go func() { app.sweepCards(); close(first) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("扫描没有往 Telegram 发")
	}
	_, no := app.submitCommand()
	second := make(chan struct{})
	go func() { app.sweepCards(); close(second) }()
	select {
	case <-second:
	case <-time.After(5 * time.Second):
		t.Fatal("重叠的扫描应跳过卡住的渠道,立刻返回")
	}
	if len(sentTo(lark, no)) != 2 {
		t.Errorf("另一个渠道没发出去: %v", sentTo(lark, no))
	}
	if len(entered) != 0 {
		t.Error("同一渠道同时有两轮扫描")
	}
	close(release)
	<-first
	app.sweepCards()
	if len(tg.sends()) != 2 {
		t.Errorf("放开之后应发完,实际 %d", len(tg.sends()))
	}
}

// 渠道 panic:算一次失败,错误里不带 panic 的内容(里面可能有令牌),别的卡片照发。
func TestChatCards_TransportPanics(t *testing.T) {
	app, lark, _ := cardApp(t)
	id, no := app.submitCommand()
	boom := func(account string) {
		if account == "lark:"+owner1 {
			panic("parse https://api/botSECRET/send")
		}
	}
	lark.setHooks(func(_ context.Context, account string, _ chat.Card) error { boom(account); return nil }, nil)
	app.sweepCards()
	row := app.cardOf(id, owner1)
	if row.State != model.ChatCardQueued || row.Attempts != 1 || row.LastError == "" || strings.Contains(row.LastError, "SECRET") {
		t.Errorf("panic 之后: %+v", row)
	}
	if len(sentTo(lark, no)) != 1 {
		t.Error("另一个人的卡片应照发")
	}
	lark.setHooks(nil, func(context.Context, chat.Ref, chat.Card) error { panic("x") })
	eq(t, app.cancel(app.login(cardAdmin, "aegisdb123"), id).Code, 0, "撤回")
	app.dueCards()
	app.sweepCards()
	if row := app.cardOf(id, owner2); row.State != model.ChatCardSent || row.Attempts != 1 {
		t.Errorf("更新时 panic: %+v", row)
	}
}

// ctx 取消之后不再发新的。
func TestChatCards_SweepStopsWithContext(t *testing.T) {
	app, lark, _ := cardApp(t)
	id, _ := app.submitCommand()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	app.svc.SweepChatCards(ctx)
	if len(lark.sends()) != 0 || app.cardOf(id, owner1).Attempts != 0 {
		t.Error("已取消的扫描不该动任何卡片")
	}
}

// 带不带 SQL、是不是全文,发出那一刻就定了:之后改设置,已发出的卡片更新时既不多出 SQL,也不改口。
func TestChatCards_IncludeSQLFixedAtSend(t *testing.T) {
	for _, c := range []struct {
		name          string
		before, after string
		sql, partial  bool
	}{
		{"先关后开", "false", "true", false, true},
		{"先开后关", "true", "false", true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			app, _, tg := cardApp(t)
			app.bindRow(owner1, chat.Telegram, tgBot)
			app.setting("chat.telegram.includeSql", c.before)
			id, _ := app.submitCommand()
			app.sweepCards()
			sent := tg.sends()
			if len(sent) != 1 || (sent[0].Card.Statement != "") != c.sql || (sent[0].Card.Reason != "") != c.sql || sent[0].Card.Partial != c.partial {
				t.Fatalf("发出的: %+v", sent)
			}
			for _, row := range app.cardsOf(id) {
				if row.Channel == "telegram" && row.Partial != c.partial {
					t.Errorf("行上的 Partial: %+v", row)
				}
			}
			app.setting("chat.telegram.includeSql", c.after)
			eq(t, app.cancel(app.login(cardAdmin, "aegisdb123"), id).Code, 0, "撤回")
			app.sweepCards()
			ups := tg.updates()
			if len(ups) != 1 {
				t.Fatalf("更新 %d 次", len(ups))
			}
			// 除了令牌与结论,逐字段一样
			want, got := sent[0].Card, ups[0].Card
			want.Token, got.Outcome = "", ""
			w, _ := json.Marshal(want)
			g, _ := json.Marshal(got)
			eq(t, string(g), string(w), "更新后的卡片")
			raw, _ := json.Marshal(ups[0].Card)
			if !c.sql && (strings.Contains(string(raw), "DROP") || strings.Contains(string(raw), "legacy")) {
				t.Errorf("后来打开设置,把语句补发出去了: %s", raw)
			}
		})
	}
}

// 排队之后、发出之前改设置:按发出那一刻的算。
func TestChatCards_IncludeSQLReadAtSend(t *testing.T) {
	app, _, tg := cardApp(t)
	app.bindRow(owner1, chat.Telegram, tgBot)
	app.setting("chat.telegram.includeSql", "true")
	app.submitCommand()
	app.setting("chat.telegram.includeSql", "false")
	app.sweepCards()
	if s := tg.sends(); len(s) != 1 || s[0].Card.Statement != "" || !s[0].Card.Partial {
		t.Errorf("发出的: %+v", s)
	}
}

// 发布单存的就是截断内容:建单时记在行上,发出的卡片写明未显示全文。
func TestChatCards_SourceTruncated(t *testing.T) {
	app, lark, _ := cardApp(t)
	token := app.login(cardAdmin, "aegisdb123")
	pid := app.createPipeline(token, "长正文流程", "", []map[string]any{
		{"name": "人工审批", "type": "approve"}, {"name": "执行变更", "type": "execute"},
	})
	for name, c := range map[string]struct {
		sql, reason string
		want        bool
	}{
		"超过 4000 字": {"SELECT 1 /* " + strings.Repeat("a", 4100) + " */;", "r", true},
		"短正文":       {"SELECT 1;", "r", false},
		"原因被截断":     {"SELECT 1;", strings.Repeat("原", 401), true}, // 提交时只留 400 字
		"原因恰好放得下":   {"SELECT 1;", strings.Repeat("原", 400), false},
	} {
		t.Run(name, func(t *testing.T) {
			r := app.submitRelease(token, map[string]any{"title": "长正文", "pipelineId": pid,
				"connectionId": app.connIDByEnv(token, "staging"), "sql": c.sql, "reason": c.reason})
			eq(t, r.Code, 0, r.Msg)
			var rel releaseView
			_ = json.Unmarshal(r.Data, &rel)
			app.waitRelease(token, rel.ID, "waiting", "failed", "success")
			var ap model.Approval
			if err := app.repo.DB().First(&ap, "release_id = ?", rel.ID).Error; err != nil {
				t.Fatal(err)
			}
			eq(t, app.cardOf(ap.ID, owner1).SourceTruncated, c.want, "建单时记下")
			app.sweepCards()
			eq(t, app.cardOf(ap.ID, owner1).Partial, c.want, "发出时定下")
			for _, s := range lark.sends() {
				if cardNo(s.Card) == ap.ApNo {
					eq(t, s.Card.Partial, c.want, "卡片上")
				}
			}
		})
	}
}

func TestChatCards_Language(t *testing.T) {
	app, lark, _ := cardApp(t)
	app.setting("chat.cardLang", `"en"`)
	id, no := app.submitCommand()
	app.sweepCards()
	if s := lark.sends(); len(s) != 2 || s[0].Card.Title != "High-risk command awaiting approval · "+no || s[0].Card.Statement != "DROP TABLE orders;" {
		t.Errorf("英文卡片: %+v", s)
	}
	eq(t, app.do(http.MethodPost, "/api/v1/approvals/"+itoa(id)+"/reject", app.login(owner1, "aegisdb123"), nil).Code, 0, "驳回")
	app.sweepCards()
	if ups := lark.updates(); len(ups) != 2 || !strings.HasPrefix(ups[0].Card.Outcome, "Rejected by Zhang Wei · ") {
		t.Errorf("英文结论: %+v", ups)
	}
}

// 卡片表写不进去,建单照常。
func TestChatCards_QueueFailureKeepsTheTicket(t *testing.T) {
	app, _, _ := cardApp(t)
	if err := app.repo.DB().Migrator().DropTable(&model.ChatCard{}); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"command", "release", "window", "export"} {
		id, _ := app.raise(kind)
		if ap, err := app.repo.GetApproval(id); err != nil || ap.Status != model.StatusPending {
			t.Errorf("%s: 建单应照常成功: %v", kind, err)
		}
	}
}

// 一轮处理不完的,下一轮接着来。
func TestChatCards_BatchesContinue(t *testing.T) {
	app, lark, _ := cardApp(t)
	u := app.storedUser(owner1).ID
	for i := 0; i < 120; i++ {
		ap := model.Approval{ApNo: fmt.Sprintf("AP-9%03d", i), Status: model.StatusPending, Env: "prod", Instance: "i", Command: "c", RiskLevel: "mid"}
		mustCreate(t, app.repo.DB(), &ap)
		mustCreate(t, app.repo.DB(), &model.ChatCard{ApprovalID: ap.ID, UserID: u, Channel: "lark", BotID: chatBot,
			State: model.ChatCardQueued, NextAt: time.Now().Add(-time.Minute)})
	}
	for i := 0; i < 4; i++ {
		app.sweepCards()
	}
	eq(t, len(lark.sends()), 120, "全部发出")
}

// `server init` 找回管理员也是口令重置:聊天绑定与没用掉的码一并删掉(PRD §5.1)。
func TestInit_RecoveryDropsChatBindings(t *testing.T) {
	app, _, _ := cardApp(t)
	u, other := app.storedUser(cardAdmin), app.storedUser(owner1)
	for i, id := range []int64{u.ID, other.ID} {
		if err := app.repo.SaveChatBindCode(id, "lark", strings.Repeat(fmt.Sprint(i+1), 64), time.Now().Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := UpsertAdmin(app.repo, cardAdmin, "N3w!Secret9xyz", ""); err != nil {
		t.Fatal(err)
	}
	if n := len(app.chatRows(u.ID)); n != 0 {
		t.Errorf("找回账号之后还有 %d 个聊天绑定", n)
	}
	if n := len(app.chatCodes(u.ID)); n != 0 {
		t.Errorf("找回账号之后还有 %d 个绑定码", n)
	}
	if len(app.chatRows(other.ID)) != 1 || len(app.chatCodes(other.ID)) != 1 {
		t.Error("不该动别人的绑定")
	}
	if fresh := app.storedUser(cardAdmin); fresh.TokenVersion != u.TokenVersion+1 {
		t.Error("会话应作废")
	}
	// 删不掉就要报错:操作的人以为找回来了,旧绑定却还能批单
	if err := app.repo.DB().Migrator().DropTable(&model.ChatBinding{}); err != nil {
		t.Fatal(err)
	}
	if _, err := UpsertAdmin(app.repo, cardAdmin, "N3w!Secret9xyz", ""); err == nil {
		t.Error("解绑失败应报错")
	}
}

// 行上定下的 Partial 说了算:发出之后口径变了(例如改了上限),更新时也不改口。
func TestChatCards_PartialComesFromRow(t *testing.T) {
	app, lark, _ := cardApp(t)
	id, _ := app.submitCommand()
	app.sweepCards()
	app.repo.DB().Model(&model.ChatCard{}).Where("approval_id = ?", id).Update("partial", true)
	eq(t, app.cancel(app.login(cardAdmin, "aegisdb123"), id).Code, 0, "撤回")
	app.sweepCards()
	if ups := lark.updates(); len(ups) != 2 || !ups[0].Card.Partial {
		t.Errorf("更新: %+v", ups)
	}
}

// 渠道调用途中,卡片被别处(按钮点击)收场了:扫描不能把它改回去。
func TestChatCards_ClosedMeanwhile(t *testing.T) {
	app, lark, _ := cardApp(t)
	id, _ := app.submitCommand()
	closeAll := func() {
		app.repo.DB().Model(&model.ChatCard{}).Where("approval_id = ?", id).Update("state", model.ChatCardClosed)
	}
	lark.setHooks(func(context.Context, string, chat.Card) error { closeAll(); return nil }, nil)
	app.sweepCards()
	for _, row := range app.cardsOf(id) {
		eq(t, row.State, model.ChatCardClosed, "发送途中收场的")
	}
	if len(lark.sends()) != 1 {
		t.Errorf("第二张在发之前就已收场,不该再发: %d", len(lark.sends()))
	}

	lark.setHooks(nil, func(context.Context, chat.Ref, chat.Card) error { closeAll(); return errors.New("boom") })
	id, _ = app.submitCommand()
	app.sweepCards()
	eq(t, app.cancel(app.login(cardAdmin, "aegisdb123"), id).Code, 0, "撤回")
	app.sweepCards()
	for _, row := range app.cardsOf(id) {
		if row.State != model.ChatCardClosed || row.LastError != "" {
			t.Errorf("更新途中收场的: %+v", row)
		}
	}
}

// 退避从失败那一刻算起:一次拖了很久才失败的调用,不能让下一次紧接着就来。
func TestChatCards_BackoffCountsFromFailure(t *testing.T) {
	app, lark, _ := cardApp(t)
	slow := func() error { time.Sleep(1500 * time.Millisecond); return errors.New("slow") }
	id, _ := app.submitCommand()
	app.repo.DB().Where("approval_id = ? AND user_id <> ?", id, app.storedUser(owner1).ID).Delete(&model.ChatCard{})
	check := func(what string) {
		t.Helper()
		app.sweepCards()
		if left := time.Until(app.cardOf(id, owner1).NextAt); left < 4500*time.Millisecond || left > 5*time.Second {
			t.Errorf("%s:失败后还应等 5 秒,实际 %s", what, left)
		}
	}
	lark.setHooks(func(context.Context, string, chat.Card) error { return slow() }, nil)
	check("发送")
	lark.setHooks(nil, func(context.Context, chat.Ref, chat.Card) error { return slow() })
	app.dueCards()
	app.sweepCards()
	eq(t, app.cancel(app.login(cardAdmin, "aegisdb123"), id).Code, 0, "撤回")
	check("更新")
}
