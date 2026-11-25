package gateway

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"velagateway/internal/model"
)

func TestBizReadOnlyCheck(t *testing.T) {
	dbs := []string{"orders_db", "finance_db", "hr_db"}
	ok := []struct{ name, sql string }{
		{"普通查询", "SELECT id, amount FROM orders WHERE id = 1"},
		{"带本库前缀", "SELECT * FROM orders_db.orders o JOIN orders_db.items i ON o.id = i.oid"},
		{"大小写不同的本库前缀", "SELECT * FROM ORDERS_DB.orders"},
		{"别名.列", "SELECT o.id, o.amount FROM orders o"},
		{"CTE", "WITH t AS (SELECT id FROM orders) SELECT * FROM t"},
		{"EXPLAIN", "EXPLAIN SELECT * FROM orders"},
		{"DESC", "DESC orders"},
		{"末尾分号与注释", "SELECT 1; -- done"},
		{"字符串里的关键字不算", "SELECT * FROM orders WHERE note = 'insert into finance_db.x for update sleep('"},
		{"中文表名", "SELECT * FROM orders_db.订单"},
	}
	for _, c := range ok {
		if r := BizReadOnlyCheck("MySQL 8.0", c.sql, "orders_db", dbs); r != nil {
			t.Errorf("%s:应当放行,实际拒绝 %q", c.name, model.RenderBizReason(r))
		}
	}
	bad := []struct{ name, sql, want string }{
		{"多条语句", "SELECT 1; SELECT 2", "一条"},
		{"写", "UPDATE orders SET amount = 0", "UPDATE"},
		{"删", "DELETE FROM orders", "DELETE"},
		{"CTE 夹带删除", "WITH d AS (DELETE FROM orders RETURNING *) SELECT * FROM d", "SELECT"},
		{"EXPLAIN ANALYZE 删除", "EXPLAIN ANALYZE DELETE FROM orders", "SELECT"},
		{"SHOW", "SHOW DATABASES", "SHOW"},
		{"切库", "USE finance_db", "USE"},
		{"加锁", "SELECT * FROM orders FOR UPDATE", "加锁"},
		{"共享锁", "SELECT * FROM orders LOCK IN SHARE MODE", "加锁"},
		{"写文件", "SELECT * FROM orders INTO OUTFILE '/tmp/x'", "INTO"},
		{"读文件", "SELECT LOAD_FILE('/etc/passwd')", "LOAD_FILE"},
		{"睡眠", "SELECT SLEEP(100)", "SLEEP"},
		{"别的库", "SELECT * FROM finance_db.salary", "finance_db"},
		{"反引号包着别的库", "SELECT * FROM `finance_db`.`salary`", "finance_db"},
		{"逗号连接里的别的库", "SELECT * FROM orders, hr_db.staff", "hr_db"},
		{"子查询里的别的库", "SELECT * FROM orders WHERE uid IN (SELECT id FROM hr_db.staff)", "hr_db"},
		{"系统库", "SELECT * FROM mysql.user", "系统库"},
		{"information_schema", "SELECT * FROM information_schema.tables", "系统库"},
		{"DESC 别的库", "DESC finance_db.salary", "finance_db"},
	}
	for _, c := range bad {
		r := model.RenderBizReason(BizReadOnlyCheck("MySQL 8.0", c.sql, "orders_db", dbs))
		if r == "" {
			t.Errorf("%s:应当拒绝", c.name)
		} else if !strings.Contains(r, c.want) {
			t.Errorf("%s:拒绝原因应含 %q,实际 %q", c.name, c.want, r)
		}
	}
	// MySQL 默认把 \' 当转义:'a\'' 是字符串 a',后面紧跟的是真语法。网关的词法按 SQL 标准
	// 把 'a\' 当完整字符串,于是后面那段被当成另一个字符串抹掉 —— 跨库、系统库、危险函数、
	// INTO 全都看不见。已在真实 MySQL 8.0 上确认第一条会返回 mysql.user 的数据。
	for _, c := range []struct{ name, sql string }{
		{"\\' 藏系统库", `SELECT 'a\'', (SELECT authentication_string FROM mysql.user LIMIT 1) AS 'z'`},
		{"\\' 藏别的库", `SELECT 'a\'', s.* FROM finance_db.salary s WHERE 'x' = 'x'`},
		{"\\' 藏 SLEEP", `SELECT 'a\'', SLEEP(30) AS 'z'`},
		{"\\' 藏 INTO", `SELECT 'a\'', 1 INTO OUTFILE '/tmp/x' -- '`},
		{"双引号字符串里的 \\\"", `SELECT "a\"", (SELECT 1 FROM mysql.user LIMIT 1) AS "z"`},
		{"\\' 与注释配合", `SELECT 'a\'' /* ', (SELECT 1 FROM mysql.user) AS x -- */`},
	} {
		if r := BizReadOnlyCheck("MySQL 8.0", c.sql, "orders_db", dbs); r == nil {
			t.Errorf("%s:应当拒绝", c.name)
		}
	}
	// 反斜杠本身不是问题:不在引号前面的反斜杠,两种理解下字面量的边界一样
	for _, s := range []string{
		`SELECT * FROM orders WHERE code LIKE 'a\_%'`,
		`SELECT * FROM orders WHERE path = 'C:\\data'`,
		`SELECT * FROM orders WHERE name = 'O''Brien'`,
	} {
		if r := BizReadOnlyCheck("MySQL 8.0", s, "orders_db", dbs); r != nil {
			t.Errorf("%s:应当放行,实际拒绝 %q", s, model.RenderBizReason(r))
		}
	}

	// PostgreSQL:限定符是 schema,不是库,不做库名检查;其余规则一样
	if r := BizReadOnlyCheck("PostgreSQL 15", "SELECT * FROM public.orders", "orders_db", dbs); r != nil {
		t.Errorf("PG 的 schema 限定符不该被当成库:%q", model.RenderBizReason(r))
	}
	if r := BizReadOnlyCheck("PostgreSQL 15", "SELECT pg_sleep(10)", "orders_db", dbs); r == nil {
		t.Error("PG 的 pg_sleep 应当拒绝")
	}
	// Oracle:限定符是 owner,就是"库"
	if r := BizReadOnlyCheck("Oracle 19c", "SELECT * FROM FINANCE_DB.SALARY", "ORDERS_DB", dbs); r == nil {
		t.Error("Oracle 引用别的 owner 应当拒绝")
	}
	if r := BizReadOnlyCheck("Oracle 19c", "SELECT * FROM SYS.USER$", "ORDERS_DB", dbs); r == nil {
		t.Error("Oracle 引用 SYS 应当拒绝")
	}
	if r := BizReadOnlyCheck("Oracle 19c", "SELECT DBMS_RANDOM.VALUE FROM DUAL", "ORDERS_DB", dbs); r == nil {
		t.Error("Oracle 调 DBMS_ 包应当拒绝")
	}
}

