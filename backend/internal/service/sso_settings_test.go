package service

import (
	"encoding/base64"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"
)

// SSO 设置在**保存那一刻**校验,而不是等第一个人登录时才发现配错了。
func TestValidateSSOSettings(t *testing.T) {
	ok := SSOSettings{Enabled: true, BaseURL: "https://srd.bssrvc66.com", CookieName: "CS-Token"}
	with := func(f func(*SSOSettings)) SSOSettings { c := ok; f(&c); return c }
	cases := []struct {
		name      string
		cfg       SSOSettings
		allowHTTP bool
		wantErr   string
	}{
		{"正常", ok, false, ""},
		{"部署环境不许 http:JWT 会明文过内网", with(func(c *SSOSettings) { c.BaseURL = "http://srd.bssrvc66.com" }), false, "https"},
		{"本地开发允许 http", with(func(c *SSOSettings) { c.BaseURL = "http://127.0.0.1:9000" }), true, ""},
		{"没填地址", with(func(c *SSOSettings) { c.BaseURL = "" }), false, "SRD 授权登录地址"},
		{"地址不是 URL", with(func(c *SSOSettings) { c.BaseURL = "srd.bssrvc66.com" }), false, "SRD 授权登录地址"},
		{"Cookie 名含非法字符", with(func(c *SSOSettings) { c.CookieName = "to ken;" }), false, "Cookie"},
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
	if err := ValidateSSOSettings(SSOSettings{BaseURL: "garbage"}, false); err != nil {
		t.Errorf("未启用 SSO 时不该校验,实际 %v", err)
	}
}

// SRD 的填充符是 *,不是 =。向量同样来自 SRD 自己的编码函数:m("abcd")、m("abcde")。
func TestSRDBase64Padding(t *testing.T) {
	for raw, want := range map[string]string{"y_SlVG**": "abcd", "y_SlVE9*": "abcde", "y_Sl": "abc"} {
		b, err := srdBase64.DecodeString(raw)
		if err != nil || string(b) != want {
			t.Errorf("%q 应解出 %q,实际 %q, %v", raw, want, b, err)
		}
	}
}

// CS-Token 的几种可能形态都要能解出 JWT;解不出来的要明确报"格式不对",不能当成"没登录"。
func TestDecodeSRDToken(t *testing.T) {
	const jwt = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ6aGFuZ3NhbiJ9.c2lnbmF0dXJl"
	payload := `{"t_time": 1710000000, "token": "` + jwt + `"}`
	std := base64.StdEncoding.EncodeToString([]byte(payload))
	// 同样的 JWT,在 JSON 里多放一个字段,挑一个编码后一定带 + 和 = 的。
	var stdPlus string
	for i := 0; ; i++ {
		stdPlus = base64.StdEncoding.EncodeToString([]byte(`{"t_time": 1710000000, "token": "` + jwt + `", "p": "` + strings.Repeat("~", i) + `"}`))
		if strings.Contains(stdPlus, "+") && strings.Contains(stdPlus, "=") {
			break
		}
	}
	cases := []struct {
		name, raw string
		wantAt    int64
	}{
		{"裸 JWT", jwt, 0},
		{"标准 base64 的 {t_time, token}", std, 1710000000},
		{"js-cookie 做过 URL 编码", url.QueryEscape(std), 1710000000},
		{"+ 被上游解成了空格", strings.ReplaceAll(stdPlus, "+", " "), 1710000000},
		{"%3D 与原样的 + 混在一起", strings.ReplaceAll(stdPlus, "=", "%3D"), 1710000000},
		{"URL 安全 base64 无填充", base64.RawURLEncoding.EncodeToString([]byte(payload)), 1710000000},
		{"base64 包着裸 JWT", base64.StdEncoding.EncodeToString([]byte(jwt)), 0},
		{"带引号", `"` + jwt + `"`, 0},
	}
	// SRD 的真实形态:下面两个值是拿 SRD 前端包(app.8db67ec2.js)里原样的编码函数跑出来的,
	// 不是用网关自己的字母表反推的 —— 字母表抄错时这里会失败。
	cases = append(cases,
		struct {
			name, raw string
			wantAt    int64
		}{"SRD 自定义 base64(无填充)", "2PScL4Xja_9Orl64HDG~HCG~HCGbMvX7N1oiMlgOVLZ.NESRy1Z5N9jSoLjSH9njUlYiVLZ.2zXLU_Z5N9g1y9fEfogADzWON9gn0zHPaEnOa9y~VkW.adS-", 1710000000},
		struct {
			name, raw string
			wantAt    int64
		}{"SRD 自定义 base64(多一个字段)", "2PScL4Xja_9Orl64HDG~HCG~HCGbMvX7N1oiMlgOVLZ.NESRy1Z5N9jSoLjSH9njUlYiVLZ.2zXLU_Z5N9g1y9fEfogADzWON9gn0zHPaEnOa9y~VkW.adMbMvwOrOSWyOS-", 1710000000},
	)
	for _, c := range cases {
		got, at, err := DecodeSRDToken(c.raw)
		if err != nil || got != jwt {
			t.Errorf("%s:应解出 JWT,实际 %q, %v", c.name, got, err)
			continue
		}
		if c.wantAt == 0 && !at.IsZero() || c.wantAt != 0 && !at.Equal(time.Unix(c.wantAt, 0)) {
			t.Errorf("%s:封装时间应为 %d,实际 %v", c.name, c.wantAt, at)
		}
	}

	if _, _, err := DecodeSRDToken("   "); !errors.Is(err, ErrSSONoSession) {
		t.Errorf("空 Cookie 是没有登录态,实际 %v", err)
	}
	for _, bad := range []string{
		"not-a-token",
		base64.StdEncoding.EncodeToString([]byte(`{"t_time":1,"token":"abc"}`)), // token 不像 JWT
		base64.StdEncoding.EncodeToString([]byte(`hello`)),
		"eyJ.a.b\x00",
	} {
		if _, _, err := DecodeSRDToken(bad); !errors.Is(err, ErrSSOTokenFormat) {
			t.Errorf("%q 应报格式错误,实际 %v", bad, err)
		}
	}
}
