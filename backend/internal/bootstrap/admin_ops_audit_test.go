package bootstrap

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"aegisdb/internal/model"
)

// auditCommandsLike 直接读审计表里命令含 sub 的行(按写入先后)。
func (a *testApp) auditCommandsLike(sub string) []string {
	a.t.Helper()
	var rows []model.AuditLog
	if err := a.repo.DB().Where("command LIKE ?", "%"+sub+"%").Order("id asc").Find(&rows).Error; err != nil {
		a.t.Fatalf("读审计: %v", err)
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Command)
	}
	return out
}

// mustAudit 断言至少有一行审计带着 sub。
func (a *testApp) mustAudit(sub, what string) []string {
	a.t.Helper()
	rows := a.auditCommandsLike(sub)
	if len(rows) == 0 {
		a.t.Errorf("%s:审计里没有 %q", what, sub)
	}
	return rows
}

// 管理操作都要进审计(2026-10 黑盒测试,工单 02 第 2 条)。
//
// 这些操作决定"谁能进来、能碰哪台库、哪条命令要审批、结果推给谁",是和执行命令同一个
// 量级的事。不留痕的话,有人可以改宽、做事、再改回去,审计链上只看得到那件事,看不到
// 让它发生的那次配置。
func TestAdminOps_UserAndRoleChangesAreAudited(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	roID := app.roleIDByCode(admin, "ro")

	eq(t, app.do(http.MethodPost, "/api/v1/users", admin, map[string]any{
		"name": "审计甲", "email": "audit-a@aegisdb.io", "password": "Str0ng-Passw0rd!", "roleIds": []int64{roID},
	}).Code, 0, "建账户")
	rows := app.mustAudit("admin.user.create", "建账户")
	for _, r := range rows {
		if strings.Contains(r, "Str0ng-Passw0rd!") {
			t.Errorf("初始口令不该进审计:%s", r)
		}
	}

	eq(t, app.do(http.MethodPost, "/api/v1/users/invite", admin, map[string]any{
		"email": "audit-b@aegisdb.io", "roleId": roID,
	}).Code, 0, "邀请")
	app.mustAudit("admin.user.invite", "邀请")

	// 给甲再加一个角色,再从 ro 里移除 —— 移除要进审计。
	u, _ := app.repo.GetUserByEmail("audit-a@aegisdb.io")
	devID := app.roleIDByCode(admin, "l2")
	eq(t, app.do(http.MethodPost, "/api/v1/roles/"+itoa(devID)+"/members", admin, map[string]any{"userId": u.ID}).Code, 0, "加成员")
	eq(t, app.do(http.MethodDelete, "/api/v1/roles/"+itoa(roID)+"/members/"+itoa(u.ID), admin, nil).Code, 0, "移除成员")
	app.mustAudit("admin.role.member.remove", "移除成员")
}

func TestAdminOps_ConnectionChangesAreAuditedWithoutPasswords(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")

	r := app.do(http.MethodPost, "/api/v1/connections", admin, map[string]any{
		"name": "audit-conn", "engine": "MySQL 8.0", "host": "10.9.9.9:3306", "env": "dev", "policy": "strict",
		"username": "app", "password": "Pl41n-Pw-Do-Not-Log",
	})
	eq(t, r.Code, 0, "建连接")
	var c struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(r.Data, &c)
	app.mustAudit("admin.connection.create", "建连接")

	eq(t, app.do(http.MethodPut, "/api/v1/connections/"+itoa(c.ID), admin, map[string]any{
		"name": "audit-conn", "engine": "MySQL 8.0", "host": "10.9.9.10:3306", "env": "dev", "policy": "strict",
		"username": "app", "password": "N3w-Pl41n-Pw-Do-Not-Log",
	}).Code, 0, "改连接(含口令、host)")
	rows := app.mustAudit("admin.connection.update", "改连接")
	if len(rows) > 0 && !strings.Contains(rows[len(rows)-1], "口令已修改") {
		t.Errorf("改了口令应当记一句「口令已修改」:%s", rows[len(rows)-1])
	}

	eq(t, app.do(http.MethodPatch, "/api/v1/connections/"+itoa(c.ID), admin, map[string]any{"policy": "audit-only"}).Code, 0, "改策略")
	eq(t, app.do(http.MethodPatch, "/api/v1/connections/"+itoa(c.ID), admin, map[string]any{"status": "maint"}).Code, 0, "改状态")
	app.mustAudit("admin.connection.policy", "改策略")
	app.mustAudit("admin.connection.status", "改状态")

	for _, r := range app.auditCommandsLike("admin.connection") {
		if strings.Contains(r, "Do-Not-Log") {
			t.Errorf("连接口令不该进审计:%s", r)
		}
	}
}

