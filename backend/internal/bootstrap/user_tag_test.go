package bootstrap

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"velagateway/pkg/resp"
)

func (a *testApp) userTags(token string, userID int64) []string {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/users/"+itoa(userID)+"/tags", token, nil)
	eq(a.t, r.Code, 0, "get user tags")
	var out []string
	_ = json.Unmarshal(r.Data, &out)
	return out
}

func (a *testApp) setUserTags(token string, userID int64, tags []string) apiResp {
	a.t.Helper()
	return a.do(http.MethodPut, "/api/v1/users/"+itoa(userID)+"/tags", token, map[string]any{"tags": tags})
}

// Tags are grants on ROLES: a role with none is unrestricted, and holding any
// unrestricted role makes the user unrestricted. That works for groups but
// cannot express "this particular person". Granting at the user level has to be
// the MORE SPECIFIC statement — otherwise, unioned with the role grants, it
// would do nothing at all for exactly the people you most want to scope: anyone
// whose role is unrestricted (an administrator) would stay unrestricted no
// matter which tags you gave them.
//
// So: a user with explicit tags is scoped to those tags. A user with none keeps
// the role-derived scope, which is the existing behaviour untouched.
func TestUserTags_NarrowAUserBelowTheirRole(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	me := app.userByEmail(admin, "linwei@vela.io")

	// The platform administrator's role carries no tags, so they see everything.
	before := app.listConnNames(admin)
	if len(before) < 3 {
		t.Fatalf("precondition: an unrestricted admin should see every instance, saw %v", before)
	}

	// Scope that same person to one tag.
	eq(t, app.setUserTags(admin, me.ID, []string{"analytics"}).Code, 0, "assign user tags")
	eq(t, len(app.userTags(admin, me.ID)), 1, "tags read back")

	after := app.listConnNames(admin)
	for _, n := range after {
		if n != "analytics-ro" {
			t.Errorf("after scoping to the analytics tag the user still sees %q", n)
		}
	}
	if len(after) == 0 {
		t.Error("scoping removed every instance; the analytics-tagged one should remain")
	}

	// Clearing the user-level grant restores the role-derived scope.
	eq(t, app.setUserTags(admin, me.ID, []string{}).Code, 0, "clear user tags")
	if got := len(app.listConnNames(admin)); got != len(before) {
		t.Errorf("after clearing user tags the user sees %d instances, want %d", got, len(before))
	}
}

// The grant must gate execution too, not merely what the console lists: a
// filtered instance list that still executes would be decoration.
func TestUserTags_GateExecutionNotJustTheListing(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	me := app.userByEmail(admin, "linwei@vela.io")
	orders := app.connIDByName(admin, "order-cluster")

	eq(t, app.setUserTags(admin, me.ID, []string{"analytics"}).Code, 0, "scope to analytics")

	r := app.do(http.MethodPost, "/api/v1/terminal/exec", admin, map[string]any{
		"connectionId": orders, "sql": "SELECT 1",
	})
	eq(t, r.Code, resp.CodeForbidden, "executing against an out-of-scope instance")
}

// A user with no explicit tags keeps exactly the behaviour they had before this
// feature existed.
func TestUserTags_AbsentGrantLeavesRoleScopeUntouched(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	target := app.userByEmail(admin, "zhaolei@vela.io") // role `ro`: analytics + readonly

	if got := app.userTags(admin, target.ID); len(got) != 0 {
		t.Fatalf("precondition: no user-level tags expected, got %v", got)
	}
	// Their role grants analytics/readonly, so the analytics instance is visible
	// and the orders one is not — unchanged by this feature.
	ro := app.login("zhaolei@vela.io", "vela123")
	names := app.listConnNames(ro)
	saw := map[string]bool{}
	for _, n := range names {
		saw[n] = true
	}
	if !saw["analytics-ro"] {
		t.Errorf("role-granted instance missing: %v", names)
	}
	if saw["order-cluster"] {
		t.Errorf("role scope leaked an untagged instance: %v", names)
	}
}

// Assigning tags is a privilege change, so it is admin-only and audited.
func TestUserTags_AdminOnlyAndAudited(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	ro := app.login("zhaolei@vela.io", "vela123")
	target := app.userByEmail(admin, "chenhao@vela.io")

	if app.setUserTags(ro, target.ID, []string{"orders"}).Code == 0 {
		t.Error("a non-admin was able to change another user's data-access scope")
	}
	eq(t, app.setUserTags(admin, target.ID, []string{"orders"}).Code, 0, "admin assigns tags")

	var rows []struct {
		Command string `json:"command"`
	}
	_ = json.Unmarshal(app.auditItemsRaw(admin, ""), &rows)
	for _, r := range rows {
		if strings.Contains(r.Command, "admin.user.tags") && strings.Contains(r.Command, "chenhao@vela.io") {
			return
		}
	}
	t.Error("no audit row for the user-scope change")
}

