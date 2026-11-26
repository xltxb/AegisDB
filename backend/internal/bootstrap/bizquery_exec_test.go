package bootstrap

// 业务方数据查询 · 执行判定(第三期)。
//
// 用一个真实的 PostgreSQL 临时库跑端到端:业务方的查询权只来自授权,只能跑单条只读语句,结果有
// 行数上限,授权撤销 / 实例取消标记立即生效,审计写明凭的是哪条授权。跨库与超时要真实的
// MySQL(设置 AEGISDB_TEST_MYSQL 时运行)。

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"aegisdb/internal/model"
)

type bizResp struct {
	Columns    []string   `json:"columns"`
	Rows       [][]string `json:"rows"`
	Truncated  bool       `json:"truncated"`
	MaxRows    int        `json:"maxRows"`
	TimeoutSec int        `json:"timeoutSec"`
	TimedOut   bool       `json:"timedOut"`
	Error      string     `json:"error"`
}

type bizEnv struct {
	*grantEnv
	user string // 业务方的网关 token
	uid  int64
}

// newBizEnv 建一个真实 PostgreSQL 业务查询源(orders 表 1500 行),下发授权给 biz@corp.io 并让他登录。
func newBizEnv(t *testing.T, maxRows int) *bizEnv {
	t.Helper()
	return newBizEnvIn(t, maxRows, "prod")
}

// newBizEnvIn 是 newBizEnv,查询源挂在 env 环境上(分层开关按环境所属的分层生效)。
func newBizEnvIn(t *testing.T, maxRows int, env string) *bizEnv {
	t.Helper()
	setup := []string{
		`CREATE TABLE orders (id INTEGER PRIMARY KEY, amount INTEGER)`,
		`CREATE TABLE notes (id INTEGER, t TEXT)`,
		`INSERT INTO notes VALUES (1, '=HYPERLINK("http://evil","x")'), (2, 'plain')`,
		`INSERT INTO orders SELECT i, i*10 FROM generate_series(1, 1500) AS i`,
	}
	database := newTargetDB(t, setup...)

	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	owner, _ := app.repo.GetRoleByCode("owner")
	sa := app.createServiceAccount(admin, "审批系统", []int64{owner.ID}, nil).Email
	e := &grantEnv{app: app, admin: admin, sa: sa}
	e.token = app.issueClient(admin, "审批系统", sa, []string{"grant:read", "grant:write"})
	r := app.do(http.MethodPost, "/api/v1/connections", admin, targetConnBody(t, "biz-pg", env, database))
	if r.Code != 0 {
		t.Fatalf("create pg conn: %s", r.Msg)
	}
	var c struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(r.Data, &c)
	e.source = c.ID
	eq(t, app.do(http.MethodPatch, "/api/v1/connections/"+itoa(e.source), admin, map[string]any{"bizQuery": true}).Code, 0, "标记业务查询源")
	var dbs []string
	_ = json.Unmarshal(app.do(http.MethodGet, "/api/v1/open/query-sources/"+itoa(e.source)+"/databases", e.token, nil).Data, &dbs)
	if len(dbs) == 0 {
		t.Fatal("PostgreSQL 业务查询源应列得出库名")
	}
	e.db = dbs[0]

	b := e.body("biz@corp.io", "OA-100")
	if maxRows > 0 {
		b["maxRows"] = maxRows
	}
	grantOf(t, e.put(b))
	u, _ := app.repo.FindUserByEmailCI("biz@corp.io")
	return &bizEnv{grantEnv: e, user: app.bizSession(u), uid: u.ID}
}

// bizSession 给业务方签一个网关会话,代替 SRD 授权登录 —— 业务方不设本地口令(PRD 决策 10)。
func (a *testApp) bizSession(u *model.User) string {
	a.t.Helper()
	tok, _, err := a.svc.JWT.IssueTTL(u.ID, u.RoleID, model.RoleCodeBizQuery, u.Name, u.TokenVersion, time.Hour)
	if err != nil {
		a.t.Fatal(err)
	}
	return tok
}

func (b *bizEnv) exec(query string) (apiResp, bizResp) {
	r := b.app.do(http.MethodPost, "/api/v1/bizquery/exec", b.user, map[string]any{"sourceId": b.source, "database": b.db, "sql": query})
	var out bizResp
	_ = json.Unmarshal(r.Data, &out)
	return r, out
}

