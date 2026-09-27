package bootstrap

import (
	"encoding/json"
	"net/http"
)

// apStatusRow mirrors the GET /approvals view fields the suite asserts on.
type apStatusRow struct {
	ApNo   string `json:"apNo"`
	Status string `json:"status"`
}

func (a *testApp) approvalRow(token, apNo string) apStatusRow {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/approvals", token, nil)
	eq(a.t, r.Code, 0, "list approvals code")
	// The listing is paged: unwrap the envelope before decoding the rows.
	var page struct {
		Items json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(r.Data, &page); err != nil {
		a.t.Fatalf("approvals envelope decode: %v", err)
	}
	var aps []apStatusRow
	_ = json.Unmarshal(page.Items, &aps)
	for _, ap := range aps {
		if ap.ApNo == apNo {
			return ap
		}
	}
	a.t.Fatalf("approval %q not found", apNo)
	return apStatusRow{}
}

func (a *testApp) setSettings(token string, kv map[string]any) {
	a.t.Helper()
	eq(a.t, a.do(http.MethodPut, "/api/v1/settings", token, kv).Code, 0, "save settings")
}

// approvalIDByNo resolves an ApNo to its row id for direct-store assertions.
func (a *testApp) approvalIDByNo(apNo string) int64 {
	a.t.Helper()
	ap, err := a.repo.GetApprovalByApNo(apNo)
	if err != nil || ap == nil {
		a.t.Fatalf("approval %q not found: %v", apNo, err)
	}
	return ap.ID
}
