package handler

import (
	"testing"

	"aegisdb/internal/service"
)

// 网关挂在反向代理的子路径下时,Cookie 路径要带上浏览器看到的前缀,否则浏览器不会把它带回来。
func TestCookiePath_FollowsGatewayPrefix(t *testing.T) {
	for redirect, want := range map[string]string{
		"https://dbgw.example.com" + service.OIDCCallbackPath:       "/api/v1/auth/sso",
		"https://a.example.com/gw" + service.OIDCCallbackPath:       "/gw/api/v1/auth/sso",
		"https://a.example.com/x/y/" + service.OIDCCallbackPath[1:]: "/x/y/api/v1/auth/sso",
		"": "/api/v1/auth/sso",
	} {
		got := cookiePath(service.SSOSettings{RedirectURL: redirect}, ssoTicketPath)
		if got != want {
			t.Errorf("cookiePath(%q) = %q, want %q", redirect, got, want)
		}
	}
}
