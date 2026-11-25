package bootstrap

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// settingRaw 读 tbl_setting 里存的原值(JSON 编码),没有这一行时返回 ""。
func (a *testApp) settingRaw(key string) string {
	a.t.Helper()
	v, err := a.repo.GetSetting(key)
	if err != nil {
		return ""
	}
	return v
}

// frontendSettingsPayload 是设置页「保存设置」实际发出的那一整包(SettingsView.vue 的 save()),
// 取的是页面初次打开时的默认值。键名、类型照抄:数值是数,开关是布尔,路径可以为空。
func frontendSettingsPayload() map[string]any {
	return map[string]any{
		"gateway.defaultPolicy":              "strict",
		"approval.onTimeout":                 "auto-escalate",
		"approval.escalate":                  true,
		"approval.allowSelfApprove":          false,
		"gateway.execTimeout":                30,
		"terminal.resultPageSize":            1000,
		"gateway.approvalExecTimeout":        3600,
		"osc.minRows":                        1000000,
		"osc.minBytes":                       1 << 30,
		"osc.maxThreadsRunning":              50,
		"osc.maxReplicaLagSec":               10,
		"osc.chunkSize":                      1000,
		"script.savePath":                    "",
		"inspection.savePath":                "",
		"export.savePath":                    "",
		"export.maxRows":                     5000000,
		"export.maxBytes":                    2000000000,
		"export.execTimeout":                 1800,
		"export.retentionDays":               3,
		"meta.sync.enabled":                  false,
		"meta.sync.intervalHours":            24,
		"bizQuery.defaultMaxRows":            1000,
		"bizQuery.maxRowsCap":                10000,
		"bizQuery.defaultTimeoutSec":         30,
		"bizQuery.timeoutCapSec":             300,
		"bizQuery.maxGrantDays":              90,
		"bizQuery.maxConcurrentPerUser":      2,
		"bizQuery.maxConcurrentPerSource":    5,
		"bizQuery.maxDailyPerUser":           0,
		"bizQuery.exportEnabled":             false,
		"bizQuery.applyUrl":                  "https://srd.bssrvc66.com/service-center/query/db-gateway-grant",
		"meta.sync.concurrency":              2,
		"conn.check.enabled":                 false,
		"conn.check.intervalMinutes":         10,
		"conn.check.concurrency":             4,
		"security.sessionTTL":                "8h",
		"security.requireMFA":                true,
		"security.mfaMandatory":              false,
		"security.mfaGraceMinutes":           30,
		"security.idleLock":                  true,
		"security.idleMinutes":               15,
		"security.adminIdleMinutes":          5,
		"security.ipAllowlist":               "",
		"security.ipAllowEnabled":            false,
		"sso.enabled":                        false,
		"sso.baseUrl":                        "",
		"sso.cookieName":                     "CS-Token",
		"notify.lark":                        true,
		"notify.email":                       false,
		"notify.push":                        true,
		"notify.larkWebhook":                 "",
		"notify.larkSecret":                  "",
		"notify.consoleURL":                  "",
		"approval.external.enabled":          false,
		"approval.external.releaseEnabled":   false,
		"approval.external.baseURL":          "",
		"approval.external.token":            "",
		"approval.external.aiGroup":          "",
		"approval.external.callbackBaseURL":  "",
		"approval.external.callbackSecret":   "",
		"approval.external.callbackAllowIPs": "",
	}
}

