package bootstrap

// 2026-10 黑盒测试 · 工单 03(开放接口与凭据),见 .scratch/blackbox-fixes。
//
// 按 PRD 的取舍:凭据绑定的账号**不在鉴权时追溯**(存量照常工作),只堵新口子 ——
// 建服务账号不许带 admin、给被启用凭据绑定的账号加 admin 时拒绝、新建/改绑凭据只能
// 绑服务账号。其余是凭据管理与开放接口的校验、审计、隔离。

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"aegisdb/internal/model"
)

// issueSA 建一个服务账号并签发一把凭据,返回 (服务账号, 凭据 id, token)。
func (a *testApp) issueSA(admin, saName, clientName string, roleCodes []string, scopes []string, pipelineID int64) (svcAccountView, int64, string) {
	a.t.Helper()
	ids := make([]int64, 0, len(roleCodes))
	for _, c := range roleCodes {
		ids = append(ids, a.roleIDByCode(admin, c))
	}
	sa := a.createServiceAccount(admin, saName, ids, nil)
	id, tok := a.issueToSA(admin, sa.ID, clientName, scopes, pipelineID)
	return sa, id, tok
}

// issueToSA 给已有服务账号再签一把凭据。
func (a *testApp) issueToSA(admin string, saID int64, clientName string, scopes []string, pipelineID int64) (int64, string) {
	a.t.Helper()
	body := map[string]any{"name": clientName, "userId": saID, "scopes": scopes}
	if pipelineID > 0 {
		body["pipelineId"] = pipelineID
	}
	r := a.do(http.MethodPost, "/api/v1/api-clients", admin, body)
	if r.Code != 0 {
		a.t.Fatalf("issue client: code=%d msg=%s", r.Code, r.Msg)
	}
	var out struct {
		Client struct {
			ID int64 `json:"id"`
		} `json:"client"`
		Token string `json:"token"`
	}
	_ = json.Unmarshal(r.Data, &out)
	return out.Client.ID, out.Token
}

// ---------------------------------------------------------------- 1. 凭据绑定管理员的口子

func TestBB03_ServiceAccountCannotBeBornAdmin(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	adminRole := app.roleIDByCode(admin, "admin")
	owner := app.roleIDByCode(admin, "owner")

	for _, ids := range [][]int64{{adminRole}, {owner, adminRole}} {
		r := app.do(http.MethodPost, "/api/v1/service-accounts", admin, map[string]any{
			"name": "影子管理员机器人", "roleIds": ids,
		})
		if r.Code != resp400 {
			t.Fatalf("建服务账号带 admin 角色应 40001,got code=%d msg=%s", r.Code, r.Msg)
		}
	}
}

func TestBB03_AddingAdminToCredentialBoundAccountRefused(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	adminRole := app.roleIDByCode(admin, "admin")
	owner := app.roleIDByCode(admin, "owner")

	sa, _, _ := app.issueSA(admin, "bound-bot", "绑定中的凭据", []string{"owner"}, []string{"release:read"}, 0)

	// 多角色路径、单角色路径、角色成员页三个入口都要挡。
	r := app.do(http.MethodPatch, "/api/v1/users/"+itoa(sa.ID), admin, map[string]any{"roleIds": []int64{owner, adminRole}})
	if r.Code == 0 {
		t.Fatal("PATCH roleIds 给被凭据绑定的服务账号加 admin 应被拒")
	}
	if !strings.Contains(r.Msg, "凭据") {
		t.Errorf("拒绝理由应指向凭据(先停用/改绑),got %q", r.Msg)
	}
	r = app.do(http.MethodPatch, "/api/v1/users/"+itoa(sa.ID), admin, map[string]any{"roleId": adminRole})
	if r.Code == 0 {
		t.Fatal("PATCH roleId 给被凭据绑定的服务账号加 admin 应被拒")
	}
	r = app.do(http.MethodPost, "/api/v1/roles/"+itoa(adminRole)+"/members", admin, map[string]any{"userId": sa.ID})
	if r.Code == 0 {
		t.Fatal("角色成员页把被凭据绑定的账号加进 admin 应被拒")
	}
	for _, id := range app.rolesOfID(sa.ID) {
		if id == adminRole {
			t.Fatal("被拒之后账号不应带上 admin")
		}
	}
	// 普通角色变更不受影响。
	r = app.do(http.MethodPatch, "/api/v1/users/"+itoa(sa.ID), admin, map[string]any{"roleIds": []int64{owner}})
	eq(t, r.Code, 0, "不涉及 admin 的角色变更照常")
}

