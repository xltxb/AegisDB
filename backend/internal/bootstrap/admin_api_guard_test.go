package bootstrap

import (
	"net/http"
	"strings"
	"testing"

	"gorm.io/gorm/clause"

	"velagateway/internal/model"
)

// 管理后台拆分(.scratch/admin-user-split 工单 01):管理类接口一律要平台管理员,**不靠菜单**。
//
// 从前这些读接口只校验菜单,而菜单可以勾给任何角色 —— 给 L2 角色勾上权限菜单,它就能读全部
// 用户与角色。这里先把 L2 的菜单全部勾上,证明光有菜单进不来;审计员只能读审计。
//
// 新加管理类接口时把它加进 adminOnlyReads,否则这道网就漏了它。
var adminOnlyReads = []string{
	"/api/v1/roles",
	"/api/v1/roles/1",
	"/api/v1/users",
	"/api/v1/users/1/tags",
	"/api/v1/query-grants",
	"/api/v1/environments/usage",
	"/api/v1/settings",
	"/api/v1/service-accounts",
	"/api/v1/api-clients",
	"/api/v1/online-users",
	"/api/v1/settings/webhook/deliveries",
}

// 全量审计的校验、轨迹、导出:平台管理员与审计员可读,其他人不行。审计列表本身人人可读,
// 但只有这两种角色看得到全部(其他人只看到自己的,见 TestAudit_DBALeadSeesOnlyOwnRecords)。
var auditReads = []string{


	"/api/v1/audit/approval/AP-0001",
	"/api/v1/audit/verify",

}

