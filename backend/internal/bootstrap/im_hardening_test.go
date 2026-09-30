package bootstrap

import (
	"net/http"
	"testing"

	"velagateway/internal/model"
)

func TestIMBind_AdminCannotBindADisabledOrServiceAccount(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	app.fakeIM("fake")
	zw := app.userIDByEmail(admin, "zhangwei@vela.io")
	ln := app.userIDByEmail(admin, "lina@vela.io")
	if err := app.repo.UpdateUserFields(zw, map[string]any{"status": "disabled"}); err != nil {
		t.Fatal(err)
	}
	if err := app.repo.UpdateUserFields(ln, map[string]any{"kind": model.UserKindService}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{zw, ln} {
		r := app.do(http.MethodPut, "/api/v1/users/"+itoa(id)+"/im-identities/fake", admin, map[string]any{"platformUserId": "p-" + itoa(id)})
		if r.Code == 0 {
			t.Fatalf("user %d: binding an account that can never approve must be refused", id)
		}
	}
	var n int64
	app.repo.DB().Model(&model.IMIdentity{}).Count(&n)
	eq(t, n, int64(0), "nothing was bound")
}

func TestIMBind_AdminUnbindWithNothingBoundWritesNoAudit(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	app.fakeIM("fake")
	ln := app.userIDByEmail(admin, "lina@vela.io")

	eq(t, app.do(http.MethodDelete, "/api/v1/users/"+itoa(ln)+"/im-identities/fake", admin, nil).Code, 0, "unbind is idempotent")
	var n int64
	app.repo.DB().Model(&model.AuditLog{}).Where("command LIKE ?", "admin.im.unbind%").Count(&n)
	eq(t, n, int64(0), "removing nothing is not an event")
}

func TestIM_CardLinkPointsAtTheApprovalsPage(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	f := app.fakeIM("fake")
	app.enableIM(admin, "fake")
	app.bindIM("zhangwei@vela.io", "fake", "p-zw")
	app.setSettings(admin, map[string]any{"notify.consoleURL": "https://gw.corp.io/"})

	app.submitProdHighRisk(admin)
	app.flushIM()
	if s := f.Sent(); len(s) != 1 || s[0].Card.ConsoleURL != "https://gw.corp.io/approvals" {
		t.Fatalf("sent = %+v — the router uses paths, a /#/ link lands on the dashboard", s)
	}
}
