package bootstrap

// harness_test.go boots the entire DP DB GATEWAY against a throwaway SQLite file
// with seed data and exposes it through an httptest server. Tests drive the real
// /api/v1 HTTP seam end-to-end (auth -> middleware -> three-layer engine -> audit),
// asserting only externally observable behaviour, never internal collaborators.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"velagateway/internal/gateway"
	"velagateway/internal/handler"
	"velagateway/internal/model"
	"velagateway/internal/repository"
	"velagateway/internal/service"
	"velagateway/pkg/crypto"
	"velagateway/pkg/jwt"
)

// testApp is a running gateway instance bound to an httptest server.
type testApp struct {
	srv  *httptest.Server
	t    *testing.T
	svc  *service.Services // for driving scheduled entry points (e.g. timeout sweep)
	repo *repository.Repo  // for asserting on stored rows directly
	cfg  *Config
}

// newTestApp assembles the app exactly like cmd/server/main.go, but against an
// isolated in-temp-dir SQLite database seeded with the demo dataset. Optional
// opts tweak the config before the router is built (e.g. to set EnvRaw for the
// docs-exposure test).
func newTestApp(t *testing.T, opts ...func(*Config)) *testApp {
	t.Helper()

	cfg := &Config{}
	cfg.Server.Mode = "release"
	cfg.Database.Driver = "sqlite"
	cfg.Database.SQLitePath = filepath.Join(t.TempDir(), "vela-test.db")
	cfg.Database.AutoMigrate = true
	cfg.Database.Seed = true
	cfg.JWT.Secret = "test-secret"
	cfg.JWT.TTLHours = 1
	cfg.Gateway.StrictMode = false
	cfg.Webhook.Enabled = false
	// Model a reverse-proxied prod deployment: trust the loopback proxy so
	// X-Forwarded-For (the real client IP) is honored by the IP allowlist.
	cfg.Server.TrustedProxies = []string{"127.0.0.1", "::1"}
	for _, o := range opts {
		o(cfg)
	}

	// VELA_TEST_STORE_PG 设了就把存储库换成真 PostgreSQL:每个测试一个库,从迁移+播种好的
	// 模板库 CREATE DATABASE … TEMPLATE 出来(PG 原生整库复制,远快于每次重跑迁移)。
	// 调用方用 opts 自带了存储库(driver 不再是 sqlite)时,用它的,也不替它播种。
	ownStore := cfg.Database.Driver == "sqlite"
	pgStore := ""
	if ownStore {
		pgStore = os.Getenv("VELA_TEST_STORE_PG")
	}
	if pgStore != "" {
		cfg.Database.Driver = "postgres"
		cfg.Database.AutoMigrate = false
		cfg.Database.PostgresDSN = pgTestStore(t, pgStore, cfg)
	}
	db, err := OpenDB(cfg)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	// Close the SQLite handle before t.TempDir() removal runs (cleanups are LIFO),
	// otherwise Windows refuses to delete the still-open database file.
	if sqlDB, derr := db.DB(); derr == nil {
		t.Cleanup(func() { sqlDB.Close() })
	}
	repo := repository.New(db)
	// 夹具不论模拟哪个环境都要有。Seed 只在本地开发时种(M6),所以拿一份环境名置空的副本去种;
	// 路由仍按 cfg 里真正要测的环境名搭(例如 staging 下不暴露接口文档)。
	seedCfg := *cfg
	seedCfg.EnvRaw = ""
	if ownStore && pgStore == "" { // PG 模板库里已经种过了
		if err := Seed(repo, &seedCfg); err != nil {
			t.Fatalf("seed: %v", err)
		}
		seedTestFixtures(t, repo) // demo users/connections/approvals/audit the suite asserts on
	}
	crypto.SetSecretKey(cfg.JWT.Secret)
	service.AllowPrivateWebhookTargets = true // stub webhook/Lark servers run on loopback
	service.AllowInsecureSSO = true           // stub SSO runs on plain-http loopback
	// 夹具模拟的是**开发环境**,所以显式打开模拟数据 —— 与 main.go 在 dev 下做的
	// 事情一样。默认是关的(见 gateway/simulation.go:漏设的后果必须是拒绝),
	// 生产那一侧由 TestProductionServesNoSimulatedData 单独把关。
	gateway.AllowSimulation = true
	engine := gateway.NewRiskEngine(repo)
	jwtMgr := jwt.New(cfg.JWT.Secret, cfg.JWT.TTLHours)
	svc := service.New(repo, engine, jwtMgr)
	// 与 main.go 保持一致:不挂上这个 provider,脱敏在测试里根本不会发生,
	// 而"测试通过了但线上才是另一套接线"是最不该有的一种绿。
	gateway.SensitiveRulesProvider = svc.SensitiveRulesForGateway
	h := handler.New(svc, repo)
	r := NewRouter(cfg, h, repo, svc, jwtMgr)

	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return &testApp{srv: srv, t: t, svc: svc, repo: repo, cfg: cfg}
}

