package bootstrap

// 发布单人人可见的范围:L2 及以上(平台管理员、DBA 负责人、DBA L2)与审计员看全部,
// 其他人只看自己的(2026-10-30 用户确认)。
//
// 判断是发布单专用的 canSeeAllReleases,比终端活动 / 审计用的 canSeeAllActivity 多一个
// l2 —— 后者口径不动:L2 照样只看得到自己的审计行(TestIsolation_AuditIsPerUser)。
// 自定义角色一律不算,哪怕勾了「可审批」。
//
// 别人的单子都把人工闸路由给 owner:这样 l2 / ro 都不在那道闸上,看得见只能是因为
// canSeeAllReleases,而不是 staffsConfirmGate 那条例外。

import (
	"encoding/json"
	"net/http"
	"testing"

	"aegisdb/internal/model"
)

// enableMenu 给某个角色打开一个菜单。发布页的路由闸是 menu("pipeline"),而种子里 ro /
// audit 没有它 —— 不打开,这两个角色连列表接口都进不去,"只看自己的"就测不到判断本身。
func (a *testApp) enableMenu(roleID int64, key string) {
	a.t.Helper()
	res := a.repo.DB().Model(&model.RoleMenu{}).
		Where("role_id = ? AND menu_key = ?", roleID, key).Update("enabled", true)
	if res.Error != nil {
		a.t.Fatalf("enable menu %s: %v", key, res.Error)
	}
	if res.RowsAffected == 0 {
		if err := a.repo.DB().Create(&model.RoleMenu{RoleID: roleID, MenuKey: key, Enabled: true}).Error; err != nil {
			a.t.Fatalf("create menu %s: %v", key, err)
		}
	}
}

func (a *testApp) enableMenuForCode(code, key string) {
	a.t.Helper()
	role, err := a.repo.GetRoleByCode(code)
	if err != nil {
		a.t.Fatalf("role %s: %v", code, err)
	}
	a.enableMenu(role.ID, key)
}

// listedReleaseIDs 用 scope=all 拉列表,返回看到的发布单 id。
func (a *testApp) listedReleaseIDs(token string) map[int64]bool {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/releases?scope=all&pageSize=100", token, nil)
	if r.Code != 0 {
		a.t.Fatalf("list releases: %d %s", r.Code, r.Msg)
	}
	var page struct {
		Items []releaseView `json:"items"`
	}
	_ = json.Unmarshal(r.Data, &page)
	out := map[int64]bool{}
	for _, it := range page.Items {
		out[it.ID] = true
	}
	return out
}

func (a *testApp) canSeeAllReleasesFlag(token string) bool {
	a.t.Helper()
	var me struct {
		CanSeeAllReleases bool `json:"canSeeAllReleases"`
	}
	_ = json.Unmarshal(a.do(http.MethodGet, "/api/v1/auth/me", token, nil).Data, &me)
	return me.CanSeeAllReleases
}

// adminReleaseWaitingOnOwner 由平台管理员提一张单,停在路由给 owner 的人工闸上。
func (a *testApp) adminReleaseWaitingOnOwner(admin, title string) releaseView {
	a.t.Helper()
	stg := a.connIDByEnv(admin, "staging")
	pid := a.createPipeline(admin, "可见性·"+title, "", []map[string]any{
		{"name": "人工确认", "type": "manual", "config": `{"confirmRole":"owner"}`},
		{"name": "执行变更", "type": "execute"},
	})
	r := a.submitRelease(admin, map[string]any{
		"title": title, "pipelineId": pid, "connectionId": stg,
		"sql": "SELECT 1;", "reason": "发布单可见性回归",
	})
	if r.Code != 0 {
		a.t.Fatalf("submit release: %d %s", r.Code, r.Msg)
	}
	var rel releaseView
	_ = json.Unmarshal(r.Data, &rel)
	return a.waitRelease(admin, rel.ID, "waiting", "failed", "success")
}

