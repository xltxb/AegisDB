package bootstrap

// 查询授权 · 开放接口(外部审批系统审批完调网关下发)。
//
// 钉住的是这扇门**不开**什么:没勾授权范围的凭据进不来;主库(非业务查询源)授不了;
// 不存在的库授不了;行数、超时、有效期越不过全局上限;授权不给服务账号、不给停用账号;
// 以及业务查询角色本身什么都不开放 —— 查询权只来自授权。

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"velagateway/internal/model"
	"velagateway/pkg/crypto"
)

type grantView struct {
	ID                  int64  `json:"id"`
	Email               string `json:"email"`
	Source              string `json:"source"`
	Database            string `json:"database"`
	Status              string `json:"status"`
	MaxRows             int    `json:"maxRows"`
	EffectiveMaxRows    int    `json:"effectiveMaxRows"`
	EffectiveTimeoutSec int    `json:"effectiveTimeoutSec"`
	AllowExport         bool   `json:"allowExport"`
	ExternalID          string `json:"externalId"`
	Client              string `json:"client"`
}

type grantEnv struct {
	app    *testApp
	admin  string
	token  string // 带 grant:read + grant:write 的凭据
	sa     string // 凭据绑定的服务账号邮箱
	source int64  // 已标记为业务查询源的实例
	main   int64  // 没标记的实例(相当于主库)
	db     string // source 上真实存在的一个库
}

func newGrantEnv(t *testing.T) *grantEnv {
	t.Helper()
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	e := &grantEnv{app: app, admin: admin}
	// 凭据不能绑管理员(那会让一串密钥拥有管理员权限),绑一个服务账号。owner 角色不带标签,
	// 即不限实例 —— 外部审批系统通常就是这样配的。
	owner, _ := app.repo.GetRoleByCode("owner")
	e.sa = app.createServiceAccount(admin, "审批系统", []int64{owner.ID}, nil).Email
	e.token = app.issueClient(admin, "审批系统", e.sa, []string{"grant:read", "grant:write"})
	e.source = app.newMySQLConn(admin, "biz-replica")
	e.main = app.newMySQLConn(admin, "biz-primary")
	eq(t, app.do(http.MethodPatch, "/api/v1/connections/"+itoa(e.source), admin, map[string]any{"bizQuery": true}).Code, 0, "标记业务查询源")
	// 库名取自元数据缓存(生产上由元数据同步写入);这里直接写两台实例的缓存。
	for _, row := range []struct {
		conn int64
		db   string
	}{{e.source, "finance_db"}, {e.source, "orders_db"}, {e.main, "finance_db"}} {
		if err := app.repo.DB().Create(&model.MetaTable{ConnectionID: row.conn, DBName: row.db, Name: "t1", Kind: "table", SyncedAt: time.Now()}).Error; err != nil {
			t.Fatal(err)
		}
	}

	r := app.do(http.MethodGet, "/api/v1/open/query-sources/"+itoa(e.source)+"/databases", e.token, nil)
	eq(t, r.Code, 0, "列库名")
	var dbs []string
	_ = json.Unmarshal(r.Data, &dbs)
	if len(dbs) == 0 {
		t.Fatal("业务查询源上应当列得出库名")
	}
	e.db = dbs[0]
	return e
}

func (a *testApp) newMySQLConn(admin, name string) int64 {
	a.t.Helper()
	r := a.do(http.MethodPost, "/api/v1/connections", admin, map[string]any{
		"name": name, "engine": "MySQL 8.0", "host": "10.9.0.1:3306", "env": "prod", "policy": "audit-only",
	})
	if r.Code != 0 {
		a.t.Fatalf("create connection: code=%d msg=%s", r.Code, r.Msg)
	}
	var c struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(r.Data, &c)
	return c.ID
}

func (e *grantEnv) put(body map[string]any) apiResp {
	return e.app.do(http.MethodPut, "/api/v1/open/query-grants", e.token, body)
}

func (e *grantEnv) body(email, ext string) map[string]any {
	return map[string]any{
		"externalId": ext, "email": email, "sourceId": e.source, "database": e.db,
		"endsAt": time.Now().Add(30 * 24 * time.Hour).Format(time.RFC3339), "approver": "王经理", "reason": "月度对账",
	}
}

