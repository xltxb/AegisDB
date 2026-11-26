package bootstrap

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// 黑盒 2026-10 工单 05 第 1 条:自助修改密码 POST /auth/password。
//
// 契约:旧密码 + 新密码(与建号 / 重置同一条规则:至少 8 位);成功后该用户的其它会话
// 全部作废(会话代次 +1),当前会话由接口**重新签发**一枚新 token 返回,前端换上即可继续用;
// 没有本地口令的账号(业务查询账号、口令为空的 SSO 账号)明确拒绝;成功与失败都写审计。
func TestSelfPassword_ChangeRotatesSessions(t *testing.T) {
	app := newTestApp(t)
	tokA := app.login("chenhao@aegisdb.io", "aegisdb123")
	tokB := app.login("chenhao@aegisdb.io", "aegisdb123") // 另一台设备上的会话

	r := app.do(http.MethodPost, "/api/v1/auth/password", tokA, map[string]any{"oldPassword": "aegisdb123", "newPassword": "newpass99"})
	eq(t, r.Code, 0, "change password: "+r.Msg)
	var out struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expiresAt"`
	}
	_ = json.Unmarshal(r.Data, &out)
	if out.Token == "" || out.ExpiresAt == "" {
		t.Fatalf("expected a re-issued token, got %s", string(r.Data))
	}

	// 新 token 可用;旧的两枚(包括发起修改的那一枚)都作废
	eq(t, app.do(http.MethodGet, "/api/v1/auth/me", out.Token, nil).Code, 0, "re-issued token works")
	eq(t, app.do(http.MethodGet, "/api/v1/auth/me", tokB, nil).Code, 40100, "other session revoked")
	eq(t, app.do(http.MethodGet, "/api/v1/auth/me", tokA, nil).Code, 40100, "old token of this session revoked")

	// 新口令能登录,旧口令不能
	eq(t, app.loginCode("chenhao@aegisdb.io", "newpass99"), 0, "login with new password")
	if app.loginCode("chenhao@aegisdb.io", "aegisdb123") == 0 {
		t.Error("old password must stop working")
	}

	// 审计里有这一条,且口令本身不进审计
	raw := string(app.auditItemsRaw(app.login("linwei@aegisdb.io", "aegisdb123"), "?pageSize=200"))
	if !strings.Contains(raw, "password.change") {
		t.Error("self password change must be audited")
	}
	if strings.Contains(raw, "newpass99") {
		t.Error("the password itself must never reach the audit log")
	}
}

func TestSelfPassword_Refusals(t *testing.T) {
	app := newTestApp(t)
	tok := app.login("chenhao@aegisdb.io", "aegisdb123")
	post := func(body map[string]any) apiResp {
		return app.do(http.MethodPost, "/api/v1/auth/password", tok, body)
	}

	// 旧密码错误
	if r := post(map[string]any{"oldPassword": "wrong-old", "newPassword": "newpass99"}); r.Code == 0 || !strings.Contains(r.Msg, "原密码") {
		t.Errorf("wrong old password must be refused with a clear message, got %d %q", r.Code, r.Msg)
	}
	// 新密码太短(与建号 / 重置一致:至少 8 位)
	if r := post(map[string]any{"oldPassword": "aegisdb123", "newPassword": "short7x"}); r.Code == 0 || !strings.Contains(r.Msg, "8") {
		t.Errorf("short new password must be refused, got %d %q", r.Code, r.Msg)
	}
	// 新旧相同
	if r := post(map[string]any{"oldPassword": "aegisdb123", "newPassword": "aegisdb123"}); r.Code == 0 {
		t.Error("new password equal to the old one must be refused")
	}
	// 常见弱口令:说清理由,而不是 50000「修改失败」(2026-10-26 回归)
	if r := post(map[string]any{"oldPassword": "aegisdb123", "newPassword": "Password123"}); r.Code != 40001 || !strings.Contains(r.Msg, "常见") {
		t.Errorf("common password must be refused as 40001 with a reason, got %d %q", r.Code, r.Msg)
	}
	// 缺字段
	if r := post(map[string]any{"oldPassword": "aegisdb123"}); r.Code == 0 {
		t.Error("missing newPassword must be refused")
	}
	// 被拒的尝试不改口令、不踢会话
	eq(t, app.do(http.MethodGet, "/api/v1/auth/me", tok, nil).Code, 0, "session survives refused attempts")
	eq(t, app.loginCode("chenhao@aegisdb.io", "aegisdb123"), 0, "password unchanged after refusals")

	// 未登录不可用
	eq(t, app.do(http.MethodPost, "/api/v1/auth/password", "", map[string]any{"oldPassword": "aegisdb123", "newPassword": "newpass99"}).Code, 40100, "unauthenticated")
}

