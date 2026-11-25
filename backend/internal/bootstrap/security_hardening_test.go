package bootstrap

import (
	"net/http"
	"testing"
)

// issue 34: baseline security headers are present on every response, including
// unauthenticated ones (the header middleware runs before the guards).
func TestSecurityHeaders_PresentOnEveryResponse(t *testing.T) {
	app := newTestApp(t)
	// /healthz is unauthenticated — headers must still be there.
	res, err := http.Get(app.srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("get /healthz: %v", err)
	}
	defer res.Body.Close()
	want := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
	}
	for h, v := range want {
		if got := res.Header.Get(h); got != v {
			t.Errorf("header %s = %q, want %q", h, got, v)
		}
	}
	// No TLS in the test harness, so HSTS must NOT be sent (it would pin clients
	// to a scheme this listener doesn't serve).
	if got := res.Header.Get("Strict-Transport-Security"); got != "" {
		t.Errorf("HSTS should be absent over plain HTTP, got %q", got)
	}
}

// issue 35: interface docs are served only on a genuine local-dev run. A real
// deployment (here: an operator who asked for "staging", which runs on the dev
// profile) must not expose the full route map unauthenticated.
func TestDocs_HiddenOnNonLocalDeployment(t *testing.T) {
	// Probe the embedded Swagger asset, not /docs itself: /docs reads the spec
	// file relative to the process CWD (the package dir in tests) and 404s when
	// it is absent, which would mask whether the ROUTE is registered. The asset
	// endpoint serves from the embedded FS, so a 200 means "route present" and a
	// 404 means "route not registered" — exactly the gate under test.
	const asset = "/docs/swagger-ui.css"

	// Default app is local dev (EnvRaw empty) — the docs routes are registered.
	dev := newTestApp(t)
	if code := getStatus(t, dev.srv.URL+asset); code != http.StatusOK {
		t.Errorf("local dev %s = %d, want 200 (docs are a dev aid)", asset, code)
	}

	// A staging deployment: EnvRaw set, Env normalizes to dev, docs routes gone.
	staging := newTestApp(t, func(c *Config) { c.EnvRaw = "staging" })
	for _, p := range []string{"/docs", asset, "/openapi.yaml"} {
		if code := getStatus(t, staging.srv.URL+p); code != http.StatusNotFound {
			t.Errorf("staging %s = %d, want 404 (docs must not be exposed on a deployment)", p, code)
		}
	}
}

func getStatus(t *testing.T, url string) int {
	t.Helper()
	res, err := http.Get(url)
	if err != nil {
		t.Fatalf("get %s: %v", url, err)
	}
	res.Body.Close()
	return res.StatusCode
}

// L-DEF1(2026-10 安全审计):CSP 只加不会把页面弄坏的部分 —— /api 是 JSON,什么都不许加载;
// 页面禁嵌入、改 base、插件对象、站外表单,脚本只认本站文件(2026-11 审计 V-16),不碰 style-src。
func TestSecurityHeaders_CSP(t *testing.T) {
	app := newTestApp(t)
	for path, want := range map[string]string{
		"/api/v1/auth/sso/config": "default-src 'none'; frame-ancestors 'none'",
		"/healthz":                "frame-ancestors 'none'; base-uri 'self'; object-src 'none'; form-action 'self'; script-src 'self'",
	} {
		res, err := http.Get(app.srv.URL + path)
		if err != nil {
			t.Fatalf("get %s: %v", path, err)
		}
		res.Body.Close()
		if got := res.Header.Get("Content-Security-Policy"); got != want {
			t.Errorf("%s: CSP = %q, want %q", path, got, want)
		}
	}
}
