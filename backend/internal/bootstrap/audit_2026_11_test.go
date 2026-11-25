package bootstrap

import (
	"encoding/json"
	"net/http"
	"testing"

	"velagateway/internal/model"
)

// 2026-11 白盒审计的回归用例(HTTP 黑盒)。纯判定层的用例在 internal/gateway 里。

func auditRole(t *testing.T, app *testApp, admin, name string) int64 {
	t.Helper()
	r := app.do(http.MethodPost, "/api/v1/roles", admin, map[string]any{"name": name, "layer": "L3"})
	eq(t, r.Code, 0, "建角色")
	var role struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(r.Data, &role)
	return role.ID
}

func connCount(t *testing.T, app *testApp, tok string) int {
	t.Helper()
	var conns []struct {
		ID int64 `json:"id"`
	}
	r := app.do(http.MethodGet, "/api/v1/connections", tok, nil)
	eq(t, r.Code, 0, "列实例")
	_ = json.Unmarshal(r.Data, &conns)
	return len(conns)
}

// V-03:新角色全 deny 不收窄能力,但没有标签 = 不限实例;受限用户加入后实例范围不能变大。
func TestV03_NewRoleDoesNotWidenInstanceScope(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	roleID := auditRole(t, app, admin, "仅审批")

	uid := app.userIDByEmail(admin, "chenhao@vela.io")
	eq(t, app.do(http.MethodPut, "/api/v1/users/"+itoa(uid)+"/tags", admin, map[string]any{"tags": []string{}}).Code, 0, "清用户级标签")
	before := connCount(t, app, app.login("chenhao@vela.io", "vela123"))
	roleTags, _, _ := app.repo.TagsForRoles(app.repo.EffectiveRoleIDs(mustUser(t, app, uid)))
	if len(roleTags) == 0 {
		t.Skip("种子里 chenhao 不受标签限制,用不了这条用例")
	}

	eq(t, app.do(http.MethodPost, "/api/v1/roles/"+itoa(roleID)+"/members", admin, map[string]any{"userId": uid}).Code, 0, "加入新角色")
	after := connCount(t, app, app.login("chenhao@vela.io", "vela123"))
	eq(t, after, before, "加入新角色后实例数不能变")
	if _, unrestricted, _ := app.repo.TagsForRoles(app.repo.EffectiveRoleIDs(mustUser(t, app, uid))); unrestricted {
		t.Fatal("加入新角色后不能变成不限实例")
	}
}

