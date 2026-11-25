package gateway

// 2026-10 黑盒测试报告的业务查询 / 导出缺陷(.scratch/blackbox-fixes/issues/04-data-tools.md 第 1~4 条)。

import (
	"context"
	"strings"
	"testing"
	"time"

	"velagateway/internal/model"
)

// 第 1 条:PG 系的系统目录(口令哈希、全库列结构、会话)业务方不该读到。
func TestBizReadOnlyCheck_PGSystemCatalogs(t *testing.T) {
	for _, engine := range []string{"PostgreSQL 15", "GaussDB", "DWS"} {
		for _, s := range []string{
			"SELECT rolname, rolpassword FROM pg_authid",
			"SELECT * FROM pg_catalog.pg_authid",
			`SELECT * FROM "pg_authid"`,
			`SELECT * FROM pg_catalog."pg_authid"`,
			`SELECT * FROM "pg_catalog"."pg_shadow"`,
			"SELECT usename, passwd FROM pg_shadow",
			"SELECT * FROM pg_user",
			"SELECT * FROM pg_roles",
			"SELECT * FROM pg_stat_activity",
			"SELECT * FROM pg_stat_statements",
			"SELECT * FROM pg_settings",
			"SELECT * FROM information_schema.columns",
			"SELECT * FROM INFORMATION_SCHEMA.tables",
			`SELECT * FROM "information_schema"."columns"`,
			"SELECT * FROM orders o JOIN pg_namespace n ON n.oid = o.ns",
			"SELECT * FROM orders WHERE uid IN (SELECT usesysid FROM pg_user)",
			"SELECT * FROM pg_toast.pg_toast_1234",
			"SELECT pg_stat_get_backend_activity(1)",
			"SELECT * FROM pgxc_node",
			"SELECT * FROM dbe_perf.session_stat",
		} {
			if r := BizReadOnlyCheck(engine, s, "appdb", nil); r == nil {
				t.Errorf("%s:%s 应当拒绝", engine, s)
			}
		}
		// 普通业务查询照常放行:pg_ 开头的普通函数(格式化、大小)不是系统表
		for _, s := range []string{
			"SELECT id, amount FROM public.orders WHERE id = 1",
			"SELECT pg_size_pretty(sum(amount)::bigint) FROM orders",
			"SELECT * FROM orders WHERE note = 'pg_authid information_schema'",
			"SELECT o.id, o.created_at::date FROM orders o JOIN items i ON i.oid = o.id",
			"WITH t AS (SELECT id FROM orders) SELECT * FROM t",
		} {
			if r := BizReadOnlyCheck(engine, s, "appdb", nil); r != nil {
				t.Errorf("%s:%s 应当放行,实际拒绝 %q", engine, s, model.RenderBizReason(r))
			}
		}
	}
	// MySQL 侧的 pg_ 开头的表不受影响
	if r := BizReadOnlyCheck("MySQL 8.0", "SELECT * FROM pg_orders", "appdb", []string{"appdb"}); r != nil {
		t.Errorf("MySQL 上 pg_ 开头的普通表不该拒绝:%q", model.RenderBizReason(r))
	}
}

// 第 2 条:函数名套上反引号 / 双引号 / 方括号照样是那个函数。
func TestBizReadOnlyCheck_QuotedFunctionNames(t *testing.T) {
	cases := []struct{ engine, sql, want string }{
		{"MySQL 8.0", "SELECT `sleep`(3)", "SLEEP"},
		{"MySQL 8.0", "SELECT `SLEEP` (3)", "SLEEP"},
		{"MySQL 8.0", "SELECT `benchmark`(1000000, md5('a'))", "BENCHMARK"},
		{"MySQL 8.0", "SELECT `get_lock`('x', 10)", "GET_LOCK"},
		{"MySQL 8.0", "SELECT id FROM orders WHERE `sleep`(1) = 0", "SLEEP"},
		{"PostgreSQL 15", `SELECT "pg_sleep"(2)`, "PG_SLEEP"},
		{"PostgreSQL 15", `SELECT pg_catalog."pg_sleep"(2)`, "PG_SLEEP"},
		{"PostgreSQL 15", `SELECT "pg_advisory_lock"(1)`, "PG_ADVISORY_LOCK"},
		{"MySQL 8.0", "SELECT [sleep](3)", "SLEEP"},
	}
	for _, c := range cases {
		r := BizReadOnlyCheck(c.engine, c.sql, "appdb", []string{"appdb"})
		if r == nil || r.Code != model.BizFunc || r.Args["name"] != c.want {
			t.Errorf("%s:应以 bizFunc(%s) 拒绝,实际 %+v", c.sql, c.want, r)
		}
	}
	// 列名本身叫 sleep / "into" 不是调用
	for _, s := range []string{"SELECT `sleep`, `into` FROM orders", "SELECT sleep_minutes FROM orders"} {
		if r := BizReadOnlyCheck("MySQL 8.0", s, "appdb", []string{"appdb"}); r != nil {
			t.Errorf("%s:应当放行,实际拒绝 %q", s, model.RenderBizReason(r))
		}
	}
}

