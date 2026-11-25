package bootstrap

import (
	"bytes"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/glebarez/go-sqlite"

	"velagateway/internal/service"
	"velagateway/pkg/crypto"
)

// Splitting a CSV export into parts must never break a single row across two files
// — not even for cells containing commas, double quotes, or embedded newlines
// (which a naive byte-boundary split would corrupt). We export such rows with a
// tiny part size (forcing many splits), then assert every part parses as valid CSV
// (a broken quoted field would fail the parse) and the reassembled records exactly
// match the source rows in order.
func TestExport_PartsPreserveCompleteRows(t *testing.T) {
	service.SetExportPartSize(120) // tiny parts to force splits mid-result
	defer service.SetExportPartSize(100 << 20)

	// Each note exercises a tricky CSV case; padding keeps rows large enough to
	// straddle the small part boundary.
	pad := strings.Repeat("x", 30)
	notes := []string{
		"plain " + pad,
		"has,comma " + pad,
		"has \"double\" quote " + pad,
		"multi\nline\nnote " + pad, // embedded newlines must stay ONE record
		"comma, quote \" and\nnewline " + pad,
		"trailing quote\" " + pad,
	}
	dbfile := filepath.Join(t.TempDir(), "tricky.db")
	raw, err := sql.Open("sqlite", dbfile)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if _, err := raw.Exec(`CREATE TABLE t (id INTEGER, note TEXT)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	for i, n := range notes {
		if _, err := raw.Exec(`INSERT INTO t VALUES (?, ?)`, i, n); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	raw.Close()

	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	cr := app.do(http.MethodPost, "/api/v1/connections", token, map[string]any{
		"name": "tricky", "engine": "SQLite", "host": "localhost:0",
		"env": "dev", "policy": "audit-only", "database": dbfile,
	})
	eq(t, cr.Code, 0, "create connection")
	var conn struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(cr.Data, &conn)
	eq(t, app.do(http.MethodPut, "/api/v1/settings", token, map[string]any{"export.savePath": t.TempDir()}).Code, 0, "set path")

	id := app.submitExport(token, conn.ID, "SELECT id, note FROM t ORDER BY id", "tricky")
	job := app.waitJob(token, id)
	eq(t, job.Status, "done", "status")
	eq(t, job.Rows, len(notes), "row count")

	parts := job.fileParts()
	if len(parts) < 2 {
		t.Fatalf("expected the result to split into multiple parts, got %d", len(parts))
	}

	// Reassemble the notes across every part; each part must be valid CSV.
	got := []string{}
	for _, f := range parts {
		body := app.raw(http.MethodGet, "/api/v1/export/download?file="+urlEscape(f), token)
		csvBytes, derr := crypto.ZipDecrypt(body, job.Password)
		if derr != nil {
			t.Fatalf("decrypt %s: %v", f, derr)
		}
		recs, rerr := csv.NewReader(bytes.NewReader(csvBytes)).ReadAll()
		if rerr != nil {
			t.Fatalf("part %s is not valid CSV (a row was broken?): %v", f, rerr)
		}
		// The mark arrives attached to the first header field: Go's csv reader does
		// not strip it, and neither does pandas without utf-8-sig. That is the known
		// price of the mark, written down here rather than discovered by whoever
		// next parses one of these files.
		if len(recs) == 0 || recs[0][0] != "\uFEFFid" {
			t.Fatalf("part %s missing BOM+header, got %v", f, recs)
		}
		for _, rec := range recs[1:] { // skip the repeated header
			if len(rec) != 2 {
				t.Fatalf("part %s has a record with %d fields (want 2): %q", f, len(rec), rec)
			}
			got = append(got, rec[1])
		}
	}

	if len(got) != len(notes) {
		t.Fatalf("reassembled %d data rows across parts, want %d", len(got), len(notes))
	}
	for i, want := range notes {
		if got[i] != want {
			t.Errorf("row %d content corrupted:\n got  %q\n want %q", i, got[i], want)
		}
	}
}
