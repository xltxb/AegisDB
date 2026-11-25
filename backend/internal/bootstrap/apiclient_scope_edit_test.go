package bootstrap

// 已有凭据改权限范围(feat/apiclient-scope-edit)。控制台加了"编辑权限范围",走既有的
// PUT /api-clients/:id 只带 {scopes}。钉住:改完立即生效、不动其它字段、审计记下改前改后、
// 空数组与认不出的范围整单拒绝且不改原值。

import (
	"net/http"
	"strings"
	"testing"
)

func TestAPIClientScopeEditTakesEffectAndAudits(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	_, id, tok := app.issueSA(admin, "scope-bot", "Scope-Edit", []string{"owner"}, []string{"release:read"}, 0)

	// 没有 release:approve 时,审批接口在范围这一关就被挡。
	r := app.openDo(http.MethodPost, "/api/v1/open/releases/REL-1/approval", tok, map[string]any{"approver": "chenhao@vela.io", "action": "approve"})
	eq(t, r.Code, 40300, "缺 release:approve")

	eq(t, app.do(http.MethodPut, "/api/v1/api-clients/"+itoa(id), admin,
		map[string]any{"scopes": []string{"release:read", "release:approve"}}).Code, 0, "加 release:approve")
	got := app.apiClientByName(admin, "Scope-Edit")
	eq(t, got.Scopes, "release:read,release:approve", "新范围落库")
	eq(t, got.Enabled, true, "改范围不动启停")

	// 立即生效:范围这一关过了,落到业务判断(单号不存在)。
	r = app.openDo(http.MethodPost, "/api/v1/open/releases/REL-999999/approval", tok, map[string]any{"approver": "chenhao@vela.io", "action": "approve"})
	eq(t, r.Code, 40001, "有了范围,拒绝理由变成单号不存在")

	rows := app.mustAudit("admin.apiclient.update", "改范围写审计")
	last := rows[len(rows)-1]
	if !strings.Contains(last, "scopes=release:read,release:approve") || !strings.Contains(last, "scopes_before=release:read") {
		t.Errorf("审计要记改前和改后的范围,got %q", last)
	}

	// 去掉一项同样立即生效。
	eq(t, app.do(http.MethodPut, "/api/v1/api-clients/"+itoa(id), admin,
		map[string]any{"scopes": []string{"release:read"}}).Code, 0, "去掉 release:approve")
	r = app.openDo(http.MethodPost, "/api/v1/open/releases/REL-1/approval", tok, map[string]any{"approver": "chenhao@vela.io", "action": "approve"})
	eq(t, r.Code, 40300, "去掉后立刻没有审批能力")
}

func TestAPIClientScopeEditRejectsBadInput(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	_, id, _ := app.issueSA(admin, "scope-bad-bot", "Scope-Bad", []string{"owner"}, []string{"release:read"}, 0)

	r := app.do(http.MethodPut, "/api/v1/api-clients/"+itoa(id), admin, map[string]any{"scopes": []string{}})
	eq(t, r.Code, 40001, "空数组拒绝")
	r = app.do(http.MethodPut, "/api/v1/api-clients/"+itoa(id), admin, map[string]any{"scopes": []string{"release:write"}})
	eq(t, r.Code, 40001, "认不出的范围拒绝")
	eq(t, app.apiClientByName(admin, "Scope-Bad").Scopes, "release:read", "拒绝后原值不变")

	dev := app.login("zhaolei@vela.io", "vela123")
	r = app.do(http.MethodPut, "/api/v1/api-clients/"+itoa(id), dev, map[string]any{"scopes": []string{"release:read", "release:approve"}})
	if r.Code == 0 {
		t.Fatal("非管理员不能改凭据范围")
	}
	eq(t, app.apiClientByName(admin, "Scope-Bad").Scopes, "release:read", "非管理员改不动")
}
