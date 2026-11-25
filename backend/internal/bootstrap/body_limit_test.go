package bootstrap

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// H3(2026-10 安全审计):请求体没有全局上限,未登录就能向 /auth/login 发超大 JSON;服务也没有
// 读请求头超时。改为按路由分档的请求体上限 + http.Server 带超时。

func (a *testApp) rawPost(path, token, contentType string, body []byte) apiResp {
	a.t.Helper()
	req, err := http.NewRequest(http.MethodPost, a.srv.URL+path, bytes.NewReader(body))
	if err != nil {
		a.t.Fatal(err)
	}
	req.Header.Set("Content-Type", contentType)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer res.Body.Close()
	var out apiResp
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		a.t.Fatalf("decode: %v", err)
	}
	return out
}

func TestBodyLimit_UnauthenticatedEndpointsAreSmall(t *testing.T) {
	app := newTestApp(t)
	big := []byte(`{"email":"linwei@aegisdb.io","password":"` + strings.Repeat("x", 2<<20) + `"}`)
	if r := app.rawPost("/api/v1/auth/login", "", "application/json", big); r.Code != 41300 {
		t.Fatalf("2MB 的登录请求应 41300,实际 %d %s", r.Code, r.Msg)
	}
	// 正常大小照常
	if tok := app.login("linwei@aegisdb.io", "aegisdb123"); tok == "" {
		t.Fatal("正常登录应成功")
	}
}

func TestBodyLimit_LoggedInRoutesKeepRoomForLargeSQL(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	dev := app.connIDByEnv(admin, "dev")
	// 3MB 的 SQL 文本(远小于 15MB 的 SQL 上限):不能被全局上限挡掉
	sql := "SELECT 1 /* " + strings.Repeat("y", 3<<20) + " */"
	body, _ := json.Marshal(map[string]any{"connectionId": dev, "sql": sql})
	if r := app.rawPost("/api/v1/terminal/exec", admin, "application/json", body); r.Code == 41300 {
		t.Fatalf("登录后的 3MB SQL 不该被全局上限挡掉")
	}
	// 超过登录后路由的上限(32MB)才拒
	huge := bytes.Repeat([]byte("z"), 33<<20)
	if r := app.rawPost("/api/v1/terminal/exec", admin, "application/json", huge); r.Code != 41300 {
		t.Fatalf("33MB 应 41300,实际 %d", r.Code)
	}
}

func TestHTTPServer_HasHeaderTimeout(t *testing.T) {
	srv := NewHTTPServer(":0", http.NewServeMux())
	if srv.ReadHeaderTimeout <= 0 || srv.ReadHeaderTimeout > time.Minute {
		t.Errorf("应设读请求头超时(防慢速攻击),实际 %v", srv.ReadHeaderTimeout)
	}
	if srv.IdleTimeout <= 0 {
		t.Errorf("应设空闲超时,实际 %v", srv.IdleTimeout)
	}
	if srv.MaxHeaderBytes <= 0 || srv.MaxHeaderBytes > 1<<20 {
		t.Errorf("请求头上限应 ≤1MB,实际 %d", srv.MaxHeaderBytes)
	}
	// 写超时不设:脚本执行、导出下载、终端 WebSocket 都要跑很久
	if srv.WriteTimeout != 0 {
		t.Errorf("不应设写超时(会掐断长请求),实际 %v", srv.WriteTimeout)
	}
}
