package bootstrap

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"aegisdb/internal/model"
	"aegisdb/pkg/resp"
)

// 2026-11 白盒审计 V-16:控制台的 JWT 从 localStorage 挪进 HttpOnly Cookie,写操作校验 CSRF。

type webSession struct {
	session, csrf *http.Cookie
	marker, token string
}

func webLogin(t *testing.T, app *testApp, email, password string) webSession {
	t.Helper()
	b, _ := json.Marshal(map[string]string{"email": email, "password": password})
	req, _ := http.NewRequest(http.MethodPost, app.srv.URL+"/api/v1/auth/login", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-AegisDB-Client", "web")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out struct {
		Code int `json:"code"`
		Data struct {
			Token   string `json:"token"`
			Session string `json:"session"`
		} `json:"data"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil || out.Code != 0 {
		t.Fatalf("登录失败:%v %+v", err, out)
	}
	s := webSession{marker: out.Data.Session, token: out.Data.Token}
	for _, c := range res.Cookies() {
		switch c.Name {
		case "aegisdb_session":
			s.session = c
		case "aegisdb_csrf":
			s.csrf = c
		}
	}
	return s
}

func webDo(t *testing.T, app *testApp, s webSession, method, path, csrf string, body any) apiResp {
	t.Helper()
	var rd *bytes.Reader = bytes.NewReader(nil)
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, app.srv.URL+path, rd)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(s.session)
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out apiResp
	_ = json.NewDecoder(res.Body).Decode(&out)
	return out
}

func TestSessionCookie_WebLoginKeepsTokenOutOfScripts(t *testing.T) {
	app := newTestApp(t)
	s := webLogin(t, app, "linwei@aegisdb.io", "aegisdb123")
	if s.token != "" {
		t.Fatal("控制台登录的响应体里不能有令牌")
	}
	if s.session == nil || !s.session.HttpOnly || s.session.SameSite != http.SameSiteStrictMode || s.session.Path != "/" {
		t.Fatalf("会话 Cookie 属性不对:%+v", s.session)
	}
	if s.csrf == nil || s.csrf.HttpOnly || s.csrf.SameSite != http.SameSiteStrictMode {
		t.Fatalf("CSRF Cookie 要能被脚本读到:%+v", s.csrf)
	}
	if !strings.HasSuffix(s.marker, "."+s.csrf.Value) || strings.Contains(s.marker, s.session.Value) {
		t.Fatalf("标记应为 <uid>.<csrf>,不含令牌:%q", s.marker)
	}
	if s.session.Secure {
		t.Fatal("明文 HTTP 上不能带 Secure,否则浏览器存不下")
	}
}

func TestSessionCookie_SecureBehindTLSProxy(t *testing.T) {
	app := newTestApp(t)
	b, _ := json.Marshal(map[string]string{"email": "linwei@aegisdb.io", "password": "aegisdb123"})
	req, _ := http.NewRequest(http.MethodPost, app.srv.URL+"/api/v1/auth/login", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-AegisDB-Client", "web")
	req.Header.Set("X-Forwarded-Proto", "https")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	for _, c := range res.Cookies() {
		if !c.Secure {
			t.Errorf("%s 经 TLS 代理时应带 Secure", c.Name)
		}
	}
}

func TestSessionCookie_CSRFRequiredForWrites(t *testing.T) {
	app := newTestApp(t)
	s := webLogin(t, app, "linwei@aegisdb.io", "aegisdb123")

	eq(t, webDo(t, app, s, http.MethodGet, "/api/v1/auth/me", "", nil).Code, 0, "读不需要 CSRF")
	eq(t, webDo(t, app, s, http.MethodPost, "/api/v1/auth/password", "", map[string]string{}).Code, resp.CodeForbidden, "写操作没带 CSRF")
	eq(t, webDo(t, app, s, http.MethodPost, "/api/v1/auth/password", "wrong", map[string]string{}).Code, resp.CodeForbidden, "CSRF 不对")
	other := webLogin(t, app, "chenhao@aegisdb.io", "aegisdb123")
	eq(t, webDo(t, app, s, http.MethodPost, "/api/v1/auth/password", other.csrf.Value, map[string]string{}).Code, resp.CodeForbidden, "别的会话的 CSRF")
	if r := webDo(t, app, s, http.MethodPost, "/api/v1/auth/password", s.csrf.Value, map[string]string{}); r.Code == resp.CodeForbidden {
		t.Fatalf("带对了 CSRF 不该被这道闸挡:%+v", r)
	}
}

func TestSessionCookie_LogoutRevokesAndClears(t *testing.T) {
	app := newTestApp(t)
	s := webLogin(t, app, "linwei@aegisdb.io", "aegisdb123")
	req, _ := http.NewRequest(http.MethodPost, app.srv.URL+"/api/v1/auth/logout", nil)
	req.AddCookie(s.session)
	req.Header.Set("X-CSRF-Token", s.csrf.Value)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	cleared := map[string]bool{}
	for _, c := range res.Cookies() {
		cleared[c.Name] = c.MaxAge < 0
	}
	if !cleared["aegisdb_session"] || !cleared["aegisdb_csrf"] {
		t.Fatalf("登出要清掉两个 Cookie:%v", cleared)
	}
	eq(t, webDo(t, app, s, http.MethodGet, "/api/v1/auth/me", "", nil).Code, resp.CodeUnauthorized, "登出后旧 Cookie 失效")
}

// 原有行为:没声明 web 的客户端照旧从响应体拿令牌、走 Bearer,不校验 CSRF,也不下发 Cookie。
func TestSessionCookie_BearerClientsUnchanged(t *testing.T) {
	app := newTestApp(t)
	r := app.loginRaw("linwei@aegisdb.io", "aegisdb123")
	eq(t, r.Code, 0, "登录")
	var d struct {
		Token   string `json:"token"`
		Session string `json:"session"`
	}
	_ = json.Unmarshal(r.Data, &d)
	if d.Token == "" || d.Session != "" {
		t.Fatalf("脚本客户端应拿到令牌:%+v", d)
	}
	if rr := app.do(http.MethodPost, "/api/v1/auth/password", d.Token, map[string]string{}); rr.Code == resp.CodeForbidden {
		t.Fatalf("Bearer 不校验 CSRF:%+v", rr)
	}
	eq(t, app.do(http.MethodGet, "/api/v1/auth/me", "not-a-token", nil).Code, resp.CodeUnauthorized, "坏的 Bearer 仍是 401")
}

// 跨站 WebSocket 劫持:浏览器会自动带 Cookie,所以握手要证明读得到 CSRF Cookie。
func TestSessionCookie_WebSocketNeedsCSRF(t *testing.T) {
	app := newTestApp(t)
	s := webLogin(t, app, "linwei@aegisdb.io", "aegisdb123")
	url := "ws" + strings.TrimPrefix(app.srv.URL, "http") + "/api/v1/terminal/ws"
	dial := func(protos ...string) (*websocket.Conn, *http.Response, error) {
		d := websocket.Dialer{Subprotocols: protos}
		h := http.Header{}
		h.Add("Cookie", s.session.Name+"="+s.session.Value)
		return d.Dial(url, h)
	}
	if c, res, err := dial(); err == nil {
		c.Close()
		t.Fatal("只带 Cookie 不能开终端")
	} else if res == nil || res.StatusCode != http.StatusForbidden {
		t.Fatalf("应 403,实际 %+v %v", res, err)
	}
	if c, _, err := dial("aegisdb-csrf", "wrong"); err == nil {
		c.Close()
		t.Fatal("CSRF 不对不能开终端")
	}
	c, _, err := dial("aegisdb-csrf", s.csrf.Value)
	if err != nil {
		t.Fatalf("带对 CSRF 应能开终端:%v", err)
	}
	if c.Subprotocol() != "aegisdb-csrf" {
		t.Errorf("握手应选中 aegisdb-csrf,实际 %q", c.Subprotocol())
	}
	c.Close()

	// 原有行为:令牌放子协议的客户端不受影响。
	tok := app.login("linwei@aegisdb.io", "aegisdb123")
	c2, _, err := (&websocket.Dialer{Subprotocols: []string{"aegisdb-token", tok}}).Dial(url, nil)
	if err != nil {
		t.Fatalf("aegisdb-token 子协议照旧可用:%v", err)
	}
	c2.Close()
}

// 巡检报告页:带沙箱 CSP、能被同源嵌入;JSON 接口与 SPA 的 CSP 不受影响。
func TestSessionCookie_InspectionViewHeaders(t *testing.T) {
	app := newTestApp(t)
	tok := app.login("linwei@aegisdb.io", "aegisdb123")
	req, _ := http.NewRequest(http.MethodGet, app.srv.URL+"/api/v1/inspection-reports/999999/view", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("不存在的报告应 404,实际 %d", res.StatusCode)
	}
	if got := res.Header.Get("Content-Security-Policy"); !strings.HasPrefix(got, "sandbox") {
		t.Errorf("要有沙箱 CSP,实际 %q", got)
	}
	if got := res.Header.Get("X-Frame-Options"); got != "SAMEORIGIN" {
		t.Errorf("要能被同源 iframe 嵌入,实际 %q", got)
	}
}

// 真报告:页面原样返回,内联脚本由响应自己的沙箱 CSP 兜着;记审计。
func TestSessionCookie_InspectionViewServesReport(t *testing.T) {
	app, admin, client, _ := inspectionSetup(t)
	html := "<!doctype html><h1>巡检</h1><script>document.title='x'</script>"
	eq(t, app.openDo(http.MethodPost, inspectionPath, client, inspectionBody("order-cluster", time.Now().Format("2006-01-02"), "ok", html)).Code, 0, "上传")
	var report model.InspectionReport
	if err := app.repo.DB().First(&report).Error; err != nil {
		t.Fatal(err)
	}
	status, hdr, body := app.rawWithHeaders("/api/v1/inspection-reports/"+itoa(report.ID)+"/view", admin)
	eq(t, status, 200, "取页面")
	eq(t, body, html, "原样返回")
	if ct := hdr.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type 应是 text/html,实际 %q", ct)
	}
	if got := hdr.Get("Content-Security-Policy"); !strings.Contains(got, "sandbox allow-scripts") || strings.Contains(got, "allow-same-origin") {
		t.Errorf("沙箱 CSP 不对:%q", got)
	}
	eq(t, hdr.Get("X-Frame-Options"), "SAMEORIGIN", "同源可嵌入")
	if audit := string(app.auditItemsRaw(admin, "?range=24h&full=1")); !strings.Contains(audit, "inspection.view 查看巡检报告") {
		t.Errorf("查看报告应记审计: %s", audit)
	}
}
