package bootstrap

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"aegisdb/internal/model"
	"aegisdb/internal/service"
	"aegisdb/pkg/totp"
)

// V-09 / V-11:重音变体不是这个账号。MySQL ai_ci 排序规则下 LOWER(email)=? 会匹配变体,测试存储库(PostgreSQL)不会,
// 所以这里直接测比较函数覆盖的那一层:用真实流程走一遍精确邮箱,确认原有登录不受影响。
func TestV11_LoginExactEmailUnchanged(t *testing.T) {
	app := newTestApp(t)
	if tok := app.login("linwei@aegisdb.io", "aegisdb123"); tok == "" {
		t.Fatal("原邮箱应能登录")
	}
	if r := app.loginRaw("linwéi@aegisdb.io", "aegisdb123"); r.Code == 0 {
		t.Fatal("重音变体不是这个账号")
	}
}

// V-10:够不到的实例与不存在的实例同一个回答,不带实例名。
func TestV10_SchemaDiff_NoInstanceNameOracle(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	uid := app.userIDByEmail(admin, "chenhao@aegisdb.io")
	eq(t, app.do(http.MethodPut, "/api/v1/users/"+itoa(uid)+"/tags", admin, map[string]any{"tags": []string{"__nothing__"}}).Code, 0, "把范围收窄到什么都够不到")
	roleID := app.roleIDByCode(admin, "l2")
	eq(t, app.do(http.MethodPut, "/api/v1/roles/"+itoa(roleID)+"/menus", admin, map[string]any{"menus": map[string]bool{"schemadiff": true}}).Code, 0, "开菜单")
	u := app.login("chenhao@aegisdb.io", "aegisdb123")

	prod := app.connIDByEnv(admin, "prod")
	reach := app.do(http.MethodGet, "/api/v1/schema-diff/connections/"+itoa(prod)+"/databases", u, nil)
	none := app.do(http.MethodGet, "/api/v1/schema-diff/connections/99999/databases", u, nil)
	eq(t, reach.Code, none.Code, "两种情况错误码相同")
	eq(t, reach.Msg, none.Msg, "两种情况文案相同")
	var conn model.Connection
	if err := app.repo.DB().First(&conn, prod).Error; err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(reach.Data), conn.Name) || strings.Contains(reach.Msg, conn.Name) {
		t.Fatalf("回答不能带实例名:%+v", reach)
	}
	// 同样的话也要用于对比接口
	cmp := app.do(http.MethodPost, "/api/v1/schema-diff", u, map[string]any{
		"left": map[string]any{"connectionId": prod}, "right": map[string]any{"connectionId": prod},
	})
	if strings.Contains(cmp.Msg, conn.Name) {
		t.Fatalf("对比接口不能带实例名:%+v", cmp)
	}
}

// V-15:最后一位管理员不能被收回角色;还有别的管理员时照常。admin 角色的 perms 菜单不能关,别的菜单和别的角色不受影响。
func TestV15_LastAdminProtected(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	adminRole := app.roleIDByCode(admin, "admin")
	ownerRole := app.roleIDByCode(admin, "owner")
	me := app.userIDByEmail(admin, "linwei@aegisdb.io")
	for _, id := range app.repo.ActiveAdminIDs() { // 种子里有两位管理员:先把别的停掉
		if id != me {
			eq(t, app.do(http.MethodPatch, "/api/v1/users/"+itoa(id), admin, map[string]any{"status": "disabled"}).Code, 0, "停用另一位管理员")
		}
	}
	if ids := app.repo.ActiveAdminIDs(); len(ids) != 1 || ids[0] != me {
		t.Fatalf("前提:只有一位活跃管理员,实际 %v", ids)
	}

	if r := app.do(http.MethodDelete, "/api/v1/roles/"+itoa(adminRole)+"/members/"+itoa(me), admin, nil); r.Code == 0 {
		t.Fatal("不能移除最后一位管理员的 admin 角色")
	}
	if r := app.do(http.MethodPatch, "/api/v1/users/"+itoa(me), admin, map[string]any{"roleIds": []int64{ownerRole}}); r.Code == 0 {
		t.Fatal("不能整组替换掉最后一位管理员的 admin 角色")
	}
	if len(app.repo.ActiveAdminIDs()) != 1 {
		t.Fatal("被拒之后管理员还在")
	}

	// 指派第二位管理员之后,前一位就可以交出角色了。
	other := app.userIDByEmail(admin, "chenhao@aegisdb.io")
	eq(t, app.do(http.MethodPatch, "/api/v1/users/"+itoa(other), admin, map[string]any{"roleIds": []int64{adminRole}}).Code, 0, "指派第二位管理员")
	eq(t, app.do(http.MethodPatch, "/api/v1/users/"+itoa(me), admin, map[string]any{"roleIds": []int64{ownerRole, adminRole}}).Code, 0, "保留 admin 的变更照常")
	admin2 := app.login("chenhao@aegisdb.io", "aegisdb123")
	eq(t, app.do(http.MethodPatch, "/api/v1/users/"+itoa(me), admin2, map[string]any{"roleIds": []int64{ownerRole}}).Code, 0, "还有别的管理员时可以收回")
}

