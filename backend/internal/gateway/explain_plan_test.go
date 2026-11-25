package gateway

// EXPLAIN 只取执行计划、绝不真的执行(各引擎,2026-10)。两层:
//   - 判定:DESC / DESCRIBE(MySQL 系里与 EXPLAIN 同义)、MariaDB 的 ANALYZE <语句>、带 FORMAT= 的
//     写法,都按里面的语句判 —— DESC ANALYZE DELETE 曾被当成"看表结构"的只读放行;
//   - 执行:会执行的写法发给数据库之前改成只出计划;Oracle 的 EXPLAIN PLAN 在同一会话里读回计划。

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"velagateway/internal/model"
)

func TestParseVerb_ExplainSynonymsAndFormats(t *testing.T) {
	cases := []struct {
		sql      string
		verb     string
		planOnly bool
	}{
		// MySQL 系:DESC / DESCRIBE 跟着语句时就是 EXPLAIN
		{"DESC UPDATE t SET v = 1", "UPDATE", true},
		{"describe DELETE FROM t WHERE id = 1", "DELETE", true},
		{"DESC ANALYZE DELETE t FROM t JOIN t2 ON t2.id = t.id", "DELETE", false},
		{"DESCRIBE ANALYZE UPDATE t SET v = 1", "UPDATE", false},
		{"DESC FORMAT=TREE SELECT * FROM t", "SELECT", true},
		// 看表结构:照旧是只读的 DESC
		{"DESC t", "DESC", false},
		{"DESCRIBE orders", "DESCRIBE", false},
		// FORMAT= 不是语句
		{"EXPLAIN FORMAT=TREE UPDATE t SET v = 1", "UPDATE", true},
		{"EXPLAIN ANALYZE FORMAT=TREE DELETE FROM t", "DELETE", false},
		{"EXPLAIN FORMAT = 'brief' DELETE FROM t", "DELETE", true},
		// MariaDB:ANALYZE <语句> 会执行
		{"ANALYZE DELETE FROM t WHERE id = 1", "DELETE", false},
		{"analyze format=json UPDATE t SET v = 1", "UPDATE", false},
		{"ANALYZE TABLE t", "ANALYZE", false},
		// 括号开头的查询((SELECT …) UNION (SELECT …)):括号不是 PostgreSQL 的选项列表,动词是 SELECT(2026-10)
		{"(SELECT a FROM t LIMIT 10) UNION (SELECT a FROM t2 LIMIT 10) LIMIT 10", "SELECT", false},
		{"((SELECT 1)) UNION ALL (SELECT 2)", "SELECT", false},
		{"EXPLAIN (SELECT a FROM t LIMIT 10) UNION (SELECT a FROM t2)", "SELECT", true},
		{"EXPLAIN ANALYZE (SELECT a FROM t LIMIT 10) UNION (SELECT a FROM t2)", "SELECT", false},
		{"EXPLAIN (ANALYZE, BUFFERS) (SELECT 1) UNION (SELECT 2)", "SELECT", false},
		{"EXPLAIN (FORMAT JSON) (SELECT 1)", "SELECT", true},
	}
	for _, c := range cases {
		v, p := parseVerbExplain(c.sql)
		if v != c.verb || p != c.planOnly {
			t.Errorf("%q: 应为 verb=%s planOnly=%v,实际 %s %v", c.sql, c.verb, c.planOnly, v, p)
		}
	}
	// DESC ANALYZE 绝不能再被当成只读
	if IsRead("DESC ANALYZE DELETE t FROM t JOIN t2 ON t2.id = t.id") {
		t.Error("DESC ANALYZE DELETE 不是只读")
	}
	if !IsRead("(SELECT a FROM t) UNION (SELECT a FROM t2)") {
		t.Error("括号开头的 UNION 查询是只读")
	}
}

