package bootstrap

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"velagateway/internal/model"
)

var bindCodeShape = regexp.MustCompile(`^BIND-[A-HJ-NP-Z2-9]{8}$`)

type imIdentityRow struct {
	Channel        string `json:"channel"`
	Enabled        bool   `json:"enabled"`
	Bound          bool   `json:"bound"`
	PlatformUserID string `json:"platformUserId"`
	BoundBy        string `json:"boundBy"`
	BotName        string `json:"botName"`
}

func (a *testApp) issueBindCode(token, channel string) (string, apiResp) {
	a.t.Helper()
	r := a.do(http.MethodPost, "/api/v1/me/im-identities/"+channel+"/code", token, nil)
	var out struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(r.Data, &out)
	return out.Code, r
}

func (a *testApp) myIdentity(token, channel string) imIdentityRow {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/me/im-identities", token, nil)
	eq(a.t, r.Code, 0, "list my identities")
	var rows []imIdentityRow
	_ = json.Unmarshal(r.Data, &rows)
	for _, row := range rows {
		if row.Channel == channel {
			return row
		}
	}
	a.t.Fatalf("channel %s not listed: %s", channel, r.Data)
	return imIdentityRow{}
}

func TestIMBind_SelfServiceHappyPath(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	me := app.login("zhangwei@vela.io", "vela123")
	f := app.runningFake(admin)

	row := app.myIdentity(me, "fake")
	if !row.Enabled || row.Bound || row.BotName != "fake-bot" {
		t.Fatalf("before binding: %+v", row)
	}
	code, r := app.issueBindCode(me, "fake")
	eq(t, r.Code, 0, "issue code")
	if !bindCodeShape.MatchString(code) {
		t.Fatalf("code %q has the wrong shape", code)
	}

	reply := f.DirectMessage("p-zw", code)
	if !strings.Contains(reply, "zhangwei@vela.io") {
		t.Fatalf("reply = %q, want it to name the bound account", reply)
	}
	row = app.myIdentity(me, "fake")
	if !row.Bound || row.PlatformUserID != "p-zw" || row.BoundBy != model.IMBoundSelf {
		t.Fatalf("after binding: %+v", row)
	}
	var bindRows []model.AuditLog
	app.repo.DB().Where("command LIKE ?", "im.bind channel=fake%").Find(&bindRows)
	if len(bindRows) != 1 {
		t.Fatalf("self-binding is audited exactly once, got %d", len(bindRows))
	}
	if !strings.Contains(bindRows[0].Command, "platformUserId=p-zw") {
		t.Fatalf("bind audit = %q, want it to name the bound platform account", bindRows[0].Command)
	}

	// 绑上以后这个账号的点击就能被认出来
	ap := app.submitProdHighRisk(admin)
	eq(t, f.Click("p-zw", ap.ApNo, true).Toast, "已批准,请发起人回控制台执行", "the bound account can now approve")
}

func TestIMBind_CodeIsSingleUseAndRegenerationInvalidatesTheOldOne(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	me := app.login("zhangwei@vela.io", "vela123")
	f := app.runningFake(admin)

	old, _ := app.issueBindCode(me, "fake")
	fresh, _ := app.issueBindCode(me, "fake")
	eq(t, f.DirectMessage("p-1", old), "无效或已过期的绑定码,请在控制台重新生成", "the replaced code is dead")
	if !strings.Contains(f.DirectMessage("p-1", fresh), "已绑定") {
		t.Fatal("the fresh code binds")
	}
	eq(t, f.DirectMessage("p-2", fresh), "无效或已过期的绑定码,请在控制台重新生成", "a used code is dead")
	eq(t, app.myIdentity(me, "fake").PlatformUserID, "p-1", "the second use did not rebind")
}

func TestIMBind_RebindAuditsThePreviousPlatformID(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	me := app.login("zhangwei@vela.io", "vela123")
	f := app.runningFake(admin)

	code1, _ := app.issueBindCode(me, "fake")
	f.DirectMessage("p-1", code1)
	code2, _ := app.issueBindCode(me, "fake")
	f.DirectMessage("p-2", code2)
	eq(t, app.myIdentity(me, "fake").PlatformUserID, "p-2", "the rebind takes effect")

	var rows []model.AuditLog
	app.repo.DB().Where("command LIKE ?", "im.bind channel=fake%").Order("id").Find(&rows)
	if len(rows) != 2 {
		t.Fatalf("want one audit row per bind, got %d: %+v", len(rows), rows)
	}
	if !strings.Contains(rows[0].Command, "platformUserId=p-1") {
		t.Fatalf("first bind audit = %q", rows[0].Command)
	}
	if !strings.Contains(rows[1].Command, "platformUserId=p-2") || !strings.Contains(rows[1].Command, "previous=p-1") {
		t.Fatalf("rebind audit = %q, want it to name both the new and the previous platform account", rows[1].Command)
	}
}

