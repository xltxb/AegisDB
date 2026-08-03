package bootstrap

import (
	"encoding/json"
	"net/http"
	"testing"
)

// apWithSteps mirrors the GET /approvals view: an approval plus its chain steps.
type apWithSteps struct {
	ID    int64  `json:"id"`
	ApNo  string `json:"apNo"`
	Steps []struct {
		StepOrder int     `json:"stepOrder"`
		Status    string `json:"status"`
		ActedAt   *string `json:"actedAt"`
	} `json:"steps"`
}

// approvalByNo fetches GET /approvals and returns the approval matching apNo.
func (a *testApp) approvalByNo(token, apNo string) apWithSteps {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/approvals", token, nil)
	// The listing is paged: unwrap the envelope before decoding the rows.
	var page struct {
		Items json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(r.Data, &page); err != nil {
		a.t.Fatalf("approvals envelope decode: %v", err)
	}
	if r.Code != 0 {
		a.t.Fatalf("list approvals: code=%d msg=%s", r.Code, r.Msg)
	}
	var aps []apWithSteps
	if err := json.Unmarshal(page.Items, &aps); err != nil {
		a.t.Fatalf("approvals decode: %v", err)
	}
	for _, ap := range aps {
		if ap.ApNo == apNo {
			return ap
		}
	}
	a.t.Fatalf("approval %q not found in list", apNo)
	return apWithSteps{}
}

// submitProdHighRisk intercepts a PROD high-risk command and returns the new
// approval as seen on the chain, asserting it starts with an active step.
func (a *testApp) submitProdHighRisk(token string) apWithSteps {
	a.t.Helper()
	prodConn := a.connIDByEnv(token, "prod")
	r := a.do(http.MethodPost, "/api/v1/terminal/exec", token, map[string]any{
		"connectionId": prodConn,
		"sql":          "DROP TABLE orders;",
		"reason":       "drop legacy table",
	})
	var data struct {
		ApprovalNo string `json:"approvalNo"`
	}
	if err := json.Unmarshal(r.Data, &data); err != nil {
		a.t.Fatalf("decode exec data: %v", err)
	}
	if data.ApprovalNo == "" {
		a.t.Fatalf("expected an approval number from PROD high-risk exec")
	}
	return a.approvalByNo(token, data.ApprovalNo)
}

// FR-APPR / US#20: approving a ticket must advance its chain — the active step
// records the decision (status=approved) and stamps actedAt. Observable purely
// through GET /approvals.
func TestApproval_ApproveMarksActiveStepApproved(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	owner := app.login("zhangwei@vela.io", "vela123") // chain member (approver)

	ap := app.submitProdHighRisk(token)
	if len(ap.Steps) == 0 {
		t.Fatal("approval created with no chain steps")
	}
	// precondition: the first step is active and not yet acted.
	if ap.Steps[0].Status != "active" {
		t.Fatalf("precondition: want first step active, got %q", ap.Steps[0].Status)
	}

	r := app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/approve", owner, nil)
	eq(t, r.Code, 0, "approve response code")

	got := app.approvalByNo(token, ap.ApNo)
	for _, s := range got.Steps {
		if s.Status == "active" {
			t.Errorf("step %d still active after approval", s.StepOrder)
		}
	}
	acted := got.Steps[0]
	eq(t, acted.Status, "approved", "active step status after approval")
	if acted.ActedAt == nil {
		t.Error("expected actedAt to be stamped on the approved step")
	}
}

// Mirror of the approve path: rejecting a ticket records the decision on the
// active step (status=rejected) and stamps actedAt.
func TestApproval_RejectMarksActiveStepRejected(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	owner := app.login("zhangwei@vela.io", "vela123") // chain member (approver)

	ap := app.submitProdHighRisk(token)
	if len(ap.Steps) == 0 {
		t.Fatal("approval created with no chain steps")
	}

	r := app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/reject", owner, nil)
	eq(t, r.Code, 0, "reject response code")

	got := app.approvalByNo(token, ap.ApNo)
	for _, s := range got.Steps {
		if s.Status == "active" {
			t.Errorf("step %d still active after rejection", s.StepOrder)
		}
	}
	acted := got.Steps[0]
	eq(t, acted.Status, "rejected", "active step status after rejection")
	if acted.ActedAt == nil {
		t.Error("expected actedAt to be stamped on the rejected step")
	}
}

// itoa avoids pulling strconv into the test for a single conversion.
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
