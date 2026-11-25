package bootstrap

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"velagateway/pkg/crypto"
)

func urlEscape(s string) string { return url.QueryEscape(s) }

// raw issues a GET and returns the raw response body (for binary downloads).
func (a *testApp) raw(method, path, token string) []byte {
	a.t.Helper()
	req, _ := http.NewRequest(method, a.srv.URL+path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		a.t.Fatalf("raw %s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return b
}

type exportJob struct {
	ID       int64  `json:"id"`
	Status   string `json:"status"`
	Rows     int    `json:"rows"`
	Bytes    int64  `json:"bytes"`
	Parts    int    `json:"parts"`
	Files    string `json:"files"`
	Password string `json:"password"`
	Error    string `json:"error"`
}

// fileParts splits the newline-joined part paths of a finished job.
func (j exportJob) fileParts() []string {
	out := []string{}
	for _, f := range strings.Split(j.Files, "\n") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

func (a *testApp) exportJobs(token string) []exportJob {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/export/jobs", token, nil)
	if r.Code != 0 {
		a.t.Fatalf("export jobs: code=%d msg=%s", r.Code, r.Msg)
	}
	var js []exportJob
	_ = json.Unmarshal(r.Data, &js)
	return js
}

// waitJob polls until job id reaches a terminal state (done/failed) or times out.
func (a *testApp) waitJob(token string, id int64) exportJob {
	a.t.Helper()
	for i := 0; i < 80; i++ {
		for _, j := range a.exportJobs(token) {
			if j.ID == id && (j.Status == "done" || j.Status == "failed") {
				return j
			}
		}
		time.Sleep(80 * time.Millisecond)
	}
	a.t.Fatalf("export job %d did not finish in time", id)
	return exportJob{}
}

func (a *testApp) submitExport(token string, conn int64, sql, name string) int64 {
	a.t.Helper()
	r := a.do(http.MethodPost, "/api/v1/export", token, map[string]any{
		"connectionId": conn, "sql": sql, "name": name,
	})
	if r.Code != 0 {
		a.t.Fatalf("submit export: code=%d msg=%s", r.Code, r.Msg)
	}
	var j exportJob
	_ = json.Unmarshal(r.Data, &j)
	return j.ID
}

// Async export: submit → job runs in background → finishes with password+file →
// downloads and decrypts back to the original CSV.
func TestExport_AsyncEncryptAndDownload(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	dev := app.connIDByEnv(token, "dev")

	dir := t.TempDir()
	eq(t, app.do(http.MethodPut, "/api/v1/settings", token, map[string]any{"export.savePath": dir}).Code, 0, "set path")

	id := app.submitExport(token, dev, "SELECT id, name, status FROM users", "users")
	job := app.waitJob(token, id)
	eq(t, job.Status, "done", "job status")
	if job.Password == "" || job.Rows <= 0 || job.Bytes <= 0 {
		t.Fatalf("bad finished job: %+v", job)
	}
	parts := job.fileParts()
	if len(parts) == 0 || job.Parts != len(parts) {
		t.Fatalf("expected part files, got parts=%d files=%q", job.Parts, job.Files)
	}
	if _, err := os.Stat(parts[0]); err != nil {
		t.Fatalf("archive not on disk: %v", err)
	}
	body := app.raw(http.MethodGet, "/api/v1/export/download?file="+urlEscape(parts[0]), token)
	csv, err := crypto.ZipDecrypt(body, job.Password)
	if err != nil {
		t.Fatalf("decrypt failed: %v", err)
	}
	header := strings.SplitN(string(csv), "\n", 2)[0]
	for _, col := range []string{"id", "name", "status"} {
		if !strings.Contains(header, col) {
			t.Errorf("decrypted header %q missing %q", header, col)
		}
	}
}

// Multiple export tasks submitted back-to-back all run (in parallel) and finish.
func TestExport_ParallelJobsAllComplete(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	dev := app.connIDByEnv(token, "dev")
	eq(t, app.do(http.MethodPut, "/api/v1/settings", token, map[string]any{"export.savePath": t.TempDir()}).Code, 0, "set path")

	ids := []int64{}
	for i := 0; i < 4; i++ {
		ids = append(ids, app.submitExport(token, dev, "SELECT id, name FROM users", fmt.Sprintf("batch-%d", i)))
	}
	files := map[string]bool{}
	for _, id := range ids {
		j := app.waitJob(token, id)
		eq(t, j.Status, "done", "parallel job status")
		files[j.fileParts()[0]] = true
	}
	if len(files) != len(ids) {
		t.Errorf("expected %d distinct archive files, got %d", len(ids), len(files))
	}
}
