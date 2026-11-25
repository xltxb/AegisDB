package bootstrap

// 系统名称与 LOGO 可配置(.scratch/branding)的黑盒回归:
//   - 没配置时:公开接口回空、接口文档 / 契约 / TOTP issuer 保持原字样,一个字节不变;
//   - 管理员改了之后:未登录的 /auth/branding 立刻拿到新值,后端拼出的名称跟着换;
//   - 非管理员改不了;改动进审计,LOGO 只记大小与摘要,不记 base64;
//   - 非法名称 / 图片整次拒绝,一项不存。

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
)

// 1x1 透明 PNG。
var brandTestPNG = func() string {
	raw, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==")
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(raw)
}()

type brandResp struct {
	Name   string `json:"name"`
	NameEn string `json:"nameEn"`
	Logo   string `json:"logo"`
}

func (a *testApp) branding() brandResp {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/auth/branding", "", nil) // 不带登录态
	if r.Code != 0 {
		a.t.Fatalf("GET /auth/branding: code=%d msg=%s", r.Code, r.Msg)
	}
	var b brandResp
	if err := json.Unmarshal(r.Data, &b); err != nil {
		a.t.Fatalf("decode branding: %v", err)
	}
	return b
}

func (a *testApp) rawGet(path string) string {
	a.t.Helper()
	res, err := http.Get(a.srv.URL + path)
	if err != nil {
		a.t.Fatalf("GET %s: %v", path, err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		a.t.Fatalf("GET %s: status %d", path, res.StatusCode)
	}
	return string(b)
}

// inBackendRoot 把工作目录切到 backend/:/docs、/openapi.yaml 按进程工作目录读 docs/openapi.yaml
// (部署时 WorkingDirectory 就是安装目录)。本文件的测试都不并行,切目录不会影响别的测试。
func inBackendRoot(t *testing.T) {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir("../.."); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })
}

// otpIssuer 给 chenhao 绑一次 OTP,返回 otpauth URI 里的 issuer。
func (a *testApp) otpIssuer(admin string) string {
	a.t.Helper()
	target := a.userByEmail(admin, "chenhao@vela.io")
	r := a.do(http.MethodPost, "/api/v1/users/"+itoa(target.ID)+"/mfa/bind", admin, nil)
	if r.Code != 0 {
		a.t.Fatalf("mfa bind: %d %s", r.Code, r.Msg)
	}
	var b struct {
		OtpauthURI string `json:"otpauthUri"`
	}
	_ = json.Unmarshal(r.Data, &b)
	u, err := url.Parse(b.OtpauthURI)
	if err != nil {
		a.t.Fatalf("parse otpauth %q: %v", b.OtpauthURI, err)
	}
	return u.Query().Get("issuer")
}