// 存量:绑在真人名下的凭据照常工作(不追溯),但给这个人加 admin 要先停用凭据。
func TestBB03_LegacyHumanBoundCredentialGuardsAdminGrant(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	adminRole := app.roleIDByCode(admin, "admin")
	uid := app.userIDByEmail(admin, "chenhao@aegisdb.io")
	tok := app.issueClient(admin, "存量凭据", "chenhao@aegisdb.io", []string{"release:read"})

	// 存量照常可用。
	eq(t, app.openDo(http.MethodGet, "/api/v1/open/instances", tok, nil).Code, 0, "存量凭据不追溯")

	r := app.do(http.MethodPatch, "/api/v1/users/"+itoa(uid), admin, map[string]any{"roleIds": []int64{adminRole}})
	if r.Code == 0 {
		t.Fatal("给被启用凭据绑定的真人加 admin 应被拒")
	}
	// 停用那把凭据后放行。
	cl := app.apiClientByName(admin, "存量凭据")
	eq(t, app.do(http.MethodPut, "/api/v1/api-clients/"+itoa(cl.ID), admin, map[string]any{"enabled": false}).Code, 0, "停用凭据")
	r = app.do(http.MethodPatch, "/api/v1/users/"+itoa(uid), admin, map[string]any{"roleIds": []int64{adminRole}})
	eq(t, r.Code, 0, "凭据停用后可以加 admin")
}

// ---------------------------------------------------------------- 2. 只能绑服务账号

func TestBB03_CredentialBindsServiceAccountsOnly(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	human := app.userIDByEmail(admin, "chenhao@aegisdb.io")

	r := app.do(http.MethodPost, "/api/v1/api-clients", admin, map[string]any{
		"name": "绑真人", "userId": human, "scopes": []string{"release:read"},
	})
	if r.Code != resp400 {
		t.Fatalf("新建凭据绑真人账号应 40001,got %d %s", r.Code, r.Msg)
	}

	_, id, _ := app.issueSA(admin, "rebind-bot", "改绑测试", []string{"owner"}, []string{"release:read"}, 0)
	r = app.do(http.MethodPut, "/api/v1/api-clients/"+itoa(id), admin, map[string]any{"userId": human})
	if r.Code != resp400 {
		t.Fatalf("改绑到真人账号应 40001,got %d %s", r.Code, r.Msg)
	}
}

// ---------------------------------------------------------------- 3. 审计

func bb03Audit(app *testApp, like string) (n int64, rows []model.AuditLog) {
	app.repo.DB().Where("command LIKE ?", like).Order("id").Find(&rows)
	return int64(len(rows)), rows
}

func TestBB03_CredentialLifecycleIsAudited(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	_, id, tok := app.issueSA(admin, "audit-bot", "审计凭据", []string{"owner"}, []string{"release:read"}, 0)
	secret := tok[strings.Index(tok, ".")+1:]

	eq(t, app.do(http.MethodPut, "/api/v1/api-clients/"+itoa(id), admin, map[string]any{"enabled": false}).Code, 0, "update")
	// 第 9 条(文档对齐实现):停用 = 40300,吊销(删除)= 40100。
	eq(t, app.openDo(http.MethodGet, "/api/v1/open/instances", tok, nil).Code, resp403, "停用的凭据 40300")
	eq(t, app.do(http.MethodDelete, "/api/v1/api-clients/"+itoa(id), admin, nil).Code, 0, "delete")
	eq(t, app.openDo(http.MethodGet, "/api/v1/open/instances", tok, nil).Code, 40100, "吊销的凭据 40100")

	for _, verb := range []string{"create", "update", "delete"} {
		n, rows := bb03Audit(app, "admin.apiclient."+verb+" %")
		if n != 1 {
			t.Errorf("凭据 %s 应写一条审计,got %d", verb, n)
			continue
		}
		if rows[0].ActorName != "Lin Wei" {
			t.Errorf("凭据管理审计 actor 应是管理员,got %q", rows[0].ActorName)
		}
		if !strings.Contains(rows[0].Command, "审计凭据") {
			t.Errorf("审计应点名凭据: %s", rows[0].Command)
		}
		if strings.Contains(rows[0].Command, secret) {
			t.Errorf("审计不得记 secret: %s", rows[0].Command)
		}
	}
}