func mustUser(t *testing.T, app *testApp, id int64) *model.User {
	t.Helper()
	u, err := app.repo.GetUserByID(id)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// V-03:界面读到的不是占位标签;选了标签后占位消失;清空标签仍是"不限",这是显式操作的原有语义。
func TestV03_RoleDetailHidesPlaceholder(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	roleID := auditRole(t, app, admin, "分析")

	type detail struct {
		Tags        []string `json:"tags"`
		NoInstances bool     `json:"noInstances"`
	}
	read := func() detail {
		var d detail
		_ = json.Unmarshal(app.do(http.MethodGet, "/api/v1/roles/"+itoa(roleID), admin, nil).Data, &d)
		return d
	}
	d := read()
	if len(d.Tags) != 0 || !d.NoInstances {
		t.Fatalf("新角色应显示未开放实例,实际 %+v", d)
	}
	eq(t, app.do(http.MethodPut, "/api/v1/roles/"+itoa(roleID)+"/tags", admin, map[string]any{"tags": []string{"orders"}}).Code, 0, "设标签")
	d = read()
	if len(d.Tags) != 1 || d.Tags[0] != "orders" || d.NoInstances {
		t.Fatalf("设了标签应只显示标签,实际 %+v", d)
	}
	eq(t, app.do(http.MethodPut, "/api/v1/roles/"+itoa(roleID)+"/tags", admin, map[string]any{"tags": []string{}}).Code, 0, "清空标签")
	if d = read(); d.NoInstances {
		t.Fatalf("显式清空 = 不限实例,实际 %+v", d)
	}
}

// V-03:存量无标签自定义角色能被列出来,带标签的和内置角色不在其中。
func TestV03_CustomRolesWithoutTagsListed(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	withTag := auditRole(t, app, admin, "新角色")
	bare := auditRole(t, app, admin, "存量角色")
	if err := app.repo.DB().Where("role_id = ?", bare).Delete(&model.RoleTag{}).Error; err != nil {
		t.Fatal(err)
	}
	roles, err := app.repo.CustomRolesWithoutTags()
	if err != nil {
		t.Fatal(err)
	}
	if len(roles) != 1 || roles[0].ID != bare || roles[0].ID == withTag {
		t.Fatalf("应只列出存量角色 %d,实际 %+v", bare, roles)
	}
}

// V-06:存量凭据"停用 → 加 admin → 启用"不能带着管理员权限回来。
func TestV06_ReenableCredentialBoundToAdminRefused(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	adminRole := app.roleIDByCode(admin, "admin")
	uid := app.userIDByEmail(admin, "chenhao@vela.io")
	tok := app.issueClient(admin, "存量凭据", "chenhao@vela.io", []string{"release:read"})
	cl := app.apiClientByName(admin, "存量凭据")
	path := "/api/v1/api-clients/" + itoa(cl.ID)

	eq(t, app.do(http.MethodPut, path, admin, map[string]any{"enabled": false}).Code, 0, "停用凭据")
	eq(t, app.do(http.MethodPatch, "/api/v1/users/"+itoa(uid), admin, map[string]any{"roleIds": []int64{adminRole}}).Code, 0, "凭据停用后可以加 admin")

	if r := app.do(http.MethodPut, path, admin, map[string]any{"enabled": true}); r.Code == 0 {
		t.Fatal("绑定账号已是管理员时不能重新启用凭据")
	}
	if r := app.openDo(http.MethodGet, "/api/v1/open/instances", tok, nil); r.Code == 0 {
		t.Fatal("凭据应仍是停用状态")
	}
	// 其它字段照常能改(启用开关没动)。
	eq(t, app.do(http.MethodPut, path, admin, map[string]any{"name": "改个名"}).Code, 0, "停用中的凭据改名不受影响")
}

// 原有行为:绑真人的存量凭据不追溯,停用后还能启用;已启用的凭据再发一次 enabled=true 不受影响。
func TestV06_LegacyHumanCredentialCanStillReenable(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	tok := app.issueClient(admin, "存量凭据", "chenhao@vela.io", []string{"release:read"})
	cl := app.apiClientByName(admin, "存量凭据")
	path := "/api/v1/api-clients/" + itoa(cl.ID)

	eq(t, app.do(http.MethodPut, path, admin, map[string]any{"enabled": true}).Code, 0, "已启用再启用")
	eq(t, app.do(http.MethodPut, path, admin, map[string]any{"enabled": false}).Code, 0, "停用")
	eq(t, app.do(http.MethodPut, path, admin, map[string]any{"enabled": true}).Code, 0, "重新启用")
	eq(t, app.openDo(http.MethodGet, "/api/v1/open/instances", tok, nil).Code, 0, "启用后照常可用")
}

// V-07:连接地址做格式校验,新建与编辑都挡。正常地址不受影响。
func TestV07_ConnectionHostValidated(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	create := func(host, engine string) int {
		return app.do(http.MethodPost, "/api/v1/connections", admin, map[string]any{
			"name": "h-" + host, "engine": engine, "host": host, "env": "dev", "policy": "strict",
			"username": "u", "password": "p", "database": "svc",
		}).Code
	}
	for _, bad := range []string{
		"evil/x?TRACE FILE=/opt/vela-gateway/web/t.html#", "a b", "a/b", "a?b=1", "a#b", "a)/x?allowAllFiles=true#",
	} {
		if code := create(bad, "Oracle"); code == 0 {
			t.Errorf("地址 %q 应被拒", bad)
		}
	}
	for _, good := range []string{"db.prod.vela.io", "10.0.0.8", "10.0.0.8:1521", "db_host-1.internal"} {
		if code := create(good, "Oracle"); code != 0 {
			t.Errorf("地址 %q 应放行,实际 %d", good, code)
		}
	}
}

// V-07:编辑连接与从库地址同样校验;合法的从库地址不受影响。
func TestV07_UpdateAndReplicaHostValidated(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	base := map[string]any{"name": "ora", "engine": "Oracle", "host": "10.0.0.8", "env": "dev", "policy": "strict", "username": "u", "password": "p", "database": "svc"}
	r := app.do(http.MethodPost, "/api/v1/connections", admin, base)
	eq(t, r.Code, 0, "建连接")
	var c struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(r.Data, &c)
	put := func(over map[string]any) int {
		body := map[string]any{}
		for k, v := range base {
			body[k] = v
		}
		for k, v := range over {
			body[k] = v
		}
		return app.do(http.MethodPut, "/api/v1/connections/"+itoa(c.ID), admin, body).Code
	}
	if put(map[string]any{"host": "evil/x?TRACE FILE=/tmp/t#"}) == 0 {
		t.Error("编辑时非法地址应被拒")
	}
	if put(map[string]any{"replicaHost": "evil/x?TRACE FILE=/tmp/t#", "replicaUsername": "r", "replicaPassword": "p"}) == 0 {
		t.Error("非法的从库地址应被拒")
	}
	eq(t, put(map[string]any{"replicaHost": "10.0.0.9", "replicaUsername": "r", "replicaPassword": "p"}), 0, "合法的从库地址照常")
}
