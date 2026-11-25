package bootstrap

import (
	"net/http"
	"strings"
	"testing"
)

// A legitimately long export query (an IN-list of a few thousand ids) used to
// die at submission with the raw driver error "Error 1406: Data too long for
// column 'sql'" — the job row's column was TEXT (64KB). The column is now
// MEDIUMTEXT (migration 0018) and the service accepts anything up to its bound,
// refusing larger SQL with an actionable message instead of a driver error.
func TestExport_LongSQLAcceptedAndOversizeRefusedClearly(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	dev := app.connIDByEnv(token, "dev")
	eq(t, app.do(http.MethodPut, "/api/v1/settings", token,
		map[string]any{"export.savePath": t.TempDir()}).Code, 0, "set export path")

	// ~120KB single read-only statement — past the old 64KB TEXT cap.
	longIn := "SELECT id, name FROM users WHERE id IN (1" + strings.Repeat(",1234567", 15_000) + ")"
	if len(longIn) < 100_000 {
		t.Fatalf("fixture too short: %d", len(longIn))
	}
	r := app.do(http.MethodPost, "/api/v1/export", token, map[string]any{
		"connectionId": dev, "sql": longIn, "name": "long-in-list",
	})
	eq(t, r.Code, 0, "long (but bounded) export SQL accepted")

	// Past the stored bound: refused with a message that names the limit,
	// never the database's "Data too long for column".
	huge := "SELECT 1 -- " + strings.Repeat("x", 16<<20)
	hr := app.do(http.MethodPost, "/api/v1/export", token, map[string]any{
		"connectionId": dev, "sql": huge, "name": "oversize",
	})
	if hr.Code == 0 {
		t.Fatal("oversize SQL must be refused")
	}
	if !strings.Contains(hr.Msg, "SQL 过长") {
		t.Errorf("refusal should be actionable, got %q", hr.Msg)
	}
}
