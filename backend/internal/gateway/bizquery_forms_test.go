package gateway

import (
	"testing"

	"velagateway/internal/model"
)

// 2026-10-29:数据查询放开 SHOW 表结构白名单与 DESC,并拦"看着是查询、实际在做别的"的变种。
func TestBizReadOnlyCheck_ShowDescWhitelist(t *testing.T) {
	dbs := []string{"orders_db", "finance_db"}
	for _, eng := range []string{"MySQL 8.0", "TiDB", "MariaDB 10.6"} {
		for _, q := range []string{
			"SHOW CREATE TABLE orders",
			"show create table `orders`;",
			"SHOW CREATE TABLE orders_db.orders",
			"SHOW CREATE VIEW v_orders",
			"SHOW COLUMNS FROM orders",
			"SHOW FULL COLUMNS FROM orders LIKE 'a%'",
			"SHOW FIELDS IN orders FROM orders_db",
			"SHOW INDEX FROM orders",
			"SHOW KEYS IN orders WHERE Key_name = 'PRIMARY'",
			"SHOW TABLES",
			"SHOW FULL TABLES",
			"SHOW TABLES FROM orders_db LIKE 'ord%'",
			"SHOW TABLES IN `ORDERS_DB`",
			"SHOW TABLE STATUS",
			"SHOW TABLE STATUS LIKE 'orders'",
			"DESC orders",
			"DESCRIBE orders_db.orders",
			"DESC orders amount",
			"DESC orders 'am%'",
			"DESC SELECT * FROM orders",
			"EXPLAIN orders",
		} {
			if r := BizReadOnlyCheck(eng, q, "orders_db", dbs); r != nil {
				t.Errorf("%s %q:应放行,实际拒绝 %q", eng, q, model.RenderBizReason(r))
			}
		}
		deny := []struct{ q, code string }{
			{"SHOW DATABASES", model.BizShowForm},
			{"SHOW PROCESSLIST", model.BizShowForm},
			{"SHOW FULL PROCESSLIST", model.BizShowForm},
			{"SHOW GRANTS", model.BizShowForm},
			{"SHOW VARIABLES LIKE 'secure%'", model.BizShowForm},
			{"SHOW GLOBAL STATUS", model.BizShowForm},
			{"SHOW ENGINE INNODB STATUS", model.BizShowForm},
			{"SHOW CREATE USER root", model.BizShowForm},
			{"SHOW CREATE PROCEDURE p", model.BizShowForm},
			{"SHOW CREATE DATABASE finance_db", model.BizShowForm},
			{"SHOW BINARY LOGS", model.BizShowForm},
			{"SHOW SLAVE STATUS", model.BizShowForm},
			{"SHOW TABLES FROM finance_db", model.BizOtherDB},
			{"SHOW TABLES IN mysql", model.BizSystemDB},
			{"SHOW TABLE STATUS FROM `finance_db`", model.BizOtherDB},
			{"SHOW COLUMNS FROM salary FROM finance_db", model.BizOtherDB},
			{"SHOW INDEX FROM user IN mysql", model.BizSystemDB},
			{"SHOW CREATE TABLE finance_db.salary", model.BizOtherDB},
			{"SHOW COLUMNS FROM information_schema.tables", model.BizSystemDB},
			{"SHOW TABLES WHERE Tables_in_orders_db IN (SELECT id FROM finance_db.salary)", model.BizOtherDB},
			{"SHOW TABLES; DROP TABLE orders", model.BizMulti},
			{"DESC FOR CONNECTION 12", model.BizOtherSession},
			{"EXPLAIN FOR CONNECTION 12", model.BizOtherSession},
			{"EXPLAIN FORMAT=JSON FOR CONNECTION 12", model.BizOtherSession},
			{"DESC finance_db.salary", model.BizOtherDB},
			{"DESC orders amount extra", model.BizDescForm},
		}
		for _, c := range deny {
			r := BizReadOnlyCheck(eng, c.q, "orders_db", dbs)
			if r == nil || r.Code != c.code {
				got := "放行"
				if r != nil {
					got = r.Code + " " + model.RenderBizReason(r)
				}
				t.Errorf("%s %q:应以 %s 拒绝,实际 %s", eng, c.q, c.code, got)
			}
		}
	}
	// PG 系的 SHOW 读的是配置项,与表结构无关,照旧不放
	for _, q := range []string{"SHOW search_path", "SHOW ALL", "DESC orders"} {
		if r := BizReadOnlyCheck("PostgreSQL 16", q, "orders_db", nil); r == nil {
			t.Errorf("PostgreSQL %q:应拒绝", q)
		}
	}
}