// 第 3 条:后端账号、主机名、数据目录这类信息不给业务方;库名与版本可以看。
func TestBizReadOnlyCheck_InfoFunctions(t *testing.T) {
	bad := []struct{ engine, sql string }{
		{"MySQL 8.0", "SELECT USER()"},
		{"MySQL 8.0", "SELECT DATABASE(), USER(), @@hostname"},
		{"MySQL 8.0", "SELECT current_user()"},
		{"MySQL 8.0", "SELECT CURRENT_USER"},
		{"MySQL 8.0", "SELECT session_user()"},
		{"MySQL 8.0", "SELECT system_user()"},
		{"MySQL 8.0", "SELECT `user`()"},
		{"MySQL 8.0", "SELECT @@hostname"},
		{"MySQL 8.0", "SELECT @@datadir"},
		{"MySQL 8.0", "SELECT @@basedir"},
		{"MySQL 8.0", "SELECT @@GLOBAL.datadir"},
		{"MySQL 8.0", "SELECT @@session . hostname"},
		{"MySQL 8.0", "SELECT @@secure_file_priv"},
		{"MySQL 8.0", "SELECT @@`hostname`"},
		{"PostgreSQL 15", "SELECT current_user"},
		{"PostgreSQL 15", "SELECT session_user"},
		{"PostgreSQL 15", "SELECT user"},
		{"PostgreSQL 15", "SELECT inet_server_addr()"},
		{"PostgreSQL 15", "SELECT current_setting('data_directory')"},
		{"Oracle 19c", "SELECT USER FROM DUAL"},
		{"Oracle 19c", "SELECT SYS_CONTEXT('USERENV', 'SERVER_HOST') FROM DUAL"},
	}
	for _, c := range bad {
		if r := BizReadOnlyCheck(c.engine, c.sql, "appdb", []string{"appdb"}); r == nil {
			t.Errorf("%s:%s 应当拒绝", c.engine, c.sql)
		}
	}
	ok := []struct{ engine, sql string }{
		{"MySQL 8.0", "SELECT DATABASE(), VERSION()"},
		{"MySQL 8.0", "SELECT @@version, @@version_comment, @@time_zone, @@session.sql_mode"},
		{"MySQL 8.0", "SELECT user, user_name FROM orders WHERE user = 'a'"},
		{"MySQL 8.0", "SELECT o.user FROM orders o"},
		{"PostgreSQL 15", "SELECT version(), current_database()"},
		{"PostgreSQL 15", `SELECT "user" FROM orders`},
		{"PostgreSQL 15", "SELECT o.user_id FROM orders o"},
	}
	for _, c := range ok {
		if r := BizReadOnlyCheck(c.engine, c.sql, "appdb", []string{"appdb"}); r != nil {
			t.Errorf("%s:%s 应当放行,实际拒绝 %q", c.engine, c.sql, model.RenderBizReason(r))
		}
	}
}

// 第 4 条:导出与业务查询同口径 —— ReadSideEffect 就是那一套判定。
func TestReadSideEffect(t *testing.T) {
	bad := []string{
		"SELECT * FROM orders FOR UPDATE",
		"SELECT * FROM orders WHERE id = 1 FOR SHARE",
		"SELECT * FROM orders LOCK IN SHARE MODE",
		"SELECT * FROM orders /*!50000 FOR UPDATE */",
		"SELECT SLEEP(5)",
		"SELECT id, SLEEP(1) FROM orders",
		"SELECT `sleep`(3)",
		"SELECT GET_LOCK('x', 10)",
		"SELECT BENCHMARK(100000000, MD5('a'))",
		`SELECT "pg_sleep"(2)`,
		"SELECT pg_advisory_lock(1)",
		"SELECT nextval('seq')",
		"SELECT * INTO new_table FROM orders",
		"SELECT id INTO @x FROM orders LIMIT 1",
	}
	for _, s := range bad {
		if r := ReadSideEffect(s); r == nil {
			t.Errorf("%s:应当认出副作用", s)
		}
	}
	// 典型的正常导出:JOIN、子查询、CTE、聚合与常用函数
	ok := []string{
		"SELECT o.id, u.name, DATE_FORMAT(o.created_at, '%Y-%m-%d') AS d FROM orders o JOIN users u ON u.id = o.uid WHERE o.status = 'paid'",
		"SELECT REPLACE(name, ' ', ''), CONCAT(first, ' ', last), IFNULL(x, 0) FROM users",
		"SELECT * FROM orders WHERE uid IN (SELECT id FROM users WHERE vip = 1)",
		"WITH t AS (SELECT uid, SUM(amount) s FROM orders GROUP BY uid) SELECT * FROM t ORDER BY s DESC",
		"SELECT * FROM orders WHERE note = 'for update sleep(1) into'",
		"SELECT `update`, `into`, `sleep` FROM t",
		"SELECT COUNT(*), MAX(created_at) FROM orders LEFT JOIN items ON items.oid = orders.id",
		"SELECT to_char(created_at, 'YYYY-MM') FROM orders FOR_UPDATE_FLAG",
	}
	for _, s := range ok {
		if r := ReadSideEffect(s); r != nil {
			t.Errorf("%s:应当放行,实际 %q", s, model.RenderBizReason(r))
		}
	}
}