func TestV15_AdminPermsMenuCannotBeClosed(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	adminRole := app.roleIDByCode(admin, "admin")
	menus := func(id int64, m map[string]bool) int {
		return app.do(http.MethodPut, "/api/v1/roles/"+itoa(id)+"/menus", admin, map[string]any{"menus": m}).Code
	}
	if menus(adminRole, map[string]bool{"perms": false}) == 0 {
		t.Fatal("不能关 admin 的 perms 菜单")
	}
	eq(t, menus(adminRole, map[string]bool{"perms": true, "project": false}), 0, "别的菜单照常")
	eq(t, menus(adminRole, map[string]bool{"project": true}), 0, "别的菜单照常")
	eq(t, menus(app.roleIDByCode(admin, "l2"), map[string]bool{"perms": false}), 0, "别的角色收回 perms 照常")
	var row model.RoleMenu
	if err := app.repo.DB().Where("role_id = ? AND menu_key = ?", adminRole, "perms").First(&row).Error; err != nil || !row.Enabled {
		t.Fatalf("perms 应还开着:%+v %v", row, err)
	}
}

// V-16:口令登录的 MFA 步骤计入按用户的失败次数,5 次后锁定,对的码也不认;换来源 IP 不重新计数。
func TestV16_LoginMFAFailuresLockAccount(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	target := app.userByEmail(admin, "chenhao@aegisdb.io")
	bind := app.do(http.MethodPost, "/api/v1/users/"+itoa(target.ID)+"/mfa/bind", admin, nil)
	eq(t, bind.Code, 0, "bind mfa")
	var b struct {
		Secret string `json:"secret"`
	}
	_ = json.Unmarshal(bind.Data, &b)
	// 直接调 Service,不经 HTTP 的按 IP 频控:那道闸换 IP 就过了,这里测的正是它之外的这一道。
	for i := 0; i < 5; i++ {
		if _, _, _, err := app.svc.Login("chenhao@aegisdb.io", "aegisdb123", "000000", "203.0.113."+itoa(int64(i))); !errors.Is(err, service.ErrMFAInvalid) {
			t.Fatalf("第 %d 次应是验证码错误,实际 %v", i+1, err)
		}
	}
	code := totp.Code(b.Secret, time.Now())
	if _, _, _, err := app.svc.Login("chenhao@aegisdb.io", "aegisdb123", code, "203.0.113.200"); !errors.Is(err, service.ErrMFALocked) {
		t.Fatalf("锁定后对的码也不认,实际 %v", err)
	}
	// 口令不对的人看不出锁定。
	if _, _, _, err := app.svc.Login("chenhao@aegisdb.io", "wrong-password", code, "203.0.113.200"); !errors.Is(err, service.ErrInvalidCredentials) {
		t.Fatalf("口令错误还是口令错误,实际 %v", err)
	}
}

// 原有行为:偶尔输错一次,下一个对的码能登录,计数清零。
func TestV16_LoginMFASuccessClearsFailures(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	target := app.userByEmail(admin, "chenhao@aegisdb.io")
	bind := app.do(http.MethodPost, "/api/v1/users/"+itoa(target.ID)+"/mfa/bind", admin, nil)
	var b struct {
		Secret string `json:"secret"`
	}
	_ = json.Unmarshal(bind.Data, &b)
	if _, _, _, err := app.svc.Login("chenhao@aegisdb.io", "aegisdb123", "000000", "203.0.113.1"); !errors.Is(err, service.ErrMFAInvalid) {
		t.Fatalf("应是验证码错误,实际 %v", err)
	}
	if _, _, _, err := app.svc.Login("chenhao@aegisdb.io", "aegisdb123", totp.Code(b.Secret, time.Now()), "203.0.113.1"); err != nil {
		t.Fatalf("对的码应能登录,实际 %v", err)
	}
}

// V-03 / V-10 的数据查询半边:没有授权的人拿不到实例名;有过授权的人照旧看到实例名。
func TestV10_BizQuery_NoNameWithoutGrant(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	u := app.login("chenhao@aegisdb.io", "aegisdb123")
	var conn model.Connection
	if err := app.repo.DB().First(&conn, app.connIDByEnv(admin, "prod")).Error; err != nil {
		t.Fatal(err)
	}
	roleID := app.roleIDByCode(admin, "l2")
	eq(t, app.do(http.MethodPut, "/api/v1/roles/"+itoa(roleID)+"/menus", admin, map[string]any{"menus": map[string]bool{"query": true}}).Code, 0, "开菜单")
	r := app.do(http.MethodPost, "/api/v1/bizquery/exec", u, map[string]any{"sourceId": conn.ID, "database": "orders", "sql": "SELECT 1"})
	raw, _ := json.Marshal(r)
	if r.Code == 0 || strings.Contains(string(raw), conn.Name) {
		t.Fatalf("没有授权的人不该看到实例名:%s", raw)
	}
}
