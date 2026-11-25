package bootstrap

// 公司 SSO(SRD Portal 授权登录)接入:只做认证,不做授权。
//
// 这一组用一个 httptest 假扮 SRD 的 GET /api/v1/user/oauth/userinfo,钉住网关这一侧的约定:
// 登录态从父域 Cookie CS-Token 取、解出其中的 JWT、以 Bearer 调 userinfo、按邮箱映射、网关里
// 没开通的邮箱拒绝、停用与服务账号进不来、SRD 出事 / JWT 被拒 / Cookie 解不开分开报;以及存量账号的
// **绑定**:第一次经 SRD 登录、邮箱对上时当场绑定,绑定的人只能走 SSO(管理员除外)。

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"velagateway/internal/model"
	"velagateway/internal/service"
	"velagateway/pkg/resp"
)

// fakeSRD 按 JWT 回用户信息。它记下收到的 Authorization,好断言用的是 Bearer。
type fakeSRD struct {
	mu       sync.Mutex
	gotAuth  []string
	identity map[string]map[string]any // JWT → userinfo 的 data
	down     bool
}

func (f *fakeSRD) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		f.mu.Lock()
		f.gotAuth = append(f.gotAuth, auth)
		down := f.down
		f.mu.Unlock()
		if down {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		if r.URL.Path != "/api/v1/user/oauth/userinfo" || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		data, ok := f.identity[strings.TrimPrefix(auth, "Bearer ")]
		if !ok || !strings.HasPrefix(auth, "Bearer ") {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 401, "message": "token 无效"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": data, "message": "success"})
	})
}

func (f *fakeSRD) lastAuth() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gotAuth[len(f.gotAuth)-1]
}

