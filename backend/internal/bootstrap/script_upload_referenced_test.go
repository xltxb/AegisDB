package bootstrap

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"velagateway/internal/model"
)

// M5(2026-10 安全审计):脚本送审、执行之后,发起人仍能删掉上传的原件 —— 审批单只存摘录与摘要,
// 原件一删,"当时到底执行了什么"就查不到了。被审批单 / 发布单引用的上传不许删。

func TestScriptUpload_ReferencedByApprovalCannotBeDeleted(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	prod := app.connIDByEnv(token, "prod")
	eq(t, app.do(http.MethodPut, "/api/v1/settings", token, map[string]any{"script.savePath": t.TempDir()}).Code, 0, "保存路径")

	// 含高危语句的脚本整份送审:审批单引用这份上传
	run := app.do(http.MethodPost, "/api/v1/scripts/execute", token, map[string]any{
		"content": "SELECT count(*) FROM orders;\nDROP TABLE orders_2024_q3;\n", "filename": "ref.sql", "connectionId": prod,
	})
	eq(t, run.Code, 42200, "送审")

	var ups []model.ScriptUpload
	_ = json.Unmarshal(app.do(http.MethodGet, "/api/v1/scripts/uploads", token, nil).Data, &ups)
	var id int64
	for _, u := range ups {
		if u.Filename == "ref.sql" {
			id = u.ID
		}
	}
	if id == 0 {
		t.Fatalf("应有这份上传: %+v", ups)
	}
	stored, err := app.repo.GetScriptUpload(id)
	if err != nil {
		t.Fatal(err)
	}

	r := app.do(http.MethodDelete, "/api/v1/scripts/uploads/"+itoa(id), token, nil)
	if r.Code == 0 || !strings.Contains(r.Msg, "引用") {
		t.Fatalf("被审批单引用的上传不能删,应说明原因,实际 code=%d msg=%s", r.Code, r.Msg)
	}
	if _, err := os.Stat(stored.Path); err != nil {
		t.Errorf("原件应还在: %v", err)
	}

	// 没被引用的上传照常能删
	up := app.do(http.MethodPost, "/api/v1/scripts/upload", token, map[string]any{"content": "SELECT 1;", "filename": "free.sql"})
	eq(t, up.Code, 0, "上传")
	var free model.ScriptUpload
	_ = json.Unmarshal(up.Data, &free)
	eq(t, app.do(http.MethodDelete, "/api/v1/scripts/uploads/"+itoa(free.ID), token, nil).Code, 0, "未被引用的上传可以删")
}
