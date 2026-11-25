package bootstrap

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// 2026-10-22:权限页「新建角色」从前打开的是当前选中角色的编辑表单(保存会改掉那个角色),后端也没有
// 建角色的接口。现在 POST /roles 建一个新角色:什么都不给 —— 每个分层 × 每项能力 deny、无菜单;
// code 由服务端生成,不会撞上 admin 这类特殊角色。
func TestRoleCreate_StartsWithNothing(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")

	r := app.do(http.MethodPost, "/api/v1/roles", admin, map[string]any{
		"name": "数据分析", "layer": "L3 · 分析", "description": "只看报表库", "canApprove": false,
	})
	eq(t, r.Code, 0, "建角色")
	var role struct {
		ID     int64                        `json:"id"`
		Code   string                       `json:"code"`
		Name   string                       `json:"name"`
		Menus  map[string]bool              `json:"menus"`
		Matrix map[string]map[string]string `json:"matrix"`
	}
	_ = json.Unmarshal(r.Data, &role)
	eq(t, role.Name, "数据分析", "名称")
	if !strings.HasPrefix(role.Code, "r_") {
		t.Errorf("code 应由服务端生成(r_ 开头),实际 %q", role.Code)
	}
	for k, on := range role.Menus {
		if on {
			t.Errorf("新角色不应带菜单,实际开着 %s", k)
		}
	}
	if len(role.Matrix) == 0 {
		t.Fatal("新角色应有显式的矩阵行(全 deny),不能是空 —— 空格子按 allow 算")
	}
	for capName, tiers := range role.Matrix {
		for tier, lvl := range tiers {
			if lvl != "deny" {
				t.Errorf("新角色 %s/%s 应为 deny,实际 %s", capName, tier, lvl)
			}
		}
	}
	if auditCount(app, "command LIKE ?", "admin.role.create role="+role.Code+"%") == 0 {
		t.Error("建角色应写审计")
	}

	// 只挂这个新角色的人:在任何环境执行都被拒(矩阵全 deny)
	eq(t, app.do(http.MethodPost, "/api/v1/users", admin, map[string]any{
		"name": "分析员", "email": "analyst@vela.io", "password": "initpass9", "roleIds": []int64{role.ID},
	}).Code, 0, "建只挂新角色的账号")
	u := app.login("analyst@vela.io", "initpass9")
	for _, env := range []string{"dev", "prod"} {
		if rr := app.do(http.MethodPost, "/api/v1/terminal/exec", u, map[string]any{
			"connectionId": app.connIDByEnv(admin, env), "sql": "SELECT 1",
		}); rr.Code == 0 {
			t.Errorf("新角色在 %s 上不应能执行,实际放行了", env)
		}
	}
}

func TestRoleCreate_ValidationAndAccess(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	if r := app.do(http.MethodPost, "/api/v1/roles", admin, map[string]any{"name": "  "}); r.Code == 0 {
		t.Error("名称为空不应建成")
	}
	if r := app.do(http.MethodPost, "/api/v1/roles", admin, map[string]any{"name": "平台管理员"}); r.Code == 0 || !strings.Contains(r.Msg, "同名") {
		t.Errorf("与已有角色同名应被拒并说明,实际 code=%d msg=%s", r.Code, r.Msg)
	}
	// 想借名字冒充 admin:code 由服务端生成,与名字无关
	r := app.do(http.MethodPost, "/api/v1/roles", admin, map[string]any{"name": "admin"})
	eq(t, r.Code, 0, "名字叫 admin 可以建")
	var role struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(r.Data, &role)
	if role.Code == "admin" {
		t.Fatal("新角色的 code 不能是 admin")
	}
	// 非管理员不能建
	owner := app.login("zhangwei@vela.io", "vela123")
	if r := app.do(http.MethodPost, "/api/v1/roles", owner, map[string]any{"name": "越权"}); r.Code == 0 {
		t.Error("非管理员不应能建角色")
	}
}

// 回归:编辑已有角色照旧(不新建、不动别的角色)。
func TestRoleUpdate_StillEditsTheSelectedRole(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	ro, _ := app.repo.GetRoleByCode("ro")
	before, _ := app.repo.ListRoles()
	eq(t, app.do(http.MethodPatch, "/api/v1/roles/"+itoa(ro.ID), admin, map[string]any{"name": "研发只读(改)"}).Code, 0, "改名")
	after, _ := app.repo.ListRoles()
	eq(t, len(after), len(before), "编辑不应多出角色")
	got, _ := app.repo.GetRole(ro.ID)
	eq(t, got.Name, "研发只读(改)", "改的是这个角色")
}

