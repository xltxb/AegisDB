package bootstrap

// 人工确认门要进审计链(审查修复 #5)。
//
// The manual gate is a DECISION: someone with an approver role waves a parked
// release onward. Every other decision in this gateway lands in the audit
// chain with the house semantics — actor = whose change it is, operator = who
// decided (finalizeApproval writes exactly that shape). The gate used to leave
// its trace only in the stage log, which is neither hash-chained nor part of
// what an auditor reads.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestManualGateConfirmationIsAudited(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	confirmer := app.login("zhangwei@vela.io", "vela123") // approver role, not the creator

	stg := app.connIDByEnv(admin, "staging")
	pid := app.createPipeline(admin, "人工门流程", "", []map[string]any{
		{"name": "人工确认", "type": "manual"},
		{"name": "执行变更", "type": "execute"},
	})
	r := app.submitRelease(admin, map[string]any{
		"title": "带人工门的发布", "pipelineId": pid, "connectionId": stg,
		"sql": "SELECT 1;", "reason": "回归 #5",
	})
	eq(t, r.Code, 0, "submit release")
	var rel releaseView
	_ = json.Unmarshal(r.Data, &rel)

	waiting := app.waitRelease(admin, rel.ID, "waiting", "failed", "success")
	eq(t, waiting.Status, "waiting", "release parks on the manual gate")
	gate := stageByType(waiting, "manual")

	dec := app.do(http.MethodPost,
		"/api/v1/releases/"+itoa(rel.ID)+"/stages/"+itoa(gate.ID)+"/continue", confirmer, nil)
	eq(t, dec.Code, 0, "confirm the gate")

	// 0024 执行闸:到点的人点击确认后才落库
	done := app.confirmExecutionAndWait(admin, rel.ID)
	eq(t, done.Status, "success", "release resumes after the confirmation")

	// The decision is on the chain, in the same shape as an approval decision:
	// actor = the release's creator (whose change it is), operator = who waved
	// it through, result = pending (a green light, not an execution).
	var rows []struct {
		Command  string `json:"command"`
		Actor    string `json:"actor"`
		Operator string `json:"operator"`
		Result   string `json:"result"`
		RefNo    string `json:"approvalNo"`
	}
	_ = json.Unmarshal(app.auditItemsRaw(admin, "?range=today&pageSize=200"), &rows)
	found := false
	for _, row := range rows {
		if !strings.Contains(row.Command, "RELEASE-CONTINUE") {
			continue
		}
		found = true
		eq(t, row.Actor, "Lin Wei", "actor is whose change it is")
		if !strings.Contains(row.Operator, "Zhang Wei") {
			t.Errorf("operator should name who decided, got %q", row.Operator)
		}
		eq(t, row.Result, "pending", "a green light, not an execution")
		if !strings.Contains(row.Command, rel.RelNo) {
			t.Errorf("the audit line should name the release, got %q", row.Command)
		}
	}
	if !found {
		t.Error("no audit row for the manual-gate confirmation")
	}
}