func grantEveryMenu(t *testing.T, app *testApp, roleCode string) {
	t.Helper()
	var role model.Role
	if err := app.repo.DB().Where("code = ?", roleCode).First(&role).Error; err != nil {
		t.Fatalf("找不到角色 %s: %v", roleCode, err)
	}
	for _, k := range []string{"terminal", "approve", "db", "rules", "envtier", "perms", "audit", "settings", "pipeline", "execwindow", "project", "schemadiff", "query"} {
		row := model.RoleMenu{RoleID: role.ID, MenuKey: k, Enabled: true}
		if err := app.repo.DB().Clauses(clause.OnConflict{UpdateAll: true}).Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
}

func TestAdminAPIs_RequirePlatformAdminNotJustAMenu(t *testing.T) {
	app := newTestApp(t)
	grantEveryMenu(t, app, "l2")
	grantEveryMenu(t, app, "audit")
	admin := app.login("linwei@vela.io", "vela123")
	l2 := app.login("chenhao@vela.io", "vela123")
	auditor := app.login("hujun@vela.io", "vela123")

	for _, p := range adminOnlyReads {
		if r := app.do(http.MethodGet, p, l2, nil); r.Code != 40300 {
			t.Errorf("L2(菜单全勾)读 %s 应 403,实际 code=%d", p, r.Code)
		}
		if r := app.do(http.MethodGet, p, auditor, nil); r.Code != 40300 {
			t.Errorf("审计员读 %s 应 403,实际 code=%d", p, r.Code)
		}
		if r := app.do(http.MethodGet, p, admin, nil); r.Code == 40300 || r.Code == 40100 {
			t.Errorf("平台管理员读 %s 不该被拦,实际 code=%d msg=%s", p, r.Code, r.Msg)
		}
	}
	// 导出返回的是 CSV 文件(不是 JSON 信封),只验被拦的一方;放行一方与上面几个审计接口同一道闸
	if r := app.do(http.MethodGet, "/api/v1/audit/export", l2, nil); r.Code != 40300 {
		t.Errorf("L2 导出全量审计应 403,实际 code=%d", r.Code)
	}
	for _, p := range auditReads {
		if r := app.do(http.MethodGet, p, l2, nil); r.Code != 40300 {
			t.Errorf("L2 读全量审计 %s 应 403,实际 code=%d", p, r.Code)
		}
		if r := app.do(http.MethodGet, p, auditor, nil); r.Code == 40300 || r.Code == 40100 {
			t.Errorf("审计员读 %s 不该被拦,实际 code=%d msg=%s", p, r.Code, r.Msg)
		}
		if r := app.do(http.MethodGet, p, admin, nil); r.Code == 40300 || r.Code == 40100 {
			t.Errorf("平台管理员读 %s 不该被拦,实际 code=%d msg=%s", p, r.Code, r.Msg)
		}
	}
}

// 用户后台还要用的读接口保持共享:收紧管理接口不能误伤终端、发布、导出。
func TestAdminAPIs_SharedReadsStayOpen(t *testing.T) {
	app := newTestApp(t)
	l2 := app.login("chenhao@vela.io", "vela123")
	for _, p := range []string{
		"/api/v1/connections", "/api/v1/tags", "/api/v1/projects", "/api/v1/env-tiers", "/api/v1/environments",
		"/api/v1/risk-commands", "/api/v1/sql-review/rules", "/api/v1/sensitive-columns", "/api/v1/pipelines",
		"/api/v1/exec-windows", "/api/v1/auth/session-policy",
	} {
		if r := app.do(http.MethodGet, p, l2, nil); r.Code != 0 {
			t.Errorf("用户后台要用的 %s 不该被拦,L2 得到 code=%d msg=%s", p, r.Code, r.Msg)
		}
	}
}

// 会话策略人人可读,时长跟着系统设置走 —— 从前非管理员读不到 /settings,一律按默认 15 分钟锁定。
func TestSessionPolicy_ReadableByEveryoneAndFollowsSettings(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	app.setSettings(admin, map[string]any{"security.idleMinutes": 42, "security.idleLock": true})
	ro := app.login("zhaolei@vela.io", "vela123")
	r := app.do(http.MethodGet, "/api/v1/auth/session-policy", ro, nil)
	eq(t, r.Code, 0, "只读用户读会话策略: "+r.Msg)
	if got := string(r.Data); !containsAll(got, `"idleLock":true`, `"idleMinutes":42`) {
		t.Errorf("会话策略应反映设置里的 42 分钟,实际 %s", got)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, x := range subs {
		if !strings.Contains(s, x) {
			return false
		}
	}
	return true
}

// 谁看得到所有人的审计(2026-10-14 用户确认):平台管理员(L0)、DBA 负责人(L1)、审计员;
// 其他人只看自己的。DBA 负责人进不了管理后台,在用户后台的列表上看全员;单据轨迹、导出、
// 链校验也给 DBA 负责人(2026-10-26 回归 18)。
func TestAudit_DBALeadSeesEveryoneOthersOnlyOwn(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	owner := app.login("zhangwei@vela.io", "vela123")
	l2 := app.login("chenhao@vela.io", "vela123")
	auditor := app.login("hujun@vela.io", "vela123")
	conn := app.connIDByName(admin, "order-cluster")
	app.do(http.MethodPost, "/api/v1/terminal/exec", admin, map[string]any{"connectionId": conn, "sql": "SELECT 1 FROM t"})
	app.do(http.MethodPost, "/api/v1/terminal/exec", owner, map[string]any{"connectionId": conn, "sql": "SELECT 2 FROM t"})
	app.do(http.MethodPost, "/api/v1/terminal/exec", l2, map[string]any{"connectionId": conn, "sql": "SELECT 3 FROM t"})

	actors := func(tok string) map[string]bool {
		seen := map[string]bool{}
		for _, r := range app.auditActorRows(tok) {
			seen[r.Actor] = true
		}
		return seen
	}
	for name, tok := range map[string]string{"DBA 负责人": owner, "审计员": auditor} {
		if seen := actors(tok); !seen["Lin Wei"] || !seen["Zhang Wei"] || !seen["Chen Hao"] {
			t.Errorf("%s应看到所有人的记录,实际 %v", name, seen)
		}
	}
	for actor := range actors(l2) {
		if actor != "Chen Hao" {
			t.Errorf("DBA L2 只该看到自己的记录,看到了 %q 的", actor)
		}
	}
	// 2026-10-26 回归 18 改了这一条的期望:原先断言 owner 导出 40300;现在 DBA 负责人恢复审计导出、
	// 审计链校验、单据轨迹(且不受管理后台空闲 / IP 白名单约束 —— 它不在管理后台用这些)。
	csv := string(app.raw(http.MethodGet, "/api/v1/audit/export", owner))
	if !strings.Contains(csv, "Lin Wei") || !strings.Contains(csv, "Chen Hao") {
		t.Errorf("DBA 负责人导出应得到全员审计 CSV,实际前 200 字节: %.200s", csv)
	}
	eq(t, app.do(http.MethodGet, "/api/v1/audit/verify", owner, nil).Code, 0, "DBA 负责人可校验审计链")
	if r := app.do(http.MethodGet, "/api/v1/audit/approval/AP-0001", owner, nil); r.Code == 40300 || r.Code == 40100 {
		t.Errorf("DBA 负责人看单据轨迹不该被拦,实际 code=%d msg=%s", r.Code, r.Msg)
	}
	// 原行为保留:其他人(L2)仍不行。
	if r := app.do(http.MethodGet, "/api/v1/audit/export", l2, nil); r.Code != 40300 {
		t.Errorf("L2 导出全量审计仍应 403,实际 code=%d", r.Code)
	}
	eq(t, app.do(http.MethodGet, "/api/v1/audit/verify", l2, nil).Code, 40300, "L2 仍不能校验审计链")
}

// 「我的操作记录」(mine=1):能看全部的管理员 / 审计员,在这里也只看到自己的;实例下拉同理。
func TestAudit_MineOnlyShowsOwnEvenForAdmins(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	l2 := app.login("chenhao@vela.io", "vela123")
	conn := app.connIDByName(admin, "order-cluster")
	app.do(http.MethodPost, "/api/v1/terminal/exec", admin, map[string]any{"connectionId": conn, "sql": "SELECT 1 FROM t"})
	app.do(http.MethodPost, "/api/v1/terminal/exec", l2, map[string]any{"connectionId": conn, "sql": "SELECT 2 FROM t"})

	r := app.do(http.MethodGet, "/api/v1/audit?mine=1&range=24h", admin, nil)
	eq(t, r.Code, 0, "mine=1: "+r.Msg)
	if strings.Contains(string(r.Data), "Chen Hao") {
		t.Errorf("管理员的「我的操作记录」不该出现别人的记录: %s", r.Data)
	}
	if !strings.Contains(string(r.Data), "Lin Wei") {
		t.Errorf("应看到自己的记录: %s", r.Data)
	}
	all := app.do(http.MethodGet, "/api/v1/audit?range=24h", admin, nil)
	if !strings.Contains(string(all.Data), "Chen Hao") {
		t.Errorf("不带 mine 时管理员应看到全部: %s", all.Data)
	}
}
