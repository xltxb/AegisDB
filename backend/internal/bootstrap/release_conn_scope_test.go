package bootstrap

import (
	"encoding/json"
	"net/http"
	"testing"
)

// L-AUTH2(2026-10 安全审计)给发布单的人工闸确认、终止都加了标签授权;2026-10-26 回归 04
// 收回了其中的人工确认闸:它语义上是审批,与审批单「批准」同口径,放行不要求实例标签,
// 而且这一步要重新出现在确认人的待办里。执行阶段(真正碰库)仍要求够得到目标实例。
//
// 原 TestRelease_GateAndAbortRequireInstanceAccess 断言"没标签的确认人看不到 / 放行不了
// 人工闸、也终止不了",按新结论改写成下面两条。

// releaseNarrowedL2 把 chenhao(l2)的标签收窄到 analytics:staging 实例他够不到了。
func releaseNarrowedL2(app *testApp, admin string) string {
	app.t.Helper()
	u, _ := app.repo.GetUserByEmail("chenhao@aegisdb.io")
	eq(app.t, app.do(http.MethodPut, "/api/v1/users/"+itoa(u.ID)+"/tags", admin, map[string]any{"tags": []string{"analytics"}}).Code, 0, "收窄标签")
	return app.login("chenhao@aegisdb.io", "aegisdb123")
}

func TestRelease_ManualGateIgnoresInstanceTags(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	conn := app.connIDByEnv(admin, "staging")
	l2 := releaseNarrowedL2(app, admin) // 本流程的确认角色,但够不到 staging

	pid := app.createPipeline(admin, "无标签·人工闸", "", []map[string]any{
		{"name": "人工确认", "type": "manual", "config": `{"confirmRole":"l2"}`},
		{"name": "执行变更", "type": "execute", "config": `{"confirmRole":"l2"}`},
	})
	r := app.submitRelease(admin, map[string]any{
		"title": "无标签确认", "pipelineId": pid, "connectionId": conn, "sql": "SELECT 1;", "reason": "回归 04",
	})
	if r.Code != 0 {
		t.Fatalf("submit: %d %s", r.Code, r.Msg)
	}
	var rel releaseView
	_ = json.Unmarshal(r.Data, &rel)
	waiting := app.waitRelease(admin, rel.ID, "waiting", "failed", "success")
	gate := stageByType(waiting, "manual")
	if gate == nil || gate.Status != "waiting" {
		t.Fatalf("前置:人工闸应停在 waiting,实际 %+v", gate)
	}

	// 新行为:人工闸不看标签 —— 待办里有,能放行。
	if _, found := app.todoOf(l2).confirmOn(waiting.RelNo); !found {
		t.Error("确认角色里的人即使没有实例标签,待办里也应看到这道人工闸")
	}
	eq(t, app.do(http.MethodPost, "/api/v1/releases/"+itoa(rel.ID)+"/stages/"+itoa(gate.ID)+"/continue", l2, nil).Code, 0,
		"确认角色里的人即使没有实例标签,也应能放行人工闸")

	// 原行为保留:执行阶段仍要求够得到目标实例。
	atExec := app.waitRelease(admin, rel.ID, "waiting", "failed", "success")
	ex := stageByType(atExec, "execute")
	if ex == nil || ex.Status != "waiting" {
		t.Fatalf("前置:执行阶段应停在 waiting,实际 %+v", ex)
	}
	if _, found := app.todoOf(l2).confirmOn(atExec.RelNo); found {
		t.Error("够不到目标实例的人不应在待办里看到执行确认")
	}
	if c := app.do(http.MethodPost, "/api/v1/releases/"+itoa(rel.ID)+"/stages/"+itoa(ex.ID)+"/continue", l2, nil); c.Code == 0 {
		t.Error("够不到目标实例的人不应能确认执行")
	}
	eq(t, stageByType(app.getRelease(admin, rel.ID), "execute").Status, "waiting", "执行阶段仍在等")
}

// 终止一张还没开始执行的发布单与审批单「驳回」同口径,不要求标签;不在确认 / 审批角色里、
// 也不是发起人的人仍终止不了(角色判断照旧)。
func TestRelease_AbortPendingIgnoresInstanceTags(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	conn := app.connIDByEnv(admin, "staging")
	l2 := app.login("chenhao@aegisdb.io", "aegisdb123") // l2 没有审批资格
	// owner(有审批资格)标签收窄到 analytics:staging 实例他够不到了
	ow, _ := app.repo.GetUserByEmail("zhangwei@aegisdb.io")
	eq(t, app.do(http.MethodPut, "/api/v1/users/"+itoa(ow.ID)+"/tags", admin, map[string]any{"tags": []string{"analytics"}}).Code, 0, "收窄标签")
	owner := app.login("zhangwei@aegisdb.io", "aegisdb123")

	pid := app.createPipeline(admin, "无标签·终止", "", []map[string]any{
		{"name": "人工确认", "type": "manual", "config": `{"confirmRole":"l2"}`},
		{"name": "执行变更", "type": "execute"},
	})
	r := app.submitRelease(admin, map[string]any{
		"title": "无标签终止", "pipelineId": pid, "connectionId": conn, "sql": "SELECT 1;", "reason": "回归 04",
	})
	if r.Code != 0 {
		t.Fatalf("submit: %d %s", r.Code, r.Msg)
	}
	var rel releaseView
	_ = json.Unmarshal(r.Data, &rel)
	app.waitRelease(admin, rel.ID, "waiting", "failed", "success")

	// 原行为保留:既不是发起人、也没有审批资格的人终止不了。
	if ab := app.do(http.MethodPost, "/api/v1/releases/"+itoa(rel.ID)+"/abort", l2, nil); ab.Code == 0 {
		t.Error("没有审批资格的非发起人不应能终止发布单")
	}
	eq(t, app.do(http.MethodPost, "/api/v1/releases/"+itoa(rel.ID)+"/abort", owner, nil).Code, 0,
		"有审批资格的人终止未执行的发布单不要求实例标签")
}
