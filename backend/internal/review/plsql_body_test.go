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

// DELIMITER 不能成为绕过规则的口子。自定义分隔符把好几条真语句并成了一条,
// 审查必须照样看进去 —— 否则同一条 DROP,换个分隔符写就通过了。
func TestReview_LooksInsideCustomDelimiterStatements(t *testing.T) {
	rules := allRules()
	bare := Check(DialectMySQL, "DROP TABLE t_orders;", rules)
	if bare.Passed {
		t.Fatal("前置条件不成立:裸 DROP 本该被拦")
	}

	merged := Check(DialectMySQL, "DELIMITER //\nSELECT 1; DROP TABLE t_orders //\nDELIMITER ;\n", rules)
	if !hasCode(merged, "ddl.forbid.drop") {
		t.Errorf("自定义分隔符并进来的 DROP 必须照样命中,实际: %v", codesOf(merged))
	}
	if merged.Passed {
		t.Error("换个分隔符写就通过了 —— 那等于给所有规则开了个后门")
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

// 2026-11-12 发布单报障:COMMENT 字符串里的分号被当成语句分隔,ALTER 从字符串中间切开,
// 前半截没有 ALGORITHM,"ALTER TABLE 必须显式指定 ALGORITHM/LOCK"误报。两份原脚本照抄。
func TestReview_SemicolonInsideCommentDoesNotSplitAlter(t *testing.T) {
	rules := allRules()
	for _, sql := range []string{
		"ALTER TABLE `t_dialog_button`\n  MODIFY COLUMN `button_type` tinyint(4) NOT NULL DEFAULT 0 COMMENT 'button_type 按钮类型 0-Viber-menu_button 1-Viber-card_button 2-Messenger卡片按钮(card_code必填), 3-Messenger底部菜单按钮,4-Messenger快速回复按钮, 5-Messenger文本按钮',\n  MODIFY COLUMN `menu_code` varchar(50) NOT NULL DEFAULT '' COMMENT '对话流菜单 button_type =0 必填 t_dialog_menu.menu_code; MessengerBot菜单项(button_type=3)为t_im_messenger_menu.menu_code',\n  ALGORITHM = INPLACE,\n  LOCK = NONE;",
		"-- 2. ekyc_completion_status 改为 NOT NULL DEFAULT 2\nALTER TABLE t_agent_contract\n  MODIFY COLUMN `ekyc_completion_status` tinyint unsigned NOT NULL DEFAULT '2' COMMENT '1: Completed,完成EKYC; 2: All,包含所有',\n  ALGORITHM=INPLACE, LOCK=NONE;",
		"ALTER TABLE t ADD COLUMN c int COMMENT \"a;b\", ALGORITHM=INSTANT, LOCK=NONE;",
	} {
		if r := Check(DialectMySQL, sql, rules); hasCode(r, "mysql.alter.require.algorithm") {
			t.Errorf("已写 ALGORITHM/LOCK,不该报未指定:%v\n%s", codesOf(r), sql)
		}
	}
	// 真没写的照旧报
	if r := Check(DialectMySQL, "ALTER TABLE t MODIFY COLUMN c int COMMENT 'a; b';", rules); !hasCode(r, "mysql.alter.require.algorithm") {
		t.Errorf("没写 ALGORITHM/LOCK 应报,实际 %v", codesOf(r))
	}
	// DELIMITER 并进来的多条语句仍要看进去(引号外的分号)
	if r := Check(DialectMySQL, "DELIMITER //\nSELECT 'x;y'; DROP TABLE t_orders //\nDELIMITER ;\n", rules); !hasCode(r, "ddl.forbid.drop") {
		t.Errorf("DELIMITER 里的 DROP 必须命中,实际 %v", codesOf(r))
	}
}
