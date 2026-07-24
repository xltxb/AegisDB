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
