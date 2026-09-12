package bootstrap

// 改判定规则,和按规则放行一样,要进同一条审计链。
//
// 角色能力矩阵、菜单、标签、执行窗口的改动早就写审计了,理由写在 AuditRoleChange 上:
// 「有人可以把角色放宽、以它的名义做事、再把它收回去,而链上只看得到那次动作,看不到
// 允许它的那次授权」。
//
// 高危命令字典和运行时设置是同一类东西 —— 而且更直接:
//
//   · 把 DROP 从字典里删掉,生产上的 DROP 从此不再需要审批
//   · 把 approval.timeoutMinutes 调到 1,所有待审工单一分钟后自动处置
//   · 把 webhook 指向别处,审计事件从此推给另一个人
//
// 这三件事做完之后,链上原本一个字都没有。事后去查「为什么那天 DROP 没走审批」,
// 看到的是一条合规的执行记录 —— 而让它合规的那次改动,不在任何地方。

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// configAuditDetails 返回这次会话里所有管理类审计行的正文。
func (a *testApp) configAuditDetails(token string) []string {
	a.t.Helper()
	var rows []struct {
		Command string `json:"command"`
		Actor   string `json:"actor"`
	}
	if err := json.Unmarshal(a.auditItemsRaw(token, ""), &rows); err != nil {
		a.t.Fatalf("decode audit: %v", err)
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Command)
	}
	return out
}

func containsSub(all []string, want string) bool {
	for _, s := range all {
		if strings.Contains(s, want) {
			return true
		}
	}
	return false
}

func TestConfigAudit_RiskDictionaryChangesAreLogged(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")

	eq(t, app.do(http.MethodPost, "/api/v1/risk-commands", token, map[string]any{
		"command": "VACUUM", "tiers": map[string]string{"prod": "high"},
	}).Code, 0, "新增字典条目")
	eq(t, app.do(http.MethodPatch, "/api/v1/risk-commands/VACUUM", token, map[string]any{
		"tier": "prod", "level": "off",
	}).Code, 0, "改字典档位")
	eq(t, app.do(http.MethodDelete, "/api/v1/risk-commands/VACUUM", token, nil).Code, 0, "删字典条目")

	got := app.configAuditDetails(token)
	for _, want := range []string{"dict.upsert", "dict.level", "dict.delete"} {
		if !containsSub(got, want) {
			t.Errorf("字典改动 %q 没进审计链 —— 事后查「为什么那条 DROP 没走审批」会一无所获", want)
		}
	}
	// 把关键内容记进去:只记「有人动过字典」而不记动了什么,查的时候等于没记。
	if !containsSub(got, "VACUUM") {
		t.Error("审计里没有被改动的那个命令名")
	}
}

func TestConfigAudit_SettingsChangesAreLogged(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")

	eq(t, app.do(http.MethodPut, "/api/v1/settings", token, map[string]any{
		"approval.timeoutMinutes": 1,
	}).Code, 0, "改设置")

	got := app.configAuditDetails(token)
	if !containsSub(got, "settings") {
		t.Error("设置改动没进审计链 —— 把审批超时调到 1 分钟这种事,链上要看得见")
	}
	if !containsSub(got, "approval.timeoutMinutes") {
		t.Error("审计里没有被改动的那个键名")
	}
}

// 秘钥类设置只记**键名**,不记值 —— 审计链是给人看的,把秘钥写进去等于多了一处泄露点。
func TestConfigAudit_SettingsAuditNeverContainsTheSecretValue(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")

	const secret = "audit-should-not-see-this"
	eq(t, app.do(http.MethodPut, "/api/v1/settings", token, map[string]any{
		"approval.external.callbackSecret": secret,
	}).Code, 0, "改秘钥设置")

	got := app.configAuditDetails(token)
	if !containsSub(got, "approval.external.callbackSecret") {
		t.Error("秘钥类设置的**改动**本身要记 —— 谁什么时候换过它,是事后要查的")
	}
	if containsSub(got, secret) {
		t.Error("秘钥的值被写进了审计链 —— 那是多了一处泄露点")
	}
}

func TestConfigAudit_WebhookChangesAreLogged(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")

	eq(t, app.do(http.MethodPut, "/api/v1/settings/webhook", token, map[string]any{
		"endpoint": "https://events.example.com/ingest", "secret": "wh-secret-value",
		"events": "exec", "retryMax": 3, "enabled": true,
	}).Code, 0, "配 webhook")

	got := app.configAuditDetails(token)
	if !containsSub(got, "webhook") {
		t.Error("webhook 改动没进审计链 —— 审计事件从此推给谁,本身就该是一条审计")
	}
	if containsSub(got, "wh-secret-value") {
		t.Error("webhook 密钥被写进了审计链")
	}
}
