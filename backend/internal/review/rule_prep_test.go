package review

// 规则的"准备工作"在语句循环里做了 N 遍。
//
// fire 每次被调用都先 parseParams(r.Params) 解一遍 JSON,regexRule 还要把运维写的
// pattern 再编译一遍。这两件事只跟**规则**有关,跟当前是第几条语句无关 —— 而它们在
// (语句 × 规则) 的双重循环里,一份 200 条语句、30 条规则的迁移脚本就是 6000 次。
//
// 把它们提到循环外是显然的,而提出去之后要守住的是这些:每条规则的参数不能串味、
// 无效正则仍要按"每条语句报一次"报出来(一条永不触发的规则和一条永远通过的规则
// 从外面看一模一样,写它的人不会知道它是死的)、自定义措辞仍要覆盖。

import (
	"strings"
	"testing"
)

func rulesFor(specs ...Rule) []Rule {
	for i := range specs {
		specs[i].Enabled = true
		if specs[i].Dialect == "" {
			specs[i].Dialect = DialectAll
		}
		if specs[i].Level == "" {
			specs[i].Level = LevelWarn
		}
		if specs[i].Category == "" {
			specs[i].Category = CatPerf
		}
	}
	return specs
}

// 每条规则带着自己的参数,不能被上一条规则的参数串味。
func TestCheck_RulesKeepTheirOwnParams(t *testing.T) {
	res := Check(DialectMySQL, "SELECT a FROM t;\nSELECT b FROM u;", rulesFor(
		Rule{Code: "r.a", Name: "找 a", Kind: "regex", Params: `{"pattern":"FROM t"}`},
		Rule{Code: "r.b", Name: "找 b", Kind: "regex", Params: `{"pattern":"FROM u"}`},
	))
	// 按**语句序号**断言,不只是数条数:两条规则共用一份参数时,各自仍报一次,
	// 只是都报在同一条语句上 —— 只数条数看不出来。
	got := map[string]int{}
	for _, f := range res.Findings {
		got[f.Code] = f.Stmt
	}
	if got["r.a"] != 1 || got["r.b"] != 2 {
		t.Errorf("r.a 该命中第 1 条、r.b 该命中第 2 条,实际 %v —— 参数串味了", got)
	}
}

// 无效的正则要报出来,而且每条语句报一次 —— 写规则的人才知道它是死的。
func TestCheck_InvalidPatternReportsItselfPerStatement(t *testing.T) {
	res := Check(DialectMySQL, "SELECT 1;\nSELECT 2;\nSELECT 3;", rulesFor(
		Rule{Code: "r.bad", Name: "坏正则", Kind: "regex", Params: `{"pattern":"([unclosed"}`},
	))
	n := 0
	for _, f := range res.Findings {
		if f.Code == "r.bad" && strings.Contains(f.Message, "正则无效") {
			n++
		}
	}
	if n != 3 {
		t.Errorf("三条语句该各报一次无效正则,实际 %d 次", n)
	}
}

// require 语义(缺席才是违规)只写在参数里,提出循环后不能丢 —— 两面都要守。
func TestCheck_RequireModeSurvives(t *testing.T) {
	rule := func() []Rule {
		return rulesFor(Rule{Code: "r.req", Name: "必须限定条件", Kind: "regex",
			Params: `{"pattern":"WHERE","mode":"require"}`})
	}
	if n := len(Check(DialectMySQL, "DELETE FROM t;", rule()).Findings); n != 1 {
		t.Errorf("require 规则该因缺席而触发,实际 %d 条", n)
	}
	if res := Check(DialectMySQL, "DELETE FROM t WHERE id = 1;", rule()); len(res.Findings) != 0 {
		t.Errorf("模式在语句里,require 不该触发:%+v", res.Findings)
	}
}
