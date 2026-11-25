package bootstrap

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"aegisdb/internal/model"
)

// M1(2026-10 安全审计):窗口回看不按人收窄,却不看窗口批没批过,一次性窗口的起点又能倒填 ——
// 有班车菜单的人建一扇起点倒填的窗口(不必等批准),就能回看那台实例上所有人过去的语句原文,
// 绕过"非监督角色只看自己的记录"。非监督角色只能回看批准过的窗口,且只看批准之后的。
func TestWindowStatements_NonOverseerOnlySeesApprovedPeriod(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	prod := app.connIDByEnv(admin, "prod")
	const targetDB = "orders_db"
	const earlier = "SELECT 'm1-before-window' AS tag"

	eq(t, app.do(http.MethodPost, "/api/v1/terminal/exec", admin, map[string]any{
		"connectionId": prod, "sql": earlier, "reason": "m1", "database": targetDB,
	}).Code, 0, "管理员先跑一条")

	// L2:有班车菜单、不是监督角色。建一扇起点倒填到一小时前的窗口。
	l2 := app.login("chenhao@aegisdb.io", "aegisdb123")
	r := app.do(http.MethodPost, "/api/v1/exec-windows", l2, map[string]any{
		"name": "m1", "enabled": true, "connectionId": prod, "database": targetDB, "kind": "once",
		"startsAt": time.Now().Add(-time.Hour).Format(time.RFC3339), "endsAt": time.Now().Add(time.Hour).Format(time.RFC3339),
		"reason": "m1",
	})
	eq(t, r.Code, 0, "建窗口")
	var w model.ExecWindow
	_ = json.Unmarshal(r.Data, &w)

	// 还没批:回看不开放
	if got := app.do(http.MethodGet, "/api/v1/exec-windows/"+itoa(w.ID)+"/statements", l2, nil); got.Code == 0 {
		t.Fatal("待审批的窗口不应允许非监督角色回看")
	}

	app.decideWindowTicket(w.ApNo, true)
	if hasCommand(app.windowStatements(l2, w.ID).Items, earlier) != nil {
		t.Fatal("批准之前别人跑的语句不应出现在回看里 —— 那时这扇门还没开")
	}
	// 监督角色查旧账不受此限
	if hasCommand(app.windowStatements(admin, w.ID).Items, earlier) == nil {
		t.Error("监督角色应仍能看到窗口时段内的全部语句")
	}
}
