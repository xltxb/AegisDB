package bootstrap

// 导出保留策略的回归网。
//
// The thing under test deletes files. That is the whole reason it needs a tight
// net: every case below is about what it must NOT delete — a job inside the
// window, a file some job still claims, anything when retention is switched off
// — and about what has to survive the deletion, which is the record of what was
// exported.

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"velagateway/internal/model"
)

// seedExportJob writes real part files and a matching row, aged as asked.
func seedExportJob(t *testing.T, app *testApp, dir string, userID int64, name string, age time.Duration) (model.ExportJob, []string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	var files []string
	for i := 1; i <= 2; i++ {
		p := filepath.Join(dir, name+"-part0"+string(rune('0'+i))+".zip")
		if err := os.WriteFile(p, []byte("PK-not-really-a-zip"), 0o644); err != nil {
			t.Fatalf("write part: %v", err)
		}
		when := time.Now().Add(-age)
		_ = os.Chtimes(p, when, when)
		files = append(files, p)
	}
	finished := time.Now().Add(-age)
	job := model.ExportJob{
		UserID: userID, ConnectionID: 1, Instance: "demo", Database: "demo",
		SQL: "SELECT 1;", Name: name, Status: model.ExportDone,
		Rows: 10, Bytes: 38, Parts: len(files), Files: strings.Join(files, "\n"),
		Password: "enc:pretend", CreatedAt: finished, FinishedAt: &finished,
	}
	if err := app.repo.DB().Create(&job).Error; err != nil {
		t.Fatalf("create export job: %v", err)
	}
	return job, files
}

