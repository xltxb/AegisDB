package bootstrap

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Uploads work out of the box: with no configured path they save under the
// default "uploads" directory, and every user's files land in a subdirectory
// named after them (<savePath>/<username>/...).
func TestScriptUpload_DefaultDirAndPerUser(t *testing.T) {
	defer os.RemoveAll("uploads") // default dir gets created under the run/test dir
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	dev := app.connIDByEnv(token, "dev")

	// enabled by default, save path defaults to "uploads"
	cfg := app.do(http.MethodGet, "/api/v1/scripts/config", token, nil)
	eq(t, cfg.Code, 0, "config code")
	var c struct {
		Enabled  bool   `json:"enabled"`
		SavePath string `json:"savePath"`
	}
	_ = json.Unmarshal(cfg.Data, &c)
	if !c.Enabled {
		t.Fatal("upload should be enabled by default")
	}
	eq(t, c.SavePath, "uploads", "default save path")

	// a safe script executes and is persisted under uploads/<username>/
	run := app.do(http.MethodPost, "/api/v1/scripts/execute", token, map[string]any{
		"content": "SELECT id FROM orders; UPDATE users SET tier='vip' WHERE id=1;", "filename": "mig.sql", "connectionId": dev,
	})
	eq(t, run.Code, 0, "execute by default")
	var out struct {
		Statements int    `json:"statements"`
		SavedPath  string `json:"savedPath"`
	}
	_ = json.Unmarshal(run.Data, &out)
	eq(t, out.Statements, 2, "executed statement count")
	if sp := filepath.ToSlash(out.SavedPath); !strings.Contains(sp, "uploads/Lin_Wei/") {
		t.Errorf("expected save under uploads/Lin_Wei/, got %q", out.SavedPath)
	}
	if _, err := os.Stat(out.SavedPath); err != nil {
		t.Errorf("saved script not found at %s: %v", out.SavedPath, err)
	}

	// a configured path is still honored, and still nests by username
	dir := t.TempDir()
	eq(t, app.do(http.MethodPut, "/api/v1/settings", token, map[string]any{"script.savePath": dir}).Code, 0, "set custom path")
	up := app.do(http.MethodPost, "/api/v1/scripts/upload", token, map[string]any{"content": "SELECT 9;", "filename": "u.sql"})
	eq(t, up.Code, 0, "upload with custom path")
	entries, _ := os.ReadDir(filepath.Join(dir, "Lin_Wei"))
	if len(entries) == 0 {
		t.Errorf("expected a file under %s/Lin_Wei", dir)
	}
}
