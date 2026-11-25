package bootstrap

import (
	"net/http"
	"testing"
)

// L-AUTH1(2026-10 安全审计):角色的"可审批"决定谁能签生产变更,改了却不写审计。
func TestRoleUpdate_CanApproveChangeIsAudited(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	role, _ := app.repo.GetRoleByCode("ro")
	if role == nil {
		t.Fatal("没有 ro 角色")
	}
	eq(t, app.do(http.MethodPatch, "/api/v1/roles/"+itoa(role.ID), admin, map[string]any{"canApprove": !role.CanApprove}).Code, 0, "改可审批")
	if auditCount(app, "command LIKE ? AND command LIKE ?", "admin.role.update role=ro %", "%canApprove%") == 0 {
		t.Error("改角色的可审批开关应写一条审计,并写明前后值")
	}
}
