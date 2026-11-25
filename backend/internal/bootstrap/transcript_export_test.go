package bootstrap

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"velagateway/internal/model"
)

// Exporting a terminal session log reads nothing new — the file is built in the
// browser from lines already displayed. What it does is put a copy of production
// query output on someone's disk, and /export already records exactly that. These
// tests hold the pair together: both routes out of the console leave a trace.

func lastAudit(t *testing.T, app *testApp) model.AuditLog {
	t.Helper()
	var row model.AuditLog
	if err := app.repo.DB().Order("id desc").First(&row).Error; err != nil {
		t.Fatalf("read audit: %v", err)
	}
	return row
}

func TestTranscriptExport_IsAudited(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	conn := app.connIDByEnv(token, "prod")

	r := app.do(http.MethodPost, "/api/v1/terminal/transcript-export", token, map[string]any{
		"connectionId": conn, "filename": "vela-prod-order-cluster-20260807-140215.log",
		"lines": 187, "dropped": 0,
	})
	eq(t, r.Code, 0, "record transcript export")

	row := lastAudit(t, app)
	eq(t, row.Result, model.ResultExported, "audit result")
	eq(t, row.ConnectionID, conn, "audit is attached to the instance")
	if !strings.Contains(row.Command, "187") {
		t.Errorf("audit row should say how much left the console, got %q", row.Command)
	}
	// It is not SQL and must not read as SQL in the audit list.
	if !strings.HasPrefix(row.Command, `\log `) {
		t.Errorf("expected a non-SQL marker, got %q", row.Command)
	}
	// Hash-chained like every other row — an export record that could be removed
	// without breaking the chain would not be worth writing.
	if row.Hash == "" || row.PrevHash == "" {
		t.Error("the export row must be part of the audit chain")
	}
}

// A partial transcript must not be recorded as if it were the whole session.
func TestTranscriptExport_RecordsThatTheFileWasTruncated(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	conn := app.connIDByEnv(token, "prod")

	eq(t, app.do(http.MethodPost, "/api/v1/terminal/transcript-export", token, map[string]any{
		"connectionId": conn, "filename": "s.log", "lines": 5000, "dropped": 320,
	}).Code, 0, "record a truncated export")

	row := lastAudit(t, app)
	if !strings.Contains(row.Command, "320") {
		t.Errorf("a truncated file must say so in the audit row, got %q", row.Command)
	}
}

// The audit trail must not be seedable with rows about instances the caller
// cannot reach.
func TestTranscriptExport_RefusesAnInstanceTheCallerCannotSee(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")

	// An instance tagged so that only a matching role may see it.
	cr := app.do(http.MethodPost, "/api/v1/connections", admin, map[string]any{
		"name": "sealed-db", "engine": "MySQL 8.0", "host": "10.0.0.77:3306",
		"env": "prod", "policy": "strict",
	})
	eq(t, cr.Code, 0, "create instance")
	var c struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(cr.Data, &c); err != nil {
		t.Fatalf("decode connection: %v", err)
	}
	eq(t, app.do(http.MethodPatch, "/api/v1/connections/"+itoa(c.ID), admin,
		map[string]any{"tags": "sealed"}).Code, 0, "tag it out of reach")

	// zhaolei holds the read-only role, whose scope does not include "sealed".
	ro := app.login("zhaolei@vela.io", "vela123")
	before := lastAudit(t, app).ID
	r := app.do(http.MethodPost, "/api/v1/terminal/transcript-export", ro, map[string]any{
		"connectionId": c.ID, "filename": "x.log", "lines": 3,
	})
	if r.Code == 0 {
		t.Error("a caller who cannot reach the instance must not be able to record an export against it")
	}
	if got := lastAudit(t, app).ID; got != before {
		t.Error("the refused call must not have written an audit row")
	}
}

// A missing instance is refused rather than recorded against nothing.
func TestTranscriptExport_RefusesAnUnknownInstance(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	if r := app.do(http.MethodPost, "/api/v1/terminal/transcript-export", token, map[string]any{
		"connectionId": 999999, "filename": "x.log", "lines": 1,
	}); r.Code == 0 {
		t.Error("an unknown instance must be refused")
	}
}
