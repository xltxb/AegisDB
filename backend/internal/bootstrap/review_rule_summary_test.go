package bootstrap

// 审查结果按规则汇总,每条规则只存 5 条示例(2026-11-10)。
//
// 起因:本地一张 50 万条语句、每条都命中的脚本,审查阶段 findings 存了约 100MB ——
// MySQL MEDIUMTEXT(16MB)写不进去时整条阶段更新失败(状态也丢,阶段永远"运行中"),
// 详情每 2.5 秒整段回传,浏览器画 50 万行。用户拍板:不逐条留痕,每条规则 5 条示例。

import (
	"encoding/json"
	"strings"
	"testing"
)

type reviewSummary struct {
	Statements int `json:"statements"`
	Errors     int `json:"errors"`
	Total      int `json:"total"`
	Passed     bool `json:"passed"`
	Rules      []struct {
		Code  string `json:"code"`
		Level string `json:"level"`
		Count int    `json:"count"`
	} `json:"rules"`
	Findings []struct {
		Code string `json:"code"`
		Stmt int    `json:"stmt"`
	} `json:"findings"`
}

func manyDeletes(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString("DELETE FROM tbl_order;\n")
	}
	return b.String()
}

func checkSummary(t *testing.T, s reviewSummary, n int) {
	t.Helper()
	if s.Statements != n {
		t.Fatalf("statements = %d, want %d", s.Statements, n)
	}
	per := map[string]int{}
	for _, f := range s.Findings {
		per[f.Code]++
	}
	for code, c := range per {
		if c > 5 {
			t.Errorf("rule %s stored %d examples, want at most 5", code, c)
		}
	}
	where := 0
	sum := 0
	for _, r := range s.Rules {
		sum += r.Count
		if r.Code == "dml.require.where" {
			where = r.Count
		}
	}
	eq(t, where, n, "the summary counts every statement the rule hit")
	eq(t, sum, s.Total, "rule counts add up to the total")
	if s.Errors < n || s.Passed {
		t.Fatalf("level counts are over all hits and still block: errors=%d passed=%v", s.Errors, s.Passed)
	}
}

// 「SQL 审查」页的即时审查:返回汇总 + 示例。
func TestReviewCheckSummarisesPerRule(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	r := app.do("POST", "/api/v1/sql-review/check", admin, map[string]any{"dialect": "mysql", "sql": manyDeletes(40)})
	eq(t, r.Code, 0, "check")
	var out struct {
		Result reviewSummary `json:"result"`
	}
	if err := json.Unmarshal(r.Data, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	checkSummary(t, out.Result, 40)
}

// 发布流水线的审查阶段:存下来的结果同样是汇总 + 示例,阶段状态照常落库。
func TestReleaseReviewStageStoresSummary(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	// 这里测的是审查结果的存法,不是无 WHERE 拦截:dev 自 2026-11-16 起新装即拦无 WHERE,
	// 30 条 DELETE 会让没有审批阶段的流程直接拒收 —— 先把 dev 的这道闸关掉。
	app.tierStrict(admin, "dev", false)
	pid := app.createPipeline(admin, "审查汇总-流程", "", []map[string]any{
		{"name": "规范审查", "type": "review", "config": `{"failOn":"error"}`},
		{"name": "执行变更", "type": "execute"},
	})
	r := app.submitRelease(admin, map[string]any{
		"title": "大脚本审查", "pipelineId": pid, "connectionId": app.connIDByEnv(admin, "dev"),
		"sql": manyDeletes(30), "reason": "审查汇总回归",
	})
	eq(t, r.Code, 0, "submit: "+r.Msg)
	var rel releaseView
	_ = json.Unmarshal(r.Data, &rel)
	done := app.waitRelease(admin, rel.ID, "failed", "success")
	eq(t, done.Status, "failed", "error-level hits still stop the release")

	rv := stageByType(done, "review")
	if rv == nil {
		t.Fatal("no review stage")
	}
	eq(t, rv.Status, "failed", "the stage status is written alongside the summary")
	var s reviewSummary
	if err := json.Unmarshal([]byte(rv.Findings), &s); err != nil {
		t.Fatalf("stored findings decode: %v (%q)", err, rv.Findings)
	}
	checkSummary(t, s, 30)
}
