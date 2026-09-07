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

// 停用账户时一并收回角色。
//
// 停用本身已被四道状态检查拦住,收回角色是纵深防御:一个停用的账户不该继续以"某某
// 角色的成员"出现在权限视图里,也不该在任何一处漏检时还带着能力。
func TestDisableUser_StripsItsRoles(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")

	// 造一个带角色的普通账户。
	r := app.do(http.MethodPost, "/api/v1/users", admin, map[string]any{
		"name": "临时工", "email": "temp@vela.io", "password": "Str0ng-Passw0rd!", "roleIds": []int64{3},
	})
	eq(t, r.Code, 0, "建账户")
	if got := app.rolesOf("temp@vela.io"); len(got) == 0 {
		t.Fatal("前置条件:新账户应当带着角色")
	}

	u, _ := app.repo.GetUserByEmail("temp@vela.io")
	eq(t, app.do(http.MethodPatch, "/api/v1/users/"+itoa(u.ID), admin,
		map[string]any{"status": "disabled"}).Code, 0, "停用")

	// 主角色与成员关系都要没。
	if got := app.rolesOf("temp@vela.io"); len(got) != 0 {
		t.Errorf("停用后不该还有角色,实际 %v", got)
	}
	var members int64
	app.repo.DB().Model(&model.RoleMember{}).Where("user_id = ?", u.ID).Count(&members)
	eq(t, members, int64(0), "成员关系已清空")
}

// 同一个请求里既停用又改角色时,以停用为准 —— 两个意图矛盾,只有一个是安全方向。
func TestDisableUser_WinsOverARoleChangeInTheSameRequest(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")

	r := app.do(http.MethodPost, "/api/v1/users", admin, map[string]any{
		"name": "临时工2", "email": "temp2@vela.io", "password": "Str0ng-Passw0rd!", "roleIds": []int64{3},
	})
	eq(t, r.Code, 0, "建账户")
	u, _ := app.repo.GetUserByEmail("temp2@vela.io")

	eq(t, app.do(http.MethodPatch, "/api/v1/users/"+itoa(u.ID), admin, map[string]any{
		"status": "disabled", "roleIds": []int64{2, 3},
	}).Code, 0, "同时停用并改角色")

	if got := app.rolesOf("temp2@vela.io"); len(got) != 0 {
		t.Errorf("停用应当压过角色改动,实际留下 %v", got)
	}
}

// 收回角色之后账户仍然要能被重新启用并登录 —— 只是登进去什么也没有。
//
// 主角色被清掉后,BuildMe 从前会因为查不到角色直接报错,于是重新启用的账户一登录就是
// 500:界面显示"服务器错误",而不是"这个人还没有角色",管理员无从下手。没有角色是一个
// 合法状态,不是故障。
func TestDisableUser_ReEnabledAccountLogsInWithNothing(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")

	r := app.do(http.MethodPost, "/api/v1/users", admin, map[string]any{
		"name": "临时工3", "email": "temp3@vela.io", "password": "Str0ng-Passw0rd!", "roleIds": []int64{3},
	})
	eq(t, r.Code, 0, "建账户")
	u, _ := app.repo.GetUserByEmail("temp3@vela.io")

	eq(t, app.do(http.MethodPatch, "/api/v1/users/"+itoa(u.ID), admin,
		map[string]any{"status": "disabled"}).Code, 0, "停用")
	eq(t, app.do(http.MethodPatch, "/api/v1/users/"+itoa(u.ID), admin,
		map[string]any{"status": "active"}).Code, 0, "重新启用")

	// 能登录。
	eq(t, app.loginCode("temp3@vela.io", "Str0ng-Passw0rd!"), 0, "重新启用后能登录")

	// 但没有角色,也就没有菜单 —— 管理员必须重新指派。
	if got := app.rolesOf("temp3@vela.io"); len(got) != 0 {
		t.Errorf("重新启用不会把角色变回来,实际 %v", got)
	}
	tok := app.login("temp3@vela.io", "Str0ng-Passw0rd!")
	me := app.do(http.MethodGet, "/api/v1/auth/me", tok, nil)
	eq(t, me.Code, 0, "auth/me 不该报错")
}
