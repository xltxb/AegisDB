package sqlutil

import (
	"reflect"
	"strings"
	"testing"
)

// PL/SQL 块必须整块保留 —— 包体里的每一个分号都是块内部的。
//
// 这是本文件里唯一一处 MERGE(把含分号的文本保留为一条语句),而 SplitStatements
// 的总纲是"过切安全、合并危险"。这里能合并,是因为合并的恰好是 Oracle 自己作为
// 一条语句执行的那个单元:判定看到的文本 == 服务端执行的文本,两个 lexer 没有分歧。
// 而且高危字典是全文正则扫描(risk.matchCommand),块内的 DROP 照样命中 —— 门禁
// 不因整块而放松。为守住这条线,块的识别要求一个明确的终结符,见下面的用例。
func TestSplitStatements_KeepsPLSQLBlockWhole(t *testing.T) {
	body := `CREATE OR REPLACE PACKAGE BODY app_pkg AS
  PROCEDURE greet(p_id NUMBER) IS
    v_n VARCHAR2(50);
  BEGIN
    SELECT name INTO v_n FROM users WHERE id = p_id;
    DBMS_OUTPUT.PUT_LINE(v_n);
  END greet;
END app_pkg;`

	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"包体以 / 结束 —— 整块一条,/ 不带进语句",
			body + "\n/",
			[]string{body}},
		{"规格 + 包体 + 后续 DDL,各自独立",
			"CREATE OR REPLACE PACKAGE app_pkg AS\n  PROCEDURE greet(p_id NUMBER);\nEND app_pkg;\n/\n" +
				body + "\n/\nGRANT EXECUTE ON app_pkg TO reporter;",
			[]string{
				"CREATE OR REPLACE PACKAGE app_pkg AS\n  PROCEDURE greet(p_id NUMBER);\nEND app_pkg;",
				body,
				"GRANT EXECUTE ON app_pkg TO reporter",
			}},
		{"匿名块 DECLARE ... END; /",
			"DECLARE\n  v NUMBER;\nBEGIN\n  v := 1;\n  UPDATE t SET x = v;\nEND;\n/",
			[]string{"DECLARE\n  v NUMBER;\nBEGIN\n  v := 1;\n  UPDATE t SET x = v;\nEND;"}},
		{"整段输入就是一个块、后面没有别的东西 —— 允许以 END; 收尾(粘贴场景)",
			body,
			[]string{body}},
		{"触发器与函数同样整块",
			"CREATE OR REPLACE FUNCTION f RETURN NUMBER IS\nBEGIN\n  RETURN 1;\nEND;\n/",
			[]string{"CREATE OR REPLACE FUNCTION f RETURN NUMBER IS\nBEGIN\n  RETURN 1;\nEND;"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SplitStatements(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("SplitStatements(%q) =\n%#v\nwant\n%#v", tc.in, got, tc.want)
			}
		})
	}
}

// 块整体保留不得成为夹带通道:/ 之后的语句仍然是独立的一条,会被单独判定。
func TestSplitStatements_PLSQLTerminatorEndsTheBlock(t *testing.T) {
	got := SplitStatements("CREATE OR REPLACE PROCEDURE p AS\nBEGIN\n  NULL;\nEND;\n/\nDROP TABLE victim;")
	want := []string{"CREATE OR REPLACE PROCEDURE p AS\nBEGIN\n  NULL;\nEND;", "DROP TABLE victim"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

// 块内的高危动词留在块里 —— 字典是全文扫描,所以整块不等于漏判。
func TestSplitStatements_PLSQLBlockStillCarriesItsVerbs(t *testing.T) {
	got := SplitStatements("CREATE OR REPLACE PROCEDURE p AS\nBEGIN\n  EXECUTE IMMEDIATE 'DROP TABLE t';\nEND;\n/")
	if len(got) != 1 {
		t.Fatalf("expected one statement, got %d: %#v", len(got), got)
	}
	if !strings.Contains(strings.ToUpper(got[0]), "DROP TABLE") {
		t.Error("the block must keep its inner statements so the dictionary can scan them")
	}
}

// 没有终结符、后面还跟着别的语句时,不做块识别 —— 宁可保持旧的过切行为,也不
// 能把后面的语句吞进来。MySQL 的 `BEGIN;` 事务脚本走的就是这条路。
func TestSplitStatements_NoBlockMergeWithoutTerminator(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"MySQL 事务脚本:BEGIN 不触发块识别",
			"BEGIN;\nUPDATE t SET x = 1;\nCOMMIT;",
			[]string{"BEGIN", "UPDATE t SET x = 1", "COMMIT"}},
		{"过程体后面还有语句、又没有 / —— 不吞后面的语句",
			"CREATE PROCEDURE p() BEGIN SELECT 1; END; SELECT 2;",
			[]string{"CREATE PROCEDURE p() BEGIN SELECT 1", "END", "SELECT 2"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SplitStatements(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("SplitStatements(%q) = %#v, want %#v", tc.in, got, tc.want)
			}
		})
	}
}