func TestPlanOnlySQL_RewritesExecutingExplainPerEngine(t *testing.T) {
	cases := []struct {
		engine, in, want string
	}{
		{"MySQL 8.0", "EXPLAIN ANALYZE SELECT * FROM t FOR UPDATE", "EXPLAIN SELECT * FROM t FOR UPDATE"},
		{"MySQL 8.0", "EXPLAIN ANALYZE SELECT * FROM t INTO OUTFILE '/tmp/x'", "EXPLAIN SELECT * FROM t INTO OUTFILE '/tmp/x'"},
		{"MySQL 8.0", "EXPLAIN ANALYZE SELECT 1--1 INTO OUTFILE '/tmp/x'", "EXPLAIN SELECT 1--1 INTO OUTFILE '/tmp/x'"},
		{"TiDB", "EXPLAIN ANALYZE SELECT SLEEP(10) FROM t", "EXPLAIN SELECT SLEEP(10) FROM t"},
		{"MySQL 8.0", "EXPLAIN ANALYZE SELECT * FROM t LOCK IN SHARE MODE", "EXPLAIN SELECT * FROM t LOCK IN SHARE MODE"},
		{"MySQL 8.0", "DESC ANALYZE DELETE t FROM t JOIN t2 ON t2.id = t.id", "DESC DELETE t FROM t JOIN t2 ON t2.id = t.id"},
		{"TiDB", "explain analyze format = 'brief' delete from t", "explain format = 'brief' delete from t"},
		{"MariaDB 10.11", "ANALYZE FORMAT=JSON UPDATE t SET v = 1", "EXPLAIN FORMAT=JSON UPDATE t SET v = 1"},
		{"MariaDB 10.11", "/* 注释 */ ANALYZE DELETE FROM t", "/* 注释 */ EXPLAIN DELETE FROM t"},
		{"PostgreSQL 16", "EXPLAIN ANALYZE DELETE FROM t", "EXPLAIN DELETE FROM t"},
		{"PostgreSQL 16", "EXPLAIN ANALYSE VERBOSE UPDATE t SET v = 1", "EXPLAIN VERBOSE UPDATE t SET v = 1"},
		{"PostgreSQL 16", "EXPLAIN (ANALYZE, BUFFERS, TIMING, FORMAT JSON) DELETE FROM t", "EXPLAIN (FORMAT JSON) DELETE FROM t"},
		{"PostgreSQL 16", "EXPLAIN (ANALYZE) UPDATE t SET v = 1", "EXPLAIN UPDATE t SET v = 1"},
		{"DWS", "EXPLAIN PERFORMANCE SELECT nextval('s') FROM t", "EXPLAIN SELECT nextval('s') FROM t"},
		{"PostgreSQL 16", "EXPLAIN ANALYZE SELECT * INTO t_copy FROM t", "EXPLAIN SELECT * INTO t_copy FROM t"},
		{"PostgreSQL 16", "EXPLAIN ANALYZE WITH d AS (DELETE FROM t RETURNING *) SELECT * FROM d", "EXPLAIN WITH d AS (DELETE FROM t RETURNING *) SELECT * FROM d"},
		{"PostgreSQL 16", "EXPLAIN (ANALYZE) SELECT * FROM t FOR SHARE", "EXPLAIN SELECT * FROM t FOR SHARE"},
		// 2026-10 审查:咨询锁、发通知、等复制位点,以及带引号的函数名
		{"PostgreSQL 16", "EXPLAIN ANALYZE SELECT pg_advisory_lock(1)", "EXPLAIN SELECT pg_advisory_lock(1)"},
		{"PostgreSQL 16", "EXPLAIN ANALYZE SELECT pg_try_advisory_xact_lock(1, 2)", "EXPLAIN SELECT pg_try_advisory_xact_lock(1, 2)"},
		{"PostgreSQL 16", "EXPLAIN ANALYZE SELECT pg_notify('c', 'x')", "EXPLAIN SELECT pg_notify('c', 'x')"},
		{"PostgreSQL 16", `EXPLAIN ANALYZE SELECT "pg_sleep"(100)`, `EXPLAIN SELECT "pg_sleep"(100)`},
		{"PostgreSQL 16", `EXPLAIN ANALYZE SELECT pg_catalog."pg_sleep"(100)`, `EXPLAIN SELECT pg_catalog."pg_sleep"(100)`},
		{"MySQL 8.0", "EXPLAIN ANALYZE SELECT MASTER_POS_WAIT('binlog.000001', 4)", "EXPLAIN SELECT MASTER_POS_WAIT('binlog.000001', 4)"},
		{"MySQL 8.0", "EXPLAIN ANALYZE SELECT WAIT_FOR_EXECUTED_GTID_SET('x:1-9')", "EXPLAIN SELECT WAIT_FOR_EXECUTED_GTID_SET('x:1-9')"},
		{"MySQL 8.0", "EXPLAIN ANALYZE SELECT `sleep`(100)", "EXPLAIN SELECT `sleep`(100)"},
		// 括号开头的查询:带锁 / 副作用照样去掉 ANALYZE,而且括号不能被当成选项列表拆掉
		{"MySQL 8.0", "EXPLAIN ANALYZE (SELECT * FROM t FOR UPDATE) UNION (SELECT * FROM t2)", "EXPLAIN (SELECT * FROM t FOR UPDATE) UNION (SELECT * FROM t2)"},
		{"MySQL 8.0", "EXPLAIN ANALYZE (SELECT SLEEP(10)) UNION (SELECT 1)", "EXPLAIN (SELECT SLEEP(10)) UNION (SELECT 1)"},
		{"PostgreSQL 16", "EXPLAIN ANALYZE (SELECT pg_sleep(10)) UNION (SELECT 1)", "EXPLAIN (SELECT pg_sleep(10)) UNION (SELECT 1)"},
		{"PostgreSQL 16", "EXPLAIN (ANALYZE, VERBOSE) (SELECT pg_sleep(10))", "EXPLAIN (VERBOSE) (SELECT pg_sleep(10))"},
	}
	for _, c := range cases {
		got, changed, err := planOnlySQL(c.engine, c.in)
		if err != nil || !changed || got != c.want {
			t.Errorf("%s %q: 应改写为 %q,实际 %q changed=%v err=%v", c.engine, c.in, c.want, got, changed, err)
		}
		if !PlanOnly(got) && !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(StripComments(got))), "DESC") {
			t.Errorf("%s %q: 改写后必须只出计划,实际 %q", c.engine, c.in, got)
		}
	}
	// 不该动的:只出计划的 EXPLAIN、看表结构、收集统计信息、普通语句、Oracle / SQLite 的计划语句,
	// 以及解释纯只读查询的 EXPLAIN ANALYZE
	for _, c := range []struct{ engine, in string }{
		{"MySQL 8.0", "EXPLAIN UPDATE t SET v = 1"},
		{"MySQL 8.0", "DESC t"},
		{"MariaDB 10.11", "ANALYZE TABLE t"},
		{"PostgreSQL 16", "EXPLAIN (FORMAT JSON) SELECT 1"},
		{"PostgreSQL 16", "SELECT 1"},
		{"Oracle 23ai", "EXPLAIN PLAN FOR DELETE FROM t"},
		{"SQLite", "EXPLAIN QUERY PLAN DELETE FROM t"},
		// 解释的是纯只读查询:保留 ANALYZE,让它真跑出实际执行数据(2026-10 用户要求)
		{"MySQL 8.0", "EXPLAIN ANALYZE SELECT * FROM t"},
		{"TiDB", "explain analyze select /*+ USE_INDEX(t, idx_a) */ id from t where a = 1 limit 100"},
		{"TiDB", "DESC ANALYZE SELECT id FROM t WHERE 'into outfile' = v"},
		{"MariaDB 10.11", "ANALYZE SELECT * FROM t"},
		{"PostgreSQL 16", "EXPLAIN (ANALYZE, BUFFERS) SELECT * FROM t"},
		{"PostgreSQL 16", "EXPLAIN ANALYZE WITH x AS (SELECT 1) SELECT * FROM x"},
		{"DWS", "EXPLAIN PERFORMANCE SELECT * FROM t"},
		// 列名里带这些字样、或字符串里提到它们,不是调用
		{"PostgreSQL 16", `EXPLAIN ANALYZE SELECT "pg_sleep_col", 'pg_advisory_lock(1)' FROM t`},
		{"MySQL 8.0", "EXPLAIN ANALYZE SELECT `sleep_at` FROM t"},
		// 括号开头的纯只读 UNION(2026-10 生产:被当成写去掉了 ANALYZE)
		{"MySQL 8.0", "explain analyze (SELECT login_name  FROM t_ekyc_request er  WHERE er.full_name LIKE 'THE%' LIMIT 10000) UNION (SELECT login_name  FROM t_ekyc e WHERE e.full_name LIKE 'THE%' LIMIT 10000) UNION (SELECT login_name  FROM t_kyc_request k WHERE k.full_name LIKE 'THE%' LIMIT 10000) LIMIT 10000"},
		{"TiDB", "EXPLAIN ANALYZE ((SELECT 1)) UNION ALL (SELECT 2)"},
		{"PostgreSQL 16", "EXPLAIN (ANALYZE, BUFFERS) (SELECT relname FROM pg_class LIMIT 5) UNION (SELECT 'x')"},
	} {
		if got, changed, err := planOnlySQL(c.engine, c.in); changed || err != nil || got != c.in {
			t.Errorf("%s %q 不该改写,实际 %q changed=%v err=%v", c.engine, c.in, got, changed, err)
		}
	}
}

