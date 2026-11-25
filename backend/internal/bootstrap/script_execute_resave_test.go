package bootstrap

import (
	"encoding/json"
	"net/http"
	"testing"
)

// Executing an already-uploaded script (dispatched with its uploadId) must not
// create a second saved copy; executing a fresh script still saves one.
func TestScriptExecute_UploadedScriptNotResaved(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	dev := app.connIDByEnv(token, "dev")
	eq(t, app.do(http.MethodPut, "/api/v1/settings", token, map[string]any{"script.savePath": t.TempDir()}).Code, 0, "set path")

	// upload once
	up := app.do(http.MethodPost, "/api/v1/scripts/upload", token, map[string]any{"content": "SELECT 1;", "filename": "job.sql"})
	eq(t, up.Code, 0, "upload")
	var rec struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(up.Data, &rec)
	eq(t, len(app.listUploads(token)), 1, "one upload after upload")

	// execute WITH uploadId → not re-saved
	r := app.do(http.MethodPost, "/api/v1/scripts/execute", token, map[string]any{
		"connectionId": dev, "content": "SELECT 1;", "filename": "job.sql", "uploadId": rec.ID,
	})
	eq(t, r.Code, 0, "execute uploaded script")
	eq(t, len(app.listUploads(token)), 1, "still one upload after executing the uploaded script")

	// execute a fresh script (no uploadId) → a new copy is saved
	r2 := app.do(http.MethodPost, "/api/v1/scripts/execute", token, map[string]any{
		"connectionId": dev, "content": "SELECT 2;", "filename": "fresh.sql",
	})
	eq(t, r2.Code, 0, "execute fresh script")
	eq(t, len(app.listUploads(token)), 2, "fresh execute saves a new upload")
}
