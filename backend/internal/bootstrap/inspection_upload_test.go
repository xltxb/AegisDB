package bootstrap

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// AI 巡检报告 · 工单 02(.scratch/inspection-report):开放接口上传。

const inspectionPath = "/api/v1/open/inspection-reports"

func inspectionBody(instance, date, level, html string) map[string]any {
	return map[string]any{
		"instance": instance, "date": date, "level": level, "score": 86,
		"counts":   map[string]int{"critical": 1, "warning": 2, "info": 3},
		"findings": []map[string]string{{"severity": "critical", "category": "分区", "title": "t_order 分区将用尽", "object": "shop.t_order"}},
		"html":     html,
	}
}

// inspectionSetup:配好保存路径,发一把只有 inspection:write 的凭据。
func inspectionSetup(t *testing.T) (app *testApp, admin, client, dir string) {
	app = newTestApp(t)
	admin = app.login("linwei@vela.io", "vela123")
	dir = t.TempDir()
	app.setSettings(admin, map[string]any{"inspection.savePath": dir})
	client = app.issueClient(admin, "ai-inspector", "zhangwei@vela.io", []string{"inspection:write"})
	return
}

func gunzipFile(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("报告文件应存在: %v", err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(zr)
	return string(b)
}

func TestInspectionUpload_StoresAndReplacesSameDay(t *testing.T) {
	app, admin, client, dir := inspectionSetup(t)
	today := time.Now().Format("2006-01-02")
	html1 := "<!doctype html><h1>第一版</h1><script>var x = 1</script>"

	r := app.openDo(http.MethodPost, inspectionPath, client, inspectionBody("order-cluster", today, "warning", html1))
	eq(t, r.Code, 0, "上传: "+r.Msg)
	var out struct {
		ID       int64 `json:"id"`
		Replaced bool  `json:"replaced"`
	}
	_ = json.Unmarshal(r.Data, &out)
	if out.ID == 0 || out.Replaced {
		t.Fatalf("第一次应新建: %s", r.Data)
	}
	file := filepath.Join(dir, inspectionDirOf(app, "order-cluster"), today[:4], today+".html.gz")
	if got := gunzipFile(t, file); got != html1 {
		t.Errorf("文件解压后应与原文一致,实际 %q", got)
	}
	rep, err := app.repo.GetInspectionReport(out.ID)
	if err != nil || rep.Level != "warning" || rep.CriticalCount != 1 || rep.InstanceName != "order-cluster" || *rep.Score != 86 {
		t.Fatalf("元数据应入库: %+v err=%v", rep, err)
	}

	html2 := "<!doctype html><h1>重跑后的第二版</h1>"
	r = app.openDo(http.MethodPost, inspectionPath, client, inspectionBody("order-cluster", today, "critical", html2))
	eq(t, r.Code, 0, "再传: "+r.Msg)
	var again struct {
		ID       int64 `json:"id"`
		Replaced bool  `json:"replaced"`
	}
	_ = json.Unmarshal(r.Data, &again)
	if !again.Replaced || again.ID != out.ID {
		t.Fatalf("同实例同一天应覆盖、保留 ID: %s", r.Data)
	}
	if got := gunzipFile(t, file); got != html2 {
		t.Errorf("覆盖后文件应是新的,实际 %q", got)
	}
	_, total, _ := app.repo.ListInspectionReports(rep.ConnectionID, 0, 10)
	eq(t, int(total), 1, "只剩一份")

	// 审计里有上传与覆盖
	audit := string(app.auditItemsRaw(admin, "?range=24h&full=1"))
	if !strings.Contains(audit, "inspection.upload 上传巡检报告") || !strings.Contains(audit, "inspection.upload 覆盖巡检报告") {
		t.Errorf("审计应记下上传与覆盖: %s", audit)
	}
}

func TestInspectionUpload_RejectsBadRequests(t *testing.T) {
	app, admin, client, dir := inspectionSetup(t)
	today := time.Now().Format("2006-01-02")
	ok := "<html>ok</html>"

	r := app.openDo(http.MethodPost, inspectionPath, client, inspectionBody("no-such-instance", today, "ok", ok))
	eq(t, r.Code, 40400, "未知实例: "+r.Msg)
	if !strings.Contains(r.Msg, "no-such-instance") {
		t.Errorf("要说出是哪个实例名对不上: %q", r.Msg)
	}
	if _, err := os.Stat(filepath.Join(dir, "no-such-instance")); !os.IsNotExist(err) {
		t.Error("未知实例不该落盘")
	}

	for name, body := range map[string]map[string]any{
		"level 非法":  inspectionBody("order-cluster", today, "bad", ok),
		"日期格式错":     inspectionBody("order-cluster", "2026/10/15", "ok", ok),
		"日期在很远的未来":  inspectionBody("order-cluster", time.Now().AddDate(0, 0, 5).Format("2006-01-02"), "ok", ok),
		"html 为空":    inspectionBody("order-cluster", today, "ok", "  "),
		"instance 为空": inspectionBody("", today, "ok", ok),
	} {
		if r := app.openDo(http.MethodPost, inspectionPath, client, body); r.Code != 40001 {
			t.Errorf("%s 应返回 40001,实际 %d %s", name, r.Code, r.Msg)
		}
	}
	bad := inspectionBody("order-cluster", today, "ok", ok)
	bad["score"] = 120
	eq(t, app.openDo(http.MethodPost, inspectionPath, client, bad).Code, 40001, "score 超出 0-100")
	bad = inspectionBody("order-cluster", today, "ok", ok)
	bad["findings"] = []map[string]string{{"severity": "fatal", "title": "x"}}
	eq(t, app.openDo(http.MethodPost, inspectionPath, client, bad).Code, 40001, "findings.severity 非法")

	// 超过 20MB
	huge := inspectionBody("order-cluster", today, "ok", strings.Repeat("x", 21<<20))
	eq(t, app.openDo(http.MethodPost, inspectionPath, client, huge).Code, 41300, "超过 20MB")

	// 没有 inspection:write 的凭据
	other := app.issueClient(admin, "ci", "zhangwei@vela.io", nil)
	eq(t, app.openDo(http.MethodPost, inspectionPath, other, inspectionBody("order-cluster", today, "ok", ok)).Code, 40300, "没有上传范围")

	// 保存路径没配
	app.setSettings(admin, map[string]any{"inspection.savePath": ""})
	eq(t, app.openDo(http.MethodPost, inspectionPath, client, inspectionBody("order-cluster", today, "ok", ok)).Code, 42602, "保存路径未配置")
}

// inspectionDirOf 是实例的报告目录名:<连接ID>-<实例名>(2026-10 安全审计 M10)。
func inspectionDirOf(app *testApp, name string) string {
	app.t.Helper()
	c, err := app.repo.GetConnectionByName(name)
	if err != nil {
		app.t.Fatal(err)
	}
	return itoa(c.ID) + "-" + name
}
