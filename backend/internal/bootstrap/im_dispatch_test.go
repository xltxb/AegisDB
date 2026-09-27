package bootstrap

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestIM_CardsGoOnlyToBoundChainMembers(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	f := app.fakeIM("fake")
	app.enableIM(admin, "fake")
	app.bindIM("zhangwei@vela.io", "fake", "p-zw")
	app.bindIM("lina@vela.io", "fake", "p-ln")
	app.bindIM("chenhao@vela.io", "fake", "p-ch") // 绑了,但不在链上

	ap := app.submitProdHighRisk(admin)
	app.flushIM()

	eq(t, fmt.Sprint(sentPIDs(f.Sent())), "[p-ln p-zw]", "only bound chain members get a card")
	for _, s := range f.Sent() {
		eq(t, s.Card.ApNo, ap.ApNo, "card carries the ticket number")
		eq(t, s.Card.Kind, "sql", "an ordinary ticket is a sql card")
	}
}

func TestIM_InitiatorOnTheChainGetsNoCard(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	owner := app.login("zhangwei@vela.io", "vela123")
	f := app.fakeIM("fake")
	app.enableIM(admin, "fake")
	app.bindIM("zhangwei@vela.io", "fake", "p-zw")
	app.bindIM("lina@vela.io", "fake", "p-ln")

	app.submitProdHighRisk(owner) // zhangwei 发起,他自己在链上
	app.flushIM()
	eq(t, fmt.Sprint(sentPIDs(f.Sent())), "[p-ln]", "the initiator is never sent their own card")
}

func TestIM_NoBoundMembersMeansNoCardsAndTicketStillPending(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	f := app.fakeIM("fake")
	app.enableIM(admin, "fake")

	ap := app.submitProdHighRisk(admin)
	app.flushIM()
	eq(t, len(f.Sent()), 0, "nobody bound, nobody messaged")
	eq(t, app.approvalRow(admin, ap.ApNo).Status, "pending", "ticket is untouched")
	owner := app.login("zhangwei@vela.io", "vela123")
	eq(t, app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/approve", owner, nil).Code, 0,
		"the console path still works")
}

func TestIM_DisabledChannelSendsNothing(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	f := app.fakeIM("fake") // 没有 enableIM
	app.bindIM("zhangwei@vela.io", "fake", "p-zw")

	app.submitProdHighRisk(admin)
	app.flushIM()
	eq(t, len(f.Sent()), 0, "approval switch off → no cards")
}

func TestIM_CardCommandIsRedacted(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	f := app.fakeIM("fake")
	app.enableIM(admin, "fake")
	app.bindIM("zhangwei@vela.io", "fake", "p-zw")

	prod := app.connIDByEnv(admin, "prod")
	r := app.do(http.MethodPost, "/api/v1/terminal/exec", admin, map[string]any{
		"connectionId": prod,
		"sql":          `CREATE USER 'db_opt'@'%' IDENTIFIED WITH 'mysql_native_password' BY '` + pwQuoted + `'`,
		"reason":       "生产加只读账号",
	})
	eq(t, r.Code, 42200, "intercepted for approval")
	app.flushIM()
	if len(f.Sent()) != 1 {
		t.Fatalf("sent %d cards, want 1", len(f.Sent()))
	}
	mustNotContainSecrets(t, "IM card command", []byte(f.Sent()[0].Card.Command))
}