func grantOf(t *testing.T, r apiResp) (grantView, bool) {
	t.Helper()
	if r.Code != 0 {
		t.Fatalf("下发失败: code=%d msg=%s", r.Code, r.Msg)
	}
	var d struct {
		Created bool      `json:"created"`
		Grant   grantView `json:"grant"`
	}
	_ = json.Unmarshal(r.Data, &d)
	return d.Grant, d.Created
}

// ---------------------------------------------------------------- 凭据与实例

func TestQueryGrant_ScopesMustBeExplicit(t *testing.T) {
	e := newGrantEnv(t)
	dflt := e.app.issueClient(e.admin, "CI", e.sa, nil) // 没勾范围 = 默认集合
	if r := e.app.do(http.MethodGet, "/api/v1/open/query-sources", dflt, nil); r.Code == 0 {
		t.Error("没显式勾选 grant:read 的凭据不该列得出业务查询源")
	}
	if r := e.app.do(http.MethodPut, "/api/v1/open/query-grants", dflt, e.body("a@corp.io", "X-1")); r.Code == 0 {
		t.Error("没显式勾选 grant:write 的凭据不该能下发授权")
	}
	ro := e.app.issueClient(e.admin, "只读", e.sa, []string{"grant:read"})
	if r := e.app.do(http.MethodPut, "/api/v1/open/query-grants", ro, e.body("a@corp.io", "X-1")); r.Code == 0 {
		t.Error("只有 grant:read 的凭据不该能下发")
	}
}

func TestQueryGrant_OnlyBizSourcesAreListed(t *testing.T) {
	e := newGrantEnv(t)
	r := e.app.do(http.MethodGet, "/api/v1/open/query-sources", e.token, nil)
	var srcs []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	_ = json.Unmarshal(r.Data, &srcs)
	if len(srcs) != 1 || srcs[0].ID != e.source {
		t.Fatalf("只应列出标记过的业务查询源,实际 %+v", srcs)
	}
	if r := e.app.do(http.MethodGet, "/api/v1/open/query-sources/"+itoa(e.main)+"/databases", e.token, nil); r.Code == 0 {
		t.Error("没标记的实例(主库)不该列得出库名")
	}
	// 服务账号的实例范围被标签限定时,范围外的业务查询源看不到、也授不了
	l2, _ := e.app.repo.GetRoleByCode("l2") // 标签 orders / users,新建的实例不带这些标签
	narrow := e.app.createServiceAccount(e.admin, "窄范围系统", []int64{l2.ID}, nil).Email
	nt := e.app.issueClient(e.admin, "窄范围系统", narrow, []string{"grant:read", "grant:write"})
	var seen []struct{ ID int64 }
	_ = json.Unmarshal(e.app.do(http.MethodGet, "/api/v1/open/query-sources", nt, nil).Data, &seen)
	if len(seen) != 0 {
		t.Errorf("标签范围外的业务查询源不该列出来,实际 %+v", seen)
	}
	if r := e.app.do(http.MethodPut, "/api/v1/open/query-grants", nt, e.body("z@corp.io", "OA-Z")); r.Code == 0 {
		t.Error("标签范围外的实例不该授得了")
	}

	ro := e.app.login("chenhao@vela.io", "vela123")
	if r := e.app.do(http.MethodPatch, "/api/v1/connections/"+itoa(e.main), ro, map[string]any{"bizQuery": true}); r.Code == 0 {
		t.Error("非管理员不该能标记业务查询源")
	}
	var n int64
	e.app.repo.DB().Model(&model.AuditLog{}).Where("command LIKE ?", "bizquery.source %").Count(&n)
	if n == 0 {
		t.Error("标记业务查询源应写审计")
	}
}

// ---------------------------------------------------------------- 下发

