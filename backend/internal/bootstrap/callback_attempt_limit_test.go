package bootstrap

import (
	"net/http"
	"testing"
)

// M2(2026-10 安全审计):审批回调能把语句送去执行,鉴权失败却不限次,密钥长短也不限。
// 同一来源 15 分钟内失败 10 次即锁定(锁定期间对的密钥也拒);新保存的回调密钥至少 16 个字符。

func TestLarkCallback_LocksSourceAfterRepeatedAuthFailures(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	const good = "s3cr3t-callback-key"
	app.setSettings(token, map[string]any{"approval.external.enabled": true, "approval.external.callbackSecret": good})
	body := map[string]any{"external_task_id": "AP-doesnotexist", "approved": true}

	// 锁定前:对的密钥能过鉴权(单号不存在,回的是"审批单不存在"而不是鉴权失败)
	env, _ := app.postLarkCallback(good, body)
	if env.Code == 40300 {
		t.Fatalf("锁定前对的密钥应通过鉴权,实际 %d %s", env.Code, env.Msg)
	}
	for i := 0; i < 10; i++ {
		app.postLarkCallback("wrong-guess", body)
	}
	env, _ = app.postLarkCallback(good, body)
	eq(t, env.Code, 40300, "连续失败后该来源被锁定,对的密钥也拒")
}

func TestSettings_CallbackSecretMinLength(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	r := app.do(http.MethodPut, "/api/v1/settings", token, map[string]any{"approval.external.callbackSecret": "short"})
	if r.Code == 0 {
		t.Fatal("短于 16 个字符的回调密钥不应保存成功")
	}
	eq(t, app.do(http.MethodPut, "/api/v1/settings", token, map[string]any{"approval.external.callbackSecret": "0123456789abcdef"}).Code, 0, "16 个字符可以保存")
	eq(t, app.do(http.MethodPut, "/api/v1/settings", token, map[string]any{"approval.external.callbackSecret": ""}).Code, 0, "空串 = 不改")
}
