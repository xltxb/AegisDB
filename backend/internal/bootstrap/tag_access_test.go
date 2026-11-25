package bootstrap

import (
	"encoding/json"
	"net/http"
	"testing"
)

func (a *testApp) listConnNames(token string) []string {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/connections", token, nil)
	if r.Code != 0 {
		a.t.Fatalf("list connections: code=%d msg=%s", r.Code, r.Msg)
	}
	var cs []struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(r.Data, &cs)
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Name)
	}
	return out
}

func (a *testApp) connIDByName(token, name string) int64 {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/connections", token, nil)
	var cs []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	_ = json.Unmarshal(r.Data, &cs)
	for _, c := range cs {
		if c.Name == name {
			return c.ID
		}
	}
	a.t.Fatalf("connection %q not visible", name)
	return 0
}

func (a *testApp) roleIDByCode(token, code string) int64 {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/roles", token, nil)
	var rs []struct {
		ID   int64  `json:"id"`
		Code string `json:"code"`
	}
	_ = json.Unmarshal(r.Data, &rs)
	for _, x := range rs {
		if x.Code == code {
			return x.ID
		}
	}
	a.t.Fatalf("role %q not found", code)
	return 0
}

func has(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// Tag-based DB assignment: a group (role) only sees/operates on connections whose
// tags it has been granted; admin (no tags) is unrestricted; granting a tag
// widens access live.
func TestTagAccess_GroupSeesOnlyTaggedDatabases(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")

	if all := app.listConnNames(admin); len(all) != 5 {
		t.Fatalf("admin (no tags) should see all 5 connections, got %d: %v", len(all), all)
	}

	// chenhao is a seeded L2 user (tags: orders,users)
	l2 := app.login("chenhao@vela.io", "vela123")
	seen := app.listConnNames(l2)
	if has(seen, "analytics-ro") || has(seen, "sandbox-dev") {
		t.Errorf("L2 must not see analytics-ro/sandbox-dev, got %v", seen)
	}
	if !has(seen, "order-cluster") || !has(seen, "user-cluster") {
		t.Errorf("L2 should see order/user clusters, got %v", seen)
	}

	// exec on an out-of-scope DB is forbidden even by id
	analyticsID := app.connIDByName(admin, "analytics-ro")
	r := app.do(http.MethodPost, "/api/v1/terminal/exec", l2, map[string]any{
		"connectionId": analyticsID, "sql": "SELECT 1",
	})
	eq(t, r.Code, 40300, "L2 exec on unassigned DB is forbidden")

	// admin grants L2 the analytics tag → L2 sees analytics-ro live
	l2id := app.roleIDByCode(admin, "l2")
	eq(t, app.do(http.MethodPut, "/api/v1/roles/"+itoa(l2id)+"/tags", admin, map[string]any{
		"tags": []string{"orders", "users", "analytics"},
	}).Code, 0, "grant analytics tag to L2")
	if !has(app.listConnNames(l2), "analytics-ro") {
		t.Error("L2 should see analytics-ro after being granted the analytics tag")
	}
}

// Tagging a connection is reflected in the global tag list.
func TestTagAccess_ConnectionTagsEditable(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	id := app.connIDByName(admin, "sandbox-dev")

	eq(t, app.do(http.MethodPatch, "/api/v1/connections/"+itoa(id), admin, map[string]any{
		"tags": "sandbox,dev,playground",
	}).Code, 0, "set connection tags")

	tagsR := app.do(http.MethodGet, "/api/v1/tags", admin, nil)
	var tags []string
	_ = json.Unmarshal(tagsR.Data, &tags)
	if !has(tags, "playground") {
		t.Errorf("global tag list should include the new 'playground' tag, got %v", tags)
	}
}
