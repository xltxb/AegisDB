package bootstrap

import (
	"net/http"
	"strings"
	"testing"

	"velagateway/internal/model"
)

// When an approved command's target connection has been deleted, the gateway
// must NOT report it as executed — nothing ran. Regression guard for the
// conn==nil path that used to record "executed" + notify "已通过并执行" (V2).
func TestApproval_DeletedTargetConnReportedNotExecuted(t *testing.T) {
	app := newTestApp(t)
	initiator := app.login("linwei@vela.io", "vela123") // admin: not in the owner chain
	prod := app.connIDByEnv(initiator, "prod")
	ap := app.submitProdHighRisk(initiator)

	// the target connection disappears before the approver acts
	if err := app.svc.Repo.DB().Where("id = ?", prod).Delete(&model.Connection{}).Error; err != nil {
		t.Fatalf("delete connection: %v", err)
	}

	approver := app.login("zhangwei@vela.io", "vela123") // owner: chain member
	r := app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/approve", approver, nil)
	eq(t, r.Code, 0, "decision is recorded")
	if !strings.Contains(string(r.Data), "未执行") {
		t.Errorf("approval against a deleted connection must report non-execution, got: %s", r.Data)
	}
}