// 拒绝原因带机器可读的 code:界面按读者语言渲染,并据此决定要不要刷新授权列表。
func TestBizReadOnlyCheck_ReasonCodes(t *testing.T) {
	dbs := []string{"orders_db", "finance_db"}
	cases := []struct {
		sql, code string
		args      map[string]string
	}{
		{"SELECT 1; SELECT 2", model.BizMulti, map[string]string{"n": "2"}},
		{"UPDATE t SET a = 1", model.BizNotSelect, map[string]string{"verb": "UPDATE"}},
		{"SELECT * FROM mysql.user", model.BizSystemDB, map[string]string{"name": "mysql", "db": "orders_db"}},
		{"SELECT * FROM finance_db.salary", model.BizOtherDB, map[string]string{"name": "finance_db", "db": "orders_db"}},
		{`SELECT 'a\'', 1 AS 'z'`, model.BizBackslash, nil},
		{"SELECT SLEEP(1)", model.BizFunc, map[string]string{"name": "SLEEP"}},
	}
	for _, c := range cases {
		r := BizReadOnlyCheck("MySQL 8.0", c.sql, "orders_db", dbs)
		if r == nil || r.Code != c.code {
			t.Errorf("%s:code 应为 %s,实际 %+v", c.sql, c.code, r)
			continue
		}
		for k, v := range c.args {
			if r.Args[k] != v {
				t.Errorf("%s:参数 %s 应为 %q,实际 %q", c.sql, k, v, r.Args[k])
			}
		}
		if model.RenderBizReason(r) == "" {
			t.Errorf("%s:code %s 没有规范中文串", c.sql, c.code)
		}
	}
}

