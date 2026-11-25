package bootstrap

import (
	"net/http"
	"testing"

	"velagateway/internal/model"
)

// rolesOf reports what the gateway would actually judge this user by.
func (a *testApp) rolesOf(email string) []int64 {
	a.t.Helper()
	u, err := a.repo.GetUserByEmail(email)
	if err != nil {
		a.t.Fatalf("get %s: %v", email, err)
	}
	return a.repo.EffectiveRoleIDs(u)
}

// 停用只改状态,不动角色(2026-10 黑盒测试,工单 02 第 1 条)。
//
// 停用的语义是**临时切断**:登录、JWT 中间件、WS 握手、每条命令前复核这四道状态检查
// 已经把停用的账户挡在门外。从前停用会顺手清空角色,于是再启用回来的账户什么都没有,
// 服务账号停用再启用后凭据也随之失效 —— 一个"暂停"按钮做成了"销号"。
func TestDisableUser_KeepsItsRoles(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")

	r := app.do(http.MethodPost, "/api/v1/users", admin, map[string]any{
		"name": "临时工", "email": "temp@vela.io", "password": "Str0ng-Passw0rd!", "roleIds": []int64{3},
	})
	eq(t, r.Code, 0, "建账户")
	before := app.rolesOf("temp@vela.io")
	if len(before) == 0 {
		t.Fatal("前置条件:新账户应当带着角色")
	}

	u, _ := app.repo.GetUserByEmail("temp@vela.io")
	eq(t, app.do(http.MethodPatch, "/api/v1/users/"+itoa(u.ID), admin,
		map[string]any{"status": "disabled"}).Code, 0, "停用")

	if got := app.rolesOf("temp@vela.io"); len(got) != len(before) {
		t.Errorf("停用不该动角色:之前 %v,之后 %v", before, got)
	}
	var members int64
	app.repo.DB().Model(&model.RoleMember{}).Where("user_id = ?", u.ID).Count(&members)
	if members == 0 {
		t.Error("停用后成员关系不该被清空")
	}
	// 停用照样生效:登不进来。
	if app.loginCode("temp@vela.io", "Str0ng-Passw0rd!") == 0 {
		t.Error("停用的账户不该还能登录")
	}
}

// 再启用回来,角色原样都在,登录后照常有菜单。
func TestDisableUser_ReEnabledAccountGetsItsRolesBack(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")

	r := app.do(http.MethodPost, "/api/v1/users", admin, map[string]any{
		"name": "临时工3", "email": "temp3@vela.io", "password": "Str0ng-Passw0rd!", "roleIds": []int64{3},
	})
	eq(t, r.Code, 0, "建账户")
	u, _ := app.repo.GetUserByEmail("temp3@vela.io")
	before := app.rolesOf("temp3@vela.io")

	eq(t, app.do(http.MethodPatch, "/api/v1/users/"+itoa(u.ID), admin,
		map[string]any{"status": "disabled"}).Code, 0, "停用")
	eq(t, app.do(http.MethodPatch, "/api/v1/users/"+itoa(u.ID), admin,
		map[string]any{"status": "active"}).Code, 0, "重新启用")

	eq(t, app.loginCode("temp3@vela.io", "Str0ng-Passw0rd!"), 0, "重新启用后能登录")
	if got := app.rolesOf("temp3@vela.io"); len(got) != len(before) {
		t.Errorf("重新启用后角色应当原样都在:之前 %v,之后 %v", before, got)
	}
	tok := app.login("temp3@vela.io", "Str0ng-Passw0rd!")
	eq(t, app.do(http.MethodGet, "/api/v1/auth/me", tok, nil).Code, 0, "auth/me 正常")
}

// 服务账号同理:停用再启用,角色还在,凭据随之恢复可用 —— 不必重建账号、重发凭据。
func TestDisableServiceAccount_KeepsItsRoles(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	owner := app.roleIDByCode(admin, "owner")

	sa := app.createServiceAccount(admin, "停用测试机器人", []int64{owner}, nil)
	before := app.rolesOf(sa.Email)
	if len(before) == 0 {
		t.Fatal("前置条件:服务账号应当带着角色")
	}
	u, _ := app.repo.GetUserByEmail(sa.Email)

	eq(t, app.do(http.MethodPatch, "/api/v1/users/"+itoa(u.ID), admin,
		map[string]any{"status": "disabled"}).Code, 0, "停用服务账号")
	eq(t, app.do(http.MethodPatch, "/api/v1/users/"+itoa(u.ID), admin,
		map[string]any{"status": "active"}).Code, 0, "重新启用服务账号")

	if got := app.rolesOf(sa.Email); len(got) != len(before) {
		t.Errorf("服务账号停用再启用后角色应当原样都在:之前 %v,之后 %v", before, got)
	}
}

// 2026-10-26 回归:停用不再清角色之后,审批链没跟着过滤停用账号 —— owner 全被停用时,新单的
// 审批步骤全落在登录不了的人身上,也不再回落到管理员,单子永远 pending。
func TestDisableUser_DropsOutOfApprovalChains(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	owner, err := app.repo.GetRoleByCode("owner")
	if err != nil {
		t.Fatal(err)
	}
	members, _ := app.repo.MembersOfRole(owner.ID)
	if len(members) == 0 {
		t.Fatal("前置条件:owner 角色应有成员")
	}
	for _, m := range members {
		if m.Email == "linwei@vela.io" {
			continue
		}
		eq(t, app.do(http.MethodPatch, "/api/v1/users/"+itoa(m.ID), admin, map[string]any{"status": "disabled"}).Code, 0, "停用 "+m.Name)
	}
	ap := app.submitProdHighRisk(admin)
	steps, err := app.repo.StepsOf(ap.ID)
	if err != nil || len(steps) == 0 {
		t.Fatalf("应建出审批步骤: %v %v", steps, err)
	}
	for _, st := range steps {
		u, _ := app.repo.GetUserByID(st.ApproverID)
		if u.Status != "active" {
			t.Errorf("审批步骤不该落在停用账号上: %s(%s)", u.Name, u.Status)
		}
	}
}
