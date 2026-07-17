package bootstrap

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// An uploaded script exposes its server path to the owner and its content can be
// loaded (own only) so the terminal can scan + dispatch it for execution.
func TestScriptUpload_PathAndContentForTerminal(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	l2 := app.login("chenhao@vela.io", "vela123")
	eq(t, app.do(http.MethodPut, "/api/v1/settings", admin, map[string]any{"script.savePath": t.TempDir()}).Code, 0, "set path")

	body := "SELECT count(*) FROM orders;\nUPDATE users SET tier='vip' WHERE id=1;\n"
	up := app.do(http.MethodPost, "/api/v1/scripts/upload", admin, map[string]any{"content": body, "filename": "job.sql"})
	eq(t, up.Code, 0, "upload")
	var rec struct {
		ID   int64  `json:"id"`
		Path string `json:"path"`
	}
	_ = json.Unmarshal(up.Data, &rec)
	if rec.Path == "" || !strings.Contains(rec.Path, "Lin_Wei") {
		t.Fatalf("expected a server path under the user dir, got %q", rec.Path)
	}

	// owner loads content for terminal dispatch
	ct := app.do(http.MethodGet, "/api/v1/scripts/uploads/"+itoa(rec.ID)+"/content", admin, nil)
	eq(t, ct.Code, 0, "content code")
	var out struct {
		Content  string `json:"content"`
		Filename string `json:"filename"`
	}
	_ = json.Unmarshal(ct.Data, &out)
	eq(t, out.Content, body, "content matches original")
	eq(t, out.Filename, "job.sql", "filename")

	// another user cannot load it
	if app.do(http.MethodGet, "/api/v1/scripts/uploads/"+itoa(rec.ID)+"/content", l2, nil).Code == 0 {
		t.Error("L2 must not load another user's script content")
	}
}
