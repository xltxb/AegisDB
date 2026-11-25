package bootstrap

// 2026-10 黑盒测试(.scratch/blackbox-fixes/issues/04-data-tools.md 第 1~3 条),走真实的
// /bizquery/exec:PG 系统目录、带引号的危险函数名、信息函数。

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
)

// newRealBizEnv 把一台真实实例登记成业务查询源,给 biz@corp.io 下发 db 库的查询授权。
func newRealBizEnv(t *testing.T, engine, hostPort, user, pw, db string) *bizEnv {
	t.Helper()
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	owner, _ := app.repo.GetRoleByCode("owner")
	sa := app.createServiceAccount(admin, "审批系统", []int64{owner.ID}, nil).Email
	e := &grantEnv{app: app, admin: admin, sa: sa, db: db}
	e.token = app.issueClient(admin, "审批系统", sa, []string{"grant:read", "grant:write"})
	r := app.do(http.MethodPost, "/api/v1/connections", admin, map[string]any{
		"name": "biz-real", "engine": engine, "host": hostPort, "env": "prod", "policy": "audit-only",
		"username": user, "password": pw, "database": db,
	})
	if r.Code != 0 {
		t.Fatalf("create conn: %s", r.Msg)
	}
	var c struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(r.Data, &c)
	e.source = c.ID
	eq(t, app.do(http.MethodPatch, "/api/v1/connections/"+itoa(e.source), admin, map[string]any{"bizQuery": true}).Code, 0, "标记业务查询源")
	grantOf(t, e.put(e.body("biz@corp.io", "OA-BB")))
	u, _ := app.repo.FindUserByEmailCI("biz@corp.io")
	return &bizEnv{grantEnv: e, user: app.bizSession(u), uid: u.ID}
}

func realDSNParts(t *testing.T, env string) []string {
	t.Helper()
	v := os.Getenv(env)
	if v == "" {
		t.Skip("未设置 " + env + ",跳过真库用例")
	}
	return strings.SplitN(v, "/", 3)
}

func TestBizQuery_PGSystemCatalogRefused(t *testing.T) {
	p := realDSNParts(t, "VELA_TEST_PG")
	b := newRealBizEnv(t, "PostgreSQL 16", p[0], p[1], p[2], "postgres")

	if r, out := b.exec("SELECT version(), current_database(), pg_size_pretty(1024::bigint)"); r.Code != 0 || out.Error != "" || len(out.Rows) != 1 {
		t.Fatalf("普通查询应成功: code=%d msg=%q err=%q", r.Code, r.Msg, out.Error)
	}
	for _, q := range []string{
		"SELECT rolname, rolpassword FROM pg_authid",
		"SELECT * FROM pg_catalog.pg_shadow",
		`SELECT * FROM "pg_authid"`,
		"SELECT * FROM pg_user",
		"SELECT * FROM pg_stat_activity",
		"SELECT table_name, column_name FROM information_schema.columns",
		`SELECT "pg_sleep"(1)`,
		`SELECT pg_catalog."pg_sleep"(1)`,
		"SELECT current_user",
		"SELECT inet_server_addr()",
	} {
		if r, out := b.exec(q); r.Code != 40300 {
			t.Errorf("%s:应拒绝,实际 code=%d msg=%q rows=%v", q, r.Code, r.Msg, out.Rows)
		}
	}
}

func TestBizQuery_MySQLQuotedFuncAndInfoRefused(t *testing.T) {
	p := realDSNParts(t, "VELA_TEST_MYSQL")
	raw, err := sql.Open("mysql", fmt.Sprintf("%s:%s@tcp(%s)/", p[1], p[2], p[0]))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{
		"CREATE DATABASE IF NOT EXISTS biz_bb",
		"CREATE TABLE IF NOT EXISTS biz_bb.t (id INT PRIMARY KEY, user VARCHAR(16))",
		"REPLACE INTO biz_bb.t VALUES (1, 'a')",
	} {
		if _, err := raw.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	raw.Close()
	b := newRealBizEnv(t, "MySQL 8.0", p[0], p[1], p[2], "biz_bb")

	if r, out := b.exec("SELECT DATABASE(), VERSION(), @@version_comment, id, user FROM t"); r.Code != 0 || out.Error != "" || len(out.Rows) != 1 {
		t.Fatalf("普通查询应成功(列名叫 user 也行): code=%d msg=%q err=%q", r.Code, r.Msg, out.Error)
	}
	for _, q := range []string{
		"SELECT `sleep`(1)",
		"SELECT `SLEEP` (1)",
		"SELECT `benchmark`(1000, md5('a'))",
		"SELECT `get_lock`('vela_bb', 0)",
		"SELECT DATABASE(), USER(), @@hostname",
		"SELECT CURRENT_USER",
		"SELECT @@datadir",
		"SELECT @@GLOBAL.basedir",
	} {
		if r, out := b.exec(q); r.Code != 40300 {
			t.Errorf("%s:应拒绝,实际 code=%d msg=%q rows=%v", q, r.Code, r.Msg, out.Rows)
		}
	}
}
