package bootstrap

// 执行变更必须由人为确认点击后才落库(execute 阶段的人工闸)。
//
// 审批回答"这个变更可不可以做",执行确认回答"现在做"——两个不同的问题。
// 变更窗口、业务低峰、上下游就绪,这些只有到点的人知道;一张审批通过的单
// 在队列里自动落库,等于把"何时执行"交给了调度器。规则:execute 阶段到达
// 即停(waiting),由创建者本人或审批角色点击确认后执行;确认动作入审计链。

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// confirmStage clicks the confirm button on a waiting stage.
func (a *testApp) confirmStage(token string, relID, stageID int64) apiResp {
	a.t.Helper()
	return a.do(http.MethodPost,
		"/api/v1/releases/"+itoa(relID)+"/stages/"+itoa(stageID)+"/continue", token, nil)
}

func TestExecuteStageWaitsForHumanConfirmation(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	stg := app.connIDByEnv(admin, "staging")
	pid := app.createPipeline(admin, "执行闸流程", "", []map[string]any{
		{"name": "执行变更", "type": "execute"},
	})
	r := app.submitRelease(admin, map[string]any{
		"title": "待确认执行", "pipelineId": pid, "connectionId": stg,
		"sql": "SELECT 1;", "reason": "执行闸回归",
	})
	eq(t, r.Code, 0, "submit")
	var rel releaseView
	_ = json.Unmarshal(r.Data, &rel)

	// 到达 execute 即停 —— 不点不执行。
	waiting := app.waitRelease(admin, rel.ID, "waiting", "success", "failed")
	eq(t, waiting.Status, "waiting", "the run parks at the execute gate")
	ex := stageByType(waiting, "execute")
	eq(t, ex.Status, "waiting", "execute stage waits for a human")
	if !strings.Contains(ex.Log, "确认") {
		t.Errorf("the stage log should say it waits for confirmation, got %q", ex.Log)
	}

	// 创建者点击确认 → 真正执行到完成。
	c := app.confirmStage(admin, rel.ID, ex.ID)
	eq(t, c.Code, 0, "creator confirms")
	done := app.waitRelease(admin, rel.ID, "success", "failed")
	eq(t, done.Status, "success", "runs after the click")
	eq(t, stageByType(done, "execute").Status, "success", "execute completed")

	// 确认动作入链:actor = 变更归属人,operator = 点击的人,pending = 放行非执行。
	var rows []struct {
		Command  string `json:"command"`
		Actor    string `json:"actor"`
		Operator string `json:"operator"`
		Result   string `json:"result"`
	}
	_ = json.Unmarshal(app.auditItemsRaw(admin, "?range=today&pageSize=200"), &rows)
	found := false
	for _, row := range rows {
		if strings.Contains(row.Command, "RELEASE-EXECUTE-CONFIRM") {
			found = true
			eq(t, row.Actor, "Lin Wei", "actor is whose change it is")
			eq(t, row.Result, "pending", "a green light, not an execution")
		}
	}
	if !found {
		t.Error("no audit row for the execute confirmation")
	}
}

// TestExecuteConfirmationAuthz — 谁能点:创建者本人(审批已由别人把关,执行
// 时机归发起人)或审批角色;无关人等不行。
func TestExecuteConfirmationAuthz(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	outsider := app.login("zhaolei@vela.io", "vela123") // 研发只读,非创建者非审批角色
	stg := app.connIDByEnv(admin, "staging")
	pid := app.createPipeline(admin, "执行闸授权流程", "", []map[string]any{
		{"name": "执行变更", "type": "execute"},
	})
	r := app.submitRelease(admin, map[string]any{
		"title": "越权确认", "pipelineId": pid, "connectionId": stg,
		"sql": "SELECT 1;", "reason": "执行闸回归",
	})
	var rel releaseView
	_ = json.Unmarshal(r.Data, &rel)
	waiting := app.waitRelease(admin, rel.ID, "waiting", "failed")
	ex := stageByType(waiting, "execute")

	bad := app.confirmStage(outsider, rel.ID, ex.ID)
	if bad.Code == 0 {
		t.Fatal("an unrelated user must not be able to trigger the execution")
	}
	still := app.getRelease(admin, rel.ID)
	eq(t, stageByType(still, "execute").Status, "waiting", "still parked after the refusal")
}

// TestAbortWhileAwaitingExecution — 停在执行闸的单可以终止,execute 标 skipped。
func TestAbortWhileAwaitingExecution(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	stg := app.connIDByEnv(admin, "staging")
	pid := app.createPipeline(admin, "执行闸终止流程", "", []map[string]any{
		{"name": "执行变更", "type": "execute"},
	})
	r := app.submitRelease(admin, map[string]any{
		"title": "确认前终止", "pipelineId": pid, "connectionId": stg,
		"sql": "SELECT 1;", "reason": "执行闸回归",
	})
	var rel releaseView
	_ = json.Unmarshal(r.Data, &rel)
	app.waitRelease(admin, rel.ID, "waiting", "failed")

	ab := app.do(http.MethodPost, "/api/v1/releases/"+itoa(rel.ID)+"/abort", admin, nil)
	eq(t, ab.Code, 0, "abort while awaiting execution")
	done := app.getRelease(admin, rel.ID)
	eq(t, done.Status, "aborted", "aborted")
	eq(t, stageByType(done, "execute").Status, "skipped", "never executed, says so")
}

// confirmExecutionAndWait waits for the run to park at the execute gate, clicks
// it with the given token (creator or approver), and waits for a terminal state.
func (a *testApp) confirmExecutionAndWait(token string, relID int64) releaseView {
	a.t.Helper()
	v := a.waitRelease(token, relID, "waiting", "success", "failed")
	if v.Status == "waiting" {
		if ex := stageByType(v, "execute"); ex != nil && ex.Status == "waiting" {
			if r := a.confirmStage(token, relID, ex.ID); r.Code != 0 {
				a.t.Fatalf("confirm execute: code=%d msg=%s", r.Code, r.Msg)
			}
		}
	}
	return a.waitRelease(token, relID, "success", "failed")
}

// confirmOpenReleaseAndWait is the console-side click for an EXTERNALLY raised
// ticket: the service account cannot log in, so an approver/admin confirms in
// the console. Looks the release up by its number.
func (a *testApp) confirmOpenReleaseAndWait(adminToken, relNo string) releaseView {
	a.t.Helper()
	deadline := 200
	for i := 0; i < deadline; i++ {
		r := a.do(http.MethodGet, "/api/v1/releases?scope=all&pageSize=100", adminToken, nil)
		var page struct {
			Items []releaseView `json:"items"`
		}
		_ = json.Unmarshal(r.Data, &page)
		for _, it := range page.Items {
			if it.RelNo == relNo {
				return a.confirmExecutionAndWait(adminToken, it.ID)
			}
		}
	}
	a.t.Fatalf("release %s not found in console listing", relNo)
	return releaseView{}
}