// ---------------------------------------------------------------- 真实 PostgreSQL

// 第 1、2 条在真库上:报告里的写法在库上确实能执行(漏洞是真的),判定层把它们拒掉;
// 口令列哪怕漏过判定,出口也打码。
func TestPostgresBiz_SystemCatalogAndQuotedFunc(t *testing.T) {
	conn := testPostgres(t)
	raw, err := pgRaw(conn)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	for _, s := range []string{
		"SELECT rolname, rolpassword FROM pg_authid",
		`SELECT * FROM pg_catalog."pg_shadow"`,
		"SELECT table_name FROM information_schema.columns",
		`SELECT "pg_sleep"(0.01)`,
		`SELECT pg_catalog."pg_sleep"(0.01)`,
		"SELECT current_user, inet_server_addr()",
	} {
		rows, err := raw.Query(s)
		if err != nil {
			t.Fatalf("%s 应当能在真库上执行(证明攻击面真实存在):%v", s, err)
		}
		rows.Close()
		if r := BizReadOnlyCheck(conn.Engine, s, conn.Database, nil); r == nil {
			t.Errorf("%s:应当拒绝", s)
		}
	}
	// 判定之外的兜底:即使口令列被读出来,业务查询的出口也打码
	conn.Database = "postgres"
	res, err := RealBizQuery(context.Background(), conn, "SELECT rolname, rolpassword FROM pg_authid WHERE rolpassword IS NOT NULL", 10, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Data) == 0 {
		t.Skip("测试库没有设口令的角色,跳过打码断言")
	}
	for _, row := range res.Data {
		if strings.HasPrefix(row[1], "SCRAM-SHA-256$") || strings.HasPrefix(row[1], "md5") {
			t.Fatalf("口令哈希未打码:%q", row[1])
		}
	}
	// 正常 PG 查询在真库上照常执行
	for _, s := range []string{
		"SELECT pg_size_pretty(1024::bigint), version(), current_database()",
		"WITH t AS (SELECT 1 AS id) SELECT * FROM t",
	} {
		if r := BizReadOnlyCheck(conn.Engine, s, conn.Database, nil); r != nil {
			t.Errorf("%s:应当放行,实际拒绝 %q", s, model.RenderBizReason(r))
			continue
		}
		if _, err := RealBizQuery(context.Background(), conn, s, 10, 5*time.Second); err != nil {
			t.Errorf("%s:真库执行失败 %v", s, err)
		}
	}
}

// ---------------------------------------------------------------- 真实 MySQL

func TestMySQLBiz_QuotedFuncAndInfo(t *testing.T) {
	_, done := bizSetup(t)
	defer done()
	conn := testMySQL(t)
	conn.Database = "biztest"
	dbs, err := RealDatabaseNames(conn)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{
		"SELECT `sleep`(0.01)",
		"SELECT `SLEEP` (0.01)",
		"SELECT `get_lock`('vela_bb', 0), `release_lock`('vela_bb')",
		"SELECT DATABASE(), USER(), @@hostname",
		"SELECT @@datadir, @@basedir",
	} {
		if _, err := RealRun(context.Background(), conn, s, 5*time.Second); err != nil {
			t.Fatalf("%s 应当能在真库上执行(证明攻击面真实存在):%v", s, err)
		}
		if r := BizReadOnlyCheck(conn.Engine, s, "biztest", dbs); r == nil {
			t.Errorf("%s:应当拒绝", s)
		}
	}
	s := "SELECT DATABASE(), VERSION(), @@version_comment, COUNT(*) FROM nums"
	if r := BizReadOnlyCheck(conn.Engine, s, "biztest", dbs); r != nil {
		t.Fatalf("%s:应当放行,实际拒绝 %q", s, model.RenderBizReason(r))
	}
	if _, err := RealBizQuery(context.Background(), conn, s, 10, 5*time.Second); err != nil {
		t.Fatalf("%s:真库执行失败 %v", s, err)
	}
}