func TestQueryGrant_ProvisionsZeroPrivilegeUser(t *testing.T) {
	e := newGrantEnv(t)
	g, created := grantOf(t, e.put(e.body("Biz.Analyst@Corp.io", "OA-1001")))
	if !created || g.Status != "active" || g.Email != "biz.analyst@corp.io" || g.Source != "biz-replica" || g.Database != e.db {
		t.Fatalf("新授权不对: created=%v %+v", created, g)
	}
	eq(t, g.EffectiveMaxRows, 1000, "不填行数上限用默认 1000")
	eq(t, g.EffectiveTimeoutSec, 30, "不填超时用默认 30 秒")
	eq(t, g.Client, "审批系统", "记下是哪个外部系统下发的")

	u, err := e.app.repo.FindUserByEmailCI("biz.analyst@corp.io")
	if err != nil {
		t.Fatalf("应自动建号: %v", err)
	}
	role, _ := e.app.repo.GetRoleByCode(model.RoleCodeBizQuery)
	ids := e.app.repo.EffectiveRoleIDs(u)
	if len(ids) != 1 || ids[0] != role.ID {
		t.Fatalf("自动建的号只应有业务查询角色,实际 %v", ids)
	}
	if conns, _ := e.app.svc.AccessibleConnections(u); len(conns) != 0 {
		t.Fatalf("业务查询角色本身不该开放任何实例,实际能看到 %d 台", len(conns))
	}
	if r := e.app.do(http.MethodPost, "/api/v1/auth/login", "", map[string]any{"email": "biz.analyst@corp.io", "password": ""}); r.Code == 0 {
		t.Error("自动建的号没有口令,不该能登录")
	}
	if e.app.svc.LiveQueryGrant(u.ID, e.source, e.db) == nil {
		t.Error("授权应当立即生效")
	}

	// 同一个人、同一个库再下发:更新同一条,不新建
	b := e.body("biz.analyst@corp.io", "OA-1002")
	b["maxRows"], b["allowExport"] = 5000, true
	g2, created2 := grantOf(t, e.put(b))
	if created2 || g2.ID != g.ID || g2.MaxRows != 5000 || !g2.AllowExport || g2.ExternalID != "OA-1002" {
		t.Fatalf("再次下发应更新同一条: created=%v %+v", created2, g2)
	}
	var n int64
	e.app.repo.DB().Model(&model.AuditLog{}).Where("command LIKE ?", "bizquery.grant %").Count(&n)
	eq(t, n, int64(2), "每次下发 / 更新都写一条审计")
	e.app.repo.DB().Model(&model.AuditLog{}).Where("command LIKE ?", "bizquery.provision %").Count(&n)
	eq(t, n, int64(1), "自动建号写审计")
}

// 决策 10:业务方等 SRD 授权登录上线后再开放,不给本地口令。只挂业务查询角色的账号 ——
// 管理员设不了口令、后台建不了带口令的号;哪怕库里已经有一个口令(修复前设的),本地登录也不放行。
func TestQueryGrant_BizOnlyAccountHasNoLocalPassword(t *testing.T) {
	e := newGrantEnv(t)
	grantOf(t, e.put(e.body("nopw@corp.io", "OA-P")))
	u, _ := e.app.repo.FindUserByEmailCI("nopw@corp.io")

	if r := e.app.do(http.MethodPost, "/api/v1/users/"+itoa(u.ID)+"/password", e.admin, map[string]any{"password": "Biz-Query-2026!"}); r.Code == 0 {
		t.Error("管理员不该能给只有业务查询角色的账号设本地口令")
	}
	role, _ := e.app.repo.GetRoleByCode(model.RoleCodeBizQuery)
	if r := e.app.do(http.MethodPost, "/api/v1/users", e.admin, map[string]any{
		"email": "newbiz@corp.io", "password": "Biz-Query-2026!", "roleIds": []int64{role.ID},
	}); r.Code == 0 {
		t.Error("后台不该能建只有业务查询角色、带本地口令的账号")
	}

	hash, _ := crypto.HashPassword("Legacy-Pass-2026!")
	if err := e.app.repo.UpdateUserPassword(u.ID, hash); err != nil {
		t.Fatal(err)
	}
	if r := e.app.loginRaw("nopw@corp.io", "Legacy-Pass-2026!"); r.Code == 0 {
		t.Error("只有业务查询角色的账号即使库里有口令,也不该能用本地口令登录")
	}
}