func TestApplyRowLimit(t *testing.T) {
	cases := []struct{ name, engine, in, want string }{
		{"补在末尾", "MySQL 8.0", "SELECT * FROM t", "SELECT * FROM t\nLIMIT 1001"},
		{"去掉分号", "MySQL 8.0", "SELECT * FROM t;", "SELECT * FROM t\nLIMIT 1001"},
		{"末尾行注释", "MySQL 8.0", "SELECT * FROM t -- 看看", "SELECT * FROM t -- 看看\nLIMIT 1001"},
		{"已有更小的 LIMIT 不动", "MySQL 8.0", "SELECT * FROM t LIMIT 5", "SELECT * FROM t LIMIT 5"},
		{"已有更大的 LIMIT 改小", "MySQL 8.0", "SELECT * FROM t LIMIT 100000000", "SELECT * FROM t LIMIT 1001"},
		{"MySQL 的 LIMIT 偏移, 行数", "MySQL 8.0", "SELECT * FROM t LIMIT 20, 99999999", "SELECT * FROM t LIMIT 20, 1001"},
		{"LIMIT … OFFSET", "PostgreSQL 15", "SELECT * FROM t LIMIT 99999 OFFSET 10", "SELECT * FROM t LIMIT 1001 OFFSET 10"},
		{"PG 的 LIMIT ALL", "PostgreSQL 15", "SELECT * FROM t LIMIT ALL", "SELECT * FROM t LIMIT 1001"},
		{"PG FETCH FIRST 更大的改小", "PostgreSQL 15", "SELECT * FROM t FETCH FIRST 50000 ROWS ONLY", "SELECT * FROM t FETCH FIRST 1001 ROWS ONLY"},
		{"大小写与换行", "MySQL 8.0", "select * from t\nlimit\n  2000", "select * from t\nlimit\n  1001"},
		{"末尾分号", "MySQL 8.0", "SELECT * FROM t LIMIT 9999;", "SELECT * FROM t LIMIT 1001"},
		{"子查询里的 LIMIT 不算", "MySQL 8.0", "SELECT * FROM (SELECT * FROM t LIMIT 5) x", "SELECT * FROM (SELECT * FROM t LIMIT 5) x\nLIMIT 1001"},
		{"字符串里的 LIMIT 不算", "MySQL 8.0", "SELECT * FROM t WHERE a = 'limit 1'", "SELECT * FROM t WHERE a = 'limit 1'\nLIMIT 1001"},
		{"CTE", "PostgreSQL 15", "WITH x AS (SELECT 1) SELECT * FROM x", "WITH x AS (SELECT 1) SELECT * FROM x\nLIMIT 1001"},
		{"PG FETCH FIRST 不动", "PostgreSQL 15", "SELECT * FROM t FETCH FIRST 3 ROWS ONLY", "SELECT * FROM t FETCH FIRST 3 ROWS ONLY"},
		{"EXPLAIN 不动", "MySQL 8.0", "EXPLAIN SELECT * FROM t", "EXPLAIN SELECT * FROM t"},
		{"DESC 不动", "MySQL 8.0", "DESC t", "DESC t"},
		{"Oracle 不改写", "Oracle 19c", "SELECT * FROM t", "SELECT * FROM t"},
	}
	for _, c := range cases {
		got, reason := ApplyRowLimit(c.engine, c.in, 1001)
		if reason != nil || got != c.want {
			t.Errorf("%s:\n got %q (%s)\nwant %q", c.name, got, model.RenderBizReason(reason), c.want)
		}
	}
	// 最外层 LIMIT 后面不是数字:网关确认不了它有多大,不执行
	for _, q := range []string{
		"SELECT * FROM t LIMIT @n",
		"SELECT * FROM t LIMIT (SELECT 100000000)",
		"SELECT * FROM t LIMIT 10 OFFSET 0 + 1, 5",
	} {
		if _, reason := ApplyRowLimit("MySQL 8.0", q, 1001); reason == nil || reason.Code != model.BizLimitNotNumber {
			t.Errorf("%s:LIMIT 不是数字,应当拒绝", q)
		}
	}
}

// ---------------------------------------------------------------- 真实 MySQL