func TestIM_DecisionClosesCardsOnEveryChannel(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	fa, fb := app.fakeIM("fa"), app.fakeIM("fb")
	app.enableIM(admin, "fa")
	app.enableIM(admin, "fb")
	app.bindIM("lina@vela.io", "fa", "a-ln")
	app.bindIM("lina@vela.io", "fb", "b-ln") // 同一个人两个渠道都绑了
	app.bindIM("zhangwei@vela.io", "fa", "a-zw")

	ap := app.submitProdHighRisk(admin)
	app.flushIM()
	eq(t, len(fa.Sent()), 2, "fa: two cards")
	eq(t, len(fb.Sent()), 1, "fb: one card")

	owner := app.login("zhangwei@vela.io", "vela123")
	eq(t, app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/approve", owner, nil).Code, 0, "approve in console")
	app.flushIM()

	eq(t, len(fa.Closed()), 2, "every fa card closed")
	eq(t, len(fb.Closed()), 1, "the fb card closed too")
	for _, c := range append(fa.Closed(), fb.Closed()...) {
		eq(t, c.State.Status, "approved", "closed as approved")
		if !strings.Contains(c.State.Text, "通过") {
			t.Errorf("closed text %q should say who approved", c.State.Text)
		}
	}
}

func TestIM_TimeoutClosesCards(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	f := app.fakeIM("fake")
	app.enableIM(admin, "fake")
	app.bindIM("zhangwei@vela.io", "fake", "p-zw")
	app.setSettings(admin, map[string]any{"approval.onTimeout": "auto-reject", "approval.timeoutMinutes": 0})

	app.submitProdHighRisk(admin)
	app.flushIM()
	app.svc.SweepApprovalTimeouts()
	app.flushIM()
	if c := f.Closed(); len(c) != 1 || c[0].State.Status != "expired" {
		t.Fatalf("closed = %+v, want one expired", c)
	}
}

func TestIM_CancelClosesCards(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	f := app.fakeIM("fake")
	app.enableIM(admin, "fake")
	app.bindIM("zhangwei@vela.io", "fake", "p-zw")

	ap := app.submitProdHighRisk(admin)
	app.flushIM()
	eq(t, app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/cancel", admin, nil).Code, 0, "cancel")
	app.flushIM()
	if c := f.Closed(); len(c) != 1 || c[0].State.Status != "cancelled" {
		t.Fatalf("closed = %+v, want one cancelled", c)
	}
}

// 改 / 删执行窗口会把它那张还在等的单作废。单作废了,私聊里那张卡也得收起 ——
// 否则审批人手里留着两个点下去只会得到「已处理」的按钮。
func TestIM_RetiredWindowTicketClosesItsCards(t *testing.T) {
	for _, tc := range []struct {
		name   string
		retire func(app *testApp, token string, winID, prod int64)
	}{
		{"edit", func(app *testApp, token string, winID, prod int64) {
			app.editWindowHours(token, winID, prod, "orders_db", "收卡", 6)
		}},
		{"delete", func(app *testApp, token string, winID, _ int64) {
			eq(t, app.do(http.MethodDelete, "/api/v1/exec-windows/"+itoa(winID), token, nil).Code, 0, "删窗口")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := newTestApp(t)
			admin := app.login("linwei@vela.io", "vela123")
			f := app.fakeIM("fake")
			app.enableIM(admin, "fake")
			app.bindIM("zhangwei@vela.io", "fake", "p-zw")
			app.bindIM("lina@vela.io", "fake", "p-ln")
			prod := app.connIDByEnv(admin, "prod")

			winID, apNo := app.applyWindow(admin, prod, "orders_db", "收卡")
			app.flushIM()
			oldRefs := map[string]bool{}
			for _, s := range f.Sent() {
				if s.Card.ApNo == apNo {
					eq(t, s.Card.Kind, "window", "a window ticket is a window card")
					oldRefs[s.Ref] = true
				}
			}
			if len(oldRefs) == 0 {
				t.Fatalf("前置条件:窗口单应当给链成员发卡,sent = %+v", f.Sent())
			}

			tc.retire(app, admin, winID, prod)
			app.flushIM()

			closed := 0
			for _, c := range f.Closed() {
				if oldRefs[c.Ref] {
					eq(t, c.State.Status, "cancelled", "the retired ticket's card closes as cancelled")
					closed++
				}
			}
			eq(t, closed, len(oldRefs), "every card of the retired ticket is closed")
		})
	}
}

func TestIM_CardsAreClosedAfterTheChannelIsSwitchedOff(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	f := app.fakeIM("fake")
	app.enableIM(admin, "fake")
	app.bindIM("zhangwei@vela.io", "fake", "p-zw")

	ap := app.submitProdHighRisk(admin)
	app.flushIM()
	app.disableIM(admin, "fake")
	owner := app.login("zhangwei@vela.io", "vela123")
	eq(t, app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/reject", owner, nil).Code, 0, "reject")
	app.flushIM()
	if c := f.Closed(); len(c) != 1 || c[0].State.Status != "rejected" {
		t.Fatalf("closed = %+v, want the old card closed as rejected", c)
	}
}