func TestQueryGrant_ExistingUserKeepsPrimaryRole(t *testing.T) {
	e := newGrantEnv(t)
	before, _ := e.app.repo.FindUserByEmailCI("chenhao@vela.io")
	grantOf(t, e.put(e.body("chenhao@vela.io", "OA-7")))
	after, _ := e.app.repo.FindUserByEmailCI("chenhao@vela.io")
	eq(t, after.RoleID, before.RoleID, "主角色不变")
	role, _ := e.app.repo.GetRoleByCode(model.RoleCodeBizQuery)
	has := false
	for _, id := range e.app.repo.EffectiveRoleIDs(after) {
		has = has || id == role.ID
	}
	if !has {
		t.Error("已有账号应追加业务查询角色")
	}
	e.app.login("chenhao@vela.io", "vela123") // 原来的登录方式不受影响
}

func TestQueryGrant_Refusals(t *testing.T) {
	e := newGrantEnv(t)
	_ = e.app.repo.CreateUser(&model.User{Name: "bot", Email: "bot@vela.io", Status: "active", Kind: model.UserKindService})
	with := func(k string, v any) map[string]any { b := e.body("x@corp.io", "OA-9"); b[k] = v; return b }
	without := func(k string) map[string]any { b := e.body("x@corp.io", "OA-9"); delete(b, k); return b }
	cases := []struct {
		what string
		body map[string]any
		want string
	}{
		{"主库(非业务查询源)", with("sourceId", e.main), "不是业务查询源"},
		{"不存在的库", with("database", "no_such_db"), "没有库"},
		{"行数上限超过全局上限", with("maxRows", 10001), "maxRows"},
		{"超时超过全局上限", with("timeoutSec", 301), "timeoutSec"},
		{"没有到期时间", without("endsAt"), "endsAt"},
		{"有效期超过 90 天", with("endsAt", time.Now().Add(91*24*time.Hour).Format(time.RFC3339)), "90"},
		{"到期时间已过", with("endsAt", time.Now().Add(-time.Hour).Format(time.RFC3339)), "endsAt"},
		{"没有外部单号", without("externalId"), "externalId"},
		{"服务账号", with("email", "bot@vela.io"), "服务账号"},
		{"停用账号", with("email", "qianyi@vela.io"), "停用"},
	}
	for _, c := range cases {
		r := e.put(c.body)
		if r.Code == 0 {
			t.Errorf("%s:应当拒绝", c.what)
			continue
		}
		if !strings.Contains(r.Msg, c.want) {
			t.Errorf("%s:拒绝原因应含 %q,实际 %q", c.what, c.want, r.Msg)
		}
	}
	var n int64
	e.app.repo.DB().Model(&model.QueryGrant{}).Count(&n)
	eq(t, n, int64(0), "被拒绝的下发一条都不落库")
}