func TestBB03_OpenAbortAndReviewAreAudited(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	pid := app.createPipeline(admin, "审计流程", "", []map[string]any{
		{"name": "执行变更", "type": "execute"},
	})
	_, _, tok := app.issueSA(admin, "abort-bot", "终止方", []string{"owner"},
		[]string{"release:create", "release:read", "review:check"}, pid)

	r := app.openDo(http.MethodPost, "/api/v1/open/releases", tok, map[string]any{
		"title": "待终止", "instance": "sandbox-dev", "sql": "SELECT 1;",
	})
	eq(t, r.Code, 0, "create")
	var rel openRelease
	_ = json.Unmarshal(r.Data, &rel)
	eq(t, app.openDo(http.MethodPost, "/api/v1/open/releases/"+rel.RelNo+"/abort", tok, nil).Code, 0, "abort")

	n, rows := bb03Audit(app, "open.release.abort %")
	if n != 1 {
		t.Fatalf("开放接口终止应写一条审计,got %d", n)
	}
	eq(t, rows[0].Operator, "API:终止方", "operator 为 API:<凭据名>")
	eq(t, rows[0].ActorName, "abort-bot", "actor 为服务账号")
	if !strings.Contains(rows[0].Command, rel.RelNo) {
		t.Errorf("审计应点名单号: %s", rows[0].Command)
	}

	eq(t, app.openDo(http.MethodPost, "/api/v1/open/sql-review", tok, map[string]any{
		"instance": "sandbox-dev", "sql": "SELECT 1;",
	}).Code, 0, "review")
	n, rows = bb03Audit(app, "open.sqlreview %")
	if n != 1 {
		t.Fatalf("/open/sql-review 应写一条审计,got %d", n)
	}
	eq(t, rows[0].Operator, "API:终止方", "operator 为 API:<凭据名>")
}

// ---------------------------------------------------------------- 4. JSON 通道 15MB

func TestBB03_OpenJSONChannelHonors15MB(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	pid := app.createPipeline(admin, "尺寸流程", "", []map[string]any{{"name": "执行变更", "type": "execute"}})
	_, _, tok := app.issueSA(admin, "size-bot", "尺寸", []string{"owner"},
		[]string{"release:create", "release:read", "review:check"}, pid)

	big := strings.Repeat("-- pad\n", (15<<20)/7+16)
	cases := []struct {
		path string
		body map[string]any
	}{
		{"/api/v1/open/releases", map[string]any{"title": "大脚本", "instance": "sandbox-dev", "script": big}},
		{"/api/v1/open/releases", map[string]any{"title": "大脚本", "instance": "sandbox-dev",
			"scriptBase64": base64.StdEncoding.EncodeToString([]byte(big))}},
		{"/api/v1/open/sql-review", map[string]any{"instance": "sandbox-dev", "script": big}},
		{"/api/v1/open/sql-review", map[string]any{"instance": "sandbox-dev",
			"scriptBase64": base64.StdEncoding.EncodeToString([]byte(big))}},
	}
	for i, c := range cases {
		r := app.openDo(http.MethodPost, c.path, tok, c.body)
		if r.Code != resp400 || !strings.Contains(r.Msg, "15MB") {
			t.Errorf("case %d %s: 超过 15MB 应 40001 并点名上限,got %d %q", i, c.path, r.Code, r.Msg)
		}
	}
	// 没有留下一张单。
	var n int64
	app.repo.DB().Model(&model.Release{}).Where("title = ?", "大脚本").Count(&n)
	eq(t, n, int64(0), "超限不建单")
}

// ---------------------------------------------------------------- 5/6. scopes / allowIps 校验

