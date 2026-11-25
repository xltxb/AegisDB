package bootstrap

import (
	"encoding/json"
	"net/http"
	"testing"

	"velagateway/internal/service"
)

// 上传脚本弹窗的进度(2026-10):扫描 / 执行请求带上 progressId,服务端逐条记进度,前端轮询
// GET /scripts/progress/:id。进度只给发起人自己看。

func (a *testApp) scriptProgress(token, id string) (int, service.ScriptProgress) {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/scripts/progress/"+id, token, nil)
	var p service.ScriptProgress
	_ = json.Unmarshal(r.Data, &p)
	return r.Code, p
}

func TestScriptProgress_ExecuteRecordsEveryStatement(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	other := app.login("chenhao@vela.io", "vela123")
	dev := app.connIDByEnv(admin, "dev")
	eq(t, app.do(http.MethodPut, "/api/v1/settings", admin, map[string]any{"script.savePath": t.TempDir()}).Code, 0, "保存路径")

	const id = "run-0123456789abcdef"
	r := app.do(http.MethodPost, "/api/v1/scripts/execute", admin, map[string]any{
		"content": "SELECT 1;\nSELECT 2;\nSELECT 3;\n", "filename": "p.sql", "connectionId": dev, "progressId": id,
	})
	eq(t, r.Code, 0, "安全脚本直接执行")

	code, p := app.scriptProgress(admin, id)
	eq(t, code, 0, "发起人查进度")
	if !p.Finished || p.Phase != "run" || p.Done != 3 || p.Total != 3 {
		t.Fatalf("执行完应为 run 阶段 3/3 且已结束,实际 %+v", p)
	}
	if p.StartedAt.IsZero() {
		t.Error("应带开始时间(前端算已用时)")
	}
	if code, _ := app.scriptProgress(other, id); code != 40400 {
		t.Errorf("别人的进度应 40400,实际 %d", code)
	}
	if code, _ := app.scriptProgress(admin, "no-such-progress-id"); code != 40400 {
		t.Errorf("不存在的进度应 40400,实际 %d", code)
	}
	if code, _ := app.scriptProgress(admin, "bad*id"); code != 40400 {
		t.Errorf("不合法的 id 应 40400,实际 %d", code)
	}
}

func TestScriptProgress_ScanRecordsProgress(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	dev := app.connIDByEnv(admin, "dev")
	eq(t, app.do(http.MethodPut, "/api/v1/settings", admin, map[string]any{"script.savePath": t.TempDir()}).Code, 0, "保存路径")

	const id = "scan-0123456789abcdef"
	r := app.do(http.MethodPost, "/api/v1/scripts/scan", admin, map[string]any{
		"content": "SELECT 1;\nSELECT 2;\n", "filename": "s.sql", "connectionId": dev, "progressId": id,
	})
	eq(t, r.Code, 0, "扫描")
	code, p := app.scriptProgress(admin, id)
	eq(t, code, 0, "查扫描进度")
	// 扫描分两段:字典扫描(scan)之后逐条判定(judge),结束时停在 judge 2/2
	if !p.Finished || p.Phase != "judge" || p.Done != 2 || p.Total != 2 {
		t.Fatalf("扫描完应为 judge 阶段 2/2 且已结束,实际 %+v", p)
	}

	// 不带 progressId 的请求照旧,不记进度
	eq(t, app.do(http.MethodPost, "/api/v1/scripts/scan", admin, map[string]any{
		"content": "SELECT 1;", "filename": "s.sql", "connectionId": dev,
	}).Code, 0, "不带 progressId 照常扫描")
}