func TestBranding_DefaultsUnchanged(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	inBackendRoot(t)

	eq(t, app.branding(), brandResp{}, "未配置时公开接口回空")
	if !strings.Contains(app.rawGet("/docs"), "<title>DP DB GATEWAY · 接口文档</title>") {
		t.Error("未配置时接口文档标题应保持 DP DB GATEWAY")
	}
	want, err := os.ReadFile("docs/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	eq(t, app.rawGet("/openapi.yaml") == string(want), true, "未配置时 /openapi.yaml 与文件逐字节一致")
	eq(t, app.otpIssuer(admin), "DP DB GATEWAY", "未配置时 TOTP issuer 不变")
}

func TestBranding_AdminChangesTakeEffectEverywhere(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	inBackendRoot(t)

	r := app.do(http.MethodPut, "/api/v1/settings", admin, map[string]any{
		"brand.name": "  星河数据网关 ", "brand.nameEn": "Galaxy DB", "brand.logo": brandTestPNG,
	})
	eq(t, r.Code, 0, "管理员保存品牌:"+r.Msg)
	eq(t, app.branding(), brandResp{Name: "星河数据网关", NameEn: "Galaxy DB", Logo: brandTestPNG}, "未登录也能读到新值(名称去首尾空白)")

	// 设置页读回来也是新值
	gr := app.do(http.MethodGet, "/api/v1/settings", admin, nil)
	var s struct {
		Settings map[string]string `json:"settings"`
	}
	_ = json.Unmarshal(gr.Data, &s)
	eq(t, s.Settings["brand.name"], `"星河数据网关"`, "GET /settings 带 brand.name")

	if !strings.Contains(app.rawGet("/docs"), "<title>星河数据网关 · 接口文档</title>") {
		t.Error("接口文档标题应换成配置的名称")
	}
	spec := app.rawGet("/openapi.yaml")
	if !strings.Contains(spec, `title: "星河数据网关 API"`) || strings.Contains(spec, "title: DP DB GATEWAY API") {
		t.Errorf("/openapi.yaml 的 info.title 应换成配置的名称:%.200s", spec)
	}
	eq(t, app.otpIssuer(admin), "Galaxy DB", "TOTP issuer 优先用英文名称")

	// 审计:名称记值;LOGO 只记大小与摘要
	rows := strings.Join(app.auditCommandsLike("admin.settings.update"), "\n")
	if !strings.Contains(rows, `brand.name="星河数据网关"`) || !strings.Contains(rows, `brand.nameEn="Galaxy DB"`) {
		t.Errorf("名称改动应进审计:%s", rows)
	}
	if !strings.Contains(rows, "brand.logo=已更新(image/png") || !strings.Contains(rows, "sha256:") {
		t.Errorf("LOGO 改动应以大小 + 摘要进审计:%s", rows)
	}
	if strings.Contains(rows, "base64") || strings.Contains(rows, "iVBOR") {
		t.Errorf("LOGO 的 base64 不该进审计:%s", rows)
	}

	// 清掉 → 回到默认,审计记「已清除」
	eq(t, app.do(http.MethodPut, "/api/v1/settings", admin, map[string]any{"brand.name": "", "brand.nameEn": "", "brand.logo": ""}).Code, 0, "清除品牌")
	eq(t, app.branding(), brandResp{}, "清除后公开接口回空")
	if !strings.Contains(strings.Join(app.auditCommandsLike("brand.logo=已清除"), ""), "brand.logo=已清除") {
		t.Error("清除 LOGO 应进审计")
	}
	if !strings.Contains(app.rawGet("/docs"), "<title>DP DB GATEWAY · 接口文档</title>") {
		t.Error("清除后接口文档标题回到默认")
	}
}

func TestBranding_OnlyAdminCanChange(t *testing.T) {
	app := newTestApp(t)
	for _, who := range []string{"chenhao@vela.io", "hujun@vela.io", "zhaolei@vela.io"} {
		tok := app.login(who, "vela123")
		r := app.do(http.MethodPut, "/api/v1/settings", tok, map[string]any{"brand.name": "偷改"})
		if r.Code == 0 {
			t.Errorf("%s 不该能改系统名称", who)
		}
	}
	eq(t, app.branding().Name, "", "非管理员改动没有落库")
}

func TestBranding_InvalidValuesRejected(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	svg := func(s string) string {
		return "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte(s))
	}
	big := "data:image/png;base64," + base64.StdEncoding.EncodeToString(append([]byte{0x89, 'P', 'N', 'G'}, make([]byte, 300<<10)...))

	cases := map[string]map[string]any{
		"名称含换行":         {"brand.name": "a\nb"},
		"名称含尖括号":        {"brand.name": "<b>x</b>"},
		"名称过长":          {"brand.nameEn": strings.Repeat("x", 41)},
		"名称不是字符串":       {"brand.name": 12},
		"LOGO 是外链":      {"brand.logo": "https://example.com/logo.png"},
		"LOGO 类型不支持":    {"brand.logo": "data:text/html;base64,PGgxPg=="},
		"LOGO 头不符":      {"brand.logo": "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("GIF89a....."))},
		"LOGO 超大":       {"brand.logo": big},
		"LOGO base64 坏": {"brand.logo": "data:image/png;base64,@@@"},
		"SVG 带脚本":       {"brand.logo": svg(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)},
		"SVG 带事件":       {"brand.logo": svg(`<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"></svg>`)},
	}
	for what, body := range cases {
		// 同一包里再带一项合法的:非法项要连累整包一起被拒
		if _, has := body["brand.name"]; !has {
			body["brand.name"] = "合法名称"
		}
		r := app.do(http.MethodPut, "/api/v1/settings", admin, body)
		if r.Code == 0 {
			t.Errorf("%s 应被拒绝", what)
		}
	}
	eq(t, app.branding(), brandResp{}, "被拒的提交一项都不存")

	// 合法的 SVG 收
	ok := svg(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><rect width="24" height="24" rx="6" fill="#3b6ef6"/></svg>`)
	eq(t, app.do(http.MethodPut, "/api/v1/settings", admin, map[string]any{"brand.logo": ok}).Code, 0, "合法 SVG")
	eq(t, app.branding().Logo, ok, "SVG 存下了")
}