func TestIMBind_ExpiredCode(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	me := app.login("zhangwei@vela.io", "vela123")
	f := app.runningFake(admin)

	code, _ := app.issueBindCode(me, "fake")
	app.repo.DB().Model(&model.IMBindCode{}).Where("channel = ?", "fake").
		Update("expires_at", time.Now().Add(-time.Minute))
	eq(t, f.DirectMessage("p-1", code), "无效或已过期的绑定码,请在控制台重新生成", "expired")
	if app.myIdentity(me, "fake").Bound {
		t.Fatal("an expired code must not bind")
	}
}

func TestIMBind_CodeIsNormalisedButNotFishedOutOfText(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	me := app.login("zhangwei@vela.io", "vela123")
	f := app.runningFake(admin)

	code, _ := app.issueBindCode(me, "fake")
	eq(t, f.DirectMessage("p-1", "我的码是 "+code), "无效或已过期的绑定码,请在控制台重新生成",
		"a code buried in a sentence is not accepted")
	eq(t, f.DirectMessage("p-1", "hello"), "无效或已过期的绑定码,请在控制台重新生成",
		"random text gets the same reply as a dead code — nothing leaks")
	if !strings.Contains(f.DirectMessage("p-1", "  "+strings.ToLower(code)+"\n"), "已绑定") {
		t.Fatal("case and surrounding whitespace are forgiven")
	}
}

func TestIMBind_PlatformAccountAlreadyTakenIsRefused(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	me := app.login("zhangwei@vela.io", "vela123")
	f := app.runningFake(admin)
	app.bindIM("lina@vela.io", "fake", "p-shared")

	code, _ := app.issueBindCode(me, "fake")
	if reply := f.DirectMessage("p-shared", code); !strings.Contains(reply, "已绑定到其他网关账户") {
		t.Fatalf("reply = %q", reply)
	}
	if app.myIdentity(me, "fake").Bound {
		t.Fatal("must not steal someone else's platform account")
	}
}

func TestIMBind_CodeRefusedWhileChannelIsOff(t *testing.T) {
	app := newTestApp(t)
	me := app.login("zhangwei@vela.io", "vela123")
	app.fakeIM("fake") // 登记了但没打开

	_, r := app.issueBindCode(me, "fake")
	if r.Code == 0 {
		t.Fatal("issuing a code for a channel nobody is listening on must fail")
	}
	_, r = app.issueBindCode(me, "nope")
	eq(t, r.Code, 40400, "unknown channel")
}

func TestIMBind_SelfUnbind(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	me := app.login("zhangwei@vela.io", "vela123")
	app.runningFake(admin)
	app.bindIM("zhangwei@vela.io", "fake", "p-zw")

	eq(t, app.do(http.MethodDelete, "/api/v1/me/im-identities/fake", me, nil).Code, 0, "unbind")
	if app.myIdentity(me, "fake").Bound {
		t.Fatal("still bound after unbind")
	}
	var rows []model.AuditLog
	app.repo.DB().Where("command LIKE ?", "im.unbind channel=fake%").Find(&rows)
	if len(rows) != 1 || !strings.Contains(rows[0].Command, "platformUserId=p-zw") {
		t.Fatalf("unbind audit = %+v, want it to name the removed platform account", rows)
	}
}

func TestIMBind_AdminBindAndUnbind(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	nonAdmin := app.login("zhangwei@vela.io", "vela123")
	app.fakeIM("fake") // 代绑不经过机器人,渠道关着也能做
	target := app.userIDByEmail(admin, "lina@vela.io")
	path := "/api/v1/users/" + itoa(target) + "/im-identities/fake"

	if app.do(http.MethodPut, path, nonAdmin, map[string]any{"platformUserId": "p-x"}).Code == 0 {
		t.Fatal("a non-admin must not bind someone else")
	}
	eq(t, app.do(http.MethodPut, path, admin, map[string]any{"platformUserId": "p-ln"}).Code, 0, "admin binds")

	r := app.do(http.MethodGet, "/api/v1/users/"+itoa(target)+"/im-identities", admin, nil)
	eq(t, r.Code, 0, "admin reads")
	if !strings.Contains(string(r.Data), `"platformUserId":"p-ln"`) || !strings.Contains(string(r.Data), `"boundBy":"admin"`) {
		t.Fatalf("identities = %s", r.Data)
	}
	if app.do(http.MethodPut, path, admin, map[string]any{"platformUserId": "   "}).Code == 0 {
		t.Fatal("a blank platform id is not a binding")
	}
	eq(t, app.do(http.MethodDelete, path, admin, nil).Code, 0, "admin unbinds")
	var rows []model.AuditLog
	app.repo.DB().Where("command LIKE ?", "admin.im.%").Order("id").Find(&rows)
	if len(rows) != 2 {
		t.Fatalf("bind and unbind are both audited, got %d: %+v", len(rows), rows)
	}
	if !strings.Contains(rows[0].Command, "admin.im.bind") || !strings.Contains(rows[0].Command, "platformUserId=p-ln") {
		t.Fatalf("bind audit = %q", rows[0].Command)
	}
	if !strings.Contains(rows[1].Command, "admin.im.unbind") || !strings.Contains(rows[1].Command, "platformUserId=p-ln") {
		t.Fatalf("unbind audit = %q, want it to name the removed platform account", rows[1].Command)
	}
}
