package bootstrap

import (
	"net/http"
	"strings"
	"testing"

	"velagateway/internal/model"
)

// 管理后台拆分(工单 03):管理类菜单(实例、规则、分层、权限、系统设置)只给平台管理员角色。

func roleIDByCode(t *testing.T, app *testApp, code string) int64 {
	t.Helper()
	var r model.Role
	if err := app.repo.DB().Where("code = ?", code).First(&r).Error; err != nil {
		t.Fatalf("角色 %s: %v", code, err)
	}
	return r.ID
}

func menuOn(app *testApp, roleID int64, key string) bool {
	var m model.RoleMenu
	if err := app.repo.DB().Where("role_id = ? AND menu_key = ?", roleID, key).First(&m).Error; err != nil {
		return false
	}
	return m.Enabled
}

// 存量的自定义角色上勾着管理类菜单:启动时收回;管理员角色、以及非管理类菜单不动。
func TestRevokeAdminOnlyMenus_FromNonAdminRoles(t *testing.T) {
	app := newTestApp(t)
	grantEveryMenu(t, app, "l2")
	l2, admin := roleIDByCode(t, app, "l2"), roleIDByCode(t, app, "admin")

	if err := revokeAdminOnlyMenus(app.repo.DB()); err != nil {
		t.Fatal(err)
	}
	for _, k := range model.AdminOnlyMenus {
		if menuOn(app, l2, k) {
			t.Errorf("L2 的 %s 菜单应已收回", k)
		}
		if !menuOn(app, admin, k) {
			t.Errorf("平台管理员的 %s 菜单不该被收回", k)
		}
	}
	for _, k := range []string{"terminal", "pipeline", "audit", "project"} {
		if !menuOn(app, l2, k) {
			t.Errorf("非管理类菜单 %s 不该被收回", k)
		}
	}
	// 再跑一次什么都不改
	if err := revokeAdminOnlyMenus(app.repo.DB()); err != nil {
		t.Fatal(err)
	}
}

// 权限页 / 接口:不能把管理类菜单勾给非管理员角色;关掉照常允许;管理员角色照常可改。
func TestSetRoleMenus_RefusesAdminOnlyMenusForOtherRoles(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	l2, admin := roleIDByCode(t, app, "l2"), roleIDByCode(t, app, "admin")

	r := app.do(http.MethodPut, "/api/v1/roles/"+itoa(l2)+"/menus", token, map[string]any{"menus": map[string]bool{"terminal": true, "perms": true, "settings": true}})
	if r.Code == 0 || !strings.Contains(r.Msg, "perms") || !strings.Contains(r.Msg, "settings") {
		t.Fatalf("给 L2 勾权限 / 设置菜单应被拒并说明是哪几项,实际 code=%d msg=%s", r.Code, r.Msg)
	}
	if menuOn(app, l2, "perms") {
		t.Fatal("被拒的请求不该写入任何菜单")
	}
	eq(t, app.do(http.MethodPut, "/api/v1/roles/"+itoa(l2)+"/menus", token, map[string]any{"menus": map[string]bool{"perms": false, "terminal": true}}).Code, 0, "关掉管理类菜单、改普通菜单照常允许")
	eq(t, app.do(http.MethodPut, "/api/v1/roles/"+itoa(admin)+"/menus", token, map[string]any{"menus": map[string]bool{"perms": true, "settings": true}}).Code, 0, "管理员角色照常可勾")
}

// 找不到平台管理员角色(code 被改过):认不出谁是管理员,就一个都不收 —— 否则实际在用的管理员角色
// 也被收走,谁都进不了权限页改回来(2026-10 审查)。
func TestRevokeAdminOnlyMenus_SkipsWhenNoAdminRole(t *testing.T) {
	app := newTestApp(t)
	admin, l2 := roleIDByCode(t, app, "admin"), roleIDByCode(t, app, "l2")
	grantEveryMenu(t, app, "l2")
	if err := app.repo.DB().Model(&model.Role{}).Where("id = ?", admin).Update("code", "superuser").Error; err != nil {
		t.Fatal(err)
	}
	if err := revokeAdminOnlyMenus(app.repo.DB()); err != nil {
		t.Fatal(err)
	}
	for _, k := range model.AdminOnlyMenus {
		if !menuOn(app, admin, k) {
			t.Errorf("找不到 code=admin 时,原管理员角色的 %s 菜单不该被收回", k)
		}
		if !menuOn(app, l2, k) {
			t.Errorf("找不到 code=admin 时一个都不收,L2 的 %s 也应保持原样", k)
		}
	}
}
