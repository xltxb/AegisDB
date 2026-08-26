package bootstrap

// 外部升级单的发布流程是网关侧策略,不是调用方的选择。
//
// 流程模板决定一张单要过哪些门(审查/审批/备份/校验),让外部系统挑流程,
// 等于让被管的人挑安检通道。规则:凭据可以绑定一条流水线(管理员配置),
// 绑了就走它;没绑走目标分层的默认流程;请求里出现 pipeline/pipelineId
// 一律拒绝 —— 静默忽略会让调用方以为自己指定成功了。

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// issueClientPiped mints a credential bound to a pipeline template.
func (a *testApp) issueClientPiped(adminToken, name, email string, scopes []string, pipelineID int64) string {
	a.t.Helper()
	uid := a.userIDByEmail(adminToken, email)
	r := a.do(http.MethodPost, "/api/v1/api-clients", adminToken, map[string]any{
		"name": name, "userId": uid, "scopes": scopes, "pipelineId": pipelineID,
	})
	if r.Code != 0 {
		a.t.Fatalf("create piped api client: code=%d msg=%s", r.Code, r.Msg)
	}
	var out struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(r.Data, &out)
	return out.Token
}

func TestOpenAPIRefusesCallerChosenPipeline(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	token := app.issueClient(admin, "想挑流程的平台", "zhangwei@vela.io", nil)

	for _, body := range []map[string]any{
		{"title": "挑流程1", "instance": "sandbox-dev", "sql": "SELECT 1;", "pipeline": "标准发布流程"},
		{"title": "挑流程2", "instance": "sandbox-dev", "sql": "SELECT 1;", "pipelineId": 1},
	} {
		r := app.openDo(http.MethodPost, "/api/v1/open/releases", token, body)
		if r.Code == 0 {
			t.Errorf("a caller-chosen pipeline must be refused, body=%v", body)
			continue
		}
		if !strings.Contains(r.Msg, "网关") && !strings.Contains(r.Msg, "配置") {
			t.Errorf("the refusal should say the flow is gateway-configured, got %q", r.Msg)
		}
	}
}

func TestOpenAPIUsesCredentialBoundPipeline(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	// 管理员侧配置:这把凭据的单一律走"外部直通"流程。
	pid := app.createPipeline(admin, "外部直通", "", []map[string]any{
		{"name": "规范审查", "type": "review", "config": `{"failOn":"none"}`},
		{"name": "执行变更", "type": "execute"},
	})
	token := app.issueClientPiped(admin, "绑定流程平台", "zhangwei@vela.io", nil, pid)

	r := app.openDo(http.MethodPost, "/api/v1/open/releases", token, map[string]any{
		"title": "走绑定流程", "instance": "sandbox-dev", "sql": "SELECT 1;", "externalRef": "PP-1",
	})
	eq(t, r.Code, 0, "create via bound pipeline")
	var created openRelease
	_ = json.Unmarshal(r.Data, &created)
	eq(t, created.Pipeline, "外部直通", "the ticket runs the credential's pipeline")

	// 0024 执行闸:控制台侧确认后绑定流程才执行完。
	app.confirmOpenReleaseAndWait(admin, created.RelNo)
	final := app.openRelease(token, created.RelNo)
	eq(t, final.Status, "success", "bound flow completes")
}

func TestOpenAPIFallsBackToTierDefaultPipeline(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	// 凭据没绑流程 → 走目标分层的默认流程(种子里的全局默认「标准发布流程」,
	// 含审批 —— 外部单在没人配置直通流程前,默认要过人)。
	token := app.issueClient(admin, "未绑定平台", "zhangwei@vela.io", nil)

	r := app.openDo(http.MethodPost, "/api/v1/open/releases", token, map[string]any{
		"title": "走默认流程", "instance": "sandbox-dev", "sql": "SELECT 1;", "externalRef": "PP-2",
	})
	eq(t, r.Code, 0, "create via tier default")
	var created openRelease
	_ = json.Unmarshal(r.Data, &created)
	eq(t, created.Pipeline, "标准发布流程", "falls back to the seeded default flow")
}
