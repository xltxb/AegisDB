package bootstrap

// 服务账号 —— 对接升级单/CI/CD 系统的机器主体。
//
// The design under test: a service account is a User with kind=service that
// every judgement layer treats exactly like a person (roles, tags, audit),
// with exactly two differences, both in the strict direction — the console
// login is never one of its doors, and the human MFA mandate does not apply
// to a principal that cannot enroll TOTP.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

type svcAccountView struct {
	ID      int64    `json:"id"`
	Name    string   `json:"name"`
	Email   string   `json:"email"`
	Status  string   `json:"status"`
	Roles   []string `json:"roles"`
	Tags    []string `json:"tags"`
	Clients int      `json:"clients"`
}

// createServiceAccount mints one and returns its listing row.
func (a *testApp) createServiceAccount(token, name string, roleIDs []int64, tags []string) svcAccountView {
	a.t.Helper()
	r := a.do(http.MethodPost, "/api/v1/service-accounts", token, map[string]any{
		"name": name, "roleIds": roleIDs, "tags": tags,
	})
	if r.Code != 0 {
		a.t.Fatalf("create service account: code=%d msg=%s", r.Code, r.Msg)
	}
	lr := a.do(http.MethodGet, "/api/v1/service-accounts", token, nil)
	var rows []svcAccountView
	_ = json.Unmarshal(lr.Data, &rows)
	for _, row := range rows {
		if row.Name == name {
			return row
		}
	}
	a.t.Fatalf("created service account %q not in listing", name)
	return svcAccountView{}
}

func TestServiceAccountLifecycle(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	owner := app.roleIDByCode(admin, "owner")

	sa := app.createServiceAccount(admin, "升级单平台", []int64{owner}, []string{"orders"})
	eq(t, sa.Status, "active", "born active — it exists to be used")
	if !strings.HasPrefix(sa.Email, "svc-") || !strings.HasSuffix(sa.Email, "@service.vela") {
		t.Errorf("identity should be a generated svc-…@service.vela handle, got %q", sa.Email)
	}
	eq(t, len(sa.Roles), 1, "role recorded")
	eq(t, len(sa.Tags), 1, "tag scope recorded")
	eq(t, sa.Clients, 0, "no credential bound yet")

	// A Chinese-only name must still get a unique identity (the slug hashes).
	sa2 := app.createServiceAccount(admin, "发布平台", []int64{owner}, nil)
	if sa2.Email == sa.Email {
		t.Error("two service accounts collided on the same generated identity")
	}
	// Same name twice = same identity = refused.
	r := app.do(http.MethodPost, "/api/v1/service-accounts", admin, map[string]any{
		"name": "升级单平台", "roleIds": []int64{owner},
	})
	if r.Code == 0 {
		t.Error("duplicate service account name must be refused")
	}

	// Creation is an admin act — it is a standing grant of change rights.
	dev := app.login("chenhao@vela.io", "vela123")
	r = app.do(http.MethodPost, "/api/v1/service-accounts", dev, map[string]any{
		"name": "越权账号", "roleIds": []int64{owner},
	})
	if r.Code == 0 {
		t.Error("non-admin must not create service accounts")
	}
}

// TestServiceAccountConsoleDoorsStayShut — 登录不是它的门,口令也造不出来。
func TestServiceAccountConsoleDoorsStayShut(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	owner := app.roleIDByCode(admin, "owner")
	sa := app.createServiceAccount(admin, "cicd-deployer", []int64{owner}, nil)

	// No password exists — but the refusal must hold against guessed ones, and
	// must be indistinguishable from an ordinary wrong password: the login
	// boundary confirms nothing about what kind of account an email is.
	lr := app.loginRaw(sa.Email, "vela123")
	if lr.Code == 0 {
		t.Fatal("service account logged in with a guessed password")
	}
	human := app.loginRaw("linwei@vela.io", "wrong-password")
	eq(t, lr.Msg, human.Msg, "same refusal as any wrong password — nothing leaked")

	// And an admin cannot mint it a password either: a hash that exists is a
	// hash that can leak.
	r := app.do(http.MethodPost, "/api/v1/users/"+itoa(sa.ID)+"/password", admin,
		map[string]any{"password": "SuperSecret99!"})
	if r.Code == 0 {
		t.Error("setting a password on a service account must be refused")
	}
	lr3 := app.loginRaw(sa.Email, "SuperSecret99!")
	if lr3.Code == 0 {
		t.Fatal("the refused password must not work")
	}
}

