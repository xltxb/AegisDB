package bootstrap

import (
	"net/http"
	"strings"
	"testing"
)

// AI 巡检报告 · 工单 01(.scratch/inspection-report):菜单默认归属与上传范围。

// 「巡检报告」菜单默认给平台管理员、DBA 负责人、DBA L2;研发只读、审计员没有
// (2026-10-15 用户确认「DBA 都可以看」)。
func TestInspection_MenuDefaultsToTheDBARoles(t *testing.T) {
	app := newTestApp(t)
	for code, want := range map[string]bool{"admin": true, "owner": true, "l2": true, "ro": false, "audit": false} {
		if got := menuOn(app, roleIDByCode(t, app, code), MenuInspection); got != want {
			t.Errorf("角色 %s 的巡检报告菜单应为 %v,实际 %v", code, want, got)
		}
	}
	// 回填只在这个键完全不存在时写:再跑一次不改动一次有意的收回
	ro := roleIDByCode(t, app, "ro")
	if err := app.repo.DB().Exec("UPDATE tbl_role_menu SET enabled = ? WHERE role_id = ? AND menu_key = ?", true, ro, MenuInspection).Error; err != nil {
		t.Fatal(err)
	}
	if err := backfillInspectionMenu(app.repo.DB()); err != nil {
		t.Fatal(err)
	}
	if !menuOn(app, ro, MenuInspection) {
		t.Error("管理员手动改过的菜单不该在回填时被改回去")
	}
}

// inspection:write 不在默认范围里:没勾的凭据没有,显式勾选才有。
func TestInspection_UploadScopeIsOptIn(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	app.issueClient(admin, "ci-default", "zhangwei@vela.io", nil)
	if s := app.apiClientByName(admin, "ci-default").Scopes; strings.Contains(s, "inspection:write") {
		t.Errorf("默认范围不该含 inspection:write,实际 %q", s)
	}
	app.issueClient(admin, "ai-inspector", "zhangwei@vela.io", []string{"inspection:write"})
	if s := app.apiClientByName(admin, "ai-inspector").Scopes; s != "inspection:write" {
		t.Errorf("显式勾选后应只有 inspection:write,实际 %q", s)
	}
	// 设置项能存能读
	app.setSettings(admin, map[string]any{"inspection.savePath": "/var/vela/inspection"})
	r := app.do(http.MethodGet, "/api/v1/settings", admin, nil)
	if !strings.Contains(string(r.Data), `inspection.savePath`) {
		t.Errorf("系统设置里应能读到 inspection.savePath: %s", r.Data)
	}
}