func TestBizQuery_RowCapAndGrantLimits(t *testing.T) {
	b := newBizEnv(t, 0)
	r, out := b.exec("SELECT id, amount FROM orders ORDER BY id")
	eq(t, r.Code, 0, "有授权的只读查询")
	if len(out.Rows) != 1000 || !out.Truncated || out.MaxRows != 1000 {
		t.Fatalf("默认最多 1000 行并标记截断,实际 %d 行 truncated=%v max=%d err=%q", len(out.Rows), out.Truncated, out.MaxRows, out.Error)
	}
	if out.Rows[0][0] != "1" || out.Rows[999][0] != "1000" {
		t.Errorf("补 LIMIT 不该打乱 ORDER BY:首行 %v 末行 %v", out.Rows[0], out.Rows[999])
	}
	_, small := b.exec("SELECT id FROM orders WHERE id <= 3")
	if len(small.Rows) != 3 || small.Truncated {
		t.Errorf("不足上限时不该标记截断: %d 行 truncated=%v", len(small.Rows), small.Truncated)
	}

	// 授权里设了更小的上限
	g := b.body("biz@corp.io", "OA-101")
	g["maxRows"] = 10
	grantOf(t, b.put(g))
	_, out = b.exec("SELECT id FROM orders")
	if len(out.Rows) != 10 || !out.Truncated {
		t.Errorf("授权上限 10 行,实际 %d 行", len(out.Rows))
	}
}

func TestBizQuery_Refusals(t *testing.T) {
	b := newBizEnv(t, 0)
	cases := []struct{ what, sql, want string }{
		{"写", "UPDATE orders SET amount = 0", "UPDATE"},
		{"删", "DELETE FROM orders", "DELETE"},
		{"多条", "SELECT 1; SELECT 2", "一条"},
		{"建表", "CREATE TABLE x (a int)", "CREATE"},
	}
	for _, c := range cases {
		r, _ := b.exec(c.sql)
		if r.Code != 40300 || !strings.Contains(r.Msg, c.want) {
			t.Errorf("%s:应以 40300 拒绝且说明 %q,实际 code=%d msg=%q", c.what, c.want, r.Code, r.Msg)
		}
	}
	var n int64
	b.app.repo.DB().Model(&model.AuditLog{}).Where("result = ? AND operator LIKE ?", model.ResultRejected, "查询授权 #%").Count(&n)
	eq(t, n, int64(len(cases)), "被拒的语句都写审计")
	_, ok := b.exec("SELECT COUNT(*) FROM orders")
	if len(ok.Rows) != 1 || ok.Rows[0][0] != "1500" {
		t.Fatalf("写被拒之后数据不变: %v", ok.Rows)
	}

	// 别的库(授权以外的库名)
	r := b.app.do(http.MethodPost, "/api/v1/bizquery/exec", b.user, map[string]any{"sourceId": b.source, "database": "other_db", "sql": "SELECT 1"})
	if r.Code != 40300 || !strings.Contains(r.Msg, "授权") {
		t.Errorf("没有授权的库应拒绝,实际 %d %q", r.Code, r.Msg)
	}
	// 业务方碰不到终端
	if r := b.app.do(http.MethodPost, "/api/v1/terminal/exec", b.user, map[string]any{"connectionId": b.source, "sql": "SELECT 1"}); r.Code == 0 {
		t.Error("业务查询角色不该能用 Web 命令行")
	}
	var me struct {
		Menus map[string]bool `json:"menus"`
	}
	_ = json.Unmarshal(b.app.do(http.MethodGet, "/api/v1/auth/me", b.user, nil).Data, &me)
	for k, on := range me.Menus {
		if on && k != "query" {
			t.Errorf("业务方只该看到数据查询页,却还能看到 %s", k)
		}
	}
	if !me.Menus["query"] {
		t.Error("业务方应能看到数据查询页")
	}
}

