package bootstrap

// 人工确认 / 执行确认也能指定角色。
//
// 审批节点解决了"谁来批",这两个门此前仍然只看角色属性(CanApprove 或 admin),
// 也就是说:任何有审批能力的人都能推动任何一条流程的人工节点。变更窗口由谁把
// 关、上线由谁点头,这些是按流程分的,不是按"有没有审批权限"分的。
//
// 与审批节点同一套语义:不配 = 保持原行为;配了就以角色为准;角色里没有能点的
// 人(空角色,或只剩服务账号)时,阶段直接失败 —— 而不是永远停在那儿等一个不会
// 到来的点击。

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// 人工确认:配了角色就以角色为准,有审批权但不在角色里的人也不能点。
func TestManualGateRespectsConfiguredRole(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	owner := app.login("zhangwei@vela.io", "vela123") // 有审批权,但不是 l2
	l2 := app.login("chenhao@vela.io", "vela123")     // 本流程指定的确认角色
	stg := app.connIDByEnv(admin, "staging")

	pid := app.createPipeline(admin, "指定确认角色", "", []map[string]any{
		{"name": "人工确认", "type": "manual", "config": `{"confirmRole":"l2"}`},
		{"name": "执行变更", "type": "execute"},
	})
	r := app.submitRelease(admin, map[string]any{
		"title": "人工门按角色", "pipelineId": pid, "connectionId": stg,
		"sql": "SELECT 1;", "reason": "确认角色回归",
	})
	eq(t, r.Code, 0, "submit")
	var rel releaseView
	_ = json.Unmarshal(r.Data, &rel)

	waiting := app.waitRelease(admin, rel.ID, "waiting", "failed", "success")
	gate := stageByType(waiting, "manual")
	if gate == nil || gate.Status != "waiting" {
		t.Fatalf("manual gate should be waiting, got %+v", gate)
	}

	no := app.confirmStage(owner, rel.ID, gate.ID)
	if no.Code == 0 {
		t.Error("holding the approve capability is not enough when a role is configured")
	}
	yes := app.confirmStage(l2, rel.ID, gate.ID)
	eq(t, yes.Code, 0, "the configured role's member may continue")
}

// 执行确认:配了角色,连发起人本人也要在角色里才能点。
//
// 默认允许发起人是因为"何时执行"归发起人;一旦运维显式指定了角色,那正是要把
// 这个决定收走(比如变更窗口由 DBA 负责人统一把关),所以配置优先。
func TestExecuteGateRespectsConfiguredRole(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123") // 发起人,且是 admin
	owner := app.login("zhangwei@vela.io", "vela123")
	stg := app.connIDByEnv(admin, "staging")

	pid := app.createPipeline(admin, "指定执行确认角色", "", []map[string]any{
		{"name": "执行变更", "type": "execute", "config": `{"confirmRole":"owner"}`},
	})
	r := app.submitRelease(admin, map[string]any{
		"title": "执行闸按角色", "pipelineId": pid, "connectionId": stg,
		"sql": "SELECT 1;", "reason": "确认角色回归",
	})
	eq(t, r.Code, 0, "submit")
	var rel releaseView
	_ = json.Unmarshal(r.Data, &rel)

	waiting := app.waitRelease(admin, rel.ID, "waiting", "failed", "success")
	ex := stageByType(waiting, "execute")
	if ex == nil || ex.Status != "waiting" {
		t.Fatalf("execute gate should be waiting, got %+v", ex)
	}

	no := app.confirmStage(admin, rel.ID, ex.ID)
	if no.Code == 0 {
		t.Error("the creator must not confirm when the flow routes this decision to a role")
	}
	yes := app.confirmStage(owner, rel.ID, ex.ID)
	eq(t, yes.Code, 0, "the configured role's member confirms")
	done := app.waitRelease(admin, rel.ID, "success", "failed")
	eq(t, done.Status, "success", "runs after the configured role confirms")
}

// 角色里没有能点的人时,阶段要失败而不是永远停着 —— 与审批节点同一条原则。
func TestGateWithUnstaffedRoleFailsInsteadOfHanging(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	stg := app.connIDByEnv(admin, "staging")

	// 清空 audit,只放一个服务账号 —— 它登录不了控制台,点不了任何东西。
	auditID := app.roleIDByCode(admin, "audit")
	roID := app.roleIDByCode(admin, "ro")
	for _, m := range app.roleMembers(admin, auditID) {
		app.do(http.MethodPost, "/api/v1/roles/"+itoa(roID)+"/members", admin, map[string]any{"userId": m})
		app.do(http.MethodDelete, "/api/v1/roles/"+itoa(auditID)+"/members/"+itoa(m), admin, nil)
	}
	app.createServiceAccount(admin, "点不了按钮的机器人", []int64{auditID}, nil)

	pid := app.createPipeline(admin, "无人可确认", "", []map[string]any{
		{"name": "人工确认", "type": "manual", "config": `{"confirmRole":"audit"}`},
	})
	r := app.submitRelease(admin, map[string]any{
		"title": "没人能点", "pipelineId": pid, "connectionId": stg,
		"sql": "SELECT 1;", "reason": "确认角色回归",
	})
	eq(t, r.Code, 0, "submit")
	var rel releaseView
	_ = json.Unmarshal(r.Data, &rel)

	done := app.waitRelease(admin, rel.ID, "failed", "waiting", "success")
	eq(t, done.Status, "failed", "a gate nobody can pass must fail, not hang forever")
	if g := stageByType(done, "manual"); !strings.Contains(g.Log, "服务账号") && !strings.Contains(g.Log, "audit") {
		t.Errorf("the failure should explain who was expected, got %q", g.Log)
	}
}

// 保存流程时就拦住不存在的确认角色。
func TestConfirmRoleValidatedAtSaveTime(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")

	for _, st := range []map[string]any{
		{"name": "人工确认", "type": "manual", "config": `{"confirmRole":"nope"}`},
		{"name": "执行变更", "type": "execute", "config": `{"confirmRole":"nope"}`},
	} {
		r := app.do(http.MethodPost, "/api/v1/pipelines", admin, map[string]any{
			"name": "错确认角色-" + st["type"].(string), "enabled": true,
			"stages": []map[string]any{st},
		})
		if r.Code == 0 {
			t.Errorf("%s: an unknown confirm role must be refused at save time", st["type"])
			continue
		}
		if !strings.Contains(r.Msg, "nope") {
			t.Errorf("%s: the refusal should name the role, got %q", st["type"], r.Msg)
		}
	}
}
