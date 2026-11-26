package gateway

import "testing"

// UPDATE t SET a='x\' WHERE id=1 -- ':MySQL 读到的 WHERE 在字符串里,更新的是全表(MySQL 8.0 实测)。
func TestNoWhereFor_BackslashHidesWhere(t *testing.T) {
	for _, s := range []string{
		`UPDATE t SET a='x\' WHERE id=1 -- '`,
		`DELETE FROM t ORDER BY 'x\' WHERE id=1 -- '`,
		`UPDATE t SET a="x\" WHERE id=1 -- "`,
		`UPDATE t SET a='x\'#b' WHERE id=1 -- '`,
	} {
		for _, engine := range []string{"MySQL", "MariaDB", "TiDB"} {
			if !NoWhereFor(engine, s) {
				t.Errorf("%s 上 %s 没有 WHERE", engine, s)
			}
		}
	}
	// PostgreSQL 的 E'' 字符串同样认反斜杠
	if !NoWhereFor("PostgreSQL", `UPDATE t SET a=E'x\' WHERE id=1 -- '`) {
		t.Error("E'' 字符串里的 WHERE 不算")
	}
}

// 原有行为:别的引擎上普通字符串不认反斜杠,结尾是 \ 的值后面的 WHERE 是真的;正常语句不受影响。
func TestNoWhereFor_BackslashKeepsRealWhere(t *testing.T) {
	for _, engine := range []string{"PostgreSQL", "DWS", "Oracle", ""} {
		if NoWhereFor(engine, `UPDATE t SET p='C:\' WHERE id=1`) {
			t.Errorf("%s 上这条有 WHERE", engine)
		}
	}
	for _, s := range []string{
		`UPDATE t SET a='C:\\' WHERE id=1`,
		`DELETE FROM t WHERE a='x\'y'`,
		`UPDATE t SET a='#' WHERE id=1`,
	} {
		if NoWhereFor("MySQL", s) {
			t.Errorf("%s 有 WHERE", s)
		}
	}
	if !NoWhereFor("MySQL", `UPDATE t SET a='x'`) {
		t.Error("没有 WHERE 的 UPDATE 照旧算无 WHERE")
	}
}

func TestEvaluate_BackslashHidesWhereUnderStrict(t *testing.T) {
	e := NewRiskEngine(&fakeStore{strict: true, caps: map[string]string{"write|prod": "allow"}})
	if v := e.EvaluateFor([]int64{1}, "MySQL", "prod", `UPDATE t SET a='x\' WHERE id=1 -- '`); v.Action == ActionAllow {
		t.Errorf("严格模式应拦下,实际 %+v", v)
	}
	for engine, s := range map[string]string{
		"MySQL":      `DELETE FROM t WHERE a='x\'y'`,
		"PostgreSQL": `UPDATE t SET p='C:\' WHERE id=1`,
		"Oracle":     `UPDATE t SET p='C:\' WHERE id=1`,
	} {
		if v := e.EvaluateFor([]int64{1}, engine, "prod", s); v.Action != ActionAllow {
			t.Errorf("%s 上 %s 应放行,实际 %+v", engine, s, v)
		}
	}
	// 严格模式关着的分层不受影响
	off := NewRiskEngine(&fakeStore{caps: map[string]string{"write|prod": "allow"}})
	if v := off.EvaluateFor([]int64{1}, "MySQL", "prod", `UPDATE t SET a='x\' WHERE id=1 -- '`); v.Action != ActionAllow {
		t.Errorf("严格模式关着应放行,实际 %+v", v)
	}
}

// SELECT 'a\'', load_file(…) -- ':MySQL 读到的 load_file 是真调用(MySQL 8.0 实测读到文件)。
func TestEffectfulCall_ReadingsHideCall(t *testing.T) {
	for s, want := range map[string]string{
		`SELECT 'a\'', load_file('/etc/passwd') -- '`:        "LOAD_FILE",
		`SELECT "a\"", load_file('/etc/passwd') -- "`:        "LOAD_FILE",
		`SELECT E'a\'', pg_terminate_backend(1) -- '`:        "PG_TERMINATE_BACKEND",
		"SELECT load_file#x\n('/etc/passwd')":                "LOAD_FILE",
		`SELECT 'a\'#b', load_file#x` + "\n" + `('/x') -- '`: "LOAD_FILE",
	} {
		if got := EffectfulCall(s); got != want {
			t.Errorf("%s:得到 %q,应为 %q", s, got, want)
		}
	}
	// 原有行为:字面量里的函数名是数据,带转义引号的普通查询不算调用
	for _, s := range []string{
		`SELECT 'load_file(1)'`,
		`SELECT 'it\'s', a FROM t`,
		`SELECT 'it\'s load_file' FROM t`,
		`SELECT a FROM t WHERE b = '#load_file(1)'`,
	} {
		if got := EffectfulCall(s); got != "" {
			t.Errorf("%s 不是调用,得到 %q", s, got)
		}
	}
}

func TestEvaluate_BackslashHidesEffectfulCall(t *testing.T) {
	e := NewRiskEngine(&fakeStore{caps: map[string]string{
		"select|prod": "allow", "write|prod": "deny", "ddl|prod": "deny", "grant|prod": "deny",
	}})
	if v := e.EvaluateFor([]int64{1}, "MySQL", "prod", `SELECT 'a\'', load_file('/etc/passwd') -- '`); v.Action != ActionDeny {
		t.Errorf("只读角色应被拒,实际 %+v", v)
	}
	if v := e.EvaluateFor([]int64{1}, "PostgreSQL", "prod", `SELECT E'a\'', pg_terminate_backend(1) -- '`); v.Action != ActionDeny {
		t.Errorf("只读角色应被拒,实际 %+v", v)
	}
	if v := e.EvaluateFor([]int64{1}, "MySQL", "prod", `SELECT 'it\'s' FROM t`); v.Action != ActionAllow {
		t.Errorf("普通查询应放行,实际 %+v", v)
	}
}