// 没有本地口令的账号(口令为空:SSO 开通 / 业务查询)不能"修改"一个不存在的口令。
func TestSelfPassword_NoLocalPassword(t *testing.T) {
	app := newTestApp(t)
	tok := app.login("chenhao@aegisdb.io", "aegisdb123")
	u, err := app.repo.GetUserByEmail("chenhao@aegisdb.io")
	if err != nil {
		t.Fatal(err)
	}
	if err := app.repo.UpdateUserPassword(u.ID, ""); err != nil {
		t.Fatal(err)
	}
	r := app.do(http.MethodPost, "/api/v1/auth/password", tok, map[string]any{"oldPassword": "", "newPassword": "newpass99"})
	if r.Code == 0 || !strings.Contains(r.Msg, "本地口令") {
		t.Errorf("account without a local password must be refused clearly, got %d %q", r.Code, r.Msg)
	}
}

// 工单 05 第 2 条:登录后的 API 响应不得被浏览器 / 代理缓存。
func TestAPI_NoStoreCacheHeaders(t *testing.T) {
	app := newTestApp(t)
	tok := app.login("linwei@aegisdb.io", "aegisdb123")
	for _, p := range []string{"/api/v1/auth/me", "/api/v1/users", "/api/v1/connections"} {
		req, _ := http.NewRequest(http.MethodGet, app.srv.URL+p, nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if cc := res.Header.Get("Cache-Control"); !strings.Contains(cc, "no-store") {
			t.Errorf("%s: Cache-Control = %q, want no-store", p, cc)
		}
		if pg := res.Header.Get("Pragma"); pg != "no-cache" {
			t.Errorf("%s: Pragma = %q, want no-cache", p, pg)
		}
	}
	// 未登录被拒的响应同样不缓存(带着错误信封的 200 也不该被代理留住)
	res, err := http.Get(app.srv.URL + "/api/v1/auth/me")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if cc := res.Header.Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("unauthenticated /auth/me: Cache-Control = %q, want no-store", cc)
	}
}

// #145:自助改密的两条审计(成功、原密码错误被拒)来源是 auth,按「登录认证」筛得到。
func TestSelfPassword_AuditSourceIsAuth(t *testing.T) {
	app := newTestApp(t)
	tok := app.login("chenhao@aegisdb.io", "aegisdb123")
	if r := app.do(http.MethodPost, "/api/v1/auth/password", tok, map[string]any{"oldPassword": "wrong-old", "newPassword": "newpass99"}); r.Code == 0 {
		t.Fatal("wrong old password must be refused")
	}
	eq(t, app.do(http.MethodPost, "/api/v1/auth/password", tok, map[string]any{"oldPassword": "aegisdb123", "newPassword": "newpass99"}).Code, 0, "change password")

	raw := string(app.auditItemsRaw(app.login("linwei@aegisdb.io", "aegisdb123"), "?pageSize=200&source=auth"))
	for _, want := range []string{"password.change.rejected", "password.change 自助修改登录口令"} {
		if !strings.Contains(raw, want) {
			t.Errorf("source=auth 应筛得到 %q", want)
		}
	}
}
