package bootstrap

import (
	"encoding/json"
	"net/http"
	"testing"
)

type uploadRow struct {
	ID       int64  `json:"id"`
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
	Source   string `json:"source"`
}

func (a *testApp) listUploads(token string) []uploadRow {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/scripts/uploads", token, nil)
	if r.Code != 0 {
		a.t.Fatalf("list uploads: code=%d msg=%s", r.Code, r.Msg)
	}
	var us []uploadRow
	_ = json.Unmarshal(r.Data, &us)
	return us
}

// Upload files are per-user: each user only lists / downloads / deletes their
// own; nobody can touch another user's uploads.
func TestScriptUpload_PerUserManagement(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	l2 := app.login("chenhao@vela.io", "vela123")
	eq(t, app.do(http.MethodPut, "/api/v1/settings", admin, map[string]any{"script.savePath": t.TempDir()}).Code, 0, "set path")

	// admin uploads a script
	up := app.do(http.MethodPost, "/api/v1/scripts/upload", admin, map[string]any{
		"content": "SELECT 1;\nSELECT 2;\n", "filename": "mine.sql",
	})
	eq(t, up.Code, 0, "upload")
	var rec uploadRow
	_ = json.Unmarshal(up.Data, &rec)
	if rec.ID == 0 || rec.Filename != "mine.sql" || rec.Size <= 0 {
		t.Fatalf("bad upload record: %+v", rec)
	}

	// admin sees it; L2 sees nothing (isolation)
	if got := app.listUploads(admin); len(got) != 1 {
		t.Fatalf("admin should see 1 upload, got %d", len(got))
	}
	if got := app.listUploads(l2); len(got) != 0 {
		t.Errorf("L2 should see no uploads, got %d", len(got))
	}

	// admin downloads own file; content matches
	body := app.raw(http.MethodGet, "/api/v1/scripts/uploads/"+itoa(rec.ID)+"/download", admin)
	if string(body) != "SELECT 1;\nSELECT 2;\n" {
		t.Errorf("downloaded content mismatch: %q", string(body))
	}

	// L2 cannot download or delete admin's upload
	if app.do(http.MethodGet, "/api/v1/scripts/uploads/"+itoa(rec.ID)+"/download", l2, nil).Code == 0 {
		t.Error("L2 must not download admin's upload")
	}
	if app.do(http.MethodDelete, "/api/v1/scripts/uploads/"+itoa(rec.ID), l2, nil).Code == 0 {
		t.Error("L2 must not delete admin's upload")
	}
	// still there after L2's failed delete
	eq(t, len(app.listUploads(admin)), 1, "admin upload survives L2 delete attempt")

	// admin deletes own → gone
	eq(t, app.do(http.MethodDelete, "/api/v1/scripts/uploads/"+itoa(rec.ID), admin, nil).Code, 0, "admin delete")
	eq(t, len(app.listUploads(admin)), 0, "upload removed")
}
