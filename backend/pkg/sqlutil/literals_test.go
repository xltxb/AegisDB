package sqlutil

import (
	"strings"
	"testing"
)

// MaskLiterals 的不变式:**长度不变**,引号本身留在原处,只有引号里的内容变成空格。
//
// 长度是这里的地基 —— 两个调用方都靠它把掩码文本上算出的位置映射回原文(审查报告
// 要指出问题在第几行第几列,判定层的规则命中要能摘出原句)。
func TestMaskLiterals_PreservesLengthAndDelimiters(t *testing.T) {
	for _, c := range []struct {
		name string
		in   string
		opt  LiteralMask
		want string
	}{
		{"字面量里的关键词不再是结构", `UPDATE t SET a='delete from x'`, LiteralMask{},
			`UPDATE t SET a='             '`},
		{"双写引号是转义,不是收尾", `UPDATE t SET note='it''s' WHERE id=1`, LiteralMask{},
			`UPDATE t SET note='     ' WHERE id=1`},
		{"双引号同样是字面量", `SELECT "drop table t"`, LiteralMask{},
			`SELECT "            "`},
		{"反引号:默认不当引号,标识符留给规则看", "SELECT `drop` FROM t", LiteralMask{},
			"SELECT `drop` FROM t"},
		{"反引号:开了就当引号", "SELECT `drop` FROM t", LiteralMask{Backtick: true},
			"SELECT `    ` FROM t"},
		{"反斜杠:关着时,第二个引号就是收尾", `UPDATE t SET a='x\' WHERE 1=1 --'`, LiteralMask{},
			`UPDATE t SET a='  ' WHERE 1=1 --'`},
		{"反斜杠:开着时,整串都是字面量", `UPDATE t SET a='x\' WHERE 1=1 --'`, LiteralMask{Backslash: true},
			`UPDATE t SET a='                '`},
		{"反引号里的反斜杠哪个引擎都不转义", "SELECT `a\\` FROM t",
			LiteralMask{Backtick: true, Backslash: true}, "SELECT `  ` FROM t"},
		{"没闭合的引号:抹到结尾,不越界", `SELECT 'abc`, LiteralMask{}, `SELECT '   `},
		{"空输入", ``, LiteralMask{}, ``},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := MaskLiterals(c.in, c.opt)
			if got != c.want {
				t.Errorf("MaskLiterals(%q)\n得到 %q\n期望 %q", c.in, got, c.want)
			}
			if len(got) != len(c.in) {
				t.Errorf("长度变了:%d → %d", len(c.in), len(got))
			}
		})
	}
}

// 换行留着 —— 审查报告的行号是数出来的,把字面量里的换行抹成空格会让它少数几行,
// 于是报告指向的位置和人在编辑器里看到的对不上。
func TestMaskLiterals_KeepsNewlines(t *testing.T) {
	in := "INSERT INTO t VALUES ('第一行\n第二行')"
	got := MaskLiterals(in, LiteralMask{})
	if strings.Count(got, "\n") != 1 {
		t.Errorf("换行被抹掉了:%q", got)
	}
	if strings.Contains(got, "第一行") || strings.Contains(got, "第二行") {
		t.Errorf("字面量内容没抹干净:%q", got)
	}
}
