package bootstrap

// 执行变更阶段要把语句的返回结果写进日志。
//
// A release's execute log used to say only "执行成功 · N 行受影响" — fine for
// DML, useless for the SELECTs releases carry to verify their own work: the
// operator opening the run wants to SEE what came back, not re-run the query
// by hand in the terminal. Runs against a REAL sqlite target (the simulator
// invents row counts and returns no result set).

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/glebarez/go-sqlite"
)

// realConnForRelease registers a live sqlite connection seeded by prep.
func realConnForRelease(t *testing.T, app *testApp, token string, prep func(*sql.DB)) int64 {
	t.Helper()
	dbfile := filepath.Join(t.TempDir(), "release-target.db")
	raw, err := sql.Open("sqlite", dbfile)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	prep(raw)
	raw.Close()
	cr := app.do(http.MethodPost, "/api/v1/connections", token, map[string]any{
		"name": "rel-real-sqlite", "engine": "SQLite", "host": "localhost:0",
		"env": "dev", "policy": "audit-only", "database": dbfile,
	})
	eq(t, cr.Code, 0, "create real connection")
	var conn struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(cr.Data, &conn)
	return conn.ID
}

func runExecOnlyRelease(t *testing.T, app *testApp, token string, connID int64, name, sqlText string) string {
	t.Helper()
	pid := app.createPipeline(token, name, "", []map[string]any{
		{"name": "执行变更", "type": "execute"},
	})
	r := app.submitRelease(token, map[string]any{
		"title": name, "pipelineId": pid, "connectionId": connID,
		"sql": sqlText, "reason": "结果日志回归",
	})
	eq(t, r.Code, 0, "submit release")
	var rel releaseView
	_ = json.Unmarshal(r.Data, &rel)
	// 0024 执行闸:到点的人点击确认后才落库
	done := app.confirmExecutionAndWait(token, rel.ID)
	eq(t, done.Status, "success", "release runs ("+done.Error+")")
	return stageByType(done, "execute").Log
}

func TestExecuteStageLogsQueryResults(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	conn := realConnForRelease(t, app, admin, func(db *sql.DB) {
		_, _ = db.Exec(`CREATE TABLE checks (status_col TEXT, answer INTEGER)`)
		_, _ = db.Exec(`INSERT INTO checks VALUES ('all-good', 42)`)
	})

	log := runExecOnlyRelease(t, app, admin, conn, "查询回显", "SELECT status_col, answer FROM checks;")

	// The result SET is in the log: column names and cell values, not just a
	// row count.
	for _, want := range []string{"status_col", "answer", "all-good", "42"} {
		if !strings.Contains(log, want) {
			t.Errorf("execute log should carry the result table (missing %q), got:\n%s", want, log)
		}
	}
}

// TestExecuteStageResultLogIsBounded — a SELECT over a big table must not turn
// the stage log into a data export: the log shows a bounded preview and says
// what it left out.
func TestExecuteStageResultLogIsBounded(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	conn := realConnForRelease(t, app, admin, func(db *sql.DB) {
		_, _ = db.Exec(`CREATE TABLE big (n INTEGER, v TEXT)`)
		for i := 1; i <= 55; i++ {
			_, _ = db.Exec(fmt.Sprintf(`INSERT INTO big VALUES (%d, 'row-%d')`, i, i))
		}
	})

	log := runExecOnlyRelease(t, app, admin, conn, "大结果回显", "SELECT n, v FROM big ORDER BY n;")

	if !strings.Contains(log, "row-1\n") && !strings.Contains(log, "row-1 ") {
		t.Errorf("preview should show the first rows, got:\n%s", clipForMsg(log))
	}
	if strings.Contains(log, "row-55") {
		t.Errorf("preview must stop at the bound, but row 55 leaked into the log")
	}
	// The elision is SAID rather than silent — a log that looks complete but
	// isn't is worse than either.
	if !strings.Contains(log, "省略") && !strings.Contains(log, "截断") {
		t.Errorf("an elided result must say so, got:\n%s", clipForMsg(log))
	}
}

func clipForMsg(s string) string {
	if len(s) > 800 {
		return s[:800] + "…"
	}
	return s
}
