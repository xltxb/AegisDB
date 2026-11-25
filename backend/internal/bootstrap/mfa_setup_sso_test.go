package bootstrap

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	jwtlib "github.com/golang-jwt/jwt/v5"

	"velagateway/pkg/jwt"
)

// 2026-10-26 回归 03:只经 SSO 登录的账号往往不知道管理员开号时设的本地口令,绑 MFA 时被
// 要求出示它 → 绑不上。决定:本次会话是 SSO 登录且登录不超过 15 分钟时免出示口令;超时
// 要求重新 SSO 登录(42801 + data.ssoReauth);本地口令登录的会话照旧要口令(L-AUTH4)。

// sessionToken 按测试密钥签一个指定登录方式、指定登录时刻的会话 token(与 JWTAuth 同一套 claims)。
func (a *testApp) sessionToken(email, authMethod string, loginAt time.Time) string {
	a.t.Helper()
	u, err := a.repo.GetUserByEmail(email)
	if err != nil {
		a.t.Fatalf("user %s: %v", email, err)
	}
	claims := jwt.Claims{
		UserID: u.ID, RoleID: u.RoleID, Name: u.Name, TokenVersion: u.TokenVersion, AuthMethod: authMethod,
		RegisteredClaims: jwtlib.RegisteredClaims{
			ID:        "test-" + authMethod + "-" + itoa(loginAt.Unix()),
			IssuedAt:  jwtlib.NewNumericDate(loginAt),
			ExpiresAt: jwtlib.NewNumericDate(time.Now().Add(time.Hour)),
			Issuer:    "vela-gateway",
		},
	}
	tok, err := jwtlib.NewWithClaims(jwtlib.SigningMethodHS256, claims).SignedString([]byte("test-secret"))
	if err != nil {
		a.t.Fatalf("sign: %v", err)
	}
	return tok
}

func TestMFASetup_FreshSSOSessionSkipsPassword(t *testing.T) {
	app := newTestApp(t)
	tok := app.sessionToken("chenhao@vela.io", jwt.AuthMethodSSO, time.Now().Add(-2*time.Minute))
	r := app.do(http.MethodPost, "/api/v1/auth/mfa/setup", tok, nil)
	eq(t, r.Code, 0, "刚经 SSO 登录的会话绑 MFA 不要求本地口令")
	var out struct {
		Secret string `json:"secret"`
	}
	_ = json.Unmarshal(r.Data, &out)
	if out.Secret == "" {
		t.Error("应返回待启用的密钥")
	}
}

func TestMFASetup_StaleSSOSessionAsksReauth(t *testing.T) {
	app := newTestApp(t)
	tok := app.sessionToken("chenhao@vela.io", jwt.AuthMethodSSO, time.Now().Add(-20*time.Minute))
	r := app.do(http.MethodPost, "/api/v1/auth/mfa/setup", tok, nil)
	eq(t, r.Code, 42801, "SSO 登录超过 15 分钟:不带口令应被拒")
	var data struct {
		SSOReauth bool `json:"ssoReauth"`
	}
	_ = json.Unmarshal(r.Data, &data)
	if !data.SSOReauth {
		t.Errorf("应带 ssoReauth 提示重新 SSO 登录,got data=%s msg=%s", string(r.Data), r.Msg)
	}
	// 知道口令的人仍可出示口令绑定。
	eq(t, app.do(http.MethodPost, "/api/v1/auth/mfa/setup", tok, map[string]any{"password": "wrong-pass"}).Code, 42801, "口令错误照旧拒绝")
	eq(t, app.do(http.MethodPost, "/api/v1/auth/mfa/setup", tok, map[string]any{"password": "vela123"}).Code, 0, "带正确口令照旧放行")
}

// 原行为保留:本地口令登录的会话、以及没有登录方式的老 token,即使刚登录也要口令。
func TestMFASetup_NonSSOSessionStillNeedsPassword(t *testing.T) {
	app := newTestApp(t)
	legacy := app.sessionToken("chenhao@vela.io", "", time.Now())
	r := app.do(http.MethodPost, "/api/v1/auth/mfa/setup", legacy, nil)
	eq(t, r.Code, 42801, "老 token(无登录方式)不算 SSO 会话")
	if string(r.Data) != "" && string(r.Data) != "null" {
		var data struct {
			SSOReauth bool `json:"ssoReauth"`
		}
		_ = json.Unmarshal(r.Data, &data)
		if data.SSOReauth {
			t.Error("非 SSO 会话不应提示重新 SSO 登录")
		}
	}
	local := app.login("chenhao@vela.io", "vela123")
	eq(t, app.do(http.MethodPost, "/api/v1/auth/mfa/setup", local, nil).Code, 42801, "本地口令登录的会话照旧要口令")
}

// 端到端:真走一遍 SSO 登录(POST /auth/sso),签出来的会话绑 MFA 不要求本地口令。
func TestMFASetup_RealSSOLoginSkipsPassword(t *testing.T) {
	srdJWT := jwtFor("chenhao")
	fake := &fakeSRD{identity: map[string]map[string]any{
		srdJWT: {"id": 7, "username": "chenhao", "email": "chenhao@vela.io", "department": "IT", "internal_department": "DBA"},
	}}
	app := newSSOApp(t, fake)
	tok := ssoToken(t, app.ssoLogin(srdJWT))
	eq(t, app.do(http.MethodPost, "/api/v1/auth/mfa/setup", tok, nil).Code, 0, "刚经 SSO 登录:免出示本地口令")
}