func TestBB03_ScopesAndAllowIPsValidated(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	owner := app.roleIDByCode(admin, "owner")
	sa := app.createServiceAccount(admin, "valid-bot", []int64{owner}, nil)

	bad := []map[string]any{
		{"name": "x1", "userId": sa.ID, "scopes": []string{"release:read", "release:delete"}},
		{"name": "x2", "userId": sa.ID, "scopes": []string{"bogus"}},
		{"name": "x3", "userId": sa.ID, "allowIps": "10.0.0.0/33"},
		{"name": "x4", "userId": sa.ID, "allowIps": "10.0.0.1, not-an-ip"},
	}
	for i, b := range bad {
		if r := app.do(http.MethodPost, "/api/v1/api-clients", admin, b); r.Code != resp400 {
			t.Errorf("create case %d 应 40001,got %d %s", i, r.Code, r.Msg)
		}
	}
	// 合法的多种写法照收。
	id, _ := app.issueToSA(admin, sa.ID, "合法", []string{"release:read"}, 0)
	r := app.do(http.MethodPut, "/api/v1/api-clients/"+itoa(id), admin, map[string]any{"allowIps": "10.0.0.1, 192.168.0.0/16\n::1"})
	eq(t, r.Code, 0, "合法 allowIps")

	for i, b := range []map[string]any{
		{"scopes": []string{"nope"}},
		{"scopes": []string{}},
		{"allowIps": "300.1.1.1"},
	} {
		if r := app.do(http.MethodPut, "/api/v1/api-clients/"+itoa(id), admin, b); r.Code != resp400 {
			t.Errorf("update case %d 应 40001,got %d %s", i, r.Code, r.Msg)
		}
	}
	got := app.apiClientByName(admin, "合法")
	eq(t, got.Scopes, "release:read", "被拒的更新不改 scopes")
}

// ---------------------------------------------------------------- 7. 同服务账号不同凭据互查

// 2026-10-26 回归 02 改了口径:同一服务账号名下的凭据可互查(轮换:签新凭据、删旧凭据后,
// 新凭据还要能轮询旧凭据建的在途单);不同服务账号仍隔离;终止仍只认同一把凭据。
// 原 TestBB03_SiblingCredentialCannotReadRelease 断言"同账号另一把凭据 40300",按新结论改写。
func TestBB03_SiblingCredentialCanReadRelease(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	pid := app.createPipeline(admin, "隔离流程", "", []map[string]any{{"name": "执行变更", "type": "execute"}})
	sa, _, tokA := app.issueSA(admin, "shared-bot", "凭据A", []string{"owner"}, []string{"release:create", "release:read"}, pid)
	_, tokB := app.issueToSA(admin, sa.ID, "凭据B", []string{"release:create", "release:read"}, pid)

	r := app.openDo(http.MethodPost, "/api/v1/open/releases", tokA, map[string]any{
		"title": "A 的单", "instance": "sandbox-dev", "sql": "SELECT 1;",
	})
	eq(t, r.Code, 0, "A create")
	var rel openRelease
	_ = json.Unmarshal(r.Data, &rel)

	eq(t, app.openDo(http.MethodGet, "/api/v1/open/releases/"+rel.RelNo, tokA, nil).Code, 0, "A 能查自己的单")
	eq(t, app.openDo(http.MethodGet, "/api/v1/open/releases/"+rel.RelNo, tokB, nil).Code, 0, "同服务账号的另一把凭据能查 A 的单")
	// 终止仍只认同一把凭据。
	if r := app.openDo(http.MethodPost, "/api/v1/open/releases/"+rel.RelNo+"/abort", tokB, nil); r.Code != resp403 {
		t.Errorf("同账号另一把凭据终止 A 的单应 40300,got %d %s", r.Code, r.Msg)
	}

	// 轮换:删掉凭据 A 后,凭据 B 仍能查 A 建的单。
	clA := app.apiClientByName(admin, "凭据A")
	eq(t, app.do(http.MethodDelete, "/api/v1/api-clients/"+itoa(clA.ID), admin, nil).Code, 0, "删除旧凭据")
	eq(t, app.openDo(http.MethodGet, "/api/v1/open/releases/"+rel.RelNo, tokB, nil).Code, 0, "轮换后新凭据仍能查旧凭据的在途单")

	// 不同服务账号仍隔离。
	_, _, tokOther := app.issueSA(admin, "other-bot", "别家凭据", []string{"owner"}, []string{"release:create", "release:read"}, pid)
	if r := app.openDo(http.MethodGet, "/api/v1/open/releases/"+rel.RelNo, tokOther, nil); r.Code != resp403 {
		t.Errorf("别的服务账号的凭据查这张单应 40300,got %d %s", r.Code, r.Msg)
	}
	if r := app.openDo(http.MethodGet, "/api/v1/open/releases/REL-999999", tokB, nil); r.Code != resp400 {
		t.Errorf("查不存在的单按实现返回 40001,got %d", r.Code)
	}
}

