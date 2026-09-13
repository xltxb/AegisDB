package review

// 按注释审查:一条规则要能看见运维写在语句上方的注释。
//
// 「每条 DDL 必须带 `-- ticket:` 注释」是审查规则里最常见的一类要求 —— 它约束的不是
// SQL 本身,而是提交这条 SQL 的人有没有交代来由。要检查它,规则就得看得见原文。
//
// 从前规则有个 `scope: "raw"` 的开关说是干这件事,而它什么都不做:SplitStatements 在
// 拆的时候就把注释全抹了,st.raw 与 st.sql 只差首尾空白。于是这条规则永远匹配不上,
// 运维怎么写都让它一直报 —— 而报错说的是"语句未匹配要求的模式",看不出是旋钮坏了。
// (那个假旋钮先前已经拆掉;这里是把它真做出来。)
//
// 注释归属于它下面那条语句:`-- ticket:1234` 写在 DROP 上面,说的就是这条 DROP。

import (
	"strings"
	"testing"
)

func ticketRule() []Rule {
	return rulesFor(Rule{
		Code: "ddl.ticket", Name: "DDL 必须注明工单号", Kind: "regex", Level: LevelError,
		Params: `{"pattern":"--\\s*ticket:\\s*\\d+","mode":"require","scope":"raw"}`,
	})
}

func TestCheck_RuleCanRequireACommentAboveTheStatement(t *testing.T) {
	withTicket := "-- ticket:1234 下线废弃表\nDROP TABLE tbl_legacy;"
	if res := Check(DialectMySQL, withTicket, ticketRule()); len(res.Findings) != 0 {
		t.Errorf("工单号就写在语句上方,规则不该触发:%+v", res.Findings)
	}

	without := "DROP TABLE tbl_legacy;"
	if res := Check(DialectMySQL, without, ticketRule()); len(res.Findings) != 1 {
		t.Errorf("没写工单号,规则该触发一次,实际 %d 条", len(res.Findings))
	}
}

// 注释算在它**下面**那条语句上,不是上面那条 —— 否则第一条会蹭到第二条的工单号。
func TestCheck_CommentBelongsToTheStatementBelowIt(t *testing.T) {
	sql := "DROP TABLE a;\n-- ticket:1234\nDROP TABLE b;"
	res := Check(DialectMySQL, sql, ticketRule())
	if len(res.Findings) != 1 {
		t.Fatalf("只有第一条没写工单号,该报一条,实际 %d 条:%+v", len(res.Findings), res.Findings)
	}
	if res.Findings[0].Stmt != 1 {
		t.Errorf("该报在第 1 条语句上,实际报在第 %d 条 —— 注释归错了语句", res.Findings[0].Stmt)
	}
}

// 行内注释也算数:有人把工单号写在语句后面。
func TestCheck_TrailingCommentIsVisibleToo(t *testing.T) {
	sql := "DROP TABLE tbl_legacy; -- ticket:1234"
	if res := Check(DialectMySQL, sql, ticketRule()); len(res.Findings) != 0 {
		t.Errorf("工单号写在语句末尾,规则不该触发:%+v", res.Findings)
	}
}

// 报告里的摘要仍是语句本身,不该被一大段注释头挤掉 —— 摘要是用来认出"是哪条语句"的。
func TestCheck_ExcerptStaysTheStatementNotTheComment(t *testing.T) {
	sql := "-- " + strings.Repeat("这是一段很长的变更说明。", 40) + "\nDROP TABLE tbl_legacy;"
	res := Check(DialectMySQL, sql, rulesFor(Rule{
		Code: "no.drop", Name: "禁止 DROP", Kind: "regex", Level: LevelError,
		Params: `{"pattern":"DROP\\s+TABLE"}`,
	}))
	if len(res.Findings) != 1 {
		t.Fatalf("该报一条,实际 %d 条", len(res.Findings))
	}
	if !strings.Contains(res.Findings[0].SQL, "DROP TABLE") {
		t.Errorf("摘要里看不见语句本身:%q", res.Findings[0].SQL)
	}
}
