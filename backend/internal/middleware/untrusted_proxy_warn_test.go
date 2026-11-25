package middleware

import (
	"bytes"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// 2026-10-26 回归 17:登录白名单开着、本机反代没被信任、回环请求带转发头 → 打告警(限频)。

// runWarn 按生产路由同样的方式(SetTrustedProxies,nil = 一台都不信)起一个引擎,发一次请求。
func runWarn(t *testing.T, remote string, headers map[string]string, trusted []string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	if err := r.SetTrustedProxies(trusted); err != nil {
		t.Fatal(err)
	}
	r.POST("/api/v1/auth/login", func(c *gin.Context) {
		warnUntrustedLocalProxy(c, net.ParseIP(c.ClientIP()))
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
	req.RemoteAddr = remote
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	r.ServeHTTP(httptest.NewRecorder(), req)
}

func captureWarn(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	untrustedProxyWarnedAt.Store(0)
	t.Cleanup(func() { untrustedProxyWarnedAt.Store(0) })
	return &buf
}

func TestWarnUntrustedLocalProxy_LogsOnceThenRateLimits(t *testing.T) {
	buf := captureWarn(t)
	for i := 0; i < 3; i++ {
		runWarn(t, "127.0.0.1:5000", map[string]string{"X-Forwarded-For": "10.1.2.3"}, nil)
	}
	if n := strings.Count(buf.String(), "level=WARN"); n != 1 {
		t.Fatalf("应只告警一次(限频),got %d 条: %s", n, buf.String())
	}
	// 超过限频间隔后再打一次
	untrustedProxyWarnedAt.Store(untrustedProxyWarnedAt.Load() - int64(untrustedProxyWarnEvery) - 1)
	runWarn(t, "127.0.0.1:5000", map[string]string{"X-Real-IP": "10.1.2.3"}, nil)
	if n := strings.Count(buf.String(), "level=WARN"); n != 2 {
		t.Fatalf("超过 10 分钟后应再告警一次,got %d 条", n)
	}
}

func TestWarnUntrustedLocalProxy_SilentOtherwise(t *testing.T) {
	buf := captureWarn(t)
	cases := []struct {
		remote  string
		headers map[string]string
	}{
		// 运维本机直接 curl,不带转发头
		{"127.0.0.1:5000", nil},
		// 不是回环对端
		{"10.0.0.8:5000", map[string]string{"X-Forwarded-For": "1.2.3.4"}},
	}
	for _, cs := range cases {
		runWarn(t, cs.remote, cs.headers, nil)
	}
	if buf.Len() != 0 {
		t.Fatalf("这些情况不应告警: %s", buf.String())
	}
}

// 本机反代已被信任(配置了 trusted_proxies):ClientIP 换成了真实客户端,不告警。
func TestWarnUntrustedLocalProxy_SilentWhenProxyTrusted(t *testing.T) {
	buf := captureWarn(t)
	runWarn(t, "127.0.0.1:5000", map[string]string{"X-Forwarded-For": "10.1.2.3"}, []string{"127.0.0.1"})
	if buf.Len() != 0 {
		t.Fatalf("代理被信任时不应告警: %s", buf.String())
	}
}
