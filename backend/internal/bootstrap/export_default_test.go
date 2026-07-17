package bootstrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Exports work out of the box: with no configured path they save under the
// default "export" directory, each user's files in a subdirectory named after
// them (<exportPath>/<username>/...).
func TestExport_DefaultDirAndPerUser(t *testing.T) {
	defer os.RemoveAll("export") // default dir gets created under the run/test dir
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	dev := app.connIDByEnv(token, "dev")

	// no export.savePath configured → default "export"
	id := app.submitExport(token, dev, "SELECT id, name FROM users", "rpt")
	job := app.waitJob(token, id)
	eq(t, job.Status, "done", "export done by default")

	parts := job.fileParts()
	if len(parts) == 0 {
		t.Fatal("expected part files")
	}
	if sp := filepath.ToSlash(parts[0]); !strings.Contains(sp, "export/Lin_Wei/") {
		t.Errorf("expected export under export/Lin_Wei/, got %q", parts[0])
	}
	if _, err := os.Stat(parts[0]); err != nil {
		t.Errorf("export file not found at %s: %v", parts[0], err)
	}
}