func TestBizQuery_RevokeAndUnmarkTakeEffectImmediately(t *testing.T) {
	b := newBizEnv(t, 0)
	r, _ := b.exec("SELECT 1")
	eq(t, r.Code, 0, "授权生效时能查")

	var mine struct {
		Grants []struct {
			ID     int64  `json:"id"`
			Source string `json:"source"`
		} `json:"grants"`
	}
	_ = json.Unmarshal(b.app.do(http.MethodGet, "/api/v1/bizquery/grants", b.user, nil).Data, &mine)
	if len(mine.Grants) != 1 || mine.Grants[0].Source != "biz-pg" {
		t.Fatalf("我的授权应列出这一条,实际 %+v", mine.Grants)
	}

	// 取消业务查询源标记 → 立即查不了,也不再列出
	b.app.do(http.MethodPatch, "/api/v1/connections/"+itoa(b.source), b.admin, map[string]any{"bizQuery": false})
	if r, _ := b.exec("SELECT 1"); r.Code != 40300 || !strings.Contains(r.Msg, "业务查询源") {
		t.Errorf("取消标记后应拒绝,实际 %d %q", r.Code, r.Msg)
	}
	_ = json.Unmarshal(b.app.do(http.MethodGet, "/api/v1/bizquery/grants", b.user, nil).Data, &mine)
	if len(mine.Grants) != 0 {
		t.Errorf("取消标记后不该再列出,实际 %+v", mine.Grants)
	}
	b.app.do(http.MethodPatch, "/api/v1/connections/"+itoa(b.source), b.admin, map[string]any{"bizQuery": true})

	grant, _ := b.app.repo.GetQueryGrantByKey(b.uid, b.source, b.db)

	// 撤销 → 立即查不了
	b.app.do(http.MethodDelete, "/api/v1/open/query-grants?externalId=OA-100", b.token, nil)
	if r, _ := b.exec("SELECT 1"); r.Code != 40300 {
		t.Errorf("撤销后应拒绝,实际 %d %q", r.Code, r.Msg)
	}

	// 审计:成功的那一次写明凭的是哪条授权
	var n int64
	b.app.repo.DB().Model(&model.AuditLog{}).Where("result = ? AND operator LIKE ?", model.ResultExecuted, "查询授权 #"+strconv.FormatInt(grant.ID, 10)+" · %").Count(&n)
	if n == 0 {
		t.Error("执行成功的查询应写审计,operator 指向查询授权")
	}
}

// ---------------------------------------------------------------- 真实 MySQL

