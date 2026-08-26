package bootstrap

// Black-box coverage for the 开放接口: an external system raising SQL upgrade
// tickets with a key/secret credential. The assertions that matter are the ones
// about what the door does NOT open: no scope escalation, no permission the
// service account lacks, no second ticket on a retry.

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"testing"
)

type openStage struct {
	Order  int    `json:"order"`
	Name   string `json:"name"`
	Type   string `json:"type"`
	Status string `json:"status"`
	Log    string `json:"log"`
}

type openRelease struct {
	RelNo       string      `json:"relNo"`
	Title       string      `json:"title"`
	Status      string      `json:"status"`
	Instance    string      `json:"instance"`
	Env         string      `json:"env"`
	Pipeline    string      `json:"pipeline"`
	ExternalRef string      `json:"externalRef"`
	Error       string      `json:"error"`
	Stages      []openStage `json:"stages"`
}

// issueClient creates an API credential bound to a service account and returns
// the plaintext token (the only copy that ever exists).
func (a *testApp) issueClient(adminToken, name, email string, scopes []string) string {
	a.t.Helper()
	uid := a.userIDByEmail(adminToken, email)
	r := a.do(http.MethodPost, "/api/v1/api-clients", adminToken, map[string]any{
		"name": name, "userId": uid, "scopes": scopes,
	})
	if r.Code != 0 {
		a.t.Fatalf("create api client: code=%d msg=%s", r.Code, r.Msg)
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(r.Data, &out); err != nil {
		a.t.Fatalf("decode client: %v", err)
	}
	if out.Token == "" {
		a.t.Fatal("a created credential must return its token exactly once")
	}
	return out.Token
}

func (a *testApp) userIDByEmail(token, email string) int64 {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/users", token, nil)
	var users []struct {
		ID    int64  `json:"id"`
		Email string `json:"email"`
	}
	_ = json.Unmarshal(r.Data, &users)
	for _, u := range users {
		if u.Email == email {
			return u.ID
		}
	}
	a.t.Fatalf("user %s not found", email)
	return 0
}

// openDo issues an open-API request with the key/secret token.
func (a *testApp) openDo(method, path, token string, body any) apiResp {
	a.t.Helper()
	return a.do(method, path, token, body)
}

func (a *testApp) openRelease(token, relNo string) openRelease {
	a.t.Helper()
	r := a.openDo(http.MethodGet, "/api/v1/open/releases/"+relNo, token, nil)
	if r.Code != 0 {
		a.t.Fatalf("open get release: code=%d msg=%s", r.Code, r.Msg)
	}
	var v openRelease
	if err := json.Unmarshal(r.Data, &v); err != nil {
		a.t.Fatalf("decode open release: %v", err)
	}
	return v
}

func TestOpenAPICreatesReleaseByInstanceName(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	// The credential acts as a DBA-lead service account — the same rights that
	// account has in the console, no more.
	// 流程由网关侧绑定在凭据上 —— 外部请求不再指定(也不允许指定)。
	pid := app.createPipeline(admin, "外部发布流程", "", []map[string]any{
		{"name": "规范审查", "type": "review", "config": `{"failOn":"error"}`},
		{"name": "执行变更", "type": "execute"},
	})
	token := app.issueClientPiped(admin, "DevOps 平台", "zhangwei@vela.io", nil, pid)

	// Addressed by NAME: an external caller knows "sandbox-dev", not a row id.
	r := app.openDo(http.MethodPost, "/api/v1/open/releases", token, map[string]any{
		"title": "外部单:建表", "externalRef": "CHG-1001",
		"instance": "sandbox-dev",
		"sql": "CREATE TABLE tbl_ext (\n id BIGINT NOT NULL AUTO_INCREMENT COMMENT 'id',\n" +
			" PRIMARY KEY (id)\n) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='ext';",
		"reason": "外部系统提交",
	})
	if r.Code != 0 {
		t.Fatalf("open create: code=%d msg=%s", r.Code, r.Msg)
	}
	var created openRelease
	if err := json.Unmarshal(r.Data, &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if created.RelNo == "" || created.ExternalRef != "CHG-1001" {
		t.Fatalf("unexpected create payload: %+v", created)
	}
	eq(t, created.Instance, "sandbox-dev", "resolved instance")
	eq(t, created.Pipeline, "外部发布流程", "resolved pipeline")

	// 0024 执行闸:外部单也停在待确认,由网关侧(审批角色)在控制台点击。
	app.confirmOpenReleaseAndWait(admin, created.RelNo)
	// Poll the final state through the SAME endpoint an integrator would use.
	final := app.openRelease(token, created.RelNo)
	eq(t, final.Status, "success", "release status")
	if len(final.Stages) != 2 {
		t.Fatalf("expected the flow's 2 stages, got %d", len(final.Stages))
	}
	// The status poll carries the stage logs — that is where an integrator looks
	// to find out why something stopped.
	if final.Stages[0].Log == "" {
		t.Error("status poll should carry stage logs")
	}
}

func TestOpenAPIRetryIsIdempotent(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	idemPid := app.createPipeline(admin, "幂等流程", "", []map[string]any{{"name": "规范审查", "type": "review", "config": `{"failOn":"none"}`}})
	token := app.issueClientPiped(admin, "CI", "zhangwei@vela.io", nil, idemPid)

	body := map[string]any{
		"title": "幂等", "externalRef": "BUILD-77", "instance": "sandbox-dev",
		"sql": "SELECT 1;",
	}
	first := app.openDo(http.MethodPost, "/api/v1/open/releases", token, body)
	second := app.openDo(http.MethodPost, "/api/v1/open/releases", token, body)
	eq(t, first.Code, 0, "first submit")
	eq(t, second.Code, 0, "retry submit")

	var a1, a2 openRelease
	_ = json.Unmarshal(first.Data, &a1)
	_ = json.Unmarshal(second.Data, &a2)
	if a1.RelNo != a2.RelNo {
		t.Fatalf("a retry must return the same ticket: %s vs %s", a1.RelNo, a2.RelNo)
	}
}

func TestOpenAPIAcceptsScriptUpload(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	scriptPid := app.createPipeline(admin, "脚本流程", "", []map[string]any{
		{"name": "规范审查", "type": "review", "config": `{"failOn":"none"}`},
		{"name": "执行变更", "type": "execute"},
	})
	token := app.issueClientPiped(admin, "发布平台", "zhangwei@vela.io", nil, scriptPid)

	script := "-- upgrade\nCREATE TABLE tbl_from_file (id BIGINT NOT NULL COMMENT 'id', PRIMARY KEY(id)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='f';\n"
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("title", "脚本升级单")
	_ = mw.WriteField("instance", "sandbox-dev")
	_ = mw.WriteField("externalRef", "REL-FILE-1")
	fw, _ := mw.CreateFormFile("file", "upgrade.sql")
	_, _ = fw.Write([]byte(script))
	_ = mw.Close()

	req, _ := http.NewRequest(http.MethodPost, app.srv.URL+"/api/v1/open/releases", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("multipart post: %v", err)
	}
	defer res.Body.Close()
	var env apiResp
	_ = json.NewDecoder(res.Body).Decode(&env)
	if env.Code != 0 {
		t.Fatalf("script upload create: code=%d msg=%s", env.Code, env.Msg)
	}
	var created openRelease
	_ = json.Unmarshal(env.Data, &created)

	// 0024 执行闸:控制台侧确认后,脚本才从文件执行(摘要校验同脚本审批路径)。
	app.confirmOpenReleaseAndWait(admin, created.RelNo)
	final := app.openRelease(token, created.RelNo)
	eq(t, final.Status, "success", "script release status")
}

func TestOpenAPIScopesAndCredentialFailures(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	readOnly := app.issueClient(admin, "看板", "chenhao@vela.io", []string{"release:read"})

	// A read-only credential may not raise a change.
	r := app.openDo(http.MethodPost, "/api/v1/open/releases", readOnly, map[string]any{
		"title": "x", "instance": "sandbox-dev", "sql": "SELECT 1;",
	})
	if r.Code == 0 {
		t.Fatal("a release:read credential must not be able to create a release")
	}

	// A wrong secret, a malformed token and a missing credential all fail closed.
	for _, bad := range []string{"ak_deadbeef.notthesecret", "garbage", ""} {
		r = app.openDo(http.MethodGet, "/api/v1/open/instances", bad, nil)
		if r.Code == 0 {
			t.Errorf("credential %q must be rejected", bad)
		}
	}

	// A disabled credential stops working immediately.
	full := app.issueClient(admin, "待停用", "chenhao@vela.io", nil)
	clients := app.do(http.MethodGet, "/api/v1/api-clients", admin, nil)
	var list []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	_ = json.Unmarshal(clients.Data, &list)
	var id int64
	for _, c := range list {
		if c.Name == "待停用" {
			id = c.ID
		}
	}
	if id == 0 {
		t.Fatal("client not listed")
	}
	upd := app.do(http.MethodPut, "/api/v1/api-clients/"+itoa(id), admin, map[string]any{"enabled": false})
	eq(t, upd.Code, 0, "disable client")
	r = app.openDo(http.MethodGet, "/api/v1/open/instances", full, nil)
	if r.Code == 0 {
		t.Fatal("a disabled credential must stop working")
	}
}

func TestOpenAPIInheritsServiceAccountPermissions(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	// Bound to a read-only RESEARCH account: it may look, not change.
	execPid := app.createPipeline(admin, "只执行", "", []map[string]any{{"name": "执行变更", "type": "execute"}})
	token := app.issueClientPiped(admin, "研发流水线", "zhaolei@vela.io", nil, execPid)

	r := app.openDo(http.MethodPost, "/api/v1/open/releases", token, map[string]any{
		"title": "越权 DDL", "instance": "sandbox-dev",
		"sql": "DROP TABLE tbl_order;",
	})
	if r.Code == 0 {
		t.Fatal("a credential must not exceed the rights of the account it acts as")
	}
	// …and the same credential can still run a pre-merge review, which reads
	// nothing and changes nothing.
	r = app.openDo(http.MethodPost, "/api/v1/open/sql-review", token, map[string]any{
		"dialect": "mysql", "sql": "CREATE TABLE t (id INT);",
	})
	eq(t, r.Code, 0, "review check should be allowed")
}

func TestAPIClientManagementIsAdminOnly(t *testing.T) {
	app := newTestApp(t)
	dba := app.login("chenhao@vela.io", "vela123")
	// The DBA holds no settings menu, so even the listing is out of reach.
	r := app.do(http.MethodGet, "/api/v1/api-clients", dba, nil)
	if r.Code == 0 {
		t.Fatal("credentials must not be visible without the settings menu")
	}
	r = app.do(http.MethodPost, "/api/v1/api-clients", dba, map[string]any{"name": "x", "userId": 1})
	if r.Code == 0 {
		t.Fatal("a non-admin must not issue API credentials")
	}
}

func TestOpenAPIDiscoveryEndpoints(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	token := app.issueClient(admin, "集成方", "chenhao@vela.io", nil)

	r := app.openDo(http.MethodGet, "/api/v1/open/instances", token, nil)
	eq(t, r.Code, 0, "instances")
	var instances []struct {
		Instance string `json:"instance"`
		Env      string `json:"env"`
	}
	_ = json.Unmarshal(r.Data, &instances)
	if len(instances) == 0 {
		t.Fatal("an integrator needs the instance names the create call accepts")
	}
	// Tag scope applies: the L2 account is scoped to orders/users, so the
	// analytics-only instance is not offered.
	for _, i := range instances {
		if i.Instance == "analytics-ro" {
			t.Error("an instance outside the service account's scope must not be listed")
		}
	}

	r = app.openDo(http.MethodGet, "/api/v1/open/pipelines", token, nil)
	eq(t, r.Code, 0, "pipelines")
	var flows []struct {
		Pipeline string   `json:"pipeline"`
		Stages   []string `json:"stages"`
	}
	_ = json.Unmarshal(r.Data, &flows)
	if len(flows) < 2 {
		t.Fatalf("the seeded flows should be discoverable, got %d", len(flows))
	}
}
