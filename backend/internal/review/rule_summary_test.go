package review

import (
	"strings"
	"testing"
)

// 审查结果按规则汇总:每条规则只带前 MaxExamplesPerRule 条示例,命中数照实全算。
//
// 2026-11-10:本地一张 50 万条语句的脚本,每条都命中,findings 存了约 100MB ——
// MySQL MEDIUMTEXT 16MB 写不进(阶段状态跟着丢)、详情每 2.5 秒整段回传、浏览器画 50 万行。
// 用户拍板:不需要逐条留痕,每条规则附 5 条示例。
func TestReviewSummarisesPerRule(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 40; i++ {
		b.WriteString("DELETE FROM tbl_order;\n") // 每条都缺 WHERE(error)
	}
	r := Check(DialectMySQL, b.String(), allRules())

	if r.Statements != 40 {
		t.Fatalf("statements = %d, want 40", r.Statements)
	}
	per := map[string]int{}
	for _, f := range r.Findings {
		per[f.Code]++
	}
	for code, n := range per {
		if n > MaxExamplesPerRule {
			t.Errorf("rule %s carries %d examples, want at most %d", code, n, MaxExamplesPerRule)
		}
	}
	if per["dml.require.where"] != MaxExamplesPerRule {
		t.Fatalf("dml.require.where examples = %d, want %d", per["dml.require.where"], MaxExamplesPerRule)
	}
	// 示例是最前面那几条,不是随便挑的
	n := 0
	for _, f := range r.Findings {
		if f.Code == "dml.require.where" {
			n++
			if f.Stmt != n {
				t.Errorf("example %d is statement %d, want the first ones in order", n, f.Stmt)
			}
		}
	}

	var where *RuleHit
	total := 0
	for i := range r.Rules {
		total += r.Rules[i].Count
		if r.Rules[i].Code == "dml.require.where" {
			where = &r.Rules[i]
		}
	}
	if where == nil || where.Count != 40 || where.Level != LevelError {
		t.Fatalf("summary for dml.require.where = %+v, want 40 error hits", where)
	}
	if total != r.Total || r.Total != r.Errors+r.Warnings+r.Infos {
		t.Fatalf("total %d, rules sum %d, levels sum %d — must all agree", r.Total, total, r.Errors+r.Warnings+r.Infos)
	}
	if r.Errors < 40 || r.Passed {
		t.Fatalf("level counts are over ALL hits and still block: errors=%d passed=%v", r.Errors, r.Passed)
	}
	// 错误级别的规则排在前面
	if r.Rules[0].Level != LevelError {
		t.Fatalf("error rules first, got %+v", r.Rules[0])
	}
}

// 干净的脚本:没有命中,汇总也是空的(界面据此显示"全部通过")。
func TestReviewSummaryEmptyWhenClean(t *testing.T) {
	r := Check(DialectMySQL, "SELECT id FROM tbl_order WHERE id = 1;", nil)
	if !r.Passed || r.Total != 0 || len(r.Rules) != 0 || len(r.Findings) != 0 {
		t.Fatalf("clean script: %+v", r)
	}
	if r.Rules == nil || r.Findings == nil {
		t.Fatal("empty lists, not null, so old clients iterating them keep working")
	}
}