func TestBizQuery_MySQLCrossDatabaseAndTimeout(t *testing.T) {
	v := os.Getenv("AEGISDB_TEST_MYSQL")
	if v == "" {
		t.Skip("未设置 AEGISDB_TEST_MYSQL,跳过真实 MySQL 用例")
	}
	parts := strings.SplitN(v, "/", 3)
	hp := strings.SplitN(parts[0], ":", 2)
	raw, err := sql.Open("mysql", fmt.Sprintf("%s:%s@tcp(%s)/", parts[1], parts[2], parts[0]))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{
		"CREATE DATABASE IF NOT EXISTS biz_orders", "CREATE DATABASE IF NOT EXISTS biz_secret",
		"CREATE TABLE IF NOT EXISTS biz_orders.nums (n INT PRIMARY KEY)",
		"CREATE TABLE IF NOT EXISTS biz_secret.salary (n INT)",
		"INSERT IGNORE INTO biz_orders.nums SELECT seq FROM (SELECT a.n*100+b.n+1 AS seq FROM (SELECT 0 n UNION SELECT 1 UNION SELECT 2 UNION SELECT 3 UNION SELECT 4 UNION SELECT 5 UNION SELECT 6 UNION SELECT 7 UNION SELECT 8 UNION SELECT 9) a, (SELECT 0 n UNION SELECT 1 UNION SELECT 2 UNION SELECT 3 UNION SELECT 4 UNION SELECT 5 UNION SELECT 6 UNION SELECT 7 UNION SELECT 8 UNION SELECT 9 UNION SELECT 10 UNION SELECT 11 UNION SELECT 12 UNION SELECT 13 UNION SELECT 14 UNION SELECT 15 UNION SELECT 16 UNION SELECT 17 UNION SELECT 18 UNION SELECT 19 UNION SELECT 20 UNION SELECT 21 UNION SELECT 22 UNION SELECT 23 UNION SELECT 24 UNION SELECT 25 UNION SELECT 26 UNION SELECT 27 UNION SELECT 28 UNION SELECT 29 UNION SELECT 30 UNION SELECT 31 UNION SELECT 32 UNION SELECT 33 UNION SELECT 34 UNION SELECT 35 UNION SELECT 36 UNION SELECT 37 UNION SELECT 38 UNION SELECT 39 UNION SELECT 40 UNION SELECT 41 UNION SELECT 42 UNION SELECT 43 UNION SELECT 44 UNION SELECT 45 UNION SELECT 46 UNION SELECT 47 UNION SELECT 48 UNION SELECT 49 UNION SELECT 50 UNION SELECT 51 UNION SELECT 52 UNION SELECT 53 UNION SELECT 54 UNION SELECT 55 UNION SELECT 56 UNION SELECT 57 UNION SELECT 58 UNION SELECT 59 UNION SELECT 60 UNION SELECT 61 UNION SELECT 62 UNION SELECT 63 UNION SELECT 64 UNION SELECT 65 UNION SELECT 66 UNION SELECT 67 UNION SELECT 68 UNION SELECT 69 UNION SELECT 70 UNION SELECT 71 UNION SELECT 72 UNION SELECT 73 UNION SELECT 74 UNION SELECT 75 UNION SELECT 76 UNION SELECT 77 UNION SELECT 78 UNION SELECT 79 UNION SELECT 80 UNION SELECT 81 UNION SELECT 82 UNION SELECT 83 UNION SELECT 84 UNION SELECT 85 UNION SELECT 86 UNION SELECT 87 UNION SELECT 88 UNION SELECT 89 UNION SELECT 90 UNION SELECT 91 UNION SELECT 92 UNION SELECT 93 UNION SELECT 94 UNION SELECT 95 UNION SELECT 96 UNION SELECT 97 UNION SELECT 98 UNION SELECT 99) b) x",
	} {
		if _, err := raw.Exec(s); err != nil {
			t.Fatalf("%s: %v", s[:40], err)
		}
	}
	raw.Close()

	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	owner, _ := app.repo.GetRoleByCode("owner")
	sa := app.createServiceAccount(admin, "审批系统", []int64{owner.ID}, nil).Email
	e := &grantEnv{app: app, admin: admin, sa: sa, db: "biz_orders"}
	e.token = app.issueClient(admin, "审批系统", sa, []string{"grant:read", "grant:write"})
	r := app.do(http.MethodPost, "/api/v1/connections", admin, map[string]any{
		"name": "biz-mysql", "engine": "MySQL 8.0", "host": parts[0], "env": "prod", "policy": "audit-only",
		"username": parts[1], "password": parts[2], "database": "biz_orders",
	})
	if r.Code != 0 {
		t.Fatalf("create mysql conn: %s", r.Msg)
	}
	var c struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(r.Data, &c)
	e.source = c.ID
	_ = hp
	app.do(http.MethodPatch, "/api/v1/connections/"+itoa(e.source), admin, map[string]any{"bizQuery": true})
	g := e.body("biz@corp.io", "OA-M1")
	g["timeoutSec"] = 1
	grantOf(t, e.put(g))
	u, _ := app.repo.FindUserByEmailCI("biz@corp.io")
	b := &bizEnv{grantEnv: e, user: app.bizSession(u), uid: u.ID}

	if r, out := b.exec("SELECT COUNT(*) FROM nums"); r.Code != 0 || len(out.Rows) != 1 {
		t.Fatalf("本库查询应成功: %d %q %+v", r.Code, r.Msg, out)
	}
	for _, q := range []string{"SELECT * FROM biz_secret.salary", "SELECT * FROM nums, `biz_secret`.salary", "SELECT * FROM mysql.user",
		// MySQL 把 \' 当转义,网关的词法不认:子查询会被当成字符串抹掉(真库上会返回数据)
		`SELECT 'a\'', (SELECT COUNT(*) FROM biz_secret.salary) AS 'z'`,
		`SELECT 'a\'', (SELECT user FROM mysql.user LIMIT 1) AS 'z'`,
	} {
		if r, _ := b.exec(q); r.Code != 40300 {
			t.Errorf("%s:应拒绝,实际 %d %q", q, r.Code, r.Msg)
		}
	}
	// 自带一个极大的 LIMIT:网关要把它改小。只在读取时截断不够 —— 读满之后 defer 的
	// rows.Close() 会让 MySQL 驱动把剩下的 10 亿行读完,一直读到授权时限(这里 1 秒,生产最长
	// 300 秒)触发 KILL 为止:结果照样显示"成功、已截断",但从库白跑、并发配额被占满全程。
	bigStart := time.Now()
	if r, out := b.exec("SELECT a.n FROM nums a, nums b, nums c LIMIT 1000000000"); r.Code != 0 || out.TimedOut || !out.Truncated {
		t.Errorf("大 LIMIT 应被改小、截断返回: code=%d %q timedOut=%v truncated=%v err=%q",
			r.Code, r.Msg, out.TimedOut, out.Truncated, out.Error)
	}
	if d := time.Since(bigStart); d > 700*time.Millisecond {
		t.Errorf("大 LIMIT 改小后应立即返回,实际耗时 %v(接近授权时限 1 秒说明库端一直跑到了被 KILL)", d)
	}
	start := time.Now()
	r, out := b.exec("SELECT SUM(a.n * b.n * c.n) FROM nums a, nums b, nums c")
	if r.Code != 0 || !out.TimedOut || !strings.Contains(out.Error, "1 秒") {
		t.Fatalf("授权超时 1 秒的慢查询应被自动取消: code=%d %+v", r.Code, out)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("超时 1 秒的查询耗时 %v", time.Since(start))
	}
}