func TestBizReadOnlyCheck_OracleDesc(t *testing.T) {
	dbs := []string{"APP", "FIN"}
	for _, q := range []string{"DESC orders", "describe APP.orders;", `DESC "MixedCase"`, "desc app . orders"} {
		if r := BizReadOnlyCheck("Oracle 19c", q, "APP", dbs); r != nil {
			t.Errorf("%q:应放行,实际拒绝 %q", q, model.RenderBizReason(r))
		}
	}
	deny := []struct{ q, code string }{
		{"DESC FIN.salary", model.BizOtherDB},
		{"DESC sys.user$", model.BizSystemDB},
		{"DESC orders@remote", model.BizDescForm},
		{"DESC dbms_output.put_line", model.BizFunc},
		{"DESC orders x", model.BizDescForm},
	}
	for _, c := range deny {
		r := BizReadOnlyCheck("Oracle 19c", c.q, "APP", dbs)
		if r == nil || r.Code != c.code {
			got := "放行"
			if r != nil {
				got = r.Code
			}
			t.Errorf("%q:应以 %s 拒绝,实际 %s", c.q, c.code, got)
		}
	}
	for q, want := range map[string][2]string{
		"DESC orders":          {"", "ORDERS"},
		"desc App.Orders;":     {"APP", "ORDERS"},
		`DESC "App"."Orders"`:  {"App", "Orders"},
		"-- c\nDESCRIBE t_user": {"", "T_USER"},
	} {
		o, n, ok := OracleBizDescribe(q)
		if !ok || o != want[0] || n != want[1] {
			t.Errorf("OracleBizDescribe(%q) = %q %q %v,应为 %q", q, o, n, ok, want)
		}
	}
}

// 字符串里藏 SQL、借链接、从库里发请求 —— 只读事务管不到的那些。
func TestBizReadOnlyCheck_HiddenExecutionVariants(t *testing.T) {
	pg := []string{
		"SELECT ts_stat('select to_tsvector(pg_terminate_backend(1)::text)')",
		"SELECT * FROM ts_stat($$select v from t$$)",
		"SELECT ts_rewrite('a'::tsquery, 'select x, y from t')",
		"SELECT query_to_xml('select * from pg_authid', true, false, '')",
		"SELECT table_to_xml('pg_authid', true, false, '')",
		"SELECT schema_to_xml('public', true, false, '')",
		"SELECT database_to_xml(true, false, '')",
		"SELECT database_to_xmlschema(true, false, '')",
		`SELECT "ts_stat"('select 1')`,
	}
	for _, q := range pg {
		if r := BizReadOnlyCheck("PostgreSQL 16", q, "orders_db", nil); r == nil || r.Code != model.BizFunc {
			t.Errorf("PG %q:应以 bizFunc 拒绝,实际 %v", q, r)
		}
	}
	ora := []struct{ q, code string }{
		{"SELECT * FROM salary@fin_link", model.BizDBLink},
		{`SELECT * FROM "SALARY"@"FIN.WORLD"`, model.BizDBLink},
		{"SELECT f@remote(1) FROM dual", model.BizDBLink},
		{"SELECT HTTPURITYPE('http://x/').getclob() FROM dual", model.BizFunc},
		{"SELECT httpuritype.createuri('http://x/').getclob() FROM dual", model.BizFunc},
		{"SELECT DBURITYPE('/FIN/SALARY').getclob() FROM dual", model.BizFunc},
		{"SELECT URIFACTORY.getUri('http://x/') FROM dual", model.BizFunc},
		{`SELECT XMLQUERY('ora:view("FIN","SALARY")' RETURNING CONTENT) FROM dual`, model.BizFunc},
		{`SELECT * FROM XMLTABLE('fn:collection("oradb:/FIN/SALARY")')`, model.BizFunc},
	}
	for _, c := range ora {
		r := BizReadOnlyCheck("Oracle 19c", c.q, "APP", []string{"APP", "FIN"})
		if r == nil || r.Code != c.code {
			got := "放行"
			if r != nil {
				got = r.Code
			}
			t.Errorf("Oracle %q:应以 %s 拒绝,实际 %s", c.q, c.code, got)
		}
	}
	// 日常写法不受影响:字符串里的 @、普通 XML 解析、名字里带 to_xml 的列 / 自定义函数
	for _, c := range []struct{ eng, q string }{
		{"Oracle 19c", "SELECT * FROM users WHERE email = 'a@b.com'"},
		{"Oracle 19c", "SELECT x.v FROM t, XMLTABLE('/r/v' PASSING t.doc COLUMNS v VARCHAR2(10) PATH '.') x"},
		{"Oracle 19c", "SELECT order_to_xml_flag, convert_to_xml(id) FROM orders"},
		{"PostgreSQL 16", "SELECT word, ndoc FROM stats_ts_stat ORDER BY ndoc DESC"},
		{"PostgreSQL 16", "SELECT to_tsvector('english', body) @@ to_tsquery('cat') FROM docs"},
		{"MySQL 8.0", "SELECT * FROM users WHERE email = 'a@b.com' AND @@sql_mode IS NOT NULL"},
	} {
		if r := BizReadOnlyCheck(c.eng, c.q, "APP", []string{"APP"}); r != nil {
			t.Errorf("%s %q:应放行,实际拒绝 %q", c.eng, c.q, model.RenderBizReason(r))
		}
	}
}

func TestOracleStmtNames(t *testing.T) {
	names, owners := oracleStmtNames(`SELECT app.f(o.id), "Mixed" FROM orders o WHERE o.note = 'not_a_name' -- tail_comment`)
	want := map[string]bool{"SELECT": true, "APP": true, "F": true, "O": true, "ID": true, "Mixed": true, "FROM": true, "ORDERS": true, "WHERE": true, "NOTE": true}
	if len(names) != len(want) {
		t.Fatalf("names = %v", names)
	}
	for _, n := range names {
		if !want[n] {
			t.Errorf("不该出现 %q(字符串、注释里的不是名字)", n)
		}
	}
	if len(owners) != 2 || owners[0] != "APP" || owners[1] != "O" {
		t.Errorf("owners = %v,应为 [APP O]", owners)
	}
}
