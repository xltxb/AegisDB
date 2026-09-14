package bootstrap

// 导出的 CSV 要带上审批链,不只是一个单号。
//
// issue 08 要求导出含审批链。现在导出的是 `approval_no` —— 一个单号。拿着它,人还得
// 回控制台一张一张翻过去,才知道这条高危命令是**谁**批的。
//
// 而这份 CSV 的用处恰恰是离开控制台之后:交给审计、交给监管、附在事故报告后面。一个
// 在那个场景里查不到审批人的单号,等于没导出这件事。

import (
	"net/http"
	"strings"
	"testing"
)

func TestAuditCSV_CarriesTheApprovalChain(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	prod := app.connIDByEnv(token, "prod")

	// 在 PROD 上做一件要审批的事 —— 它会变成一张待审单,而不是直接执行。
	r := app.do(http.MethodPost, "/api/v1/terminal/exec", token, map[string]any{
		"connectionId": prod, "sql": "DELETE FROM tbl_order WHERE id = 1", "reason": "清理测试数据",
	})
	if r.Code != 42200 {
		t.Fatalf("前置条件不成立:这条命令该被拦下送审,实际 code=%d msg=%s", r.Code, r.Msg)
	}

	csv := string(app.raw(http.MethodGet, "/api/v1/audit/export?risk=all", token))
	head := strings.SplitN(csv, "\n", 2)[0]
	if !strings.Contains(head, "approval_chain") {
		t.Fatalf("CSV 表头里没有审批链这一列:%q", head)
	}
	// 单号还在 —— 加一列不是替换掉它。
	if !strings.Contains(head, "approval_no") {
		t.Errorf("单号那一列不见了:%q", head)
	}

	// 待审状态下,链上应当已经能看见**该谁批** —— 那正是这一列的用处:
	// 一张卡在那儿的单,看的人第一个问题是"在等谁"。
	if !strings.Contains(csv, "AP-") {
		t.Fatalf("导出里没有任何单号,前置条件不成立:\n%s", clipCSV(csv))
	}
	line := ""
	for _, l := range strings.Split(csv, "\n") {
		if strings.Contains(l, "AP-") {
			line = l
			break
		}
	}
	cols := strings.Split(head, ",")
	idx := -1
	for i, c := range cols {
		if c == "approval_chain" {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatal("表头里找不到 approval_chain")
	}
	if !strings.Contains(line, "待") && !strings.Contains(line, "waiting") && !strings.Contains(line, "@") {
		t.Errorf("带单号那一行的审批链是空的 —— 一张待审的单,看的人第一个问题是「在等谁」:\n%s", line)
	}
}

func clipCSV(s string) string {
	if len(s) > 600 {
		return s[:600] + "…"
	}
	return s
}