func reloadJob(t *testing.T, app *testApp, id int64) model.ExportJob {
	t.Helper()
	j, err := app.repo.GetExportJob(id)
	if err != nil {
		t.Fatalf("reload job %d: %v", id, err)
	}
	return *j
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// TestExportRetentionDeletesAgedArchives — the headline behaviour, plus the two
// halves people forget: the row survives, and the password does not.
func TestExportRetentionDeletesAgedArchives(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	dir := t.TempDir()
	app.setSettings(token, map[string]any{"export.savePath": dir})

	uid := app.userIDByEmail(token, "linwei@vela.io")
	old, oldFiles := seedExportJob(t, app, dir, uid, "aged", 4*24*time.Hour)
	fresh, freshFiles := seedExportJob(t, app, dir, uid, "recent", 12*time.Hour)

	app.svc.SweepExportRetention()

	// Past the window: files gone, row expired, pointer and secret cleared.
	for _, f := range oldFiles {
		if exists(f) {
			t.Errorf("aged part still on disk: %s", f)
		}
	}
	got := reloadJob(t, app, old.ID)
	eq(t, got.Status, model.ExportExpired, "aged job status")
	eq(t, got.Files, "", "files pointer cleared")
	eq(t, got.Password, "", "archive password cleared")
	// …but what was exported is still on the record.
	eq(t, got.Rows, 10, "row count survives the file")
	eq(t, got.Parts, 2, "part count survives the file")
	if got.SQL == "" {
		t.Error("the exported query must outlive the archive")
	}

	// Inside the window: untouched, still downloadable.
	for _, f := range freshFiles {
		if !exists(f) {
			t.Errorf("a job inside the retention window lost a part: %s", f)
		}
	}
	eq(t, reloadJob(t, app, fresh.ID).Status, model.ExportDone, "fresh job status")
}

// TestExportRetentionAuditsTheDeletion — deleting a copy of production data is
// an event, not housekeeping. The trail names the owner AND the sweep.
func TestExportRetentionAuditsTheDeletion(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	dir := t.TempDir()
	app.setSettings(token, map[string]any{"export.savePath": dir})
	uid := app.userIDByEmail(token, "linwei@vela.io")
	seedExportJob(t, app, dir, uid, "aged", 5*24*time.Hour)

	app.svc.SweepExportRetention()

	var rows []struct {
		Command  string `json:"command"`
		Actor    string `json:"actor"`
		Operator string `json:"operator"`
		Result   string `json:"result"`
	}
	_ = json.Unmarshal(app.auditItemsRaw(token, "?range=today&pageSize=100"), &rows)
	found := false
	for _, r := range rows {
		if strings.HasPrefix(r.Command, "EXPORT-CLEANUP") {
			found = true
			eq(t, r.Actor, "Lin Wei", "audited against the export's owner")
			if !strings.Contains(r.Operator, "保留策略") {
				t.Errorf("operator should name the sweep, got %q", r.Operator)
			}
			if !strings.Contains(r.Command, "保留期 3 天") {
				t.Errorf("the audit line should state the window it applied, got %q", r.Command)
			}
		}
	}
	if !found {
		t.Error("no audit row for the retention deletion")
	}
}

// TestExportRetentionDisabled — 0 means keep forever, not "delete everything".
func TestExportRetentionDisabled(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	dir := t.TempDir()
	app.setSettings(token, map[string]any{"export.savePath": dir})
	app.setSettings(token, map[string]any{"export.retentionDays": 0})
	uid := app.userIDByEmail(token, "linwei@vela.io")
	job, files := seedExportJob(t, app, dir, uid, "ancient", 400*24*time.Hour)

	app.svc.SweepExportRetention()

	for _, f := range files {
		if !exists(f) {
			t.Errorf("retention is off; %s must not be deleted", f)
		}
	}
	eq(t, reloadJob(t, app, job.ID).Status, model.ExportDone, "job untouched")
}

// TestExportRetentionOrphanSweep — files no row claims are collected, but only
// ones this exporter's own naming shows it wrote. export.savePath is an
// operator-chosen directory and may hold other people's files.
func TestExportRetentionOrphanSweep(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	dir := t.TempDir()
	app.setSettings(token, map[string]any{"export.savePath": dir})
	uid := app.userIDByEmail(token, "linwei@vela.io")

	// A live job's parts, and an aged orphan of the same shape.
	_, claimed := seedExportJob(t, app, dir, uid, "claimed", 1*time.Hour)
	aged := time.Now().Add(-10 * 24 * time.Hour)

	orphan := filepath.Join(dir, "leftover-part01.zip")
	stranger := filepath.Join(dir, "someone-elses-backup.tar.gz")
	freshOrphan := filepath.Join(dir, "yesterday-part01.zip")
	for _, f := range []string{orphan, stranger, freshOrphan} {
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", f, err)
		}
	}
	_ = os.Chtimes(orphan, aged, aged)
	_ = os.Chtimes(stranger, aged, aged)
	// freshOrphan keeps "now" as its mtime.

	app.svc.SweepExportRetention()

	if exists(orphan) {
		t.Error("an aged, unclaimed part file should have been collected")
	}
	if !exists(stranger) {
		t.Error("a file this exporter did not write must never be deleted")
	}
	if !exists(freshOrphan) {
		t.Error("an unclaimed part inside the window must be kept")
	}
	for _, f := range claimed {
		if !exists(f) {
			t.Errorf("a file a job still claims was collected as an orphan: %s", f)
		}
	}
}

// TestExpiredExportIsNotDownloadable — the row stays, so the API must refuse it
// rather than 500 on a path that is gone.
func TestExpiredExportIsNotDownloadable(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	dir := t.TempDir()
	app.setSettings(token, map[string]any{"export.savePath": dir})
	uid := app.userIDByEmail(token, "linwei@vela.io")
	_, files := seedExportJob(t, app, dir, uid, "aged", 7*24*time.Hour)

	app.svc.SweepExportRetention()

	r := app.do(http.MethodGet, "/api/v1/export/download?file="+files[0], token, nil)
	if r.Code == 0 {
		t.Fatal("a swept archive must not still be downloadable")
	}
}
