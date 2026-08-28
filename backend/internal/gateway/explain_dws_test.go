package gateway

import "testing"

// DWS / GaussDB 的 EXPLAIN 比 PostgreSQL 多一个裸选项:
//
//	EXPLAIN { [ANALYZE | ANALYSE] [VERBOSE] | PERFORMANCE } <stmt>
//
// PERFORMANCE 和 ANALYZE 一样**会真的执行**被包住的语句,只是报告更详细。
// 原先的前缀解析只认 ANALYZE / VERBOSE,于是 PERFORMANCE 被当成了被包住的语句
// 本身 —— 有效动词变成 "PERFORMANCE" 这个根本不存在的动词。两个后果:
//
//  1. 它不是读动词,执行侧就走了 ExecContext(不取结果集)——用户看到的是
//     "EXPLAIN PERFORMANCE 没有显示结果"。
//  2. planOnly 仍然是 true,而它其实会执行 —— `EXPLAIN PERFORMANCE DELETE …`
//     会被当成"只看计划"放行,然后真的把数据删掉。第 1 条是体验问题,第 2 条
//     是判定被绕过。

// EXPLAIN PERFORMANCE 会执行,所以它绝不是 plan-only。
func TestPlanOnly_DWSPerformanceExecutes(t *testing.T) {
	executing := []string{
		`EXPLAIN PERFORMANCE SELECT * FROM orders`,
		`EXPLAIN PERFORMANCE DELETE FROM orders WHERE id = 1`,
		`explain performance update t set x = 1`,
		`  /* tag */ EXPLAIN PERFORMANCE SELECT 1`,
	}
	for _, s := range executing {
		if PlanOnly(s) {
			t.Errorf("PlanOnly(%q) = true —— EXPLAIN PERFORMANCE 会真的执行被包住的语句", s)
		}
	}
}

// 英式拼写 ANALYSE 同样会执行。PostgreSQL 与 GaussDB 都接受它,而原先只认
// ANALYZE —— 一个字母之差就能让 DELETE 被判成"只看计划"。
func TestPlanOnly_BritishAnalyseAlsoExecutes(t *testing.T) {
	executing := []string{
		`EXPLAIN ANALYSE DELETE FROM orders`,
		`EXPLAIN ANALYSE VERBOSE UPDATE t SET x = 1`,
		`EXPLAIN (ANALYSE) DELETE FROM orders`,
		`EXPLAIN (ANALYSE, BUFFERS) DELETE FROM orders`,
	}
	for _, s := range executing {
		if PlanOnly(s) {
			t.Errorf("PlanOnly(%q) = true —— ANALYSE 是 ANALYZE 的英式拼写,一样会执行", s)
		}
	}
}

// 有效动词要落在**被包住的语句**上,而不是选项关键字上。
// 这一条直接决定了结果显不显示:动词不是读动词,执行侧就不会去取结果集。
func TestParseVerb_UnwrapsDWSExplainOptions(t *testing.T) {
	cases := []struct{ sql, want string }{
		{`EXPLAIN PERFORMANCE SELECT * FROM orders`, "SELECT"},
		{`EXPLAIN PERFORMANCE DELETE FROM orders`, "DELETE"},
		{`EXPLAIN ANALYSE SELECT 1`, "SELECT"},
		{`EXPLAIN ANALYSE VERBOSE SELECT 1`, "SELECT"},
		{`EXPLAIN VERBOSE SELECT 1`, "SELECT"},
	}
	for _, c := range cases {
		if got := ParseVerb(c.sql); got != c.want {
			t.Errorf("ParseVerb(%q) = %q, want %q", c.sql, got, c.want)
		}
	}
}

// 结果能不能显示,取决于 IsRead —— 它决定执行侧走 QueryContext 还是 ExecContext。
// 这是用户报的那个现象最直接的一条:EXPLAIN PERFORMANCE 一个 SELECT,必须按读处理。
func TestIsRead_DWSExplainPerformanceOnASelectIsARead(t *testing.T) {
	if !IsRead(`EXPLAIN PERFORMANCE SELECT * FROM g_ods_big_order_di`) {
		t.Error("EXPLAIN PERFORMANCE 一个 SELECT 应按读处理,否则执行侧不会去取结果集,页面上什么都不显示")
	}
	// 但它包住的是 DELETE 时就不是读了 —— 它会真的删。
	if IsRead(`EXPLAIN PERFORMANCE DELETE FROM g_ods_big_order_di`) {
		t.Error("EXPLAIN PERFORMANCE DELETE 会真的删除,不能按读处理")
	}
}

// 能力维度也要落在被包住的语句上:EXPLAIN PERFORMANCE 一个查询是 select,
// 而不是因为动词认不出来而落进默认的 write —— 后者会让它在严格环境里被拦下,
// 或者被推去审批,而这本来只是想看一眼执行计划。
func TestCapability_DWSExplainPerformanceIsNotMisfiledAsWrite(t *testing.T) {
	if got := MapVerbToCapability(ParseVerb(`EXPLAIN PERFORMANCE SELECT 1`)); got != "select" {
		t.Errorf("能力维度 = %q, want select", got)
	}
	if got := MapVerbToCapability(ParseVerb(`EXPLAIN PERFORMANCE DELETE FROM t`)); got != "write" {
		t.Errorf("能力维度 = %q, want write —— 它真的会删", got)
	}
}