// 角色详情回传可审批、描述、默认连接身份 —— 编辑表单据此回显,不再按 code 猜
func TestRoleDetail_ReturnsEditableFields(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	owner, _ := app.repo.GetRoleByCode("owner")
	var d struct {
		CanApprove      bool   `json:"canApprove"`
		Description     string `json:"description"`
		DefaultConnRole string `json:"defaultConnRole"`
	}
	_ = json.Unmarshal(app.do(http.MethodGet, "/api/v1/roles/"+itoa(owner.ID), admin, nil).Data, &d)
	eq(t, d.CanApprove, owner.CanApprove, "可审批")
	eq(t, d.Description, owner.Description, "描述")
	eq(t, d.DefaultConnRole, owner.DefaultConnRole, "默认连接身份")
}

// ---------------------------------------------------------------- 删除

func newCustomRole(t *testing.T, app *testApp, admin, name string) (int64, string) {
	t.Helper()
	r := app.do(http.MethodPost, "/api/v1/roles", admin, map[string]any{"name": name})
	eq(t, r.Code, 0, "建角色 "+name)
	var role struct {
		ID        int64  `json:"id"`
		Code      string `json:"code"`
		Deletable bool   `json:"deletable"`
	}
	_ = json.Unmarshal(r.Data, &role)
	if !role.Deletable {
		t.Fatalf("自定义角色应可删除: %+v", role)
	}
	return role.ID, role.Code
}

func TestRoleDelete_BuiltinRefused(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	for _, code := range []string{"admin", "owner", "l2", "ro", "audit"} {
		role, err := app.repo.GetRoleByCode(code)
		if err != nil {
			t.Fatal(err)
		}
		var d struct {
			Deletable bool `json:"deletable"`
		}
		_ = json.Unmarshal(app.do(http.MethodGet, "/api/v1/roles/"+itoa(role.ID), admin, nil).Data, &d)
		if d.Deletable {
			t.Errorf("%s 不应标为可删除", code)
		}
		r := app.do(http.MethodDelete, "/api/v1/roles/"+itoa(role.ID), admin, nil)
		if r.Code == 0 || !strings.Contains(r.Msg, "内置") {
			t.Errorf("%s 不应能删,实际 code=%d msg=%s", code, r.Code, r.Msg)
		}
	}
}

func TestRoleDelete_RefusedWhileInUse_ThenCleansUp(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	id, code := newCustomRole(t, app, admin, "临时角色")

	// 有成员:拒绝
	eq(t, app.do(http.MethodPost, "/api/v1/users", admin, map[string]any{
		"name": "临时工", "email": "temp@vela.io", "password": "initpass9", "roleIds": []int64{id},
	}).Code, 0, "建挂着该角色的账号")
	if r := app.do(http.MethodDelete, "/api/v1/roles/"+itoa(id), admin, nil); r.Code == 0 || !strings.Contains(r.Msg, "用户") {
		t.Fatalf("有成员时不应能删,实际 code=%d msg=%s", r.Code, r.Msg)
	}
	ro, _ := app.repo.GetRoleByCode("ro")
	u, _ := app.repo.GetUserByEmail("temp@vela.io")
	if err := app.repo.SetUserRoles(u.ID, []int64{ro.ID}); err != nil {
		t.Fatal(err)
	}

	// 被流程用作确认角色:拒绝并说出流程名
	pid := app.createPipeline(admin, "引用临时角色", "", []map[string]any{
		{"name": "人工确认", "type": "manual", "config": `{"confirmRole":"` + code + `"}`},
		{"name": "执行变更", "type": "execute"},
	})
	if r := app.do(http.MethodDelete, "/api/v1/roles/"+itoa(id), admin, nil); r.Code == 0 || !strings.Contains(r.Msg, "引用临时角色") {
		t.Fatalf("被流程引用时不应能删,并说出流程名,实际 code=%d msg=%s", r.Code, r.Msg)
	}
	eq(t, app.do(http.MethodDelete, "/api/v1/pipelines/"+itoa(pid), admin, nil).Code, 0, "删掉引用它的流程")

	// 都不再引用:删除成功,矩阵 / 菜单 / 标签一并清掉,写审计
	eq(t, app.do(http.MethodDelete, "/api/v1/roles/"+itoa(id), admin, nil).Code, 0, "删除")
	if _, err := app.repo.GetRole(id); err == nil {
		t.Error("角色应已删除")
	}
	if m, _ := app.repo.MatrixForRole(id); len(m) != 0 {
		t.Errorf("矩阵行应一并删除,剩 %v", m)
	}
	if auditCount(app, "command LIKE ?", "admin.role.delete role="+code+"%") == 0 {
		t.Error("删除应写审计")
	}
	// 回归:别的角色不受影响
	if _, err := app.repo.GetRoleByCode("ro"); err != nil {
		t.Error("内置角色不应受影响")
	}
}

func TestRoleDelete_AdminOnly(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	id, _ := newCustomRole(t, app, admin, "只读分析")
	owner := app.login("zhangwei@vela.io", "vela123")
	if r := app.do(http.MethodDelete, "/api/v1/roles/"+itoa(id), owner, nil); r.Code == 0 {
		t.Error("非管理员不应能删角色")
	}
	if _, err := app.repo.GetRole(id); err != nil {
		t.Error("角色应还在")
	}
}
