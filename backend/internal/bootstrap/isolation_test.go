package bootstrap

import (
	"encoding/json"
	"net/http"
	"testing"
)

type auditActorRow struct {
	Actor   string `json:"actor"`
	Command string `json:"command"`
}

func (a *testApp) auditActorRows(token string) []auditActorRow {
	a.t.Helper()
	var rows []auditActorRow
	_ = json.Unmarshal(a.auditItemsRaw(token, ""), &rows)
	return rows
}

// A non-oversight user only sees their own commands in the audit log; an
// oversight role (admin) sees everyone's.
func TestIsolation_AuditIsPerUser(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123") // admin → oversight
	l2 := app.login("chenhao@vela.io", "vela123")   // DBA L2 → own only

	// each runs a command (l2 may access order-cluster via its 'orders' tag)
	orderCluster := app.connIDByName(admin, "order-cluster")
	app.do(http.MethodPost, "/api/v1/terminal/exec", admin, map[string]any{"connectionId": orderCluster, "sql": "SELECT 1 FROM t"})
	app.do(http.MethodPost, "/api/v1/terminal/exec", l2, map[string]any{"connectionId": orderCluster, "sql": "SELECT 2 FROM t"})

	// L2 sees only "Chen Hao" rows, never anyone else's
	l2rows := app.auditActorRows(l2)
	if len(l2rows) == 0 {
		t.Fatal("L2 should see at least its own command")
	}
	for _, r := range l2rows {
		if r.Actor != "Chen Hao" {
			t.Errorf("L2 must not see other users' commands, saw actor %q", r.Actor)
		}
	}

	// admin sees more than one actor
	actors := map[string]bool{}
	for _, r := range app.auditActorRows(admin) {
		actors[r.Actor] = true
	}
	if len(actors) < 2 {
		t.Errorf("admin (oversight) should see multiple actors, got %v", actors)
	}
	if !actors["Chen Hao"] {
		t.Error("admin should see L2's command too")
	}
}

// A user cannot download another user's export file even with the exact path.
func TestIsolation_ExportDownloadIsPerUser(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	l2 := app.login("chenhao@vela.io", "vela123")
	dev := app.connIDByEnv(admin, "dev")
	eq(t, app.do(http.MethodPut, "/api/v1/settings", admin, map[string]any{"export.savePath": t.TempDir()}).Code, 0, "set path")

	id := app.submitExport(admin, dev, "SELECT id, name FROM users", "admin-only")
	job := app.waitJob(admin, id)
	eq(t, job.Status, "done", "admin export done")
	file := job.fileParts()[0]

	// L2 cannot download admin's file (ownership check → generic error, no bytes)
	blocked := app.do(http.MethodGet, "/api/v1/export/download?file="+urlEscape(file), l2, nil)
	if blocked.Code == 0 {
		t.Fatalf("L2 must not download admin's export file (got success)")
	}

	// admin downloads own file fine
	body := app.raw(http.MethodGet, "/api/v1/export/download?file="+urlEscape(file), admin)
	if len(body) == 0 {
		t.Error("admin should download own export file")
	}

	// L2's own export list is empty (does not include admin's job)
	if jobs := app.exportJobs(l2); len(jobs) != 0 {
		t.Errorf("L2 should see no export jobs, got %d", len(jobs))
	}
}
