package bootstrap

import (
	"net/http"
	"testing"

	"aegisdb/internal/model"
)

// L-API2(2026-10 安全审计):导出、后台执行的队列全员共用,却没有按人限额 —— 一个人连发几十个
// 大任务就把别人挤在外面。每人同时排队 + 执行中的各自不超过 5 个。

func TestJobQuota_PerUserCap(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@aegisdb.io", "aegisdb123")
	u, _ := app.repo.GetUserByEmail("linwei@aegisdb.io")
	dev := app.connIDByEnv(token, "dev")
	eq(t, app.do(http.MethodPut, "/api/v1/settings", token, map[string]any{"export.savePath": t.TempDir()}).Code, 0, "导出路径")

	// 先占满:直接写 5 条排队中的任务(worker 不会去捡这些手写的行)
	for i := 0; i < 5; i++ {
		if err := app.repo.CreateExportJob(&model.ExportJob{UserID: u.ID, ConnectionID: dev, SQL: "SELECT 1", Status: model.ExportPending}); err != nil {
			t.Fatal(err)
		}
		if err := app.repo.CreateAsyncJob(&model.AsyncJob{UserID: u.ID, ConnectionID: dev, SQL: "SELECT 1", Status: model.AsyncPending}); err != nil {
			t.Fatal(err)
		}
	}
	eq(t, app.do(http.MethodPost, "/api/v1/export", token, map[string]any{"connectionId": dev, "sql": "SELECT 1", "name": "q"}).Code, 42900, "导出超出按人限额")
	eq(t, app.do(http.MethodPost, "/api/v1/terminal/exec-async", token, map[string]any{"connectionId": dev, "sql": "SELECT 1", "reason": "q"}).Code, 42900, "后台执行超出按人限额")

	// 别人不受影响
	other := app.login("zhangwei@aegisdb.io", "aegisdb123")
	if r := app.do(http.MethodPost, "/api/v1/terminal/exec-async", other, map[string]any{"connectionId": dev, "sql": "SELECT 1", "reason": "q"}); r.Code == 42900 {
		t.Error("别人的限额不应受牵连")
	}
}