// jwtFor 造一个 JWT 形状的 token(网关不验签,由 SRD 的 userinfo 判真假)。
func jwtFor(who string) string {
	return "eyJhbGciOiJIUzI1NiJ9." + base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"`+who+`"}`)) + ".sig"
}

// srdCookieEncoding 是 SRD 写 CS-Token 用的"自定义 base64"(字母表换过、填充符 *)。
// 故意在测试里单独抄一份而不引用网关的实现:两边任何一边抄错,黑盒都会失败。
var srdCookieEncoding = base64.NewEncoding("GedC6kERMS.0H3r5tXUD9o_LyVNaIf2JwWOlYZzvgjs8bpi7~TPAch14QnqKx-Fu").WithPadding('*')

// csToken 按 SRD 线上的形态封装 CS-Token:自定义 base64 的 {"t_time": 秒, "token": jwt}。
func csToken(jwt string, at time.Time) string {
	b, _ := json.Marshal(map[string]any{"t_time": at.Unix(), "token": jwt})
	return srdCookieEncoding.EncodeToString(b)
}

// newSSOApp 起一个启用了 SSO 的网关,SRD 指向 fake(设置写进设置表,与界面保存同一个位置)。
func newSSOApp(t *testing.T, fake *fakeSRD) *testApp {
	t.Helper()
	srd := httptest.NewServer(fake.handler())
	t.Cleanup(srd.Close)
	app := newTestApp(t)
	if err := app.repo.SetSetting("sso.enabled", "true"); err != nil {
		t.Fatal(err)
	}
	if err := app.repo.SetSetting("sso.baseUrl", `"`+srd.URL+`"`); err != nil {
		t.Fatal(err)
	}
	return app
}

// withSSOCookie 发一个带 CS-Token Cookie 的请求(可带网关 JWT)。cookie 为空就不带。
func (a *testApp) withSSOCookie(method, path, cookie, jwtTok string, body any) apiResp {
	a.t.Helper()
	b, _ := json.Marshal(body)
	if body == nil {
		b = []byte("{}")
	}
	req, _ := http.NewRequest(method, a.srv.URL+path, strings.NewReader(string(b)))
	req.Header.Set("Content-Type", "application/json")
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: "CS-Token", Value: cookie})
	}
	if jwtTok != "" {
		req.Header.Set("Authorization", "Bearer "+jwtTok)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		a.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	var r apiResp
	_ = json.NewDecoder(res.Body).Decode(&r)
	return r
}

// ssoLogin 用一个刚签的 CS-Token 登录。
func (a *testApp) ssoLogin(jwt string) apiResp {
	if jwt == "" {
		return a.withSSOCookie(http.MethodPost, "/api/v1/auth/sso", "", "", nil)
	}
	return a.withSSOCookie(http.MethodPost, "/api/v1/auth/sso", csToken(jwt, time.Now()), "", nil)
}

func ssoToken(t *testing.T, r apiResp) string {
	t.Helper()
	var d struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(r.Data, &d)
	if d.Token == "" {
		t.Fatalf("没拿到网关 token: code=%d msg=%s", r.Code, r.Msg)
	}
	return d.Token
}

func auditCount(app *testApp, where string, args ...any) int64 {
	var n int64
	app.repo.DB().Model(&model.AuditLog{}).Where(where, args...).Count(&n)
	return n
}

// ----------------------------------------------------------------- 登录

// 已有账号:按邮箱(不分大小写)映射,拿到的是**这个人原来的**账号和权限;以 Bearer 调 userinfo。
func TestSSO_ExistingUserByEmail(t *testing.T) {
	jwt := jwtFor("linwei")
	fake := &fakeSRD{identity: map[string]map[string]any{
		jwt: {"id": 1, "username": "linwei", "email": "LinWei@Vela.IO", "department": "IT", "internal_department": "DBA"},
	}}
	app := newSSOApp(t, fake)

	tok := ssoToken(t, app.ssoLogin(jwt))
	r := app.do(http.MethodGet, "/api/v1/auth/me", tok, nil)
	eq(t, r.Code, 0, "SSO 会话能用")
	var me struct {
		Email string          `json:"email"`
		Menus map[string]bool `json:"menus"`
	}
	_ = json.Unmarshal(r.Data, &me)
	eq(t, me.Email, "linwei@vela.io", "映射到原账号(大小写不同也算同一个人)")
	if !me.Menus["terminal"] {
		t.Error("原账号的权限应当原样带过来")
	}
	eq(t, fake.lastAuth(), "Bearer "+jwt, "userinfo 用 Bearer <jwt>")

	u, _ := app.repo.GetUserByEmailFold("linwei@vela.io")
	eq(t, u.Dept, "DBA", "部门同步自 SRD 的 internal_department")
	if auditCount(app, "command = ? AND actor_id = ?", "login via sso", u.ID) == 0 {
		t.Error("SSO 登录应写一条 'login via sso' 审计")
	}
}

// SRD 的 internal_department 为空时退回 department,不把网关里原有的部门清空。
func TestSSO_DepartmentFallsBackWhenInternalIsEmpty(t *testing.T) {
	jwt := jwtFor("fallback")
	fake := &fakeSRD{identity: map[string]map[string]any{
		jwt: {"username": "fallback", "email": "fallback@corp.io", "department": "财务部", "internal_department": ""},
	}}
	app := newSSOApp(t, fake)
	if err := app.repo.CreateUser(&model.User{Name: "fallback", Email: "fallback@corp.io", Status: "active",
		Kind: model.UserKindHuman, Initials: "FB", Dept: "—", LastActive: "—"}); err != nil {
		t.Fatal(err)
	}
	ssoToken(t, app.ssoLogin(jwt))
	u, _ := app.repo.GetUserByEmailFold("fallback@corp.io")
	eq(t, u.Dept, "财务部", "internal_department 为空时用 department")
}

// 网关里没有这个邮箱:SRD 认这个人也不放进来,不建号,给出"未开通、无权限"的专用码。
func TestSSO_NoGatewayAccountIsRefused(t *testing.T) {
	jwt := jwtFor("newbie")
	fake := &fakeSRD{identity: map[string]map[string]any{
		jwt: {"username": "newbie", "email": "newbie@corp.io", "department": "IT", "internal_department": "研发部"},
	}}
	app := newSSOApp(t, fake)

	for range 2 {
		r := app.ssoLogin(jwt)
		eq(t, r.Code, resp.CodeSSONoAccount, "未开通账号应拒绝: "+r.Msg)
		if !strings.Contains(r.Msg, "无权限") {
			t.Errorf("提示要说清无权限访问,实际 %q", r.Msg)
		}
	}
	var cnt int64
	app.repo.DB().Model(&model.User{}).Where("LOWER(email) = ?", "newbie@corp.io").Count(&cnt)
	eq(t, cnt, int64(0), "不该自动建号")
	if auditCount(app, "actor_name = ? AND command = ?", "newbie@corp.io", "login failed") == 0 {
		t.Error("被拒的 SSO 登录要留一条登录失败审计")
	}

	// 管理员在后台开通之后,同一个人就能经 SSO 进来。
	admin := app.login("linwei@vela.io", "vela123")
	eq(t, app.do(http.MethodPost, "/api/v1/users", admin, map[string]any{
		"name": "newbie", "email": "newbie@corp.io", "password": "Init-Pass-123", "roleIds": []int64{2},
	}).Code, 0, "管理员开通账号")
	ssoToken(t, app.ssoLogin(jwt))
}

// 停用、服务账号:SRD 认,网关不认。受邀账号:激活。
func TestSSO_AccountStatusRules(t *testing.T) {
	jd, ji, js := jwtFor("disabled"), jwtFor("invited"), jwtFor("service")
	fake := &fakeSRD{identity: map[string]map[string]any{
		jd: {"email": "qianyi@vela.io"},
		ji: {"email": "invitee@vela.io"},
		js: {"email": "bot@vela.io"},
	}}
	app := newSSOApp(t, fake)

	q, _ := app.repo.GetUserByEmailFold("qianyi@vela.io")
	if q == nil || q.Status != "disabled" {
		t.Fatalf("前提不成立:夹具里 qianyi 应是停用账号")
	}
	if r := app.ssoLogin(jd); r.Code == 0 {
		t.Error("网关里停用的账号不该能经 SSO 登录")
	}
	if err := app.repo.CreateUser(&model.User{Name: "bot", Email: "bot@vela.io", Status: "active", Kind: model.UserKindService}); err != nil {
		t.Fatalf("造服务账号: %v", err)
	}
	if r := app.ssoLogin(js); r.Code == 0 {
		t.Error("服务账号的门只有开放接口凭据,SSO 不是它的门")
	}
	if err := app.repo.CreateUser(&model.User{Name: "invitee", Email: "invitee@vela.io", Status: "invited", Kind: model.UserKindHuman}); err != nil {
		t.Fatalf("造受邀账号: %v", err)
	}
	ssoToken(t, app.ssoLogin(ji))
	inv, _ := app.repo.GetUserByEmailFold("invitee@vela.io")
	eq(t, inv.Status, "active", "受邀账号经 SSO 首登后激活")
}

// 没有 Cookie、登录态过期、JWT 被拒、Cookie 解不开、SRD 挂了 —— 各有各的回答,要人做的事不同。
func TestSSO_FailureModesAreDistinct(t *testing.T) {
	fake := &fakeSRD{identity: map[string]map[string]any{}}
	app := newSSOApp(t, fake)
	post := func(cookie string) apiResp {
		return app.withSSOCookie(http.MethodPost, "/api/v1/auth/sso", cookie, "", nil)
	}

	eq(t, post("").Code, 40101, "没有 CS-Token:专门的码,前端据此露出入口")
	before := len(fake.gotAuth)
	eq(t, post(csToken(jwtFor("old"), time.Now().Add(-25*time.Hour))).Code, 40101,
		"封装超过 24 小时:与 SRD 前端一致,当作没有登录态")
	eq(t, len(fake.gotAuth), before, "过期的登录态不该拿去打 SRD")
	eq(t, post(csToken(jwtFor("forged"), time.Now())).Code, 40100, "JWT 被 SRD 拒绝")
	bad := post("definitely-not-srd")
	eq(t, bad.Code, 50000, "Cookie 解不开:不是'没登录',是网关要改")
	if !strings.Contains(bad.Msg, "Cookie") {
		t.Errorf("解不开 Cookie 要说清楚,实际 %q", bad.Msg)
	}
	fake.mu.Lock()
	fake.down = true
	fake.mu.Unlock()
	eq(t, post(csToken(jwtFor("x"), time.Now())).Code, 50000, "SRD 挂了:与'被拒'分开报")

	if n := auditCount(app, "command = ? AND actor_name = ?", "login failed", "sso"); n < 3 {
		t.Errorf("被拒、解不开、SRD 不可用都应写失败审计,实际 %d 条", n)
	}
}

// 裸 JWT 也认:SRD 的"自定义 base64"若本就是不做编码,网关不该因此拒绝。
func TestSSO_AcceptsBareJWTCookie(t *testing.T) {
	jwt := jwtFor("linwei")
	app := newSSOApp(t, &fakeSRD{identity: map[string]map[string]any{jwt: {"email": "linwei@vela.io"}}})
	ssoToken(t, app.withSSOCookie(http.MethodPost, "/api/v1/auth/sso", jwt, "", nil))
}

// js-cookie 写 Cookie 时 + / = 原样保留;gin 的 c.Cookie() 却做 QueryUnescape,把 + 变成空格。
// 纯 ASCII 字母数字的 JSON 编出来不会有 +(+ 只来自 ~ > 或非 ASCII 字节),所以塞一个带 ~ 的字段。
func TestSSO_CookieWithPlusSign(t *testing.T) {
	jwt := jwtFor("linwei")
	var cookie string
	for i := 1; i <= 3 && !strings.Contains(cookie, "+"); i++ {
		b, _ := json.Marshal(map[string]any{"t_time": time.Now().Unix(), "token": jwt, "x": strings.Repeat("~", i)})
		cookie = base64.StdEncoding.EncodeToString(b)
	}
	if !strings.Contains(cookie, "+") {
		t.Fatalf("没造出带 + 的 Cookie: %s", cookie)
	}
	app := newSSOApp(t, &fakeSRD{identity: map[string]map[string]any{jwt: {"email": "linwei@vela.io"}}})
	ssoToken(t, app.withSSOCookie(http.MethodPost, "/api/v1/auth/sso", cookie, "", nil))
}

// ----------------------------------------------------------------- 绑定

// 存量账号第一次经 SRD 登录就绑定;绑定即收紧:之后只能走 SSO。没登录过 SRD 的人照常用口令。
// 管理员绑定了也保留口令应急;管理员解绑后恢复口令;SSO 关掉后绑定不再生效。
func TestSSO_BindOnFirstLoginTightensLocalLogin(t *testing.T) {
	jwt := jwtFor("chenhao")
	fake := &fakeSRD{identity: map[string]map[string]any{
		jwt:              {"username": "chenhao", "email": "ChenHao@vela.io"},
		jwtFor("linwei"): {"username": "linwei", "email": "linwei@vela.io"},
	}}
	app := newSSOApp(t, fake)
	ch, _ := app.repo.GetUserByEmailFold("chenhao@vela.io")
	if ch == nil || app.svc.IsAdmin(ch) || ch.SSOBound {
		t.Fatalf("前提不成立:夹具里 chenhao 应是未绑定的非管理员")
	}
	// 还没经 SRD 登录过:启用了 SSO 也照常口令登录
	app.login("chenhao@vela.io", "vela123")

	ssoToken(t, app.ssoLogin(jwt))
	ch, _ = app.repo.GetUserByEmailFold("chenhao@vela.io")
	eq(t, ch.SSOBound, true, "邮箱对上,当场绑定")
	eq(t, ch.SSOUserName, "chenhao", "记下 SRD 里的用户名")
	if ch.SSOBoundAt == nil {
		t.Error("记下绑定时间")
	}
	if auditCount(app, "command LIKE ? AND actor_id = ?", "sso.bind %", ch.ID) != 1 {
		t.Error("绑定应写一条审计")
	}
	ssoToken(t, app.ssoLogin(jwt))
	if auditCount(app, "command LIKE ? AND actor_id = ?", "sso.bind %", ch.ID) != 1 {
		t.Error("已绑定的人再次登录不该重复写绑定审计")
	}

	r := app.do(http.MethodPost, "/api/v1/auth/login", "", map[string]any{"email": "chenhao@vela.io", "password": "vela123"})
	if r.Code == 0 {
		t.Fatal("已绑定 SSO 的非管理员不该能用口令登录")
	}
	if !strings.Contains(r.Msg, "SSO") {
		t.Errorf("拒绝要说清去哪儿登录,实际 %q", r.Msg)
	}
	wrong := app.do(http.MethodPost, "/api/v1/auth/login", "", map[string]any{"email": "chenhao@vela.io", "password": "nope"})
	if strings.Contains(wrong.Msg, "SSO") {
		t.Error("口令错时不该泄露'这个账号绑了 SSO'")
	}

	// 管理员经 SRD 登录后同样绑定,但口令仍可用 —— SRD 宕机时的应急门
	ssoToken(t, app.ssoLogin(jwtFor("linwei")))
	adm, _ := app.repo.GetUserByEmailFold("linwei@vela.io")
	eq(t, adm.SSOBound, true, "管理员同样绑定")
	admin := app.login("linwei@vela.io", "vela123")

	// 解绑:恢复口令登录,并留审计
	eq(t, app.do(http.MethodPost, "/api/v1/users/"+itoa(ch.ID)+"/sso-unbind", admin, nil).Code, 0, "解绑")
	app.login("chenhao@vela.io", "vela123")
	if auditCount(app, "command LIKE ?", "sso.unbind %") == 0 {
		t.Error("解绑应写审计")
	}
	ro := app.login("zhaolei@vela.io", "vela123")
	if r := app.do(http.MethodPost, "/api/v1/users/"+itoa(ch.ID)+"/sso-unbind", ro, nil); r.Code == 0 {
		t.Error("非管理员不该能解绑")
	}

	// SSO 关掉之后绑定不再生效,所有人回到口令
	ssoToken(t, app.ssoLogin(jwt))
	_ = app.repo.SetSetting("sso.enabled", "false")
	app.login("chenhao@vela.io", "vela123")
}

// ----------------------------------------------------------------- 设置

func TestSSO_DisabledChangesNothing(t *testing.T) {
	app := newTestApp(t)
	r := app.do(http.MethodGet, "/api/v1/auth/sso/config", "", nil)
	eq(t, r.Code, 0, "config 公开可读")
	var cfg struct {
		Enabled bool `json:"enabled"`
	}
	_ = json.Unmarshal(r.Data, &cfg)
	if cfg.Enabled {
		t.Error("默认不启用")
	}
	if r := app.ssoLogin(jwtFor("x")); r.Code == 0 {
		t.Error("未启用时 SSO 登录不该成功")
	}
	app.login("chenhao@vela.io", "vela123")
}

// SSO 开关在【设置 · 会话与安全】里:通过设置接口保存,即改即生效,保存时整体校验并留审计。
func TestSSO_ConfiguredViaSettings(t *testing.T) {
	jwt := jwtFor("linwei")
	srd := httptest.NewServer((&fakeSRD{identity: map[string]map[string]any{jwt: {"email": "linwei@vela.io"}}}).handler())
	t.Cleanup(srd.Close)
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")

	// 开着但没填地址:拒绝,而且一项都不存
	r := app.do(http.MethodPut, "/api/v1/settings", admin, map[string]any{"sso.enabled": true, "sso.cookieName": "tk"})
	if r.Code == 0 {
		t.Fatal("启用 SSO 却没填 SRD 地址,应当拒绝保存")
	}
	if v, _ := app.repo.GetSetting("sso.cookieName"); v != "" {
		t.Errorf("校验失败时一项都不该存,sso.cookieName 却被存成了 %q", v)
	}

	eq(t, app.do(http.MethodPut, "/api/v1/settings", admin, map[string]any{
		"sso.enabled": true, "sso.baseUrl": srd.URL + "/",
	}).Code, 0, "保存 SSO 设置")
	var cfg struct {
		Enabled  bool   `json:"enabled"`
		LoginURL string `json:"loginUrl"`
	}
	_ = json.Unmarshal(app.do(http.MethodGet, "/api/v1/auth/sso/config", "", nil).Data, &cfg)
	if !cfg.Enabled {
		t.Fatal("保存后应立即生效")
	}
	eq(t, cfg.LoginURL, srd.URL+"/auth/login", "登录入口由 SRD 地址推出(尾部斜杠不重复)")
	ssoToken(t, app.ssoLogin(jwt))

	if auditCount(app, "command LIKE ?", "settings.sso %") == 0 {
		t.Error("修改 SSO 设置应写审计")
	}
	ro := app.login("chenhao@vela.io", "vela123")
	if r := app.do(http.MethodPut, "/api/v1/settings", ro, map[string]any{"sso.enabled": false}); r.Code == 0 {
		t.Error("非管理员不该能改 SSO 设置")
	}
}

// 部署环境下 SRD 地址必须是 https:保存时就挡住。
func TestSSO_SettingsRequireHTTPSOutsideDev(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	orig := service.AllowInsecureSSO
	service.AllowInsecureSSO = false
	t.Cleanup(func() { service.AllowInsecureSSO = orig })

	r := app.do(http.MethodPut, "/api/v1/settings", admin, map[string]any{"sso.enabled": true, "sso.baseUrl": "http://srd.bssrvc66.com"})
	if r.Code == 0 || !strings.Contains(r.Msg, "https") {
		t.Fatalf("部署环境下 http 地址应被拒,实际 code=%d msg=%q", r.Code, r.Msg)
	}
}

// 开关开着但地址被清空(例如有人直接改了库):当作没开,已绑定的人也能用口令 —— 否则被锁在外面。
func TestSSO_EnabledWithoutURLIsTreatedAsOff(t *testing.T) {
	app := newTestApp(t)
	_ = app.repo.SetSetting("sso.enabled", "true")
	_ = app.repo.SetSetting("sso.baseUrl", `""`)
	ch, _ := app.repo.GetUserByEmailFold("chenhao@vela.io")
	_ = app.repo.UpdateUserFields(ch.ID, map[string]any{"sso_bound": true})
	app.login("chenhao@vela.io", "vela123")
}


// ----------------------------------------------------------------- 关闭 SSO、退回口令登录

// 测试中发现 SSO 有问题时的退路:在设置页关掉开关,所有人回到账号口令。
func TestSSO_TurnOffFallsBackToLocalLogin(t *testing.T) {
	jwtCh := jwtFor("chenhao")
	fake := &fakeSRD{identity: map[string]map[string]any{
		jwtCh: {"username": "chenhao", "email": "chenhao@vela.io"},
	}}
	app := newSSOApp(t, fake)
	ssoToken(t, app.ssoLogin(jwtCh)) // 存量账号,当场绑定
	if r := app.loginRaw("chenhao@vela.io", "vela123"); r.Code == 0 {
		t.Fatal("前提不成立:SSO 开着时已绑定的人不该能用口令登录")
	}

	// 管理员照常用口令进来,在设置页关掉 SSO
	admin := app.login("linwei@vela.io", "vela123")
	eq(t, app.do(http.MethodPut, "/api/v1/settings", admin, map[string]any{"sso.enabled": false}).Code, 0, "关闭 SSO")

	var cfg struct {
		Enabled bool `json:"enabled"`
	}
	_ = json.Unmarshal(app.do(http.MethodGet, "/api/v1/auth/sso/config", "", nil).Data, &cfg)
	if cfg.Enabled {
		t.Error("关掉后登录页不该再露出 SSO 入口")
	}
	if r := app.ssoLogin(jwtCh); r.Code == 0 {
		t.Error("关掉后 SSO 登录接口不该再放人进来")
	}
	app.login("chenhao@vela.io", "vela123") // 已绑定的人立即恢复口令登录

	// 再打开:绑定关系还在,已绑定的人重新只能走 SSO
	eq(t, app.do(http.MethodPut, "/api/v1/settings", admin, map[string]any{"sso.enabled": true}).Code, 0, "重新打开 SSO")
	if r := app.loginRaw("chenhao@vela.io", "vela123"); r.Code == 0 {
		t.Error("重新打开后,已绑定的人又该只能走 SSO")
	}
	ssoToken(t, app.ssoLogin(jwtCh))
}

// 部署层总闸(sso.disabled / VELA_SSO_DISABLED):设置表里开着也按关闭处理 ——
// 给"SSO 出问题、偏偏又进不去设置页"的时候用。
func TestSSO_ForcedOffByDeployConfig(t *testing.T) {
	jwt := jwtFor("chenhao")
	app := newSSOApp(t, &fakeSRD{identity: map[string]map[string]any{jwt: {"email": "chenhao@vela.io"}}})
	ssoToken(t, app.ssoLogin(jwt)) // 绑定

	service.SSOForcedOff = true
	t.Cleanup(func() { service.SSOForcedOff = false })

	var cfg struct {
		Enabled   bool `json:"enabled"`
		ForcedOff bool `json:"forcedOff"`
	}
	_ = json.Unmarshal(app.do(http.MethodGet, "/api/v1/auth/sso/config", "", nil).Data, &cfg)
	if cfg.Enabled || !cfg.ForcedOff {
		t.Fatalf("总闸拉下:应为 enabled=false forcedOff=true,实际 %+v", cfg)
	}
	if r := app.ssoLogin(jwt); r.Code == 0 {
		t.Error("总闸拉下时 SSO 登录不该成功")
	}
	app.login("chenhao@vela.io", "vela123")
}

func TestLoadConfig_SSODisabledFromEnv(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(path, []byte("env: dev\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SSO.Disabled {
		t.Fatal("默认不拉总闸")
	}
	t.Setenv("VELA_SSO_DISABLED", "1")
	cfg, _ = LoadConfig(path)
	if !cfg.SSO.Disabled {
		t.Error("VELA_SSO_DISABLED=1 应拉下总闸")
	}
	if err := os.WriteFile(path, []byte("sso:\n  disabled: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VELA_SSO_DISABLED", "")
	cfg, _ = LoadConfig(path)
	if !cfg.SSO.Disabled {
		t.Error("配置文件 sso.disabled: true 应拉下总闸")
	}
}
