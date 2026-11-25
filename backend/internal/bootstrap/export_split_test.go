package bootstrap

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/glebarez/go-sqlite"

	"velagateway/internal/service"
	"velagateway/pkg/crypto"
)

// No row limit: a result larger than the part threshold is split into multiple
// ~100MB CSV files (here the threshold is shrunk so a small result still
// splits). Every part is independently gzip+encrypted and repeats the header,
// and together they hold all rows.
func TestExport_SplitsIntoParts(t *testing.T) {
	service.SetExportPartSize(2048) // 2 KB parts for the test
	defer service.SetExportPartSize(100 << 20)

	const total = 60
	dbfile := filepath.Join(t.TempDir(), "big.db")
	raw, err := sql.Open("sqlite", dbfile)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if _, err := raw.Exec(`CREATE TABLE t (id INTEGER, note TEXT)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	for i := 0; i < total; i++ {
		if _, err := raw.Exec(`INSERT INTO t VALUES (?, ?)`, i, fmt.Sprintf("row-%04d-%s", i, strings.Repeat("x", 60))); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	raw.Close()

	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	cr := app.do(http.MethodPost, "/api/v1/connections", token, map[string]any{
		"name": "big-sqlite", "engine": "SQLite", "host": "localhost:0",
		"env": "dev", "policy": "audit-only", "database": dbfile,
	})
	eq(t, cr.Code, 0, "create connection")
	var conn struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(cr.Data, &conn)
	eq(t, app.do(http.MethodPut, "/api/v1/settings", token, map[string]any{"export.savePath": t.TempDir()}).Code, 0, "set path")

	id := app.submitExport(token, conn.ID, "SELECT id, note FROM t", "big")
	job := app.waitJob(token, id)
	eq(t, job.Status, "done", "status")
	eq(t, job.Rows, total, "total exported rows")

	parts := job.fileParts()
	if len(parts) < 2 {
		t.Fatalf("expected the result to split into multiple parts, got %d", len(parts))
	}
	eq(t, job.Parts, len(parts), "parts count matches files")

	// download+decrypt every part; each has the header, and data rows sum to total
	data := 0
	for _, f := range parts {
		body := app.raw(http.MethodGet, "/api/v1/export/download?file="+urlEscape(f), token)
		csv, err := crypto.ZipDecrypt(body, job.Password)
		if err != nil {
			t.Fatalf("decrypt %s: %v", f, err)
		}
		lines := strings.Split(strings.TrimRight(string(csv), "\n"), "\n")
		// EVERY part carries the byte order mark, not just the first: each one is a
		// standalone .csv opened on its own, and a part without it opens as mojibake
		// in Excel on a Chinese Windows while its siblings read fine.
		if len(lines) == 0 || !strings.HasPrefix(lines[0], "\uFEFFid,note") {
			t.Errorf("part %s missing BOM+header, first line %q", f, lines[0])
		}
		data += len(lines) - 1 // minus the header row
	}
	eq(t, data, total, "data rows across all parts")
}
