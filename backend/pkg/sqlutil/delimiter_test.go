package sqlutil

// MySQL 的 DELIMITER —— 一条客户端指令,存在的理由正是"让存储过程体里的分号不再
// 是分隔符"。
//
// 不认它的后果:每一份存储过程脚本都会从体中间被切开,碎片被服务端逐条拒绝,
// 于是这类脚本从控制台根本跑不起来 —— 而它是 MySQL 侧最常见的一种变更。
//
// 认它**不会**削弱判定:自定义分隔符产出的是**更长**的一条语句,而真正兜底的两层
// 读的都是整条语句的文本 —— 高危字典对它做正则扫描,规范审查会看进仍带分号的语句
// 里面。下面最后两个用例钉的就是这一点。

import (
	"strings"
	"testing"
)

func TestDelimiter_StoredProcedureScriptSplitsCorrectly(t *testing.T) {
	script := "DELIMITER //\n" +
		"CREATE PROCEDURE p()\n" +
		"BEGIN\n" +
		"  SELECT 1;\n" +
		"  SELECT 2;\n" +
		"END //\n" +
		"DELIMITER ;\n"
	got := SplitStatements(script)
	if len(got) != 1 {
		t.Fatalf("整份脚本应是一条语句(过程体),实际 %d 条: %q", len(got), got)
	}
	body := got[0]
	if !strings.HasPrefix(strings.ToUpper(body), "CREATE PROCEDURE") {
		t.Errorf("语句应从 CREATE PROCEDURE 开始,实际: %q", body)
	}
	// DELIMITER 指令本身不能下发 —— 服务器不认它,发过去就是第一行报错。
	if strings.Contains(strings.ToUpper(body), "DELIMITER") {
		t.Errorf("DELIMITER 指令不该出现在下发文本里: %q", body)
	}
	// 体里的两条 SELECT 都要留在语句里,一个字都不能丢。
	for _, want := range []string{"SELECT 1", "SELECT 2", "END"} {
		if !strings.Contains(body, want) {
			t.Errorf("过程体缺了 %q: %q", want, body)
		}
	}
}

// 恢复默认分隔符后,后面的语句照常按分号切。
func TestDelimiter_RestoresTheDefault(t *testing.T) {
	script := "DELIMITER //\n" +
		"CREATE PROCEDURE p() BEGIN SELECT 1; END //\n" +
		"DELIMITER ;\n" +
		"SELECT 3;\n" +
		"SELECT 4;\n"
	got := SplitStatements(script)
	if len(got) != 3 {
		t.Fatalf("应为 3 条(过程 + 两条 SELECT),实际 %d: %q", len(got), got)
	}
	if !strings.Contains(got[1], "SELECT 3") || !strings.Contains(got[2], "SELECT 4") {
		t.Errorf("恢复默认分隔符后的语句切错了: %q", got)
	}
}

// "delimiter" 出现在 SQL 文本里(列名、字符串)不能被当成指令 —— 那会把真正的
// 语句吃掉,而被吃掉的语句没人判过。
func TestDelimiter_OnlyARealDirectiveCounts(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want int
	}{
		{"列名叫 delimiter", "SELECT delimiter FROM t; SELECT 2;", 2},
		{"字符串里出现", "SELECT 'delimiter //' AS x; SELECT 2;", 2},
		{"不在行首", "SELECT 1; DELIMITER // SELECT 2;", 2},
		{"DELIMITERS 是别的词", "SELECT delimiters FROM t; SELECT 2;", 2},
	}
	for _, c := range cases {
		if got := SplitStatements(c.sql); len(got) != c.want {
			t.Errorf("[%s] 切成 %d 条, want %d: %q", c.name, len(got), c.want, got)
		}
	}
}

// 判定的地基:分句器**不能丢文本**。判定看到的必须覆盖服务端会执行的全部 ——
// 丢掉一段就意味着那段没人判过却照样跑了。
func TestDelimiter_NoExecutableTextIsLost(t *testing.T) {
	script := "DELIMITER //\n" +
		"CREATE PROCEDURE p() BEGIN DROP TABLE t_secret; END //\n" +
		"DELIMITER ;\n"
	joined := strings.ToUpper(strings.Join(SplitStatements(script), " "))
	for _, tok := range []string{"CREATE", "PROCEDURE", "BEGIN", "DROP", "TABLE", "T_SECRET", "END"} {
		if !strings.Contains(joined, tok) {
			t.Errorf("token %q 在切分后消失了 —— 它会被执行,却没人判过", tok)
		}
	}
}

// 有人可能想用 DELIMITER 把两条语句并成一条,好让判定只看见前一条。
// 并不会得逞:并进来的文本仍在同一条语句里,而字典和审查读的都是整条语句的文本。
func TestDelimiter_CannotHideAStatementFromTheJudge(t *testing.T) {
	script := "DELIMITER //\nSELECT 1; DROP TABLE t_secret //\nDELIMITER ;\n"
	got := SplitStatements(script)
	if len(got) != 1 {
		t.Fatalf("自定义分隔符下这是一条语句,实际 %d 条: %q", len(got), got)
	}
	if !strings.Contains(strings.ToUpper(got[0]), "DROP TABLE T_SECRET") {
		t.Errorf("被并进来的 DROP 必须留在判定看得到的文本里: %q", got[0])
	}
}
