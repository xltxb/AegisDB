package bootstrap

// 维护态下什么也不做,也要留下痕迹。
//
// 八个执行入口都判维护态,而它们的应答方式有四类:终端与异步执行记一条 warn 审计并软
// 返回一句提示;审批执行、提发布、导出报错;流水线执行阶段记阶段失败。
//
// 只有脚本执行这一条是 `return 0, nil` —— **既不报错,也不记审计**。注释说它
// "mirrors Exec's soft no-op",可 Exec 记了审计、还回了一句「实例维护中」的提示。
// 这一条什么都没有:接口回「执行了 0 条语句」,审计里一个字都没有。
//
// 人把脚本提上去,看到的是一次没有报错的返回,而他要的那些语句一条也没跑 —— 事后想
// 查「那天到底跑没跑」,唯一能查的地方是空的。
//
// 软返回本身没有问题(维护态不是错误,是一种状态);**不留痕**才是。

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestMaintenance_ScriptExecutionLeavesATrace(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	conn := app.connIDByEnv(token, "dev")

	// 把实例切到维护态。
	eq(t, app.do(http.MethodPatch, "/api/v1/connections/"+itoa(conn), token,
		map[string]any{"status": "maint"}).Code, 0, "切维护态")

	r := app.do(http.MethodPost, "/api/v1/scripts/execute", token, map[string]any{
		"connectionId": conn, "filename": "x.sql", "content": "SELECT 1;",
	})
	// 接口怎么答都可以(软返回或报错),但**不能既说没事又什么都不记**。
	var rows []struct {
		Command string `json:"command"`
		Result  string `json:"result"`
	}
	if err := json.Unmarshal(app.auditItemsRaw(token, ""), &rows); err != nil {
		t.Fatalf("decode audit: %v", err)
	}
	found := false
	for _, row := range rows {
		if strings.Contains(row.Command, "SELECT 1") {
			found = true
		}
	}
	if !found {
		t.Errorf("维护态下的脚本执行没有留下任何审计(接口 code=%d)—— 人看到一次没报错的"+
			"返回,而事后想查「那天到底跑没跑」,唯一能查的地方是空的", r.Code)
	}
}
