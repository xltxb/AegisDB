package gateway

import "testing"

// 2026-11 白盒审计 V-04:MySQL 把 #…<换行> 当空白,INTO#x<换行>OUTFILE 躲过了写文件检测。
func TestV04_WritesServerFile_HashComment(t *testing.T) {
	for _, s := range []string{
		"SELECT 1 INTO#\nOUTFILE '/tmp/a'",
		"SELECT 'x' INTO#x\nOUTFILE '/tmp/a'",
		"SELECT 'x' INTO#'\nOUTFILE '/tmp/a'",
		"SELECT 'x' INTO # aaa\n # bbb\n DUMPFILE '/tmp/a'",
	} {
		if !WritesServerFile(s) {
			t.Errorf("%q 应认出写文件", s)
		}
	}
	// 原有行为:字面量里的 # 和 into outfile 是数据,普通 INTO 不算写文件
	for _, s := range []string{
		"SELECT 'into#\noutfile'", "SELECT '#' FROM t", "SELECT a INTO @x FROM t", "SELECT `a#b` FROM t",
		"SELECT 1 -- # \nFROM t", "SELECT 1 /* # */ FROM t",
	} {
		if WritesServerFile(s) {
			t.Errorf("%q 不是写文件", s)
		}
	}
}

func TestV04_Evaluate_HashHidesEverything(t *testing.T) {
	e := NewRiskEngine(&fakeStore{caps: map[string]string{
		"select|prod": "allow", "write|prod": "allow", "ddl|prod": "deny", "grant|prod": "deny",
	}})
	if v := e.EvaluateFor([]int64{1}, "MariaDB", "prod", "SELECT 'x' INTO#x\nOUTFILE '/tmp/a'"); v.Action != ActionDeny {
		t.Errorf("终端应拒绝,实际 %s", v.Action)
	}
	if v := e.EvaluateFor([]int64{1}, "MySQL", "prod", "DROP#x\nTABLE t"); v.Action != ActionDeny {
		t.Errorf("DROP 应被拒,实际 %s", v.Action)
	}
	// 原有行为:带 # 注释的正常语句不受影响
	for _, s := range []string{
		"SELECT 1 # 备注", "# 开头的注释\nSELECT 1", "SELECT '#1' FROM t", "SELECT * FROM t WHERE a = '#' # 尾注释",
		"UPDATE t SET a='#' WHERE id=1",
	} {
		if v := e.EvaluateFor([]int64{1}, "MySQL", "prod", s); v.Action != ActionAllow {
			t.Errorf("%q 应放行,实际 %+v", s, v)
		}
	}
}

// 原有行为:带 # 注释的存储过程照旧按整块判(DELIMITER 已把它标成一条语句),不能拆成碎片。
func TestV04_Evaluate_RoutineWithHashComment(t *testing.T) {
	e := NewRiskEngine(&fakeStore{caps: map[string]string{
		"select|prod": "allow", "write|prod": "deny", "ddl|prod": "allow", "grant|prod": "deny",
	}})
	const body = "CREATE PROCEDURE p()\nBEGIN\n  # 备注\n  UPDATE t SET a=1 WHERE id=1;\nEND"
	if v := e.EvaluateFor([]int64{1}, "MySQL", "prod", body); v.Action != ActionAllow {
		t.Errorf("有 ddl 能力就能建过程,实际 %+v", v)
	}
	// 藏在 #' 后面的第二条语句要逐条判
	if v := e.EvaluateFor([]int64{1}, "MySQL", "prod", "SELECT 1 #'\n; UPDATE t SET a=1 WHERE id=1; -- '"); v.Action != ActionDeny {
		t.Errorf("藏起来的 UPDATE 应被拒,实际 %+v", v)
	}
}

// 2026-11 白盒审计 V-17:DELETE FROM t # WHERE id=1 在 MySQL 上是全表删除。
func TestV17_NoWhere_HashComment(t *testing.T) {
	for _, s := range []string{
		"DELETE FROM t # WHERE id=1",
		"UPDATE t SET a=1 # WHERE id=1",
		"DELETE FROM t #\n# WHERE id=1",
	} {
		if !NoWhereFor("MySQL", s) {
			t.Errorf("%q 没有 WHERE", s)
		}
	}
	e := NewRiskEngine(&fakeStore{strict: true, caps: map[string]string{"write|prod": "allow"}})
	if v := e.EvaluateFor([]int64{1}, "MySQL", "prod", "DELETE FROM t # WHERE id=1"); v.Action == ActionAllow {
		t.Error("严格模式应拦")
	}
	if verb, _, noWhere := e.ScanStatementFor("MySQL", "prod", "DELETE FROM t # WHERE id=1"); !noWhere {
		t.Errorf("脚本扫描也要认出无 WHERE(%s)", verb)
	}
	// 原有行为
	for _, s := range []string{
		"DELETE FROM t WHERE id=1 # 备注", "UPDATE t SET note='#' WHERE id=1",
		"DELETE FROM t # 备注\nWHERE id=1", "DELETE FROM t WHERE a='#'",
	} {
		if NoWhereFor("MySQL", s) {
			t.Errorf("%q 有 WHERE", s)
		}
	}
	// PostgreSQL 里 # 是异或运算符:后面的 WHERE 是真的
	for _, eng := range []string{"PostgreSQL", "DWS"} {
		if NoWhereFor(eng, "UPDATE t SET a = b # c WHERE id=1") {
			t.Errorf("%s 上 # 不是注释", eng)
		}
	}
}

func TestV04_BizQuery_HashComment(t *testing.T) {
	dbs := []string{"orders", "other"}
	for _, s := range []string{
		"SELECT 1 #'\n, (SELECT authentication_string FROM mysql.user LIMIT 1) #'",
		"SELECT * FROM other#x\n.t",
		"SELECT 'a' INTO#x\nOUTFILE '/tmp/a'",
	} {
		if r := BizReadOnlyCheck("MySQL", s, "orders", dbs); r == nil {
			t.Errorf("%q 应被拒", s)
		}
	}
	for _, s := range []string{"SELECT 1 # 备注", "# 备注\nSELECT * FROM t", "SELECT '#' FROM t"} {
		if r := BizReadOnlyCheck("MySQL", s, "orders", dbs); r != nil {
			t.Errorf("%q 应放行,实际 %+v", s, r)
		}
	}
}
