package bootstrap

import (
	"encoding/json"
	"net/http"
	"testing"
)

// After an approval is approved, the gateway executes the command and returns
// the execution result — both in the approve response and persisted on the
// ticket (visible in the approval list).
func TestApproval_ApproveReturnsExecutionResult(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	owner := app.login("zhangwei@vela.io", "vela123") // chain member (approver)
	ap := app.submitProdHighRisk(token)

	// approve (as a chain member) → response carries the execution result
	r := app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/approve", owner, nil)
	eq(t, r.Code, 0, "approve code")
	var resp struct {
		Status string `json:"status"`
		Result struct {
			Output string `json:"output"`
			Rows   int    `json:"rows"`
		} `json:"result"`
	}
	if err := json.Unmarshal(r.Data, &resp); err != nil {
		t.Fatalf("decode approve response: %v", err)
	}
	eq(t, resp.Status, "approved", "status")
	if resp.Result.Output == "" {
		t.Errorf("expected an execution result output in the approve response, got %+v", resp.Result)
	}

	// and it is persisted on the ticket for later viewing
	full := app.approvalFull(token, ap.ApNo)
	if full.Result == "" {
		t.Errorf("approval should persist its execution result, got empty")
	}
	if full.DecidedAt == nil {
		t.Error("approval should stamp decidedAt")
	}
}

type apFull struct {
	ApNo      string  `json:"apNo"`
	Result    string  `json:"result"`
	Status    string  `json:"status"`
	DecidedAt *string `json:"decidedAt"`
}

func (a *testApp) approvalFull(token, apNo string) apFull {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/approvals", token, nil)
	var aps []apFull
	_ = json.Unmarshal(r.Data, &aps)
	for _, ap := range aps {
		if ap.ApNo == apNo {
			return ap
		}
	}
	a.t.Fatalf("approval %q not found", apNo)
	return apFull{}
}
