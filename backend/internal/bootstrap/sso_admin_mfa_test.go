package bootstrap

import (
	"testing"
	"time"

	"aegisdb/pkg/totp"
)

// M8(2026-10 安全审计):SSO 登录不经过网关 TOTP。平台管理员是 SSO 一旦失守损失最大的账号 ——
// 已绑定 TOTP 的管理员走 SSO 也要输码;普通用户与没绑定的管理员不受影响。
func TestSSO_AdminWithTOTPMustPresentCode(t *testing.T) {
	idp := newFakeIdP(t)
	app := newSSOApp(t, idp)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	secret := app.setupMFA(admin)

	idp.as(map[string]any{"email": "linwei@aegisdb.io"})
	b := newBrowser()
	app.ssoBrowse(b)
	eq(t, app.ssoPost(b, nil).Code, 42800, "已绑定 TOTP 的管理员走 SSO 也要输码")
	// 要码、错码都不消费登录结果:同一次登录接着输
	eq(t, app.ssoPost(b, map[string]any{"mfaCode": "000000"}).Code, 42800, "错码")
	ssoToken(t, app.ssoPost(b, map[string]any{"mfaCode": totp.Code(secret, time.Now())}))

	// 普通用户(没有 admin 角色)照旧只凭 SSO
	ssoToken(t, app.ssoLogin(idp, map[string]any{"email": "chenhao@aegisdb.io"}))
}
