package bootstrap

// 变更发布要能看出「执行变更人」—— 点「确认执行」让变更落库的那个人。
//
// 它不一定是发起人:审批角色也能点(见 release_exec_confirm_test)。发布页把执行阶段的
// confirmedBy 显示成执行人,这里钉住后端在详情和列表两处都把它带出来,且是点击的人
// 而不是发起人。

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestReleaseExposesExecutor(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	l2 := app.login("chenhao@vela.io", "vela123") // 审批角色,不是发起人
	stg := app.connIDByEnv(admin, "staging")
	pid := app.createPipeline(admin, "执行人流程", "", []map[string]any{
		{"name": "执行变更", "type": "execute", "config": `{"confirmRole":"l2"}`},
	})
	r := app.submitRelease(admin, map[string]any{
		"title": "看执行人", "pipelineId": pid, "connectionId": stg,
		"sql": "SELECT 1;", "reason": "执行人回归",
	})
	eq(t, r.Code, 0, "submit")
	var rel releaseView
	_ = json.Unmarshal(r.Data, &rel)

	waiting := app.waitRelease(admin, rel.ID, "waiting", "success", "failed")
	ex := stageByType(waiting, "execute")
	eq(t, ex.ConfirmedBy, "", "nobody has executed it yet")

	eq(t, app.confirmStage(l2, rel.ID, ex.ID).Code, 0, "approver confirms execution")
	done := app.waitRelease(admin, rel.ID, "success", "failed")
	eq(t, done.Status, "success", "ran after the click")
	eq(t, stageByType(done, "execute").ConfirmedBy, "Chen Hao", "detail names who clicked, not the creator")

	lr := app.do(http.MethodGet, "/api/v1/releases?scope=all&pageSize=100", admin, nil)
	eq(t, lr.Code, 0, "list")
	var page struct {
		Items []releaseView `json:"items"`
	}
	_ = json.Unmarshal(lr.Data, &page)
	found := false
	for _, it := range page.Items {
		if it.ID == rel.ID {
			found = true
			eq(t, stageByType(it, "execute").ConfirmedBy, "Chen Hao", "list carries the executor too")
		}
	}
	if !found {
		t.Fatal("release missing from list")
	}
}