// 设置页实际发出的合法请求一个都不能被新校验误伤(工单 02 第 7 条的回归网)。
func TestSettings_FrontendPayloadIsAccepted(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")

	r := app.do(http.MethodPut, "/api/v1/settings", admin, frontendSettingsPayload())
	eq(t, r.Code, 0, "默认值整包保存:"+r.Msg)

	// 填满了的样子:路径、地址、白名单都有值。
	p := frontendSettingsPayload()
	p["script.savePath"] = t.TempDir()
	p["inspection.savePath"] = "/var/aegisdb/inspection"
	p["export.savePath"] = t.TempDir()
	p["notify.larkWebhook"] = "https://open.feishu.cn/open-apis/bot/v2/hook/abc"
	p["notify.larkSecret"] = "lark-secret"
	p["notify.consoleURL"] = "https://gw.corp.io"
	p["security.ipAllowlist"] = "10.20.0.0/16, 203.0.113.5"
	p["security.ipAllowEnabled"] = true
	p["approval.external.baseURL"] = "https://approval.corp.io"
	p["approval.external.callbackBaseURL"] = "https://gw.corp.io"
	p["approval.external.callbackAllowIPs"] = "10.1.2.3,10.9.0.0/16"
	p["export.retentionDays"] = 0
	p["security.mfaGraceMinutes"] = 0
	p["approval.onTimeout"] = "keep-waiting"
	p["security.sessionTTL"] = "24h"
	p["gateway.defaultPolicy"] = "approve-1"
	r = app.do(http.MethodPut, "/api/v1/settings", admin, p)
	eq(t, r.Code, 0, "填满值的整包保存:"+r.Msg)

	// 布尔存成布尔、数存成数(读的一侧按类型解码,存成字符串就读不出来)。
	eq(t, app.settingRaw("security.ipAllowEnabled"), "true", "布尔存布尔")
	eq(t, app.settingRaw("gateway.execTimeout"), "30", "数存数")
	eq(t, app.settingRaw("gateway.defaultPolicy"), `"approve-1"`, "枚举存字符串")
}

// 逐键校验:非法值整次拒绝、不落库;未知键拒绝(工单 02 第 7 条)。
func TestSettings_InvalidValuesAreRefusedAndNothingIsStored(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	// 先存一个合法值:与存量值相同的键不再校验(见 LegacyInvalidStoredValue),下面每个非法值都得是真改
	_ = app.repo.SetSetting("gateway.execTimeout", "30")

	before := app.settingRaw("gateway.execTimeout")
	cases := []struct {
		what string
		body map[string]any
	}{
		{"未知键", map[string]any{"no.such.key": 1}},
		{"export.retentionDays 负数", map[string]any{"export.retentionDays": -1}},
		{"export.retentionDays 字符串", map[string]any{"export.retentionDays": "abc"}},
		{"gateway.defaultPolicy 非法枚举", map[string]any{"gateway.defaultPolicy": "yolo"}},
		{"gateway.execTimeout 0", map[string]any{"gateway.execTimeout": 0}},
		{"gateway.execTimeout 过大", map[string]any{"gateway.execTimeout": 999999}},
		{"gateway.execTimeout 小数", map[string]any{"gateway.execTimeout": 1.5}},
		{"approval.timeoutMinutes 负数", map[string]any{"approval.timeoutMinutes": -5}},
		{"approval.onTimeout 非法", map[string]any{"approval.onTimeout": "explode"}},
		{"security.mfaGraceMinutes 负数", map[string]any{"security.mfaGraceMinutes": -1}},
		{"security.sessionTTL 非法", map[string]any{"security.sessionTTL": "99y"}},
		{"conn.check.concurrency 0", map[string]any{"conn.check.concurrency": 0}},
		{"conn.check.concurrency 过大", map[string]any{"conn.check.concurrency": 10000}},
		{"meta.sync.intervalHours 0", map[string]any{"meta.sync.intervalHours": 0}},
		{"notify.larkWebhook 非 http", map[string]any{"notify.larkWebhook": "ftp://x.example.com/hook"}},
		{"notify.larkWebhook javascript", map[string]any{"notify.larkWebhook": "javascript:alert(1)"}},
		{"inspection.savePath 相对路径", map[string]any{"inspection.savePath": "reports/inspection"}},
		{"inspection.savePath 含 ..", map[string]any{"inspection.savePath": "/var/aegisdb/../etc"}},
		{"script.savePath 含 ..", map[string]any{"script.savePath": "/data/../../etc"}},
		{"sso.baseUrl 非 http", map[string]any{"sso.baseUrl": "gopher://srd.example.com"}},
		{"布尔键给字符串", map[string]any{"security.idleLock": "yes"}},
		{"白名单写错", map[string]any{"security.ipAllowlist": "10.0.0.0/99"}},
		{"非 secret 键给 null", map[string]any{"gateway.execTimeout": nil}},
	}
	for _, c := range cases {
		body := map[string]any{"gateway.execTimeout": 77} // 同一请求里的合法键,不该被存下
		for k, v := range c.body {
			body[k] = v
		}
		r := app.do(http.MethodPut, "/api/v1/settings", admin, body)
		if r.Code != 40001 {
			t.Errorf("%s:应当 40001,实际 code=%d msg=%s", c.what, r.Code, r.Msg)
		}
		if got := app.settingRaw("gateway.execTimeout"); got != before {
			t.Errorf("%s:整次应当不落库,gateway.execTimeout 却变成了 %s", c.what, got)
		}
	}
}

