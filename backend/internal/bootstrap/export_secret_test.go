package bootstrap

import (
	"net/http"
	"strings"
	"testing"
)

// The archive password must be encrypted at rest so a DB dump can't unlock the
// exported data, while the owner still sees a usable plaintext password via the
// API (R25).
func TestExport_PasswordEncryptedAtRest(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	dev := app.connIDByEnv(token, "dev")

	dir := t.TempDir()
	eq(t, app.do(http.MethodPut, "/api/v1/settings", token, map[string]any{"export.savePath": dir}).Code, 0, "set path")

	id := app.submitExport(token, dev, "SELECT id FROM users", "u")
	job := app.waitJob(token, id) // API path → decrypted
	eq(t, job.Status, "done", "job done")
	if job.Password == "" {
		t.Fatal("API should return a usable plaintext password")
	}

	// read the raw stored row (repo does not decrypt)
	u, _ := app.svc.Repo.GetUserByEmail("linwei@vela.io")
	rows, _ := app.svc.Repo.ListExportJobs(u.ID, 0)
	var stored string
	for _, r := range rows {
		if r.ID == id {
			stored = r.Password
		}
	}
	if stored == "" {
		t.Fatal("stored job not found")
	}
	if stored == job.Password {
		t.Error("export password is stored in cleartext")
	}
	if !strings.HasPrefix(stored, "enc:") {
		t.Errorf("expected an encrypted-at-rest password, got %q", stored)
	}
}
