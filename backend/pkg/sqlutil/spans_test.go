package sqlutil

// 语句在原文里的位置。
//
// 判定读的是抹掉注释之后的语句文本 —— 注释不是语法,留在里面只会让关键词启发式读到
// 数据。但审查有时要读原文:「每条 DDL 必须带 -- ticket: 注释」约束的不是 SQL 本身,
// 是提交它的人有没有交代来由,而那句话只存在于原文里。
//
// 这组用例钉的是归属:一段注释算在哪条语句头上。

import "testing"

func TestSplitStatementsWithSpans_Raw(t *testing.T) {
	for _, tc := range []struct {
		name string
		sql  string
		want []string // 每条语句在原文里的样子
	}{
		{
			"前导注释算下面那条的",
			"-- ticket:1234\nDROP TABLE a;",
			[]string{"-- ticket:1234\nDROP TABLE a;"},
		},
		{
			"注释归下面那条,不蹭上面那条",
			"DROP TABLE a;\n-- ticket:1234\nDROP TABLE b;",
			[]string{"DROP TABLE a;", "-- ticket:1234\nDROP TABLE b;"},
		},
		{
			"行尾注释跟着它那条",
			"DROP TABLE a; -- ticket:1234\nDROP TABLE b;",
			[]string{"DROP TABLE a; -- ticket:1234", "DROP TABLE b;"},
		},
		{
			"块注释",
			"/* ticket:1234 */ DROP TABLE a;",
			[]string{"/* ticket:1234 */ DROP TABLE a;"},
		},
		{
			"语句中间的注释留在原文里",
			"SELECT /* hint */ 1;",
			[]string{"SELECT /* hint */ 1;"},
		},
		{
			"末尾没有分号",
			"-- ticket:1\nSELECT 1",
			[]string{"-- ticket:1\nSELECT 1"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sts := SplitStatementsWithSpans(tc.sql)
			if len(sts) != len(tc.want) {
				t.Fatalf("拆出 %d 条,want %d 条:%+v", len(sts), len(tc.want), sts)
			}
			for i, st := range sts {
				if got := st.Raw(tc.sql); got != tc.want[i] {
					t.Errorf("第 %d 条原文 = %q,want %q", i+1, got, tc.want[i])
				}
			}
		})
	}
}

// 区间版与字符串版必须逐字一致 —— 后者现在就是从前者派生的,这条守的是它别再分家。
func TestSplitStatementsWithSpans_TextMatchesSplitStatements(t *testing.T) {
	for _, sql := range []string{
		"",
		"   \n  ",
		"-- 只有注释\n",
		"SELECT 1; SELECT 2;",
		"DELIMITER $$\nCREATE PROCEDURE p() BEGIN SELECT 1; END$$\nDELIMITER ;\nSELECT 3;",
		"SELECT 'a;b'; DROP TABLE x;",
		"BEGIN\n  DELETE FROM t;\nEND;",
	} {
		plain := SplitStatements(sql)
		sts := SplitStatementsWithSpans(sql)
		if len(plain) != len(sts) {
			t.Fatalf("%q:字符串版 %d 条、区间版 %d 条", sql, len(plain), len(sts))
		}
		for i := range plain {
			if plain[i] != sts[i].Text {
				t.Errorf("%q 第 %d 条:%q vs %q", sql, i+1, plain[i], sts[i].Text)
			}
		}
	}
}

// 区间必须落在原文里,而且不倒退 —— 否则切片会越界或者把两条语句的原文搅在一起。
func TestSplitStatementsWithSpans_SpansAreSaneAndOrdered(t *testing.T) {
	sql := "-- 头\nSELECT 1; /* 中 */ SELECT 2; -- 尾\nDELETE FROM t WHERE id=1;"
	prevEnd := 0
	for i, st := range SplitStatementsWithSpans(sql) {
		if st.Start < 0 || st.End > len(sql) || st.Start > st.End {
			t.Fatalf("第 %d 条区间越界:[%d,%d) 原文长 %d", i+1, st.Start, st.End, len(sql))
		}
		if st.Start < prevEnd {
			t.Errorf("第 %d 条的起点 %d 退回到上一条的终点 %d 之前", i+1, st.Start, prevEnd)
		}
		prevEnd = st.End
	}
}