// externalId 做幂等:同一张审批单重发不改任何东西(也不让已撤销的复活);换一张新单才更新;
// 已经被新单取代的旧单再发过来,拒绝 —— 否则一次延迟到达的重试就能把新批的条件改回旧的。
func TestQueryGrant_ExternalIDIsIdempotent(t *testing.T) {
	e := newGrantEnv(t)
	with := func(ext string, rows int) map[string]any { b := e.body("idem@corp.io", ext); b["maxRows"] = rows; return b }

	g, created := grantOf(t, e.put(with("OA-1", 100)))
	if !created || g.MaxRows != 100 {
		t.Fatalf("首次下发: created=%v %+v", created, g)
	}
	g1, created1 := grantOf(t, e.put(with("OA-1", 200)))
	if created1 || g1.ID != g.ID || g1.MaxRows != 100 {
		t.Errorf("同一审批单重发应原样返回、不改动: created=%v %+v", created1, g1)
	}

	eq(t, e.app.do(http.MethodPost, "/api/v1/query-grants/"+itoa(g.ID)+"/revoke", e.admin, map[string]any{"reason": "安全事件"}).Code, 0, "管理员撤销")
	if g2, _ := grantOf(t, e.put(with("OA-1", 100))); g2.Status != "revoked" {
		t.Errorf("同一审批单重发不该让已撤销的授权复活,实际 %s", g2.Status)
	}

	g3, _ := grantOf(t, e.put(with("OA-2", 300)))
	if g3.ID != g.ID || g3.Status != "active" || g3.MaxRows != 300 || g3.ExternalID != "OA-2" {
		t.Errorf("新审批单应更新并重新生效: %+v", g3)
	}

	r := e.put(with("OA-1", 100))
	if r.Code == 0 || !strings.Contains(r.Msg, "OA-2") {
		t.Errorf("被取代的旧审批单应拒绝并说明被哪张取代,实际 %d %q", r.Code, r.Msg)
	}
	if cur, _ := grantOf(t, e.put(with("OA-2", 300))); cur.MaxRows != 300 || cur.ExternalID != "OA-2" {
		t.Errorf("旧单被拒后授权应保持新单的条件: %+v", cur)
	}

	var n int64
	e.app.repo.DB().Model(&model.AuditLog{}).Where("command LIKE ?", "bizquery.grant %").Count(&n)
	eq(t, n, int64(2), "只有真正改动的两次(OA-1 首发、OA-2)写下发审计")
}

// ADR-0006:开放接口写的审计,operator 是 "API:<凭据名>" —— 审计里要看得出是哪个外部系统做的,
// 不只是它绑定的服务账号。控制台管理员的操作 operator 为空。
func TestQueryGrant_AuditOperatorNamesTheClient(t *testing.T) {
	e := newGrantEnv(t)
	g, _ := grantOf(t, e.put(e.body("aud@corp.io", "OA-AUD")))                 // 下发 + 自动建号
	grantOf(t, e.put(e.body("chenhao@vela.io", "OA-AUD2")))                    // 已有账号追加角色
	e.app.do(http.MethodDelete, "/api/v1/open/query-grants/"+itoa(g.ID), e.token, nil) // 外部撤销
	g2, _ := grantOf(t, e.put(e.body("aud2@corp.io", "OA-AUD3")))
	e.app.do(http.MethodPost, "/api/v1/query-grants/"+itoa(g2.ID)+"/revoke", e.admin, map[string]any{"reason": "x"})

	op := func(like string) []string {
		var ops []string
		e.app.repo.DB().Model(&model.AuditLog{}).Where("command LIKE ?", like).Order("id").Pluck("operator", &ops)
		return ops
	}
	for _, like := range []string{"bizquery.grant %", "bizquery.provision %", "bizquery.role %"} {
		for _, o := range op(like) {
			eq(t, o, "API:审批系统", like+" 的 operator")
		}
	}
	rev := op("bizquery.revoke %")
	if len(rev) != 2 || rev[0] != "API:审批系统" || rev[1] != "" {
		t.Errorf("外部撤销 operator 应为 API:审批系统、管理员撤销为空,实际 %q", rev)
	}
}

// 开放接口上,一个外部系统只能看到、撤销自己下发的授权。
func TestQueryGrant_OtherClientCannotSeeOrRevoke(t *testing.T) {
	e := newGrantEnv(t)
	g, _ := grantOf(t, e.put(e.body("mine@corp.io", "OA-M")))
	other := e.app.issueClient(e.admin, "另一个系统", e.sa, []string{"grant:read", "grant:write"})

	if r := e.app.do(http.MethodDelete, "/api/v1/open/query-grants/"+itoa(g.ID), other, nil); r.Code == 0 {
		t.Error("另一个系统不该能按 id 撤销本系统下发的授权")
	}
	u, _ := e.app.repo.FindUserByEmailCI("mine@corp.io")
	if e.app.svc.LiveQueryGrant(u.ID, e.source, e.db) == nil {
		t.Fatal("授权不该被别的系统撤掉")
	}
	var seen []grantView
	_ = json.Unmarshal(e.app.do(http.MethodGet, "/api/v1/open/query-grants", other, nil).Data, &seen)
	if len(seen) != 0 {
		t.Errorf("另一个系统不该列得出本系统下发的授权,实际 %+v", seen)
	}
	var mine []grantView
	_ = json.Unmarshal(e.app.do(http.MethodGet, "/api/v1/open/query-grants", e.token, nil).Data, &mine)
	if len(mine) != 1 || mine[0].ID != g.ID {
		t.Errorf("本系统应列得出自己下发的授权,实际 %+v", mine)
	}
}