// TestServiceAccountDrivesCICDRelease — 完整链路:服务账号 + API 凭据 →
// 外部系统建升级单 → CI/CD 流水线执行 → 审计记到服务账号名下。
func TestServiceAccountDrivesCICDRelease(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	owner := app.roleIDByCode(admin, "owner")
	sa := app.createServiceAccount(admin, "upgrade-portal", []int64{owner}, nil)

	// Bind a credential to the service account — the one door it has. 发布流程
	// 也绑在凭据上:外部请求不指定流程。
	relPid := app.createPipeline(admin, "升级单流程", "", []map[string]any{
		{"name": "规范审查", "type": "review", "config": `{"failOn":"none"}`},
		{"name": "执行变更", "type": "execute"},
	})
	cr := app.do(http.MethodPost, "/api/v1/api-clients", admin, map[string]any{
		"name": "升级单系统", "userId": sa.ID, "scopes": []string{"release:create", "release:read"},
		"pipelineId": relPid,
	})
	eq(t, cr.Code, 0, "issue credential")
	var issued struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(cr.Data, &issued)

	r := app.openDo(http.MethodPost, "/api/v1/open/releases", issued.Token, map[string]any{
		"title": "订单库升级 v3", "instance": "sandbox-dev",
		"sql": "SELECT 1;", "externalRef": "CHG-2026-0825", "reason": "升级单系统同步",
	})
	eq(t, r.Code, 0, "external system raises the ticket")
	var created openRelease
	_ = json.Unmarshal(r.Data, &created)

	// 0024 执行闸:外部单停在待确认,由控制台侧审批角色点击后才落库。
	app.confirmOpenReleaseAndWait(admin, created.RelNo)
	final := app.openRelease(issued.Token, created.RelNo)
	eq(t, final.Status, "success", "the CI/CD run completes")

	// The chain answers "who did this": actor = the service account (the
	// principal), operator = API:<client> (which system asked).
	var rows []struct {
		Actor    string `json:"actor"`
		Operator string `json:"operator"`
		Command  string `json:"command"`
	}
	_ = json.Unmarshal(app.auditItemsRaw(admin, "?range=today&pageSize=200"), &rows)
	found := false
	for _, row := range rows {
		if strings.Contains(row.Command, "RELEASE 订单库升级 v3") {
			found = true
			eq(t, row.Actor, "upgrade-portal", "audited against the service account")
			eq(t, row.Operator, "API:升级单系统", "the asking system is named")
		}
	}
	if !found {
		t.Error("no audit row for the externally-raised release")
	}
}

// TestServiceAccountExemptFromMandatoryMFA — mfaMandatory 只约束人:机器注册
// 不了 TOTP,硬套的现实结局是共享秘钥进 CI 仓库。它的第二因子是凭据本身。
func TestServiceAccountExemptFromMandatoryMFA(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	app.setSettings(admin, map[string]any{"security.mfaMandatory": true})
	owner := app.roleIDByCode(admin, "owner")
	prod := app.connIDByEnv(admin, "prod")

	// The flow carries an approve stage: owner 在 prod 的发布权限是「需审批」,
	// 这里要隔离的是 MFA 这一个变量,不是能力矩阵。
	pid := app.createPipeline(admin, "生产发布", "", []map[string]any{
		{"name": "人工审批", "type": "approve"},
		{"name": "执行变更", "type": "execute"},
	})

	// Control: a human who never enrolled is blocked on PROD by the mandate.
	hr := app.submitRelease(admin, map[string]any{
		"title": "人类提交", "pipelineId": pid, "connectionId": prod, "sql": "SELECT 1;",
	})
	if hr.Code == 0 {
		t.Fatal("a non-enrolled human should be blocked by the MFA mandate")
	}

	// The machine principal passes: its second factor is the credential.
	sa := app.createServiceAccount(admin, "mfa-exempt-bot", []int64{owner}, nil)
	cr := app.do(http.MethodPost, "/api/v1/api-clients", admin, map[string]any{
		"name": "MFA豁免验证", "userId": sa.ID, "scopes": []string{"release:create", "release:read"},
		"pipelineId": pid, // 网关侧绑定,外部请求不指定流程
	})
	var issued struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(cr.Data, &issued)
	r := app.openDo(http.MethodPost, "/api/v1/open/releases", issued.Token, map[string]any{
		"title": "机器提交", "connectionId": prod, "sql": "SELECT 1;", "reason": "MFA豁免回归",
	})
	eq(t, r.Code, 0, "service account raises a PROD release under the mandate")
}
