package review

// 包一层块,规则就该照样管得住。
//
// 分句器把 PL/SQL 块整体交回来(那是 Oracle 执行的单元),而审查的检查器都锚在语句
// 开头 —— 于是同一条 DELETE,裸着写会被拦,包进 BEGIN…END 就通过了。高危字典没有
// 这个问题(它扫整块文本),但规范审查是逐语句判的,看不进去就是真的看不见。
//
// 这一组用例钉的就是这件事:**在块里和在块外,规则的结论必须一致**。

import (
	"strings"
	"testing"
)

func codesOf(r Result) []string {
	out := make([]string, 0, len(r.Findings))
	for _, f := range r.Findings {
		out = append(out, f.Code)
	}
	return out
}

func hasCode(r Result, code string) bool {
	for _, f := range r.Findings {
		if f.Code == code {
			return true
		}
	}
	return false
}

// 裸着能拦住的,包进块里也要拦住。
func TestReview_LooksInsidePLSQLBlocks(t *testing.T) {
	rules := allRules()
	cases := []struct {
		name, sql, wantCode string
	}{
		{"匿名块里的 DROP", "BEGIN\n  DROP TABLE t_orders;\nEND;", "ddl.forbid.drop"},
		{"DECLARE 块里的无 WHERE DELETE", "DECLARE v NUMBER;\nBEGIN\n  DELETE FROM t_orders;\nEND;", "dml.require.where"},
		{"存储过程里的 DROP", "CREATE OR REPLACE PROCEDURE p IS BEGIN\n  DROP TABLE t_orders;\nEND;", "ddl.forbid.drop"},
		{"存储过程里的 TRUNCATE", "CREATE OR REPLACE PROCEDURE p IS BEGIN\n  TRUNCATE TABLE t_orders;\nEND;", "ddl.forbid.truncate"},
		{"块里的无 WHERE UPDATE", "BEGIN\n  UPDATE t_orders SET flag = 1;\nEND;", "dml.require.where"},
	}
	for _, c := range cases {
		r := Check(DialectOracle, c.sql, rules)
		if !hasCode(r, c.wantCode) {
			t.Errorf("[%s] 没有命中 %s —— 包一层块就漏掉,等于给规则开了个后门。实际: %v",
				c.name, c.wantCode, codesOf(r))
		}
		if r.Passed {
			t.Errorf("[%s] 审查判为通过 —— 裸着写是拦的,包进块里不该放行", c.name)
		}
	}
}

// 合规的块不能因此变成一片噪音:块里的控制流、变量赋值不该被当成语句报出来。
// 一个老是误报的审查,换来的是所有人学会无视它。
func TestReview_CleanBlockStaysQuiet(t *testing.T) {
	rules := allRules()
	clean := "DECLARE\n  v_cnt NUMBER;\nBEGIN\n  v_cnt := 0;\n  SELECT COUNT(1) INTO v_cnt FROM t_orders WHERE id = 1;\n  IF v_cnt > 0 THEN\n    UPDATE t_orders SET flag = 1 WHERE id = 1;\n  END IF;\nEND;"
	r := Check(DialectOracle, clean, rules)
	if !r.Passed {
		t.Errorf("合规的块不该被拦,实际命中: %v", codesOf(r))
	}
}

// 语句数是给人看的:块还是一条语句,拆开只是为了让规则看得见里面。
func TestReview_InnerStatementsDoNotInflateTheCount(t *testing.T) {
	rules := allRules()
	r := Check(DialectOracle, "BEGIN\n  DROP TABLE a;\n  DROP TABLE b;\nEND;", rules)
	if r.Statements != 1 {
		t.Errorf("语句数 = %d, want 1 —— 块整体就是一条语句,内部拆分不该体现在这个数字上", r.Statements)
	}
	// 但里面两条 DROP 都要报出来。
	n := 0
	for _, f := range r.Findings {
		if f.Code == "ddl.forbid.drop" {
			n++
		}
	}
	if n != 2 {
		t.Errorf("块里两条 DROP 应各报一次,实际 %d 次", n)
	}
}

// 报出来的位置要指向这个块,而不是一个凭空多出来的语句序号 —— 否则人拿着行号
// 回去找,找到的是别的地方。
func TestReview_FindingsInsideABlockPointAtTheBlock(t *testing.T) {
	rules := allRules()
	script := "SELECT 1 FROM dual;\n\nBEGIN\n  DROP TABLE t_orders;\nEND;\n/"
	r := Check(DialectOracle, script, rules)
	for _, f := range r.Findings {
		if f.Code != "ddl.forbid.drop" {
			continue
		}
		if f.Stmt != 2 {
			t.Errorf("块内发现的语句序号 = %d, want 2(它属于第二条语句)", f.Stmt)
		}
		if f.Line < 3 {
			t.Errorf("块内发现的行号 = %d,应指向块的起始行", f.Line)
		}
		if !strings.Contains(f.SQL, "BEGIN") && !strings.Contains(f.SQL, "DROP") {
			t.Errorf("摘录应看得出是哪一段: %q", f.SQL)
		}
	}
}
