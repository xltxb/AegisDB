package bootstrap

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"velagateway/internal/service"
)

// countFilesUnder walks dir recursively and returns how many regular files exist.
func countFilesUnder(t *testing.T, dir string) int {
	t.Helper()
	n := 0
	_ = filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err == nil && d != nil && !d.IsDir() {
			n++
		}
		return nil
	})
	return n
}

// The export row cap was hard-coded at 5,000,000 — the "tests/config" hook was
// never wired to config, so an operator with a legitimately larger export had
// no recourse but a rebuild. The cap must honour the export.maxRows setting
// (0 = unlimited), and a job that DOES trip the cap must not strand its
// already-flushed part files on disk: a failed job records no files, so
// nothing could ever download or clean them (B3).
func TestExport_RowCapConfigurableAndFailureLeavesNoOrphans(t *testing.T) {
	service.SetExportPartSize(64) // tiny parts so several are flushed before the cap trips
	defer service.SetExportPartSize(100 << 20)

	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	dev := app.connIDByEnv(token, "dev")
	dir := t.TempDir()
	eq(t, app.do(http.MethodPut, "/api/v1/settings", token, map[string]any{
		"export.savePath": dir, "export.maxRows": 50,
	}).Code, 0, "set export path + row cap")

	// The simulated dev connection synthesises well over 50 rows.
	id := app.submitExport(token, dev, "SELECT id, name, status FROM users", "capped")
	job := app.waitJob(token, id)
	eq(t, job.Status, "failed", "job over the configured cap fails")
	if !strings.Contains(job.Error, "行数上限 50") {
		t.Errorf("failure should cite the configured cap, got %q", job.Error)
	}
	if n := countFilesUnder(t, dir); n != 0 {
		t.Errorf("failed export stranded %d orphaned part file(s) under %s", n, dir)
	}

	// The tiny part size has done its job (parts flushed before the cap trips).
	// Restore the default before the uncapped run — at 64 bytes/part the second
	// job would write thousands of archives and outlast the poll window (flaky).
	service.SetExportPartSize(100 << 20)

	// Raising the cap (0 = unlimited) lets the same export through.
	eq(t, app.do(http.MethodPut, "/api/v1/settings", token, map[string]any{
		"export.maxRows": 0,
	}).Code, 0, "lift row cap")
	id2 := app.submitExport(token, dev, "SELECT id, name, status FROM users", "uncapped")
	job2 := app.waitJob(token, id2)
	eq(t, job2.Status, "done", "same export succeeds once the cap is lifted")
	if job2.Rows <= 50 {
		t.Errorf("expected more rows than the old cap, got %d", job2.Rows)
	}
}
