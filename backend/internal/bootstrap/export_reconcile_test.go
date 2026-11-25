package bootstrap

import (
	"testing"

	"velagateway/internal/model"
	"velagateway/internal/service"
)

// Export jobs left running/pending by a crashed process must be reconciled to
// failed when a new process starts — the in-memory queue doesn't survive, so
// they would otherwise spin in the UI forever (R24).
func TestExport_StuckJobsFailedOnStartup(t *testing.T) {
	app := newTestApp(t)
	repo := app.svc.Repo
	u, _ := repo.GetUserByEmail("linwei@vela.io")

	// an orphaned in-flight job from a previous process
	if err := repo.CreateExportJob(&model.ExportJob{UserID: u.ID, Status: model.ExportRunning, Name: "orphan"}); err != nil {
		t.Fatalf("seed job: %v", err)
	}

	// simulate a restart: constructing a fresh Services reconciles orphans
	_ = service.New(repo, app.svc.Engine, app.svc.JWT)

	jobs, _ := repo.ListExportJobs(u.ID, 0)
	found := false
	for _, j := range jobs {
		if j.Name == "orphan" {
			found = true
			if j.Status != model.ExportFailed {
				t.Errorf("orphaned job should be failed after restart, got %q", j.Status)
			}
		}
	}
	if !found {
		t.Fatal("orphan job not found")
	}
}
