package service

import "testing"

// 导出只收只读语句,SELECT … INTO OUTFILE 不算(EX2)。`1--1` 在 MySQL 上是减负一,
// 拆分器却把它后面整行当注释剥掉 —— 只看拆分结果就会把一次写文件当成普通查询。
func TestExportSQLReadOnly_RejectsServerFileWrites(t *testing.T) {
	for _, sql := range []string{
		"SELECT * FROM t INTO OUTFILE '/tmp/x'",
		"SELECT 1--1 INTO OUTFILE '/tmp/p'",
		`SELECT 'a\'', 1 INTO OUTFILE '/tmp/x' -- '`,
	} {
		if exportSQLReadOnly(sql) {
			t.Errorf("exportSQLReadOnly(%q) = true; 写服务器文件不是只读", sql)
		}
	}
	if !exportSQLReadOnly("SELECT id FROM t WHERE note = 'into outfile'") {
		t.Error("字面量里的 into outfile 只是数据")
	}
	if !exportSQLReadOnly(`SELECT id FROM t WHERE note = 'it\'s'`) {
		t.Error("带转义引号的普通查询是只读")
	}
}

// 导出不走能力矩阵,调用即生效的内置函数只能在这里拒。
func TestExportSQLReadOnly_RejectsEffectfulCalls(t *testing.T) {
	for _, sql := range []string{
		"SELECT pg_terminate_backend(pid) FROM pg_stat_activity",
		"SELECT dblink_exec('host=x', 'DROP TABLE t')",
	} {
		if exportSQLReadOnly(sql) {
			t.Errorf("exportSQLReadOnly(%q) = true;调用有副作用的函数不是只读", sql)
		}
	}
}

// 加锁读与睡眠 / 加锁函数:与业务查询同一套判定(gateway.ReadSideEffect),2026-10 黑盒测试。
func TestExportSQLReadOnly_RejectsLockingReadsAndSleep(t *testing.T) {
	for _, sql := range []string{
		"SELECT * FROM t FOR UPDATE",
		"SELECT * FROM t LOCK IN SHARE MODE",
		"SELECT * FROM t FOR SHARE",
		"SELECT SLEEP(5)",
		"SELECT `sleep`(5)",
		"SELECT GET_LOCK('x', 10)",
		`SELECT 'a\'', SLEEP(5) -- '`,
	} {
		if exportSQLReadOnly(sql) {
			t.Errorf("exportSQLReadOnly(%q) = true;加锁读 / 睡眠 / 加锁函数不是只读", sql)
		}
	}
	for _, sql := range []string{
		"SELECT DATE_FORMAT(created_at, '%Y-%m'), REPLACE(name, ' ', ''), CONCAT(a, b) FROM t JOIN u ON u.id = t.uid",
		"WITH x AS (SELECT * FROM t) SELECT * FROM x WHERE id IN (SELECT id FROM u)",
		"SHOW TABLES",
	} {
		if !exportSQLReadOnly(sql) {
			t.Errorf("exportSQLReadOnly(%q) = false;正常导出不该拒绝", sql)
		}
	}
}

// 2026-10-26 回归排查:8302027 让导出复用业务查询的整张函数清单,Oracle 导 CLOB 的标准写法
// DBMS_LOB.SUBSTR 被拒;Oracle 里末尾带 \ 的字符串按 MySQL 读法读,后面字面量里的关键字被当成代码。
func TestExportReadOnly_OraclePurePackagesAndLiterals(t *testing.T) {
	for _, sql := range []string{
		"SELECT id, DBMS_LOB.SUBSTR(content, 4000, 1) AS content FROM t_doc",
		"SELECT id, DBMS_LOB.GETLENGTH(content) FROM t_doc",
		"SELECT id, UTL_RAW.CAST_TO_VARCHAR2(DBMS_LOB.SUBSTR(b, 2000, 1)) FROM t",
		"SELECT * FROM t WHERE DBMS_RANDOM.VALUE < 0.5",
	} {
		if !exportSQLReadOnly(sql, "Oracle 19c") {
			t.Errorf("Oracle 纯函数包导出不该拒绝: %q", sql)
		}
	}
	for _, sql := range []string{
		"SELECT UTL_HTTP.REQUEST('http://x') FROM dual",
		"SELECT DBMS_PIPE.RECEIVE_MESSAGE('p', 10) FROM dual",
		"SELECT * FROM t FOR UPDATE",
	} {
		if exportSQLReadOnly(sql, "Oracle 19c") {
			t.Errorf("有副作用的导出仍应拒绝: %q", sql)
		}
	}
	lit := `SELECT * FROM t WHERE path = 'D:\' AND remark LIKE '%for update%'`
	if !exportSQLReadOnly(lit, "Oracle 19c") {
		t.Errorf(`Oracle 上 \ 不是转义符,字面量里的 for update 不该当成代码: %q`, lit)
	}
	// PG 系两种读法都判:E'…' 里 \' 是转义,藏在"字符串"后面的函数 / 加锁读照旧拒
	for _, sql := range []string{
		`SELECT E'a\' ', pg_sleep(30), ' ' FROM t`,
		`SELECT E'a\' ', id FROM t FOR UPDATE --'`,
		`SELECT E'a\' ', pg_advisory_lock(1), ' '`,
	} {
		if exportSQLReadOnly(sql, "PostgreSQL 15") {
			t.Errorf("PG 上 E'' 串里藏的代码仍应拒绝: %q", sql)
		}
	}
	// MySQL 仍两种读法都判(sql_mode 决定 \ 是不是转义)
	if exportSQLReadOnly(`SELECT 'a\'', SLEEP(5) -- '`, "MySQL 8.0") {
		t.Error(`MySQL 上 \' 伪装的 SLEEP 仍应拒绝`)
	}
}
