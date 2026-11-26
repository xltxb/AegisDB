package service

import (
	"aegisdb/pkg/crypto"
	"reflect"
	"strings"
	"testing"
	"time"
)

// SSO 设置在**保存那一刻**校验,而不是等第一个人登录时才发现配错了。
func TestValidateSSOSettings(t *testing.T) {
	ok := SSOSettings{
		Enabled: true, Issuer: "https://idp.example.com", ClientID: "aegisdb",
		RedirectURL: "https://dbgw.example.com" + OIDCCallbackPath, EmailClaim: "email",
	}
	with := func(f func(*SSOSettings)) SSOSettings { c := ok; f(&c); return c }
	cases := []struct {
		name      string
		cfg       SSOSettings
		allowHTTP bool
		wantErr   string
	}{
		{"正常", ok, false, ""},
		{"网关挂在子路径下", with(func(c *SSOSettings) { c.RedirectURL = "https://a.example.com/gw" + OIDCCallbackPath }), false, ""},
		{"部署环境不许 http:授权码会明文过内网", with(func(c *SSOSettings) { c.Issuer = "http://idp.example.com" }), false, "https"},
		{"本地开发允许 http", with(func(c *SSOSettings) {
			c.Issuer, c.RedirectURL = "http://127.0.0.1:9000", "http://localhost:5173"+OIDCCallbackPath
		}), true, ""},
		{"没填 Issuer", with(func(c *SSOSettings) { c.Issuer = "" }), false, "Issuer"},
		{"Issuer 不是 URL", with(func(c *SSOSettings) { c.Issuer = "idp.example.com" }), false, "Issuer"},
		{"没填 Client ID", with(func(c *SSOSettings) { c.ClientID = " " }), false, "Client ID"},
		{"没填回调地址", with(func(c *SSOSettings) { c.RedirectURL = "" }), false, "回调地址"},
		{"回调地址不是网关的回调接口", with(func(c *SSOSettings) { c.RedirectURL = "https://dbgw.example.com/callback" }), false, OIDCCallbackPath},
		{"回调地址带查询串", with(func(c *SSOSettings) { c.RedirectURL = "https://dbgw.example.com" + OIDCCallbackPath + "?x=1" }), false, "回调地址"},
		{"邮箱字段含空白", with(func(c *SSOSettings) { c.EmailClaim = "e mail" }), false, "邮箱字段"},
	}
	for _, c := range cases {
		err := ValidateSSOSettings(c.cfg, c.allowHTTP)
		switch {
		case c.wantErr == "" && err != nil:
			t.Errorf("%s:不该报错,实际 %v", c.name, err)
		case c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)):
			t.Errorf("%s:应报含 %q 的错,实际 %v", c.name, c.wantErr, err)
		}
	}
	// 未启用时什么都不查:允许先把地址填了、再择时打开
	if err := ValidateSSOSettings(SSOSettings{Issuer: "garbage"}, false); err != nil {
		t.Errorf("未启用 SSO 时不该校验,实际 %v", err)
	}
}

func TestParseScopes_AlwaysCarriesOpenID(t *testing.T) {
	for raw, want := range map[string][]string{
		"openid email profile": {"openid", "email", "profile"},
		"email,profile":        {"openid", "email", "profile"},
		"":                     {"openid"},
		" email  email openid": {"openid", "email"},
	} {
		if got := parseScopes(raw); !reflect.DeepEqual(got, want) {
			t.Errorf("parseScopes(%q) = %v, want %v", raw, got, want)
		}
	}
}

func TestGatewayBaseURL(t *testing.T) {
	c := SSOSettings{RedirectURL: "https://a.example.com/gw" + OIDCCallbackPath}
	if got := c.GatewayBaseURL(); got != "https://a.example.com/gw" {
		t.Errorf("GatewayBaseURL = %q", got)
	}
}

// 邮箱是网关认人的唯一依据:标准 email 字段必须是身份服务验证过的。
func TestIdentityFrom(t *testing.T) {
	cases := []struct {
		name   string
		claims map[string]any
		claim  string
		email  string
		user   string
		ok     bool
	}{
		{"已验证", map[string]any{"email": "LinWei@Corp.io", "email_verified": true, "name": "Lin Wei"}, "email", "linwei@corp.io", "Lin Wei", true},
		{"字符串 true 也认", map[string]any{"email": "a@corp.io", "email_verified": "true"}, "email", "a@corp.io", "", true},
		{"未验证不认", map[string]any{"email": "a@corp.io", "email_verified": false}, "email", "", "", false},
		{"没说验证过不认", map[string]any{"email": "a@corp.io"}, "email", "", "", false},
		{"不是邮箱", map[string]any{"email": "alice", "email_verified": true}, "email", "", "", false},
		{"自定义字段由管理员担保", map[string]any{"upn": "b@corp.io", "preferred_username": "bob"}, "upn", "b@corp.io", "bob", true},
		{"自定义字段缺失", map[string]any{"email": "b@corp.io", "email_verified": true}, "upn", "", "", false},
	}
	for _, c := range cases {
		email, user, ok := identityFrom(c.claims, c.claim)
		if ok != c.ok || email != c.email || user != c.user {
			t.Errorf("%s:得到 (%q, %q, %v),期望 (%q, %q, %v)", c.name, email, user, ok, c.email, c.user, c.ok)
		}
	}
}

// 登录结果只认网关自己封的、没过期的、同一个 Issuer 的。
func TestOpenSSOTicket(t *testing.T) {
	crypto.SetSecretKey("sso-ticket-test-key")
	s := &Services{}
	cfg := SSOSettings{Issuer: "https://idp.example.com"}
	good, err := seal(sealSSOTicket, ssoTicket{Email: "a@corp.io", Name: "A", Issuer: cfg.Issuer, Exp: time.Now().Add(time.Minute).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	if id, err := s.openSSOTicket(good, cfg); err != nil || id.Email != "a@corp.io" || id.UserName != "A" {
		t.Fatalf("正常的登录结果应能解开:%+v %v", id, err)
	}
	expired, _ := seal(sealSSOTicket, ssoTicket{Email: "a@corp.io", Issuer: cfg.Issuer, Exp: time.Now().Add(-time.Second).Unix()})
	other, _ := seal(sealSSOTicket, ssoTicket{Email: "a@corp.io", Issuer: "https://evil.example.com", Exp: time.Now().Add(time.Minute).Unix()})
	// 同样的内容,用别的用途封的(流程 Cookie),或者用静态存储的 EncryptSecret 加密的:都打不开。
	asFlow, _ := seal(sealOIDCFlow, ssoTicket{Email: "a@corp.io", Issuer: cfg.Issuer, Exp: time.Now().Add(time.Minute).Unix()})
	atRest, _ := crypto.EncryptSecret(`{"m":"a@corp.io","i":"https://idp.example.com","e":9999999999}`)
	for name, raw := range map[string]string{
		"用途不同":      asFlow,
		"静态存储的密文":   atRest,
		"空":         "",
		"过期":        expired,
		"别的 Issuer": other,
		"不是网关封的":    "eyJlbWFpbCI6ImFAY29ycC5pbyJ9",
		"被改过":       good[:len(good)-4] + "AAAA",
	} {
		if _, err := s.openSSOTicket(raw, cfg); err != ErrSSONoSession {
			t.Errorf("%s:应当作没有登录态,实际 %v", name, err)
		}
	}
}