// ---- 真库:拿到计划,而且数据一行都没变 ----

func explainProbeRows(t *testing.T, db *sql.DB, q string) string {
	t.Helper()
	var s string
	if err := db.QueryRow(q).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRealRun_PostgresExplainNeverExecutes(t *testing.T) {
	conn := testPostgres(t)
	raw, err := pgRaw(conn)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	tb := fmt.Sprintf("explain_probe_%d", time.Now().UnixNano()%1_000_000)
	if _, err := raw.Exec("CREATE TABLE " + tb + " (id INT PRIMARY KEY, v INT); INSERT INTO " + tb + " VALUES (1,1),(2,2),(3,3)"); err != nil {
		t.Fatal(err)
	}
	defer raw.Exec("DROP TABLE " + tb)
	snapshot := "SELECT COALESCE(string_agg(id || ':' || v, ',' ORDER BY id), '空表') FROM " + tb
	before := explainProbeRows(t, raw, snapshot)
	for _, q := range []string{
		"EXPLAIN ANALYZE DELETE FROM " + tb + " WHERE id = 1",
		"EXPLAIN (ANALYZE, BUFFERS, TIMING) UPDATE " + tb + " SET v = 99 WHERE id = 2",
		"EXPLAIN ANALYSE VERBOSE DELETE FROM " + tb,
		"EXPLAIN UPDATE " + tb + " SET v = 7",
	} {
		res, err := RealRun(context.Background(), conn, q, 30*time.Second)
		if err != nil {
			t.Fatalf("%q: %v", q, err)
		}
		if len(res.Columns) == 0 || res.Rows == 0 {
			t.Errorf("%q: 应返回执行计划,实际 %+v", q, res)
		}
		if strings.Contains(strings.ToUpper(q), "ANALY") && res.NoticeRef == nil {
			t.Errorf("%q: 改写成只取计划时要告诉用户", q)
		}
	}
	if after := explainProbeRows(t, raw, snapshot); after != before {
		t.Fatalf("EXPLAIN 不该改动数据:之前 %s,之后 %s", before, after)
	}
}

func TestRealRun_MySQLExplainNeverExecutes(t *testing.T) {
	conn, admin := testMySQLConn(t)
	schema := fmt.Sprintf("explain_probe_%d", time.Now().UnixNano()%1_000_000)
	for _, q := range []string{
		"CREATE DATABASE " + schema,
		"CREATE TABLE " + schema + ".t (id INT PRIMARY KEY, v INT)",
		"CREATE TABLE " + schema + ".t2 (id INT PRIMARY KEY)",
		"INSERT INTO " + schema + ".t VALUES (1,1),(2,2),(3,3)",
		"INSERT INTO " + schema + ".t2 VALUES (1),(2),(3)",
	} {
		if _, err := admin.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	defer admin.Exec("DROP DATABASE " + schema)
	conn.Database = schema
	snapshot := "SELECT COALESCE(GROUP_CONCAT(CONCAT(id, ':', v) ORDER BY id), '空表') FROM " + schema + ".t"
	before := explainProbeRows(t, admin, snapshot)
	for _, q := range []string{
		"EXPLAIN UPDATE t JOIN t2 ON t2.id = t.id SET t.v = 500 WHERE t.id = 2",
		"DESC UPDATE t SET v = 9 WHERE id = 1",
		"DESC ANALYZE UPDATE t JOIN t2 ON t2.id = t.id SET t.v = 500 WHERE t.id = 2",
		"EXPLAIN ANALYZE DELETE t FROM t JOIN t2 ON t2.id = t.id WHERE t.id = 1",
	} {
		res, err := RealRun(context.Background(), conn, q, 30*time.Second)
		if err != nil {
			t.Fatalf("%q: %v", q, err)
		}
		if len(res.Columns) == 0 || res.Rows == 0 {
			t.Errorf("%q: 应返回执行计划,实际 %+v", q, res)
		}
	}
	if after := explainProbeRows(t, admin, snapshot); after != before {
		t.Fatalf("EXPLAIN 不该改动数据:之前 %s,之后 %s", before, after)
	}
}

func TestRealRun_OracleExplainPlanShowsThePlan(t *testing.T) {
	conn := testOracle(t)
	tb := fmt.Sprintf("EXPLAIN_PROBE_%d", time.Now().UnixNano()%1_000_000)
	run := func(q string) ExecResult {
		t.Helper()
		res, err := RealRun(context.Background(), conn, q, 60*time.Second)
		if err != nil {
			t.Fatalf("%q: %v", q, err)
		}
		return res
	}
	run("CREATE TABLE " + tb + " (id NUMBER PRIMARY KEY, v NUMBER)")
	defer RealRun(context.Background(), conn, "DROP TABLE "+tb+" PURGE", 60*time.Second)
	run("INSERT INTO " + tb + " VALUES (1, 1)")
	for _, q := range []string{
		"EXPLAIN PLAN FOR DELETE FROM " + tb + " WHERE id = 1",
		"EXPLAIN PLAN SET STATEMENT_ID = 'mine' FOR UPDATE " + tb + " SET v = 9",
	} {
		res := run(q)
		joined := ""
		for _, r := range res.Data {
			joined += r[0] + "\n"
		}
		if len(res.Columns) != 1 || !strings.Contains(strings.ToUpper(joined), strings.ToUpper(tb)) {
			t.Errorf("%q: 应读回包含表名的执行计划,实际:\n%s", q, joined)
		}
	}
	res := run("SELECT COUNT(*) FROM " + tb + " WHERE v = 1")
	if len(res.Data) != 1 || res.Data[0][0] != "1" {
		t.Fatalf("EXPLAIN PLAN 不该改动数据: %+v", res)
	}
}

var _ = model.Connection{}

// 解释的是纯只读查询:ANALYZE 保留,真跑出实际执行数据,也不再提示"已去掉 ANALYZE"。
func TestRealRun_ExplainAnalyzeOfAReadRunsForReal(t *testing.T) {
	check := func(t *testing.T, conn *model.Connection, q string) {
		t.Helper()
		res, err := RealRun(context.Background(), conn, q, 30*time.Second)
		if err != nil {
			t.Fatalf("%q: %v", q, err)
		}
		if res.NoticeRef != nil {
			t.Errorf("%q: 纯只读查询不该被改写,却提示 %+v", q, res.NoticeRef)
		}
		var all strings.Builder
		for _, row := range res.Data {
			all.WriteString(strings.Join(row, " "))
		}
		if !strings.Contains(strings.ToLower(all.String()), "actual") {
			t.Errorf("%q: 应返回实际执行数据(actual …),实际 %v", q, res.Data)
		}
	}
	t.Run("postgres", func(t *testing.T) {
		conn := testPostgres(t)
		check(t, conn, "EXPLAIN ANALYZE SELECT g FROM generate_series(1, 100) g WHERE g % 7 = 0")
		check(t, conn, "EXPLAIN (ANALYZE, BUFFERS) SELECT count(*) FROM pg_class")
		check(t, conn, "EXPLAIN ANALYZE (SELECT relname FROM pg_class LIMIT 5) UNION (SELECT nspname FROM pg_namespace LIMIT 5)")
	})
	t.Run("mysql", func(t *testing.T) {
		conn, _ := testMySQLConn(t)
		check(t, conn, "EXPLAIN ANALYZE SELECT COUNT(*) FROM information_schema.schemata")
		check(t, conn, "explain analyze (SELECT schema_name FROM information_schema.schemata LIMIT 5) UNION (SELECT table_name FROM information_schema.tables LIMIT 5) LIMIT 10")
	})
}
