package bootstrap

import (
	"encoding/json"
	"net/http"
	"testing"
)

// approvalItems unwraps the paged listing envelope {items,total,page,pageSize}
// so a test can decode the rows.
func approvalItems(t *testing.T, data json.RawMessage) json.RawMessage {
	t.Helper()
	var page struct {
		Items json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(data, &page); err != nil {
		t.Fatalf("approvals envelope decode: %v (%s)", err, string(data))
	}
	return page.Items
}

// seesApproval reports whether apNo appears in the user's visible approval list.
func (a *testApp) seesApproval(token, apNo string) bool {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/approvals?scope=all&pageSize=500", token, nil)
	var aps []struct {
		ApNo string `json:"apNo"`
	}
	_ = json.Unmarshal(approvalItems(a.t, r.Data), &aps)
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

// ED13: the approval list had no LIMIT. It was tolerable while the page rendered
// a handful of cards, but the list view invites long histories, and every request
// also pulled the caller's entire approver-step set into memory to build an
// `IN (...)`. Both grow without bound.
func TestApprovals_ArePaged(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")

	// Enough tickets to need more than one page.
	for i := 0; i < 7; i++ {
		app.submitProdHighRisk(token)
	}
	first := app.approvalPage(token, "?scope=all&page=1&pageSize=3")
	eq(t, len(first.Items), 3, "page size honoured")
	if first.Total < 7 {
		t.Errorf("total = %d, want at least the 7 submitted", first.Total)
	}
	second := app.approvalPage(token, "?scope=all&page=2&pageSize=3")
	eq(t, len(second.Items), 3, "second page")
	if first.Items[0].ApNo == second.Items[0].ApNo {
		t.Error("page 2 repeated page 1")
	}
	// The pending badge must count every pending ticket the caller can see, not
	// merely those on the page in hand — otherwise it silently caps at page size.
	if first.Pending < 7 {
		t.Errorf("pending count = %d, want at least the 7 submitted (it must not be capped by the page)", first.Pending)
	}

	// An absurd page size must be clamped rather than honoured.
	big := app.approvalPage(token, "?scope=all&page=1&pageSize=100000")
	if len(big.Items) > 500 {
		t.Errorf("page size was not clamped: got %d rows", len(big.Items))
	}
}

// The audit log links a ticket by number. With pagination that ticket may sit on
// any page, so resolving it must not depend on the caller happening to be on the
// right one — otherwise the link silently fails exactly as it did before paging.
func TestApprovals_LookupByTicketNumberIgnoresPaging(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")

	target := app.submitProdHighRisk(token) // oldest of the batch
	for i := 0; i < 5; i++ {
		app.submitProdHighRisk(token)
	}
	// It is not on the first page any more…
	first := app.approvalPage(token, "?scope=all&page=1&pageSize=3")
	for _, a := range first.Items {
		if a.ApNo == target.ApNo {
			t.Fatalf("precondition: %s should have been pushed off page 1", target.ApNo)
		}
	}
	// …but asking for it by number still finds it.
	byNo := app.approvalPage(token, "?ap="+target.ApNo)
	eq(t, len(byNo.Items), 1, "lookup by ticket number")
	eq(t, byNo.Items[0].ApNo, target.ApNo, "the right ticket")
}

type approvalPageResp struct {
	Pending int64 `json:"pending"`
	Items []struct {
		ID     int64  `json:"id"`
		ApNo   string `json:"apNo"`
		Status string `json:"status"`
	} `json:"items"`
	Total int64 `json:"total"`
}

func (a *testApp) approvalPage(token, query string) approvalPageResp {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/approvals"+query, token, nil)
	eq(a.t, r.Code, 0, "list approvals")
	var out approvalPageResp
	if err := json.Unmarshal(r.Data, &out); err != nil {
		a.t.Fatalf("approval page decode: %v (%s)", err, string(r.Data))
	}
	return out
}
