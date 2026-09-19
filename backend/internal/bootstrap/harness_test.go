package bootstrap

// harness_test.go boots the entire AegisDB against a throwaway PostgreSQL schema
// with seed data and exposes it through an httptest server. Tests drive the real
// /api/v1 HTTP seam end-to-end (auth -> middleware -> three-layer engine -> audit),
// asserting only externally observable behaviour, never internal collaborators.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"gorm.io/gorm"

	"velagateway/internal/gateway"
	"velagateway/internal/handler"
	"velagateway/internal/model"
	"velagateway/internal/repository"
	"velagateway/internal/service"
	"velagateway/internal/testsupport"
	"velagateway/pkg/crypto"
	"velagateway/pkg/jwt"
)

// newMigratedDB 是 testsupport.NewDB 再加一次 Migrate —— 也就是**真实启动做的事**。
//
// testsupport.NewDB 直接 Exec baseline SQL,绕过了 Migrate。它必须这么做:bootstrap
// 的测试是内部测试,testsupport 再 import bootstrap 就是循环导入。代价是 Migrate 里
// 那五个回填(backfillGliEnv / backfillEnvTiers / seedPipelineReference /
// backfillApprovalExecuted / backfillStrictNoWhere)在测试里一个都不跑,于是测试看到的
// 是一个「表齐了但参考数据全空」的库 —— 而空的参考数据在这套系统里到处读作「放行」,
// 那正是这些回填存在的理由。
//
// 所以补这一刀的位置只能在 bootstrap 包里。baseline 整体幂等(36 条 CREATE TABLE
// IF NOT EXISTS + 48 条 CREATE INDEX IF NOT EXISTS),重跑只出 NOTICE;而这一跑也
// 顺带让整套 harness 测试每次都压一遍 RunSQLMigrations 和它的 advisory lock。
func newMigratedDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := testsupport.NewDB(t)
	cfg := &Config{}
	// 与 newTestApp 取同一个值,理由只是「夹具对自己说的话要一致」—— 不是因为这个取值
	// 会让折叠真的发生。
	//
	// 恰恰相反:此刻 schema 刚建好、一行角色都没有,backfillStrictNoWhere 认出这是个新库
	// (databaseIsUnseeded),于是**只盖戳、不改任何行**。各分层身上留下的是 builtinTiers
	// 定的出厂默认(PROD 开、DEV 关)—— 而整套 harness 测试正是按这组默认写断言的。
	// 升级那一条路由 TestStrictBackfill_UpgradeFoldsAnExplicitOffOntoTheTiers 单独管。
	cfg.Gateway.StrictMode = false
	if err := Migrate(cfg, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// testApp is a running gateway instance bound to an httptest server.
type testApp struct {
	srv  *httptest.Server
	t    *testing.T
	svc  *service.Services // for driving scheduled entry points (e.g. timeout sweep)
	repo *repository.Repo  // for asserting on stored rows directly
	cfg  *Config
}

// newTestApp assembles the app exactly like cmd/server/main.go, but against an
// isolated PostgreSQL schema seeded with the demo dataset.
func newTestApp(t *testing.T) *testApp {
	t.Helper()

	cfg := &Config{}
	cfg.Server.Mode = "release"
	cfg.Database.Seed = true
	cfg.JWT.Secret = "test-secret"
	cfg.JWT.TTLHours = 1
	cfg.Gateway.StrictMode = false
	cfg.Webhook.Enabled = false
	// Model a reverse-proxied prod deployment: trust the loopback proxy so
	// X-Forwarded-For (the real client IP) is honored by the IP allowlist.
	cfg.Server.TrustedProxies = []string{"127.0.0.1", "::1"}

	db := newMigratedDB(t)
	repo := repository.New(db)
	if err := Seed(repo, cfg); err != nil {
		t.Fatalf("seed: %v", err)
	}
	seedTestFixtures(t, repo) // demo users/connections/approvals/audit the suite asserts on
	crypto.SetSecretKey(cfg.JWT.Secret)
	service.AllowPrivateWebhookTargets = true // stub webhook/Lark servers run on loopback
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

// exportTempDir 建一个导出目录,并挂上「等后台导出任务收敛」的清理。
//
// 导出是**异步**的:审批通过或提交之后,任务进 exportQueue,由 service.New 起的
// worker goroutine 去跑。测试体到这里就返回了,而那个 goroutine 还活着。
//
// t.Cleanup 是 LIFO,而 t.TempDir() 的 RemoveAll 是在**调用它的那一刻**注册的 ——
// 也就是 newTestApp 之后。于是清理的实际顺序是:先删目录、再关服务器、最后关库,
// 而 worker 这时可能正往那个目录写归档、正把任务标成 done。两件事同时发生:
//
//	ERROR export job completed but marking it done failed err="sql: database is closed"
//	TempDir RemoveAll cleanup: unlinkat ...: directory not empty
//
// 单跑时 worker 来得及收尾,所以看不见;`go test ./...` 全包并行、机器满载时就来不及。
// 更糟的是它会**吃掉后面的清理** —— 清理链在中途失败,排在后面的 DROP SCHEMA 就不跑了,
// 残留的 schema 攒起来会撞 PG 的 max_connections,而那时的报错和真正的病因毫无关系。
//
// 所以等待必须注册得**比 RemoveAll 更晚**(才能更早执行),这也是它住在这个函数里、
// 而不是 newTestApp 里的原因 —— 那里注册太早了。
func (a *testApp) exportTempDir() string {
	a.t.Helper()
	dir := a.t.TempDir()
	a.t.Cleanup(func() { a.waitExportsSettled() })
	return dir
}

// waitExportsSettled 等到没有导出任务停在 pending / running。
//
// 超时报错而不是静静放过:一个迟迟不收敛的任务要么是 worker 卡住了,要么是任务
// 根本没被消费 —— 两者都是真问题,不该被一句「等了 5 秒还没好」盖过去。
func (a *testApp) waitExportsSettled() {
	a.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var n int64
		err := a.repo.DB().Model(&model.ExportJob{}).
			Where("status IN ?", []string{model.ExportPending, model.ExportRunning}).
			Count(&n).Error
		if err != nil {
			// 库已经关了 —— 说明注册顺序错了,等待排在了 DB 清理后面。
			a.t.Errorf("等待导出收敛时数据库已关闭(清理顺序不对):%v", err)
			return
		}
		if n == 0 {
			return
		}
		if time.Now().After(deadline) {
			a.t.Errorf("等了 5 秒仍有 %d 个导出任务停在 pending/running —— "+
				"worker 卡住了,或者任务压根没进队列", n)
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// apiResp mirrors the unified envelope { code, msg, data }.
type apiResp struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
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

// lastRelease returns the most recently created release row, read straight from
// the repo (not the HTTP envelope) — for tests that need to assert on a field
// (like OSCMode) that a create response never echoes back.
func (a *testApp) lastRelease(t *testing.T) *model.Release {
	t.Helper()
	var rel model.Release
	if err := a.repo.DB().Order("id DESC").First(&rel).Error; err != nil {
		t.Fatalf("lastRelease: %v", err)
	}
	return &rel
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
