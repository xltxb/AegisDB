package bootstrap

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

// The approval chain is the "owner" (DBA 负责人) role membership. When no owner is
// assigned (e.g. a freshly-seeded install), it must fall back to the platform
// admins so high-risk commands aren't created with an empty, un-approvable chain.
func TestApprovalChain_FallsBackToAdminWhenOwnerEmpty(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")

	// Resolve the owner role id.
	rr := app.do(http.MethodGet, "/api/v1/roles", token, nil)
	eq(t, rr.Code, 0, "list roles code")
	var roles []struct {
		ID   int64  `json:"id"`
		Code string `json:"code"`
	}
	_ = json.Unmarshal(rr.Data, &roles)
	var ownerID int64
	for _, r := range roles {
		if r.Code == "owner" {
			ownerID = r.ID
		}
	}
	if ownerID == 0 {
		t.Fatal("owner role not found")
	}

	// The seeded chain is the owner members — capture and remove them all.
	var chain struct {
		Chain []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"chain"`
	}
	cr := app.do(http.MethodGet, "/api/v1/approval-chain", token, nil)
	_ = json.Unmarshal(cr.Data, &chain)
	if len(chain.Chain) == 0 {
		t.Fatal("expected a seeded owner chain to start from")
	}
	// Owner is these users' only role, and a user may not be left with none (that
	// would leave role_id dangling), so give them a second role before revoking
	// owner — the same order an administrator has to follow in the UI.
	roID := app.roleIDByCode(token, "ro")
	for _, m := range chain.Chain {
		add := app.do(http.MethodPost, fmt.Sprintf("/api/v1/roles/%d/members", roID), token, map[string]any{"userId": m.ID})
		eq(t, add.Code, 0, "grant replacement role")
		d := app.do(http.MethodDelete, fmt.Sprintf("/api/v1/roles/%d/members/%d", ownerID, m.ID), token, nil)
		eq(t, d.Code, 0, "remove owner member")
	}

	// With the owner role empty, the chain must fall back to the admins.
	var after struct {
		Chain []struct {
			Name string `json:"name"`
		} `json:"chain"`
	}
	cr2 := app.do(http.MethodGet, "/api/v1/approval-chain", token, nil)
	_ = json.Unmarshal(cr2.Data, &after)
	if len(after.Chain) == 0 {
		t.Fatal("chain should fall back to admins, got empty")
	}
	found := false
	for _, m := range after.Chain {
		if m.Name == "Lin Wei" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected admin fallback chain to include Lin Wei, got %+v", after.Chain)
	}
}