// 已设置的密钥类设置可以清除:显式 null 清除,"" 仍表示不改(工单 02 第 8 条)。
func TestSettings_SecretCanBeClearedWithNull(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")

	secretSet := func(key string) bool {
		t.Helper()
		r := app.do(http.MethodGet, "/api/v1/settings", admin, nil)
		var v struct {
			SecretsSet map[string]bool `json:"secretsSet"`
		}
		_ = json.Unmarshal(r.Data, &v)
		return v.SecretsSet[key]
	}

	for _, key := range []string{"notify.larkSecret", "approval.external.token", "approval.external.callbackSecret"} {
		eq(t, app.do(http.MethodPut, "/api/v1/settings", admin, map[string]any{key: "s3cr3t-0123456789"}).Code, 0, key+" 设置")
		eq(t, secretSet(key), true, key+" 已配置")

		eq(t, app.do(http.MethodPut, "/api/v1/settings", admin, map[string]any{key: ""}).Code, 0, key+" 传空串")
		eq(t, secretSet(key), true, key+" 空串表示不改")

		eq(t, app.do(http.MethodPut, "/api/v1/settings", admin, map[string]any{key: nil}).Code, 0, key+" 传 null")
		eq(t, secretSet(key), false, key+" null 表示清除")
	}

	// 清除也要进审计,只记键名。
	rows := app.auditCommandsLike("admin.settings.update")
	found := false
	for _, r := range rows {
		if strings.Contains(r, "notify.larkSecret") && strings.Contains(r, "已清除") {
			found = true
		}
		if strings.Contains(r, "s3cr3t") {
			t.Errorf("secret 明文进了审计:%s", r)
		}
	}
	if !found {
		t.Error("清除密钥应当在审计里留一句「已清除」")
	}
}

// 2026-10-26 回归:设置页整包发回几十个键,库里只要有一个存量值不合现在的规则,改任何一项都存不下。
// 与存量值一字不差的键不再校验;真改成非法值照旧拒。
func TestSettings_LegacyInvalidStoredValueDoesNotBlockOtherSaves(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	// 规则收紧前存下的值:相对路径、没写 http 前缀的地址
	_ = app.repo.SetSetting("inspection.savePath", `"reports/inspection"`)
	_ = app.repo.SetSetting("notify.consoleURL", `"gw.corp.io"`)

	p := frontendSettingsPayload()
	p["inspection.savePath"] = "reports/inspection"
	p["notify.consoleURL"] = "gw.corp.io"
	p["gateway.execTimeout"] = 45
	r := app.do(http.MethodPut, "/api/v1/settings", admin, p)
	eq(t, r.Code, 0, "改别的键不该被存量值挡住:"+r.Msg)
	eq(t, app.settingRaw("gateway.execTimeout"), "45", "改的那一项存下了")
	eq(t, app.settingRaw("inspection.savePath"), `"reports/inspection"`, "存量值原样留着")

	// 把它改成另一个非法值:照旧拒
	p["inspection.savePath"] = "other/relative"
	if r := app.do(http.MethodPut, "/api/v1/settings", admin, p); r.Code != 40001 {
		t.Errorf("改成新的非法值仍应 40001,实际 %d %s", r.Code, r.Msg)
	}
}
