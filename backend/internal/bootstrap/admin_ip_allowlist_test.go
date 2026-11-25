package bootstrap

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"aegisdb/internal/middleware"
)

// 管理后台 IP 白名单(工单 08):只作用于管理接口;开关没开、列表为空、本机回环、部署总闸拉下时放行。

func adminIPCtx(remote string) *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	c.Request.RemoteAddr = remote
	return c
}

func TestAdminIPAllowed_Rules(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	set := func(enabled bool, list string) {
		app.setSettings(admin, map[string]any{"security.adminIpAllowEnabled": enabled, "security.adminIpAllowlist": list})
	}
	allowed := func(remote string) bool { return middleware.AdminIPAllowed(app.repo, adminIPCtx(remote)) }

	set(false, "10.9.9.9")
	if !allowed("10.1.2.3:5555") {
		t.Error("开关没开:不限制")
	}
	set(true, "10.9.9.9, 192.168.10.0/24")
	if allowed("10.1.2.3:5555") {
		t.Error("不在白名单里的 IP 应被拒")
	}
	if !allowed("10.9.9.9:5555") || !allowed("192.168.10.77:5555") {
		t.Error("白名单里的单个 IP 与网段都应放行")
	}
	if !allowed("127.0.0.1:5555") {
		t.Error("本机回环始终放行")
	}
	set(true, "   ")
	if !allowed("10.1.2.3:5555") {
		t.Error("开着但列表为空:不限制(防止把所有管理员锁在外面)")
	}
	set(true, "10.9.9.9")
	middleware.AdminIPAllowlistForcedOff = true
	defer func() { middleware.AdminIPAllowlistForcedOff = false }()
	if !allowed("10.1.2.3:5555") {
		t.Error("部署总闸拉下:白名单不生效")
	}
}

// 回环只在"本机直接访问"时放行(2026-10 审查):同机反代没配 trusted_proxies 时,带转发头的请求
// ClientIP 都是 127.0.0.1,不能因此全部放行。
func TestAdminIPAllowed_ProxiedLoopbackIsNotLocal(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	app.setSettings(admin, map[string]any{"security.adminIpAllowEnabled": true, "security.adminIpAllowlist": "10.9.9.9"})
	untrusted := func(hdr, val string) *gin.Context {
		c, e := gin.CreateTestContext(httptest.NewRecorder())
		_ = e.SetTrustedProxies(nil) // 没配 trusted_proxies
		c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
		c.Request.RemoteAddr = "127.0.0.1:5555"
		if hdr != "" {
			c.Request.Header.Set(hdr, val)
		}
		return c
	}
	if !middleware.AdminIPAllowed(app.repo, untrusted("", "")) {
		t.Error("本机直接访问(不带转发头)应放行")
	}
	if middleware.AdminIPAllowed(app.repo, untrusted("X-Forwarded-For", "10.1.2.3")) {
		t.Error("代理转来的请求(带 X-Forwarded-For)不该因来源是回环就放行")
	}
	if middleware.AdminIPAllowed(app.repo, untrusted("X-Real-IP", "10.1.2.3")) {
		t.Error("带 X-Real-IP 同理")
	}
	// 配了 trusted_proxies:ClientIP 就是真实客户端,按列表判
	trusted := adminIPCtx("127.0.0.1:5555")
	trusted.Request.Header.Set("X-Forwarded-For", "10.9.9.9")
	if !middleware.AdminIPAllowed(app.repo, trusted) {
		t.Error("可信代理转来的白名单内 IP 应放行")
	}
}

// 走完整的 HTTP 链路:本机访问永远进得来;设置接口带出总闸状态。
func TestAdminIPAllowlist_LoopbackNeverLockedOut(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	app.setSettings(admin, map[string]any{"security.adminIpAllowEnabled": true, "security.adminIpAllowlist": "10.9.9.9"})
	eq(t, app.do(http.MethodGet, "/api/v1/roles", admin, nil).Code, 0, "本机访问管理接口不受白名单影响")
	r := app.do(http.MethodGet, "/api/v1/settings", admin, nil)
	if !strings.Contains(string(r.Data), `"adminIpAllowlistForcedOff":false`) {
		t.Errorf("设置接口应带出总闸状态: %s", r.Data)
	}
}