// ---------------------------------------------------------------- 撤销 / 到期 / 查询

func TestQueryGrant_RevokeAndExpire(t *testing.T) {
	e := newGrantEnv(t)
	g1, _ := grantOf(t, e.put(e.body("a@corp.io", "OA-A")))
	g2, _ := grantOf(t, e.put(e.body("b@corp.io", "OA-B")))

	eq(t, e.app.do(http.MethodDelete, "/api/v1/open/query-grants/"+itoa(g1.ID), e.token, map[string]any{"reason": "离职"}).Code, 0, "按 id 撤销")
	eq(t, e.app.do(http.MethodDelete, "/api/v1/open/query-grants?externalId=OA-B", e.token, nil).Code, 0, "按外部单号撤销")
	ua, _ := e.app.repo.FindUserByEmailCI("a@corp.io")
	if e.app.svc.LiveQueryGrant(ua.ID, e.source, e.db) != nil {
		t.Error("撤销后应立即失效")
	}
	// 别的外部系统按外部单号撤不到本系统的授权
	other := e.app.issueClient(e.admin, "另一个系统", e.sa, []string{"grant:write"})
	grantOf(t, e.put(e.body("c@corp.io", "OA-C")))
	if r := e.app.do(http.MethodDelete, "/api/v1/open/query-grants?externalId=OA-C", other, nil); r.Code == 0 {
		t.Error("外部单号是各系统自己的:另一个系统不该按它撤到本系统的授权")
	}

	// 到期:有效期一过就失效(不等过期任务),过期任务把状态和审计补上
	b := e.body("d@corp.io", "OA-D")
	b["endsAt"] = time.Now().Add(1200 * time.Millisecond).Format(time.RFC3339Nano)
	grantOf(t, e.put(b))
	time.Sleep(1300 * time.Millisecond)
	ud, _ := e.app.repo.FindUserByEmailCI("d@corp.io")
	if e.app.svc.LiveQueryGrant(ud.ID, e.source, e.db) != nil {
		t.Error("过了有效期就应失效,不等过期任务")
	}
	e.app.svc.ExpireQueryGrants()

	list := func(q string) []grantView {
		r := e.app.do(http.MethodGet, "/api/v1/open/query-grants"+q, e.token, nil)
		var out []grantView
		_ = json.Unmarshal(r.Data, &out)
		return out
	}
	status := map[string]string{}
	for _, g := range list("") {
		status[g.Email] = g.Status
	}
	want := map[string]string{"a@corp.io": "revoked", "b@corp.io": "revoked", "c@corp.io": "active", "d@corp.io": "expired"}
	for k, v := range want {
		if status[k] != v {
			t.Errorf("%s 应为 %s,实际 %s", k, v, status[k])
		}
	}
	if got := list("?status=active"); len(got) != 1 || got[0].Email != "c@corp.io" {
		t.Errorf("按状态筛选不对: %+v", got)
	}
	if got := list("?email=B@CORP.IO"); len(got) != 1 || got[0].ID != g2.ID {
		t.Errorf("按邮箱(不分大小写)筛选不对: %+v", got)
	}
	var n int64
	e.app.repo.DB().Model(&model.AuditLog{}).Where("command LIKE ?", "bizquery.revoke %").Count(&n)
	eq(t, n, int64(2), "两次撤销各写一条审计")
	e.app.repo.DB().Model(&model.AuditLog{}).Where("command LIKE ?", "bizquery.expire %").Count(&n)
	eq(t, n, int64(1), "到期写一条审计")
	e.app.svc.ExpireQueryGrants()
	e.app.repo.DB().Model(&model.AuditLog{}).Where("command LIKE ?", "bizquery.expire %").Count(&n)
	eq(t, n, int64(1), "过期任务再跑一次不重复写")
}

