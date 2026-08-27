package bootstrap

// 审批节点指定审批角色。
//
// 在此之前,审批人只有一个来源:全局默认链(owner 角色的全部成员,没有则回落
// admin)。这意味着生产发布和开发发布找的是同一批人,也无法按业务线分派。审批
// 节点的 config 里加一个 approverRole 就够了 —— 阶段配置本来就是自由 JSON。
//
// 最要紧的一条在最后一个用例:配了一个**没有成员**的角色,会造出一张谁都不在
// 链上的审批单。isChainMember 对空链返回 false,于是没有任何人能批它,发布单
// 永远停在 waiting。那不是"权限严格",那是死单 —— 必须在阶段就失败并说清楚。

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestApproveStageUsesConfiguredRole(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	owner := app.login("zhangwei@vela.io", "vela123") // owner —— 全局默认链上的人
	l2 := app.login("chenhao@vela.io", "vela123")     // l2 —— 本流程指定的审批角色
	stg := app.connIDByEnv(admin, "staging")

	pid := app.createPipeline(admin, "指定审批角色", "", []map[string]any{
		{"name": "人工审批", "type": "approve", "config": `{"approverRole":"l2"}`},
		{"name": "执行变更", "type": "execute"},
	})
	r := app.submitRelease(admin, map[string]any{
		"title": "按角色分派审批", "pipelineId": pid, "connectionId": stg,
		"sql": "SELECT 1;", "reason": "审批角色回归",
	})
	eq(t, r.Code, 0, "submit")
	var rel releaseView
	_ = json.Unmarshal(r.Data, &rel)

	waiting := app.waitRelease(admin, rel.ID, "waiting", "failed", "success")
	eq(t, waiting.Status, "waiting", "parks on the approval")
	ap := stageByType(waiting, "approve")
	if ap == nil || ap.ApprovalID == 0 {
		t.Fatalf("approve stage should carry a ticket, got %+v", ap)
	}

	// 全局默认链上的人(owner)不在这条链上 —— 配置说了算,不是"谁能批都能批"。
	no := app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ApprovalID)+"/approve", owner, nil)
	if no.Code == 0 {
		t.Error("a member of the GLOBAL chain must not decide a ticket routed to another role")
	}
	// 被指定角色的成员可以批。
	yes := app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ApprovalID)+"/approve", l2, nil)
	eq(t, yes.Code, 0, "the configured role's member decides it")
}

// 不配 approverRole 时行为不变:仍走全局默认链。
func TestApproveStageWithoutRoleKeepsGlobalChain(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	owner := app.login("zhangwei@vela.io", "vela123")
	stg := app.connIDByEnv(admin, "staging")

	pid := app.createPipeline(admin, "默认审批链", "", []map[string]any{
		{"name": "人工审批", "type": "approve"},
		{"name": "执行变更", "type": "execute"},
	})
	r := app.submitRelease(admin, map[string]any{
		"title": "默认链", "pipelineId": pid, "connectionId": stg,
		"sql": "SELECT 1;", "reason": "审批角色回归",
	})
	var rel releaseView
	_ = json.Unmarshal(r.Data, &rel)
	waiting := app.waitRelease(admin, rel.ID, "waiting", "failed", "success")
	ap := stageByType(waiting, "approve")
	dec := app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ApprovalID)+"/approve", owner, nil)
	eq(t, dec.Code, 0, "the global chain still decides when no role is configured")
}

// 保存流程时就要拦住不存在的角色 —— 否则错误要等到某次真实发布才暴露。
func TestApproveStageRejectsUnknownRoleAtSaveTime(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")

	r := app.do(http.MethodPost, "/api/v1/pipelines", admin, map[string]any{
		"name": "错角色流程", "enabled": true, "stages": []map[string]any{
			{"name": "人工审批", "type": "approve", "config": `{"approverRole":"nonexistent"}`},
		},
	})
	if r.Code == 0 {
		t.Fatal("an approver role that does not exist must be refused when the flow is saved")
	}
	if !strings.Contains(r.Msg, "nonexistent") {
		t.Errorf("the refusal should name the role, got %q", r.Msg)
	}
}

