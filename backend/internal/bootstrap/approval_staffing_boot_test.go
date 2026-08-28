package bootstrap

// 审批人自检跑在真实数据上的样子。
//
// 纯判定逻辑在 service/approval_staffing_test.go 里逐条钉过了;这里要证的是另一件
// 事:它接到真的角色、真的流程、真的待办上,说的还是不是同一句话。这两者会分家 ——
// 最典型的一次就是查询本身把数据过滤没了(存量工单那条最初用了按人过滤的列表,
// userID=0 于是一张也查不到,自检安静地"通过"了)。一个报平安的坏自检,比没有更糟。

import (
	"net/http"
	"strings"
	"testing"

	"velagateway/internal/service"
)

func issuesAt(app *testApp, wherePart string) []service.StaffingIssue {
	out := []service.StaffingIssue{}
	for _, is := range app.svc.ApprovalStaffingReport() {
		if strings.Contains(is.Where, wherePart) {
			out = append(out, is)
		}
	}
	return out
}

// shrinkOwnerRoleTo 把 DBA 负责人角色减到只剩 keep 一个人。
//
// 移除前先给对方补一个只读角色:系统不允许把人的最后一个角色摘掉(那会让他一个
// 权限都不剩),而这里要的是不再是审批人,不是注销这个人。
func (a *testApp) shrinkOwnerRoleTo(adminToken string, keep int64) {
	a.t.Helper()
	roleID := a.roleIDByCode(adminToken, "owner")
	roID := a.roleIDByCode(adminToken, "ro")
	for _, m := range a.roleMembers(adminToken, roleID) {
		if m == keep {
			continue
		}
		a.do(http.MethodPost, "/api/v1/roles/"+itoa(roID)+"/members", adminToken, map[string]any{"userId": m})
		r := a.do(http.MethodDelete, "/api/v1/roles/"+itoa(roleID)+"/members/"+itoa(m), adminToken, nil)
		if r.Code != 0 {
			a.t.Fatalf("移除 owner 成员 %d 失败: %s", m, r.Msg)
		}
	}
}

// 种子环境是健康的:自检不该无中生有。一个总在报警的自检,和没有自检是一回事。
func TestStaffingSelfCheck_IsQuietOnAHealthySetup(t *testing.T) {
	app := newTestApp(t)
	for _, is := range app.svc.ApprovalStaffingReport() {
		if strings.Contains(is.Where, "默认审批链") {
			t.Errorf("默认链本是健康的,却报了: %s / %s", is.Problem, is.Fix)
		}
	}
}

// 把审批链减到只剩一个人:工单还建得出来,但那个人自己发起的就没人能批。
// 这正是自检要提前喊住的情况 —— 现有的任何一层校验都不会为此说话。
func TestStaffingSelfCheck_CatchesTheOnePersonChain(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	zw := app.userIDByEmail(admin, "zhangwei@vela.io")
	app.shrinkOwnerRoleTo(admin, zw)

	found := issuesAt(app, "默认审批链")
	if len(found) == 0 {
		t.Fatal("审批链只剩一个人时,自检必须告警")
	}
	is := found[0]
	if is.Severity != service.StaffingDeadlock {
		t.Errorf("应为 deadlock,实际 %q", is.Severity)
	}
	if !strings.Contains(is.Problem, "张伟") && !strings.Contains(is.Problem, "Zhang") {
		t.Logf("问题描述: %q", is.Problem) // 名字随种子而定,不硬断言
	}
	if !strings.Contains(is.Fix, "自审批") {
		t.Errorf("建议里要给出自审批这条出路,实际: %q", is.Fix)
	}
}

// 开了自审批,一个人的链就不再是隐患 —— 自检要跟着设置走,否则改了设置还在报警,
// 人很快就会学会无视它。
func TestStaffingSelfCheck_RespectsTheSelfApproveSetting(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	zw := app.userIDByEmail(admin, "zhangwei@vela.io")
	app.shrinkOwnerRoleTo(admin, zw)
	if len(issuesAt(app, "默认审批链")) == 0 {
		t.Fatal("前置条件不成立:此时本应有告警")
	}

	eq(t, app.do(http.MethodPut, "/api/v1/settings", admin, map[string]any{
		"approval.allowSelfApprove": true,
	}).Code, 0, "enable self-approval")

	if found := issuesAt(app, "默认审批链"); len(found) != 0 {
		t.Errorf("开了自审批后不该再报默认链,实际: %+v", found)
	}
}

// 存量卡单要真的数得出来。这条是冲着那次查询错误去的:用按人过滤的列表去查全量,
// 一张都查不到,自检会因此安静地"通过"。
func TestStaffingSelfCheck_CountsTicketsNobodyCanDecide(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	zw := app.userIDByEmail(admin, "zhangwei@vela.io")
	// 链上只剩张伟,再由张伟本人提一张高危单 —— 一张谁都批不了的单。
	app.shrinkOwnerRoleTo(admin, zw)
	owner := app.login("zhangwei@vela.io", "vela123")
	app.submitProdHighRisk(owner)

	found := issuesAt(app, "存量工单")
	if len(found) == 0 {
		t.Fatal("有谁都批不了的待办时,自检必须数出来 —— 查不到不等于没有")
	}
	if !strings.Contains(found[0].Problem, "1") {
		t.Errorf("要说清有几张,实际: %q", found[0].Problem)
	}
}