// ---------------------------------------------------------------- 8. dialect 校验

func TestBB03_SQLReviewRejectsUnknownDialect(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	_, _, tok := app.issueSA(admin, "dialect-bot", "方言", []string{"owner"}, []string{"review:check"}, 0)

	r := app.openDo(http.MethodPost, "/api/v1/open/sql-review", tok, map[string]any{
		"dialect": "sqlserver", "sql": "SELECT 1;",
	})
	if r.Code != resp400 {
		t.Fatalf("非法 dialect 应 40001,got %d %s", r.Code, r.Msg)
	}
	for _, d := range []string{"mysql", "tidb", "dws", "oracle", "MySQL"} {
		r = app.openDo(http.MethodPost, "/api/v1/open/sql-review", tok, map[string]any{"dialect": d, "sql": "SELECT 1;"})
		eq(t, r.Code, 0, "合法 dialect "+d)
	}
	// 2026-10-26 回归:收紧前外部 CI 一直在传的值照旧认
	for _, d := range []string{"all", "generic", "gaussdb", "polardb", "postgres", "PostgreSQL", "aurora", "Aurora"} {
		r = app.openDo(http.MethodPost, "/api/v1/open/sql-review", tok, map[string]any{"dialect": d, "sql": "SELECT 1;"})
		eq(t, r.Code, 0, "旧值 dialect "+d)
	}
	// 带了实例:以实例引擎为准,dialect 不参与校验
	r = app.openDo(http.MethodPost, "/api/v1/open/sql-review", tok, map[string]any{
		"dialect": "sqlserver", "connectionId": app.connIDByEnv(admin, "dev"), "sql": "SELECT 1;",
	})
	if r.Code == resp400 {
		t.Errorf("带实例时不该因 dialect 拒绝,got %d %s", r.Code, r.Msg)
	}
}

// ---------------------------------------------------------------- 10. 对接文档示例 SQL 过默认规范

// 对接文档「三、提交一张升级单」的示例要能直接跑通 —— 照抄示例就被规范审查拦下,
// 第一次对接的人会以为网关坏了。原样取自 docs/开放接口对接文档.md。
func TestBB03_DocExampleSQLPassesDefaultReview(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	_, _, tok := app.issueSA(admin, "doc-bot", "文档示例", []string{"owner"}, []string{"review:check"}, 0)

	example := `ALTER TABLE tbl_order ADD COLUMN memo VARCHAR(64) NOT NULL DEFAULT "" COMMENT "备注", ALGORITHM=INPLACE, LOCK=NONE;`
	r := app.openDo(http.MethodPost, "/api/v1/open/sql-review", tok, map[string]any{"dialect": "mysql", "sql": example})
	eq(t, r.Code, 0, "review")
	var out struct {
		Result struct {
			Passed   bool `json:"passed"`
			Findings []struct {
				Name string `json:"name"`
			} `json:"findings"`
		} `json:"result"`
	}
	_ = json.Unmarshal(r.Data, &out)
	if !out.Result.Passed {
		t.Fatalf("文档示例应通过默认规范审查: %+v", out.Result.Findings)
	}
}

// ---------------------------------------------------------------- helpers

const (
	resp400 = 40001
	resp403 = 40300
)

// rolesOfID 读账号当前的全部角色 id(成员表)。
func (a *testApp) rolesOfID(uid int64) []int64 {
	a.t.Helper()
	u, err := a.repo.GetUserByID(uid)
	if err != nil {
		a.t.Fatalf("load user: %v", err)
	}
	return a.repo.EffectiveRoleIDs(u)
}
