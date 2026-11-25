package bootstrap

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"velagateway/internal/middleware"
)

// 管理后台独立空闲超时(工单 09,后端那一半)。

func jtiOf(t *testing.T, token string) string {
	t.Helper()
	parts := strings.Split(token, ".")
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		ID string `json:"jti"`
	}
	_ = json.Unmarshal(raw, &c)
	if c.ID == "" {
		t.Fatal("token 里没有 jti")
	}
	return c.ID
}

func TestAdminIdle_ExpiresAndOnlyEnterRevives(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	app.setSettings(admin, map[string]any{"security.adminIdleMinutes": 5})
	sid := jtiOf(t, admin)

	eq(t, app.do(http.MethodGet, "/api/v1/roles", admin, nil).Code, 0, "第一次调管理接口:开始计时")
	// 管理接口本身不算活跃:定时刷新的面板不能让超时失效 —— 拨到 6 分钟前
	middleware.AgeAdminSessionForTest(sid, 6*time.Minute)
	eq(t, app.do(http.MethodGet, "/api/v1/roles", admin, nil).Code, 40304, "空闲超过 5 分钟,管理接口应返回 40304")
	eq(t, app.do(http.MethodPost, "/api/v1/admin/heartbeat", admin, nil).Code, 40304, "超时之后心跳救不回来")
	eq(t, app.do(http.MethodGet, "/api/v1/connections", admin, nil).Code, 0, "用户后台的接口不受影响")
	eq(t, app.do(http.MethodGet, "/api/v1/auth/me", admin, nil).Code, 0, "没有被登出")

	eq(t, app.do(http.MethodPost, "/api/v1/admin/enter", admin, nil).Code, 0, "人主动重新进入")
	eq(t, app.do(http.MethodGet, "/api/v1/roles", admin, nil).Code, 0, "进入后恢复")

	// 心跳续期:拨到 4 分钟前(未超时),心跳后再拨 4 分钟仍在活跃期
	middleware.AgeAdminSessionForTest(sid, 4*time.Minute)
	eq(t, app.do(http.MethodPost, "/api/v1/admin/heartbeat", admin, nil).Code, 0, "未超时的心跳续期")
	middleware.AgeAdminSessionForTest(sid, 4*time.Minute)
	eq(t, app.do(http.MethodGet, "/api/v1/roles", admin, nil).Code, 0, "心跳之后重新计时")
}

func TestAdminIdle_EnterNeedsAdminPortalAccess(t *testing.T) {
	app := newTestApp(t)
	l2 := app.login("chenhao@vela.io", "vela123")
	auditor := app.login("hujun@vela.io", "vela123")
	eq(t, app.do(http.MethodPost, "/api/v1/admin/enter", l2, nil).Code, 40300, "非管理员不能进入管理后台")
	eq(t, app.do(http.MethodPost, "/api/v1/admin/enter", auditor, nil).Code, 0, "审计员可以进入(只看审计)")
	r := app.do(http.MethodGet, "/api/v1/auth/session-policy", l2, nil)
	if !strings.Contains(string(r.Data), `"adminIdleMinutes":5`) {
		t.Errorf("会话策略应带出管理后台空闲分钟数(默认 5): %s", r.Data)
	}
}

// 全量审计也受管理后台空闲超时约束(2026-10 审查):管理后台的审计页(full=1)超时就拒;
// 用户后台工作台上取的最近审计(不带 full)不受空闲影响(2026-10-26 回归 16);mine=1 不受影响。
func TestAudit_FullListFollowsAdminIdle(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	l2 := app.login("chenhao@vela.io", "vela123")
	conn := app.connIDByName(admin, "order-cluster")
	app.do(http.MethodPost, "/api/v1/terminal/exec", admin, map[string]any{"connectionId": conn, "sql": "SELECT 1 FROM t"})
	app.do(http.MethodPost, "/api/v1/terminal/exec", l2, map[string]any{"connectionId": conn, "sql": "SELECT 2 FROM t"})

	full := app.do(http.MethodGet, "/api/v1/audit?full=1&range=24h", admin, nil)
	eq(t, full.Code, 0, "活跃期内取全量: "+full.Msg)
	if !strings.Contains(string(full.Data), "Chen Hao") {
		t.Fatalf("活跃期内管理员应看到全部: %s", full.Data)
	}

	middleware.AgeAdminSessionForTest(jtiOf(t, admin), 6*time.Minute)
	eq(t, app.do(http.MethodGet, "/api/v1/audit?full=1&range=24h", admin, nil).Code, 40304, "管理后台空闲超时后,全量审计应拒绝")
	eq(t, app.do(http.MethodGet, "/api/v1/audit/instances?full=1", admin, nil).Code, 40304, "实例下拉同理")

	// 2026-10-26 回归 16 改了这一条的期望:原先断言"超时后不带 full 降级成只看自己的",
	// 现在总览 / 工作台的审计卡片不受管理后台空闲影响,始终按这个人的审计可见范围(管理员看全员)。
	dash := app.do(http.MethodGet, "/api/v1/audit?range=24h", admin, nil)
	eq(t, dash.Code, 0, "不带 full(工作台)不报错: "+dash.Msg)
	if !strings.Contains(string(dash.Data), "Chen Hao") || !strings.Contains(string(dash.Data), "Lin Wei") {
		t.Errorf("管理后台空闲超时后,不带 full 仍应按管理员的可见范围看全员: %s", dash.Data)
	}
	eq(t, app.do(http.MethodGet, "/api/v1/audit/instances", admin, nil).Code, 0, "实例下拉不带 full 同样不受空闲影响")
	eq(t, app.do(http.MethodGet, "/api/v1/audit?mine=1&range=24h", admin, nil).Code, 0, "我的操作记录不受影响")
	eq(t, app.do(http.MethodGet, "/api/v1/audit?range=24h", l2, nil).Code, 0, "普通用户本来就只看自己的,不受影响")

	eq(t, app.do(http.MethodPost, "/api/v1/admin/enter", admin, nil).Code, 0, "重新进入")
	eq(t, app.do(http.MethodGet, "/api/v1/audit?full=1&range=24h", admin, nil).Code, 0, "进入后恢复")
}

// 后端判空闲留 30 秒余量:前端心跳节流,后端看到的最后一次操作会落后一点。
func TestAdminIdle_GraceCoversHeartbeatThrottle(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	app.setSettings(admin, map[string]any{"security.adminIdleMinutes": 5})
	eq(t, app.do(http.MethodPost, "/api/v1/admin/enter", admin, nil).Code, 0, "进入")
	middleware.AgeAdminSessionForTest(jtiOf(t, admin), 5*time.Minute+15*time.Second)
	eq(t, app.do(http.MethodGet, "/api/v1/roles", admin, nil).Code, 0, "刚过 5 分钟、在余量内:不算超时")
	middleware.AgeAdminSessionForTest(jtiOf(t, admin), 30*time.Second) // 累计 5 分 45 秒
	eq(t, app.do(http.MethodGet, "/api/v1/roles", admin, nil).Code, 40304, "超过余量:超时")
}
