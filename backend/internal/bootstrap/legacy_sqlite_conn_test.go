package bootstrap

// SQLite 已不再是可接入的目标库引擎。升级前建好的 SQLite 连接还留在存储库里:不删不改,
// 列表里照样看得到(管理员要知道它在、自己决定删不删),但新建被挡,对它执行查询、测试连接
// 都以"不支持的数据库引擎"失败 —— 不能悄悄退化成模拟结果,也不能 panic。

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"aegisdb/internal/model"
)

func TestLegacySQLiteConnectionIsRefused(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")

	legacy := &model.Connection{
		Name: "legacy-sqlite", Engine: "SQLite", Host: "localhost", Port: 0,
		Env: "dev", Policy: "audit-only", DefaultRole: "developer", Layer: "L4 沙盒",
		Database: "/data/legacy.db", Status: model.ConnStatusOnline,
	}
	if err := app.repo.CreateConnection(legacy); err != nil {
		t.Fatalf("seed legacy SQLite connection: %v", err)
	}

	// 新建:引擎 sqlite 被挡。
	cr := app.do(http.MethodPost, "/api/v1/connections", admin, map[string]any{
		"name": "new-sqlite", "engine": "sqlite", "host": "localhost",
		"env": "dev", "policy": "audit-only", "database": "/tmp/x.db",
	})
	if cr.Code == 0 || !strings.Contains(cr.Msg, "不支持") {
		t.Errorf("新建 SQLite 连接应以「不支持」被拒,got code=%d msg=%q", cr.Code, cr.Msg)
	}

	// 对存量那条执行查询:失败,并说清是引擎不支持(不是模拟出一份结果)。
	ex := app.do(http.MethodPost, "/api/v1/terminal/exec", admin, map[string]any{
		"connectionId": legacy.ID, "sql": "SELECT 1",
	})
	if ex.Code == 0 || !strings.Contains(ex.Msg, "不支持") {
		t.Errorf("对存量 SQLite 连接执行查询应以「不支持」失败,got code=%d msg=%q data=%s", ex.Code, ex.Msg, ex.Data)
	}

	// 后台执行:同样失败。
	ea := app.do(http.MethodPost, "/api/v1/terminal/exec-async", admin, map[string]any{
		"connectionId": legacy.ID, "sql": "SELECT 1",
	})
	if ea.Code == 0 || !strings.Contains(ea.Msg, "不支持") {
		t.Errorf("对存量 SQLite 连接提交后台执行应以「不支持」失败,got code=%d msg=%q", ea.Code, ea.Msg)
	}

	// 数据导出(CSV 与 SQL 两种格式):提交时就拒,不入队、不产出编出来的文件。
	eq(t, app.do(http.MethodPut, "/api/v1/settings", admin, map[string]any{"export.savePath": t.TempDir()}).Code, 0, "set export path")
	for _, format := range []string{"csv", "sql"} {
		body := map[string]any{"connectionId": legacy.ID, "sql": "SELECT * FROM t", "name": "legacy-" + format, "format": format}
		if format == "sql" {
			body["targetTable"] = "t"
		}
		r := app.do(http.MethodPost, "/api/v1/export", admin, body)
		if r.Code == 0 || !strings.Contains(r.Msg, "不支持") {
			t.Errorf("%s 导出存量 SQLite 连接应以「不支持」被拒,got code=%d msg=%q data=%s", format, r.Code, r.Msg, r.Data)
		}
	}
	var queued int64
	app.repo.DB().Model(&model.ExportJob{}).Where("connection_id = ?", legacy.ID).Count(&queued)
	if queued != 0 {
		t.Errorf("被拒的导出不该入队,实际 %d 条任务", queued)
	}

	// 测试连接:同样失败。
	tc := app.do(http.MethodPost, "/api/v1/connections/"+itoa(legacy.ID)+"/test", admin, nil)
	if tc.Code == 0 || !strings.Contains(tc.Msg, "不支持") {
		t.Errorf("测试存量 SQLite 连接应以「不支持」失败,got code=%d msg=%q", tc.Code, tc.Msg)
	}

	// 列表里仍然在。
	lr := app.do(http.MethodGet, "/api/v1/connections", admin, nil)
	if lr.Code != 0 {
		t.Fatalf("list connections: code=%d msg=%s", lr.Code, lr.Msg)
	}
	var conns []struct {
		ID     int64  `json:"id"`
		Engine string `json:"engine"`
	}
	if err := json.Unmarshal(lr.Data, &conns); err != nil {
		t.Fatalf("decode connections: %v", err)
	}
	found := false
	for _, c := range conns {
		if c.ID == legacy.ID {
			found = true
			if c.Engine != "SQLite" {
				t.Errorf("存量连接的引擎被改了:%q", c.Engine)
			}
		}
	}
	if !found {
		t.Error("存量 SQLite 连接应仍出现在连接列表里")
	}
}