func TestReleaseVisibility_L2AndAboveSeeEveryone(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	rel := app.adminReleaseWaitingOnOwner(admin, "别人提的单")

	app.enableMenuForCode("audit", "pipeline") // 审计员默认没有发布菜单,开了才测得到判断
	for _, c := range []struct{ who, email string }{
		{"admin(另一位平台管理员)", "wangmin@aegisdb.io"},
		{"owner", "lina@aegisdb.io"},
		{"l2", "chenhao@aegisdb.io"},
		{"audit", "hujun@aegisdb.io"},
	} {
		tok := app.login(c.email, "aegisdb123")
		if !app.canSeeAllReleasesFlag(tok) {
			t.Errorf("%s:/auth/me 的 canSeeAllReleases 应为 true", c.who)
		}
		if !app.listedReleaseIDs(tok)[rel.ID] {
			t.Errorf("%s:scope=all 应列出别人的发布单 %s", c.who, rel.RelNo)
		}
		if got := app.do(http.MethodGet, "/api/v1/releases/"+itoa(rel.ID), tok, nil); got.Code != 0 {
			t.Errorf("%s:应能打开别人的发布单详情,实际 %d %s", c.who, got.Code, got.Msg)
		}
	}

	// scope=mine 仍然只是自己的:能看全部不等于默认塞全部。
	l2 := app.login("chenhao@aegisdb.io", "aegisdb123")
	r := app.do(http.MethodGet, "/api/v1/releases?scope=mine&pageSize=100", l2, nil)
	var page struct {
		Items []releaseView `json:"items"`
	}
	_ = json.Unmarshal(r.Data, &page)
	for _, it := range page.Items {
		if it.ID == rel.ID {
			t.Error("scope=mine 不该列出别人的发布单")
		}
	}
}

func TestReleaseVisibility_OthersOnlySeeTheirOwn(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	rel := app.adminReleaseWaitingOnOwner(admin, "研发看不到的单")

	// 研发只读:给它发布菜单,证明拦它的是可见性判断,而不是菜单闸。
	app.enableMenuForCode("ro", "pipeline")
	ro := app.login("zhaolei@aegisdb.io", "aegisdb123")

	// 自定义角色(新角色默认全 deny、不可审批)不算 L2 及以上。
	custom := app.customRoleUser(admin, "发布观察员", "observer@aegisdb.io", false)

	for _, c := range []struct{ who, tok string }{{"ro", ro}, {"自定义角色", custom}} {
		if app.canSeeAllReleasesFlag(c.tok) {
			t.Errorf("%s:/auth/me 的 canSeeAllReleases 应为 false", c.who)
		}
		if app.listedReleaseIDs(c.tok)[rel.ID] {
			t.Errorf("%s:scope=all 也只能看到自己的,不该列出 %s", c.who, rel.RelNo)
		}
		if got := app.do(http.MethodGet, "/api/v1/releases/"+itoa(rel.ID), c.tok, nil); got.Code == 0 {
			t.Errorf("%s:打开别人的发布单详情应被拒", c.who)
		}
	}

	// 勾了「可审批」的自定义角色也不算:列表照样只有自己的。它能打开**详情**是另一回事 ——
	// 没配确认角色的执行闸本来就交给可审批角色(mayConfirmGate),那是 staffsConfirmGate
	// 的既有例外,不在本次范围内,这里不断言。
	approver := app.customRoleUser(admin, "发布审批员", "approver@aegisdb.io", true)
	if app.canSeeAllReleasesFlag(approver) {
		t.Error("可审批的自定义角色:/auth/me 的 canSeeAllReleases 应为 false")
	}
	if app.listedReleaseIDs(approver)[rel.ID] {
		t.Errorf("可审批的自定义角色:scope=all 也不该列出别人的 %s", rel.RelNo)
	}
}

// customRoleUser 建一个自定义角色(打开发布菜单)和一个只挂它的账号,返回其登录 token。
func (a *testApp) customRoleUser(admin, roleName, email string, canApprove bool) string {
	a.t.Helper()
	cr := a.do(http.MethodPost, "/api/v1/roles", admin, map[string]any{"name": roleName, "canApprove": canApprove})
	eq(a.t, cr.Code, 0, "建自定义角色 "+roleName)
	var role struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(cr.Data, &role)
	a.enableMenu(role.ID, "pipeline")
	eq(a.t, a.do(http.MethodPost, "/api/v1/users", admin, map[string]any{
		"name": roleName, "email": email, "password": "initpass9", "roleIds": []int64{role.ID},
	}).Code, 0, "建只挂 "+roleName+" 的账号")
	return a.login(email, "initpass9")
}
