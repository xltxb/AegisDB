package bootstrap

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"aegisdb/internal/model"
)

// 班车(执行窗口)时段内执行 \i 脚本:本该审批的语句由窗口放行,整份脚本直接执行,不送审。
//
// 生产 2026-10-13(AP-2554):窗口 AP-2552 生效中,\i game.sql 照样整份送审,工单上写着
// "高危 4 · 需审批 0"。逐条判定时窗口确实放行了,但"要不要送审"是按高危条数算的,而窗口
// 不降风险等级 —— 高危还是高危,于是含字典高危语句的脚本在窗口里永远要审批。
func TestScript_WindowRelaxesTheWholeScript(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@aegisdb.io", "aegisdb123")
	prod := app.connIDByEnv(token, "prod")
	app.setSettings(token, map[string]any{"script.savePath": t.TempDir()})
	const targetDB = "orders_db"
	const script = "ALTER TABLE happy_hour_event ADD (daily_max_round NUMBER(10) DEFAULT 0 NOT NULL);\nDROP TABLE orders_2024_q3;\n"

	scan := func() (out struct {
		High     int    `json:"high"`
		HasRisky bool   `json:"hasRisky"`
		Window   string `json:"window"`
	}) {
		r := app.do(http.MethodPost, "/api/v1/scripts/scan", token, map[string]any{
			"connectionId": prod, "filename": "game.sql", "content": script, "database": targetDB})
		eq(t, r.Code, 0, "scan: "+r.Msg)
		_ = json.Unmarshal(r.Data, &out)
		return
	}

	// 窗口之前:高危语句要审批
	if s := scan(); !s.HasRisky {
		t.Fatalf("开窗前含高危语句的脚本应送审: %+v", s)
	}

	app.openWindow(token, prod, targetDB, time.Now().Add(-time.Minute), time.Now().Add(time.Hour))

	// 窗口之内:高危条数照报(风险等级不降),但不再要求送审,并说明是哪个窗口放的
	s := scan()
	if s.HasRisky || s.High == 0 || s.Window == "" {
		t.Fatalf("窗口内应:仍报高危、不送审、写明窗口;实际 %+v", s)
	}

	var before int64
	app.repo.DB().Model(&model.Approval{}).Count(&before)
	r := app.do(http.MethodPost, "/api/v1/scripts/execute", token, map[string]any{
		"connectionId": prod, "filename": "game.sql", "content": script, "database": targetDB})
	eq(t, r.Code, 0, "窗口内脚本应直接执行,不该送审: "+r.Msg)
	var after int64
	app.repo.DB().Model(&model.Approval{}).Count(&after)
	eq(t, after, before, "窗口内不该生成审批单")
}
