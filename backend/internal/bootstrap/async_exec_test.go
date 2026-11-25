package bootstrap

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"velagateway/pkg/resp"
)

// A long-running SQL submitted for background execution returns a jobId
// immediately (decoupled from the HTTP request), then the worker runs it and the
// job reaches a terminal state with a streamed log — the poll model that lets a
// 30–60min procedure finish without a request timeout.
func TestAsyncExec_SubmitPollDone(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	dev := app.connIDByEnv(token, "dev")

	r := app.do(http.MethodPost, "/api/v1/terminal/exec-async", token, map[string]any{
		"connectionId": dev, "sql": "SELECT run_long_proc()", "reason": "nightly job",
	})
	eq(t, r.Code, 0, "submit async")
	var sub struct {
		JobID int64 `json:"jobId"`
	}
	_ = json.Unmarshal(r.Data, &sub)
	if sub.JobID == 0 {
		t.Fatalf("expected a jobId, got %s", r.Data)
	}

	var job struct {
		Status string `json:"status"`
		Log    string `json:"log"`
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		jr := app.do(http.MethodGet, fmt.Sprintf("/api/v1/async-jobs/%d", sub.JobID), token, nil)
		eq(t, jr.Code, 0, "get async job")
		_ = json.Unmarshal(jr.Data, &job)
		if job.Status == "done" || job.Status == "failed" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	eq(t, job.Status, "done", "async job completes")
	if job.Log == "" {
		t.Error("expected a streamed/completion log")
	}

	// The job shows up in the caller's list.
	lr := app.do(http.MethodGet, "/api/v1/async-jobs", token, nil)
	eq(t, lr.Code, 0, "list async jobs")
	var jobs []struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(lr.Data, &jobs)
	found := false
	for _, j := range jobs {
		if j.ID == sub.JobID {
			found = true
		}
	}
	if !found {
		t.Error("submitted job missing from the list")
	}
}

// Async submission runs the same three-layer gate: a PROD high-risk command is
// intercepted for approval (no background job is created) rather than run.
func TestAsyncExec_HighRiskGoesToApproval(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	prod := app.connIDByEnv(token, "prod")

	r := app.do(http.MethodPost, "/api/v1/terminal/exec-async", token, map[string]any{
		"connectionId": prod, "sql": "DROP TABLE orders;", "reason": "cleanup",
	})
	eq(t, r.Code, resp.CodeIntercepted, "high-risk async intercepted")
	var d struct {
		ApprovalNo string `json:"approvalNo"`
		JobID      int64  `json:"jobId"`
	}
	_ = json.Unmarshal(r.Data, &d)
	if !strings.HasPrefix(d.ApprovalNo, "AP-") {
		t.Errorf("expected an approval ticket, got %s", r.Data)
	}
	if d.JobID != 0 {
		t.Error("a gated command must NOT create a background job")
	}
}

// ER7: the async channel executes the same SQL as the terminal but audits it
// differently — the command was truncated to 80 characters and the risk was
// hardcoded to "mid" regardless of the verdict the engine actually reached. A
// long migration script is exactly the kind of statement people run here, so the
// audited text stopped mid-statement and could not show what really ran; and a
// low-risk statement was filed as mid while a high-risk one was ALSO filed as
// mid, making the field useless for filtering. The audit must carry the whole
// command and the verdict's own risk.
func TestAsyncExec_AuditsFullCommandAndRealRisk(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	dev := app.connIDByEnv(token, "dev")

	// Comfortably longer than the old 80-character clip, with a distinctive tail.
	longSQL := "SELECT 1 /* " + strings.Repeat("padding ", 20) + " */ AS marker_tail_visible"
	r := app.do(http.MethodPost, "/api/v1/terminal/exec-async", token, map[string]any{
		"connectionId": dev, "sql": longSQL, "reason": "long script",
	})
	eq(t, r.Code, 0, "submit async job")

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var rows []struct {
			Command string `json:"command"`
			Risk    string `json:"risk"`
		}
		_ = json.Unmarshal(app.auditItemsRaw(token, ""), &rows)
		for _, row := range rows {
			if strings.Contains(row.Command, "marker_tail_visible") {
				eq(t, row.Risk, "low", "async audit carries the verdict's own risk")
				return
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Error("no async audit row carrying the full command (it was truncated)")
}

// ER6: putting an instance into maintenance is how an operator freezes activity
// on it, and /terminal/exec honours that. The async channel executes against the
// same instance through the same executor but never checked the flag, so anyone
// blocked in the terminal could simply resubmit the identical statement as a
// background job and it would run.
func TestAsyncExec_RefusedWhileInstanceIsInMaintenance(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	dev := app.connIDByEnv(token, "dev")

	eq(t, app.do(http.MethodPatch, "/api/v1/connections/"+itoa(dev), token,
		map[string]any{"status": "maint"}).Code, 0, "put instance into maintenance")

	// The terminal refuses to run it (it reports the restriction rather than executing).
	sync := app.do(http.MethodPost, "/api/v1/terminal/exec", token, map[string]any{
		"connectionId": dev, "sql": "SELECT 1"})
	if !strings.Contains(string(sync.Data), "维护") {
		t.Fatalf("precondition: terminal should report the maintenance restriction, got %s", string(sync.Data))
	}

	// The async channel must not execute it either.
	r := app.do(http.MethodPost, "/api/v1/terminal/exec-async", token, map[string]any{
		"connectionId": dev, "sql": "SELECT 1", "reason": "bypass attempt"})
	if r.Code == 0 && !strings.Contains(string(r.Data), "维护") {
		t.Errorf("async execution was accepted on an instance in maintenance: code=%d data=%s", r.Code, string(r.Data))
	}
}
