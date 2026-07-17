package bootstrap

import (
	"encoding/json"
	"net/http"
	"testing"
)

// seesApproval reports whether apNo appears in the user's visible approval list.
func (a *testApp) seesApproval(token, apNo string) bool {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/approvals?scope=all", token, nil)
	var aps []struct {
		ApNo string `json:"apNo"`
	}
	_ = json.Unmarshal(r.Data, &aps)
	for _, x := range aps {
		if x.ApNo == apNo {
			return true
		}
	}
	return false
}

// An approval ticket is only visible to its initiator and the members on its
// approval chain — nobody else can see other people's approvals.
func TestApproval_VisibleOnlyToInitiatorAndChain(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")    // initiator
	owner := app.login("zhangwei@vela.io", "vela123")  // chain member (approver)
	l2 := app.login("chenhao@vela.io", "vela123")      // neither
	wangmin := app.login("wangmin@vela.io", "vela123") // admin role, but neither party

	ap := app.submitProdHighRisk(admin) // initiator = Lin Wei, chain = owner members

	eq(t, app.seesApproval(admin, ap.ApNo), true, "initiator sees own approval")
	eq(t, app.seesApproval(owner, ap.ApNo), true, "chain approver sees approval")
	eq(t, app.seesApproval(l2, ap.ApNo), false, "unrelated user (L2) must not see it")
	eq(t, app.seesApproval(wangmin, ap.ApNo), false, "unrelated admin must not see it")
}