func TestAdminOps_TierEnvRiskPipelineChangesAreAudited(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")

	eq(t, app.do(http.MethodPost, "/api/v1/env-tiers", admin, map[string]any{
		"code": "audt", "displayName": "审计分层", "templateCode": "dev",
	}).Code, 0, "建分层")
	eq(t, app.do(http.MethodPut, "/api/v1/env-tiers/audt", admin, map[string]any{"displayName": "审计分层2"}).Code, 0, "改分层")
	eq(t, app.do(http.MethodPost, "/api/v1/environments", admin, map[string]any{
		"code": "audt-env", "displayName": "审计环境", "tierCode": "audt",
	}).Code, 0, "建环境")
	eq(t, app.do(http.MethodDelete, "/api/v1/environments/audt-env", admin, map[string]any{"moveTo": "dev"}).Code, 0, "删环境")
	eq(t, app.do(http.MethodDelete, "/api/v1/env-tiers/audt", admin, nil).Code, 0, "删分层")
	app.mustAudit("admin.envtier.create", "建分层")
	app.mustAudit("admin.envtier.update", "改分层")
	app.mustAudit("admin.envtier.delete", "删分层")
	app.mustAudit("admin.environment.create", "建环境")
	app.mustAudit("admin.environment.delete", "删环境")

	eq(t, app.do(http.MethodPost, "/api/v1/risk-commands", admin, map[string]any{
		"command": "AUDITCMD", "tiers": map[string]string{"prod": "high"},
	}).Code, 0, "加字典命令")
	eq(t, app.do(http.MethodPatch, "/api/v1/risk-commands/AUDITCMD", admin, map[string]any{"tier": "dev", "level": "mid"}).Code, 0, "改字典")
	eq(t, app.do(http.MethodDelete, "/api/v1/risk-commands/AUDITCMD", admin, nil).Code, 0, "删字典命令")
	app.mustAudit("admin.riskcmd.upsert", "加字典命令")
	app.mustAudit("admin.riskcmd.patch", "改字典")
	app.mustAudit("admin.riskcmd.delete", "删字典命令")

	pid := app.createPipeline(admin, "审计流程", "", []map[string]any{{"name": "执行变更", "type": "execute"}})
	eq(t, app.do(http.MethodPut, "/api/v1/pipelines/"+itoa(pid), admin, map[string]any{
		"name": "审计流程2", "enabled": true, "stages": []map[string]any{{"type": "execute"}},
	}).Code, 0, "改流程")
	eq(t, app.do(http.MethodDelete, "/api/v1/pipelines/"+itoa(pid), admin, nil).Code, 0, "删流程")
	app.mustAudit("admin.pipeline.create", "建流程")
	app.mustAudit("admin.pipeline.update", "改流程")
	app.mustAudit("admin.pipeline.delete", "删流程")

	r := app.do(http.MethodPost, "/api/v1/sql-review/rules", admin, map[string]any{
		"code": "audit.rule", "name": "审计规则", "level": "warn", "enabled": true, "params": `{"pattern":"FOO"}`,
	})
	eq(t, r.Code, 0, "建规范规则")
	var rule struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(r.Data, &rule)
	eq(t, app.do(http.MethodPut, "/api/v1/sql-review/rules/"+itoa(rule.ID), admin, map[string]any{
		"level": "info", "enabled": true, "params": `{"pattern":"FOO"}`,
	}).Code, 0, "改规范规则")
	eq(t, app.do(http.MethodDelete, "/api/v1/sql-review/rules/"+itoa(rule.ID), admin, nil).Code, 0, "删规范规则")
	app.mustAudit("admin.sqlreview.create", "建规范规则")
	app.mustAudit("admin.sqlreview.update", "改规范规则")
	app.mustAudit("admin.sqlreview.delete", "删规范规则")
}

func TestAdminOps_SettingsAndWebhookChangesAreAuditedWithoutSecrets(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")

	const secret = "lark-s3cr3t-do-not-log"
	eq(t, app.do(http.MethodPut, "/api/v1/settings", admin, map[string]any{
		"gateway.execTimeout": 45, "notify.larkSecret": secret,
	}).Code, 0, "存设置")
	rows := app.mustAudit("admin.settings.update", "存设置")
	if len(rows) > 0 {
		last := rows[len(rows)-1]
		if !strings.Contains(last, "gateway.execTimeout=45") {
			t.Errorf("普通键要记新值:%s", last)
		}
		if !strings.Contains(last, "notify.larkSecret") {
			t.Errorf("secret 类要记键名:%s", last)
		}
	}

	const whSecret = "wh-s3cr3t-do-not-log"
	eq(t, app.do(http.MethodPut, "/api/v1/settings/webhook", admin, map[string]any{
		"endpoint": "https://siem.example.com/hook", "secret": whSecret, "enabled": true, "retryMax": 3,
	}).Code, 0, "存 webhook")
	app.mustAudit("admin.webhook.update", "存 webhook")

	for _, r := range append(app.auditCommandsLike("admin.settings"), app.auditCommandsLike("admin.webhook")...) {
		if strings.Contains(r, secret) || strings.Contains(r, whSecret) {
			t.Errorf("secret 明文进了审计:%s", r)
		}
	}
}