func bizSetup(t *testing.T) (*sql.DB, func()) {
	t.Helper()
	conn := testMySQL(t)
	drv, dsn, _ := engineDriver(conn)
	db, err := sql.Open(drv, dsn)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{
		"CREATE DATABASE IF NOT EXISTS biztest",
		"CREATE TABLE IF NOT EXISTS biztest.nums (n INT PRIMARY KEY)",
	} {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	var c int
	_ = db.QueryRow("SELECT COUNT(*) FROM biztest.nums").Scan(&c)
	if c < 1000 {
		_, _ = db.Exec("DELETE FROM biztest.nums")
		var b strings.Builder
		b.WriteString("INSERT INTO biztest.nums VALUES ")
		for i := 1; i <= 1000; i++ {
			if i > 1 {
				b.WriteString(",")
			}
			b.WriteString("(" + itoa64(int64(i)) + ")")
		}
		if _, err := db.Exec(b.String()); err != nil {
			t.Fatal(err)
		}
	}
	return db, func() { db.Close() }
}

func itoa64(n int64) string { return strings.TrimSpace(strings.Replace(fmtInt(n), " ", "", -1)) }
func fmtInt(n int64) string {
	const d = "0123456789"
	if n == 0 {
		return "0"
	}
	s := ""
	for n > 0 {
		s = string(d[n%10]) + s
		n /= 10
	}
	return s
}

// 判定用的库名全集必须包含**没有表的库**:库今天是空的、明天建了表,中间不能有一段时间
// 它不算"别的库"。元数据缓存(information_schema.tables)恰恰漏掉空库,也会被行数上限截断。
func TestMySQLRealDatabaseNames_IncludesEmptyDatabase(t *testing.T) {
	db, done := bizSetup(t)
	defer done()
	if _, err := db.Exec("CREATE DATABASE IF NOT EXISTS biz_empty_db"); err != nil {
		t.Fatal(err)
	}
	names, err := RealDatabaseNames(testMySQL(t))
	if err != nil {
		t.Fatal(err)
	}
	has := map[string]bool{}
	for _, n := range names {
		has[n] = true
	}
	for _, want := range []string{"biz_empty_db", "biztest", "mysql"} {
		if !has[want] {
			t.Errorf("库名清单应含 %s,实际 %v", want, names)
		}
	}
}

func TestMySQLBizQuery_ReadOnlyTransactionRefusesWrites(t *testing.T) {
	_, done := bizSetup(t)
	defer done()
	conn := testMySQL(t)
	conn.Database = "biztest"
	// 判定层之外的那道闸:哪怕一条写语句漏过了判定,只读事务里数据库自己会拒绝。
	_, err := RealBizQuery(context.Background(), conn, "INSERT INTO nums VALUES (100000)", 10, 5*time.Second)
	if err == nil || !strings.Contains(err.Error(), "READ ONLY") {
		t.Fatalf("只读事务应当拒绝写入,实际 err=%v", err)
	}
}

func TestMySQLBizQuery_RowCapAndServerTimeout(t *testing.T) {
	_, done := bizSetup(t)
	defer done()
	conn := testMySQL(t)
	conn.Database = "biztest"

	res, err := RealBizQuery(context.Background(), conn, "SELECT n FROM nums ORDER BY n", 5, 5*time.Second)
	if err != nil || len(res.Data) != 5 || !res.Truncated || res.Data[0][0] != "1" {
		t.Fatalf("应只读 5 行并标记截断,实际 %d 行 truncated=%v err=%v", len(res.Data), res.Truncated, err)
	}

	// 10 亿行的笛卡尔积(SUM 让优化器没法走捷径):库端 max_execution_time 或网关超时,总之要在 ~1 秒停下
	start := time.Now()
	_, err = RealBizQuery(context.Background(), conn, "SELECT SUM(a.n * b.n * c.n) FROM nums a, nums b, nums c", 10, time.Second)
	if err == nil {
		t.Fatal("超长查询应当超时")
	}
	if el := time.Since(start); el > 4*time.Second {
		t.Fatalf("超时 1 秒的查询跑了 %v", el)
	}
	if !errors.Is(err, context.DeadlineExceeded) && !strings.Contains(err.Error(), "maximum statement execution time") {
		t.Errorf("应当是超时错误,实际 %v", err)
	}

	// 会话变量恢复:连接回到池子后,别人拿到的 max_execution_time 不是这次设的 1000
	for i := 0; i < 6; i++ {
		r, err := RealRun(context.Background(), conn, "SELECT @@SESSION.max_execution_time", 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if r.Data[0][0] == "1000" {
			t.Fatalf("业务查询的会话时限泄漏到了连接池里的别的连接上")
		}
	}
}

// 业务查询源的库列表(外部审批系统的下拉)现查这个函数:没有表的库要在,系统库不能在。
func TestMySQLDiffDatabaseNames_EmptyDatabaseInSystemOut(t *testing.T) {
	db, done := bizSetup(t)
	defer done()
	if _, err := db.Exec("CREATE DATABASE IF NOT EXISTS biz_empty_db"); err != nil {
		t.Fatal(err)
	}
	names, err := DiffDatabaseNames(testMySQL(t))
	if err != nil {
		t.Fatal(err)
	}
	has := map[string]bool{}
	for _, n := range names {
		has[strings.ToLower(n)] = true // TiDB 的系统库是大写的
	}
	if !has["biz_empty_db"] || !has["biztest"] {
		t.Errorf("应含空库和普通库,实际 %v", names)
	}
	for _, sys := range []string{"mysql", "information_schema", "performance_schema", "sys", "metrics_schema"} {
		if has[sys] {
			t.Errorf("不该列系统库 %s,实际 %v", sys, names)
		}
	}
}
