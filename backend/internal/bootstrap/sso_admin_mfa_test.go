package bootstrap

import (
	"net/http"
	"testing"
	"time"

	"velagateway/pkg/totp"
)

// M8(2026-10 安全审计):SSO 登录不经过网关 TOTP。平台管理员是 SSO 一旦失守损失最大的账号 ——
// 已绑定 TOTP 的管理员走 SSO 也要输码;普通用户与没绑定的管理员不受影响。
func TestSSO_AdminWithTOTPMustPresentCode(t *testing.T) {
	jwt := jwtFor("linwei")
	fake := &fakeSRD{identity: map[string]map[string]any{
		jwt: {"id": 1, "username": "linwei", "email": "linwei@vela.io", "department": "IT"},
	}}
	app := newSSOApp(t, fake)
	admin := app.login("linwei@vela.io", "vela123")
	secret := app.setupMFA(admin)

	r := app.ssoLogin(jwt)
	eq(t, r.Code, 42800, "已绑定 TOTP 的管理员走 SSO 也要输码")

	sso := func(code string) apiResp {
		return app.withSSOCookie(http.MethodPost, "/api/v1/auth/sso", csToken(jwt, time.Now()), "", map[string]any{"mfaCode": code})
	}
	eq(t, sso("000000").Code, 42800, "错码")
	ssoToken(t, sso(totp.Code(secret, time.Now())))

	// 普通用户(没有 admin 角色)照旧只凭 SSO
	jwt2 := jwtFor("chenhao")
	fake.identity[jwt2] = map[string]any{"id": 2, "username": "chenhao", "email": "chenhao@vela.io", "department": "IT"}
	ssoToken(t, app.ssoLogin(jwt2))
}