// apiResp mirrors the unified envelope { code, msg, data }.
type apiResp struct {
	Code   int             `json:"code"`
	Msg    string          `json:"msg"`
	MsgRef json.RawMessage `json:"msgRef"`
	Data   json.RawMessage `json:"data"`
}

// do issues an HTTP request and decodes the envelope. token may be empty.
func (a *testApp) do(method, path, token string, body any) apiResp {
	a.t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, a.srv.URL+path, rdr)
	if err != nil {
		a.t.Fatalf("new request %s %s: %v", method, path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		a.t.Fatalf("do %s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	var out apiResp
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		a.t.Fatalf("decode %s %s: %v", method, path, err)
	}
	return out
}

// login authenticates and returns the bearer token, failing the test on error.
// An MFA-enrolled account must pass its current TOTP code as the optional arg.
func (a *testApp) login(email, password string, mfaCode ...string) string {
	a.t.Helper()
	body := map[string]string{"email": email, "password": password}
	if len(mfaCode) > 0 {
		body["mfaCode"] = mfaCode[0]
	}
	r := a.do(http.MethodPost, "/api/v1/auth/login", "", body)
	if r.Code != 0 {
		a.t.Fatalf("login %s: code=%d msg=%s", email, r.Code, r.Msg)
	}
	var data struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(r.Data, &data); err != nil {
		a.t.Fatalf("login decode: %v", err)
	}
	if data.Token == "" {
		a.t.Fatalf("login %s: empty token", email)
	}
	return data.Token
}

// loginRaw is login without the fail-on-error, for asserting that a particular
// credential combination is REFUSED.
func (a *testApp) loginRaw(email, password string, mfaCode ...string) apiResp {
	a.t.Helper()
	body := map[string]string{"email": email, "password": password}
	if len(mfaCode) > 0 {
		body["mfaCode"] = mfaCode[0]
	}
	return a.do(http.MethodPost, "/api/v1/auth/login", "", body)
}

// auditItemsRaw fetches GET /audit (optionally with a query string like
// "?risk=high&page=2") and returns the raw JSON array of items, unwrapping the
// paginated envelope {items,total,page,pageSize} so callers can decode into
// their own row type.
func (a *testApp) auditItemsRaw(token, query string) json.RawMessage {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/audit"+query, token, nil)
	if r.Code != 0 {
		a.t.Fatalf("audit list: code=%d msg=%s", r.Code, r.Msg)
	}
	var page struct {
		Items json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(r.Data, &page); err != nil {
		a.t.Fatalf("audit envelope decode: %v", err)
	}
	return page.Items
}

// connIDByEnv returns the id of the first seeded connection in the given env.
func (a *testApp) connIDByEnv(token, env string) int64 {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/connections", token, nil)
	if r.Code != 0 {
		a.t.Fatalf("list connections: code=%d msg=%s", r.Code, r.Msg)
	}
	var conns []struct {
		ID  int64  `json:"id"`
		Env string `json:"env"`
	}
	if err := json.Unmarshal(r.Data, &conns); err != nil {
		a.t.Fatalf("connections decode: %v", err)
	}
	for _, c := range conns {
		if c.Env == env {
			return c.ID
		}
	}
	a.t.Fatalf("no seeded connection for env %q", env)
	return 0
}

// riskCheckResult is the decoded /risk/check payload used by engine tests.
type riskCheckResult struct {
	Risk             string         `json:"risk"`
	Action           string         `json:"action"`
	RequiresApproval bool           `json:"requiresApproval"`
	Command          string         `json:"command"`
	MatchedRule      string         `json:"matchedRule"`
	MatchedRuleRef   *model.RuleRef `json:"matchedRuleRef"`
}

// riskCheck runs the pure three-layer pre-check for a (connection, sql) pair.
func (a *testApp) riskCheck(token string, connID int64, sql string) riskCheckResult {
	return a.riskCheckIn(token, connID, sql, "")
}

// riskCheckIn is riskCheck against a specific target database —执行窗口按库开,
// 不带库名就判不出窗口。
func (a *testApp) riskCheckIn(token string, connID int64, sql, database string) riskCheckResult {
	a.t.Helper()
	r := a.do(http.MethodPost, "/api/v1/risk/check", token, map[string]any{
		"connectionId": connID, "sql": sql, "database": database,
	})
	if r.Code != 0 {
		a.t.Fatalf("risk/check %q: code=%d msg=%s", sql, r.Code, r.Msg)
	}
	var out riskCheckResult
	if err := json.Unmarshal(r.Data, &out); err != nil {
		a.t.Fatalf("risk/check decode: %v", err)
	}
	return out
}

// eq is a minimal equality assertion (stdlib only, no testify).
func eq[T comparable](t *testing.T, got, want T, what string) {
	t.Helper()
	if got != want {
		t.Errorf("%s: got %v, want %v", what, got, want)
	}
}