// 角色存在但**没有成员** —— 这是最危险的一种配置:审批单会建出来,而链上一个人
// 都没有,isChainMember 对谁都返回 false,发布单永远停在那儿。必须在阶段失败。
func TestApproveStageRefusesRoleWithNoMembers(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	stg := app.connIDByEnv(admin, "staging")

	// 把 audit 角色清空,造出"存在但没有成员"的状态。成员不止夹具里那一个
	// (产品种子还放了一个 Audit Bot),所以逐个移;移除前先给每人另一个角色 ——
	// 系统不允许摘掉用户的最后一个角色。
	auditID := app.roleIDByCode(admin, "audit")
	roID := app.roleIDByCode(admin, "ro")
	for _, m := range app.roleMembers(admin, auditID) {
		app.do(http.MethodPost, "/api/v1/roles/"+itoa(roID)+"/members", admin,
			map[string]any{"userId": m})
		eq(t, app.do(http.MethodDelete,
			"/api/v1/roles/"+itoa(auditID)+"/members/"+itoa(m), admin, nil).Code, 0, "remove audit member")
	}
	if n := len(app.roleMembers(admin, auditID)); n != 0 {
		t.Fatalf("audit role should be empty for this test, still has %d", n)
	}

	pid := app.createPipeline(admin, "空角色流程", "", []map[string]any{
		{"name": "人工审批", "type": "approve", "config": `{"approverRole":"audit"}`},
		{"name": "执行变更", "type": "execute"},
	})
	r := app.submitRelease(admin, map[string]any{
		"title": "空审批角色", "pipelineId": pid, "connectionId": stg,
		"sql": "SELECT 1;", "reason": "审批角色回归",
	})
	eq(t, r.Code, 0, "submit")
	var rel releaseView
	_ = json.Unmarshal(r.Data, &rel)

	done := app.waitRelease(admin, rel.ID, "failed", "waiting", "success")
	eq(t, done.Status, "failed", "a flow routed to an empty role must fail, not hang forever")
	ap := stageByType(done, "approve")
	eq(t, ap.Status, "failed", "the approve stage says why")
	if !strings.Contains(ap.Log, "没有成员") && !strings.Contains(ap.Log, "audit") {
		t.Errorf("the failure should name the empty role, got %q", ap.Log)
	}
	if ap.ApprovalID != 0 {
		t.Error("no ticket should be created for a chain nobody is on")
	}
}

// roleMembers returns the user ids currently in a role.
func (a *testApp) roleMembers(token string, roleID int64) []int64 {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/roles/"+itoa(roleID), token, nil)
	var det struct {
		Members []struct {
			ID int64 `json:"id"`
		} `json:"members"`
	}
	_ = json.Unmarshal(r.Data, &det)
	out := make([]int64, 0, len(det.Members))
	for _, m := range det.Members {
		out = append(out, m.ID)
	}
	return out
}

// 服务账号不能进审批链。
//
// 它是机器主体:登录不了控制台,也就永远点不了"通过"。把它放进链里,单子看着
// 有审批人,实际没有人能批 —— 和空角色是同一种死单,只是伪装得更好。若一个角色
// 里只剩服务账号,等同于空角色。
func TestApproverChainExcludesServiceAccounts(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	stg := app.connIDByEnv(admin, "staging")

	// 把 audit 角色清空,只放一个服务账号进去。
	auditID := app.roleIDByCode(admin, "audit")
	roID := app.roleIDByCode(admin, "ro")
	for _, m := range app.roleMembers(admin, auditID) {
		app.do(http.MethodPost, "/api/v1/roles/"+itoa(roID)+"/members", admin, map[string]any{"userId": m})
		app.do(http.MethodDelete, "/api/v1/roles/"+itoa(auditID)+"/members/"+itoa(m), admin, nil)
	}
	sa := app.createServiceAccount(admin, "审批不了的机器人", []int64{auditID}, nil)
	if len(app.roleMembers(admin, auditID)) == 0 {
		t.Fatalf("service account %d should be a member of the role", sa.ID)
	}

	pid := app.createPipeline(admin, "只有服务账号的审批", "", []map[string]any{
		{"name": "人工审批", "type": "approve", "config": `{"approverRole":"audit"}`},
		{"name": "执行变更", "type": "execute"},
	})
	r := app.submitRelease(admin, map[string]any{
		"title": "机器人审批", "pipelineId": pid, "connectionId": stg,
		"sql": "SELECT 1;", "reason": "审批角色回归",
	})
	eq(t, r.Code, 0, "submit")
	var rel releaseView
	_ = json.Unmarshal(r.Data, &rel)

	done := app.waitRelease(admin, rel.ID, "failed", "waiting", "success")
	eq(t, done.Status, "failed", "a chain of service accounts is a chain nobody can act on")
	ap := stageByType(done, "approve")
	if ap.ApprovalID != 0 {
		t.Error("no ticket should be created when nobody on the chain can log in")
	}
}
