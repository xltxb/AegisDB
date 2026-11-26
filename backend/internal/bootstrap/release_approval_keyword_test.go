package bootstrap

// 2026-11 生产:发布单「人工审批」阶段失败 ——
//   审批单创建失败: Error 1406 (22001): Data too long for column 'keyword' at row 1
//
// 审批单的 keyword 列宽 32,取的却是"脚本按空白切开的第一段"。脚本以一行分隔线注释
// 或一句不带空格的中文注释开头时，那一整行就是第一段。查列宽的库
// (MySQL 严格模式 / PostgreSQL)上就炸;测试存储库是 PG,本用例修复前失败。

import (
	"encoding/json"
	"net/http"
	"testing"
	"unicode/utf8"
)

func TestReleaseApproval_LeadingLongCommentStillParks(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	stg := app.connIDByEnv(admin, "staging")
	pid := app.createPipeline(admin, "长注释开头流程", "", []map[string]any{
		{"name": "人工审批", "type": "approve"},
		{"name": "执行变更", "type": "execute"},
	})
	script := "--==============================================================\n" +
		"--本次变更说明：订单表新增渠道字段，配合十一月渠道结算需求上线，请在低峰期执行\n" +
		"/* 作者:某某 日期:2026-11-20 ******************************************/\n" +
		"SELECT 1;"
	r := app.submitRelease(admin, map[string]any{
		"title": "长注释开头", "pipelineId": pid, "connectionId": stg,
		"sql": script, "reason": "回归 keyword 1406",
	})
	eq(t, r.Code, 0, "submit release")
	var rel releaseView
	_ = json.Unmarshal(r.Data, &rel)
	got := app.waitRelease(admin, rel.ID, "waiting", "failed", "success")
	if got.Status != "waiting" {
		st := stageByType(got, "approve")
		t.Fatalf("发布单应停在审批上，实际 %s(审批阶段 %+v)", got.Status, st)
	}
	ap := stageByType(got, "approve")
	if ap == nil || ap.ApprovalID == 0 {
		t.Fatalf("审批阶段应带一张审批单，实际 %+v", ap)
	}

	d := app.do(http.MethodGet, "/api/v1/approvals?scope=all&pageSize=500", admin, nil)
	eq(t, d.Code, 0, "approval list")
	var page struct {
		Items []struct {
			ID      int64  `json:"id"`
			Keyword string `json:"keyword"`
		} `json:"items"`
	}
	_ = json.Unmarshal(d.Data, &page)
	var detail struct{ Keyword string }
	found := false
	for _, it := range page.Items {
		if it.ID == ap.ApprovalID {
			detail.Keyword, found = it.Keyword, true
		}
	}
	if !found {
		t.Fatalf("审批列表里找不到审批单 %d", ap.ApprovalID)
	}
	eq(t, detail.Keyword, "SELECT", "关键字是注释后面那条语句的动词")
	if n := utf8.RuneCountInString(detail.Keyword); n > 32 {
		t.Fatalf("keyword %d 字符，超过列宽 32", n)
	}
}