// ---------------------------------------------------------------- 业务查询角色

// 能力矩阵"查无行 = 放行"、标签"无标签 = 不限实例" —— 业务查询角色必须写满拒绝。
func TestQueryGrant_BizRoleOpensNothing(t *testing.T) {
	app := newTestApp(t)
	role, err := app.repo.GetRoleByCode(model.RoleCodeBizQuery)
	if err != nil {
		t.Fatalf("业务查询角色应在启动时建好: %v", err)
	}
	var tiers []model.EnvTier
	app.repo.DB().Find(&tiers)
	if len(tiers) == 0 {
		t.Fatal("前提不成立:没有分层")
	}
	for _, tr := range tiers {
		for _, capName := range model.Capabilities {
			lvl, err := app.repo.CapabilityLevel(role.ID, capName, tr.Code)
			if err != nil || lvl != model.LevelDeny {
				t.Errorf("业务查询角色在 %s/%s 上应为 deny,实际 %q %v", tr.Code, capName, lvl, err)
			}
		}
	}
	allow, unrestricted, _ := app.repo.TagsForRoles([]int64{role.ID})
	if unrestricted || len(allow) != 1 || allow[0] != model.BizQueryNoTag {
		t.Errorf("业务查询角色应只挂占位标签,实际 %v unrestricted=%v", allow, unrestricted)
	}
	var on []string
	app.repo.DB().Model(&model.RoleMenu{}).Where("role_id = ? AND enabled = ?", role.ID, true).Pluck("menu_key", &on)
	if len(on) != 1 || on[0] != "query" {
		t.Errorf("业务查询角色只该打开数据查询页,实际打开 %v", on)
	}
	// 平台管理员也能进数据查询页;其余内置角色不能
	var qs []model.RoleMenu
	app.repo.DB().Where("menu_key = ?", "query").Find(&qs)
	for _, m := range qs {
		r, _ := app.repo.GetRole(m.RoleID)
		want := r.Code == "admin" || r.Code == model.RoleCodeBizQuery
		if m.Enabled != want {
			t.Errorf("query 菜单在角色 %s 上应为 %v", r.Code, want)
		}
	}
}

// M3(2026-10 安全审计):授权已由凭据 A 下发时,凭据 B 用一张新单号重发同一 (用户, 实例, 库),
// 会把条件改掉并把归属改成 B —— A 此后既列不出也撤不掉它。原凭据仍在且启用时拒绝覆盖;
// 原凭据已停用 / 删除时允许接管(凭据轮换)。
func TestQueryGrant_OtherClientCannotTakeOverLiveGrant(t *testing.T) {
	e := newGrantEnv(t)
	g, _ := grantOf(t, e.put(e.body("mine@corp.io", "OA-A1")))
	other := e.app.issueClient(e.admin, "另一个系统", e.sa, []string{"grant:read", "grant:write"})

	r := e.app.do(http.MethodPut, "/api/v1/open/query-grants", other, e.body("mine@corp.io", "OB-1"))
	if r.Code == 0 {
		t.Fatal("原凭据仍启用时,另一把凭据不应能覆盖它下发的授权")
	}
	var mine []grantView
	_ = json.Unmarshal(e.app.do(http.MethodGet, "/api/v1/open/query-grants", e.token, nil).Data, &mine)
	if len(mine) != 1 || mine[0].ID != g.ID || mine[0].ExternalID != "OA-A1" {
		t.Fatalf("授权应仍归原凭据、条件不变,实际 %+v", mine)
	}

	// 原凭据停用(轮换):新凭据可以接管
	a := e.app.apiClientByName(e.admin, "审批系统")
	if err := e.app.repo.UpdateAPIClientFields(a.ID, map[string]any{"enabled": false}); err != nil {
		t.Fatal(err)
	}
	g2, _ := grantOf(t, e.app.do(http.MethodPut, "/api/v1/open/query-grants", other, e.body("mine@corp.io", "OB-2")))
	eq(t, g2.ID, g.ID, "接管的是同一条授权")
	eq(t, g2.ExternalID, "OB-2", "接管后归新凭据的单号")
}
