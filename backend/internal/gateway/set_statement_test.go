package gateway

import "testing"

// 2026-11 白盒审计 V-02:SET STATEMENT … FOR <语句> 曾被判成会话设置,只过 select 一道闸。
func TestV02_SetStatement_JudgedByInnerStatement(t *testing.T) {
	const q = "SET STATEMENT max_statement_time=1 FOR DROP TABLE t"
	if SessionScoped(q) {
		t.Fatal("SET STATEMENT … FOR 不是会话设置")
	}
	e := NewRiskEngine(&fakeStore{strict: true, caps: map[string]string{
		"select|prod": "allow", "write|prod": "allow", "ddl|prod": "deny", "grant|prod": "deny",
	}})
	deny := []string{
		q,
		"set statement a=1, b='FOR' for drop table t",
		"SET /* c */ STATEMENT a=1 FOR /*!50000 DROP TABLE t */",
		"SET#x\nSTATEMENT a=1 FOR DROP TABLE t",
		"SET STATEMENT a='\\'' FOR DROP TABLE t #'",
		"SET STATEMENT a=1 FOR GRANT ALL ON *.* TO u",
	}
	for _, s := range deny {
		if v := e.EvaluateFor([]int64{1}, "MariaDB", "prod", s); v.Action != ActionDeny {
			t.Errorf("%q 应被拒,实际 %s", s, v.Action)
		}
	}
	// 严格模式要看内层
	if v := e.EvaluateFor([]int64{1}, "MariaDB", "prod", "SET STATEMENT a=1 FOR DELETE FROM t"); v.Action == ActionAllow {
		t.Error("内层无 WHERE 的 DELETE 不能放行")
	}
}

// 只读角色:外层盖掉只读守卫的变量时,内层再像读也不行;普通读照常放行且加守卫。
func TestV02_SetStatement_ReadOnlyRole(t *testing.T) {
	e := NewRiskEngine(&fakeStore{caps: map[string]string{
		"select|prod": "allow", "write|prod": "deny", "ddl|prod": "deny", "grant|prod": "deny",
	}})
	for _, s := range []string{
		"SET STATEMENT transaction_read_only=0 FOR SELECT f()",
		"SET STATEMENT tx_read_only=0 FOR SELECT f()",
		"SET STATEMENT a=1 FOR DELETE FROM t WHERE id=1",
	} {
		if v := e.EvaluateFor([]int64{1}, "MariaDB", "prod", s); v.Action != ActionDeny {
			t.Errorf("%q 应被拒,实际 %s", s, v.Action)
		}
	}
	const read = "SET STATEMENT max_statement_time=30 FOR SELECT * FROM t"
	if v := e.EvaluateFor([]int64{1}, "MariaDB", "prod", read); v.Action != ActionAllow || v.Command != "SELECT" {
		t.Errorf("内层是读应放行,实际 %+v", v)
	}
	if !ReadOnlyStatements("MariaDB", read) {
		t.Error("内层是读就要加只读守卫,否则 SELECT f() 能借外层躲开守卫")
	}
	if ReadOnlyStatements("MariaDB", "SET STATEMENT a=1 FOR UPDATE t SET a=1 WHERE id=1") {
		t.Error("内层是写不算只读")
	}
}

// 原有行为:真正的会话设置照旧。
func TestV02_RealSessionSettingsUnchanged(t *testing.T) {
	for _, s := range []string{
		"SET NAMES utf8mb4", "SET @a = 1", "SET SESSION sql_mode=''", "SET search_path TO dwd",
		"SET statement_timeout = 1000", "SET statement_timeout TO '5s'",
		"ALTER SESSION SET NLS_DATE_FORMAT='YYYY-MM-DD'",
	} {
		if !SessionScoped(s) {
			t.Errorf("%q 应仍是会话设置", s)
		}
	}
	for _, s := range []string{"SET GLOBAL read_only=0", "SET PASSWORD FOR u='x'", "SET ROLE dba"} {
		if SessionScoped(s) {
			t.Errorf("%q 不是会话设置", s)
		}
	}
	// 外层写错、没有 FOR:不是可执行的包装,按原来的动词判(write)
	if inners := setStatementInners("SET STATEMENT a=1"); len(inners) != 0 {
		t.Errorf("没有 FOR 不该解开,实际 %q", inners)
	}
	// 括号里的 FOR 不是外层的 FOR
	if inners := setStatementInners("SET STATEMENT a=(SELECT 1 FOR UPDATE) FOR SELECT 2"); len(inners) != 1 || inners[0] != "SELECT 2" {
		t.Errorf("实际 %q", inners)
	}
}
