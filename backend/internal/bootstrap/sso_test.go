package bootstrap

// 公司 SSO(标准 OIDC)接入:只做认证,不做授权。
//
// 这一组用一个假的 OIDC 身份服务(sso_idp_test.go)把浏览器登录完整走一遍,钉住网关这一侧的约定:
// 授权码 + PKCE、state / nonce 核对、ID Token 验签、只认已验证的邮箱、按邮箱映射、网关里没开通的
// 邮箱拒绝、停用与服务账号进不来、各种失败分开报;以及存量账号的**绑定**:第一次经 SSO 登录、
// 邮箱对上时当场绑定,绑定的人只能走 SSO(管理员除外)。

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aegisdb/internal/model"
	"aegisdb/internal/service"
	"aegisdb/pkg/resp"
)

func auditCount(app *testApp, where string, args ...any) int64 {
	var n int64
	app.repo.DB().Model(&model.AuditLog{}).Where(where, args...).Count(&n)
	return n
}

// ssoErrorOf 取回调跳回登录页时带的 sso_error。
func ssoErrorOf(t *testing.T, back string) string {
	t.Helper()
	i := strings.Index(back, "#/login?")
	if i < 0 {
		t.Fatalf("没有跳回登录页:%s", back)
	}
	q, _ := url.ParseQuery(back[i+len("#/login?"):])
	return q.Get("sso_error")
}

// ----------------------------------------------------------------- 登录

// 已有账号:按邮箱(不分大小写)映射,拿到的是**这个人原来的**账号和权限。
func TestSSO_ExistingUserByEmail(t *testing.T) {
	idp := newFakeIdP(t)
	app := newSSOApp(t, idp)

	tok := ssoToken(t, app.ssoLogin(idp, map[string]any{"email": "LinWei@AegisDB.IO", "name": "Lin Wei"}))
	r := app.do(http.MethodGet, "/api/v1/auth/me", tok, nil)
	eq(t, r.Code, 0, "SSO 会话能用")
	var me struct {
		Email string          `json:"email"`
		Menus map[string]bool `json:"menus"`
	}
	_ = json.Unmarshal(r.Data, &me)
	eq(t, me.Email, "linwei@aegisdb.io", "映射到原账号(大小写不同也算同一个人)")
	if !me.Menus["terminal"] {
		t.Error("原账号的权限应当原样带过来")
	}
	u, _ := app.repo.GetUserByEmailFold("linwei@aegisdb.io")
	if auditCount(app, "command = ? AND actor_id = ?", "login via sso", u.ID) == 0 {
		t.Error("SSO 登录应写一条 'login via sso' 审计")
	}
	eq(t, idp.tokenCalls, 1, "授权码在服务端换了一次 token")
}

// 回调跳回的是网关自己的登录页:由回调地址推出,网关挂在子路径下也对。
func TestSSO_CallbackReturnsToGatewayLoginPage(t *testing.T) {
	idp := newFakeIdP(t).as(map[string]any{"email": "linwei@aegisdb.io"})
	app := newSSOApp(t, idp)
	back := app.ssoBrowse(newBrowser())
	eq(t, back, app.srv.URL+"/#/login?sso=1", "成功后回到登录页")
}

// 网关里没有这个邮箱:身份服务认这个人也不放进来,不建号,给出"未开通、无权限"的专用码。
func TestSSO_NoGatewayAccountIsRefused(t *testing.T) {
	idp := newFakeIdP(t)
	app := newSSOApp(t, idp)
	newbie := map[string]any{"email": "newbie@corp.io", "name": "newbie"}

	for range 2 {
		r := app.ssoLogin(idp, newbie)
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
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	eq(t, app.do(http.MethodPost, "/api/v1/users", admin, map[string]any{
		"name": "newbie", "email": "newbie@corp.io", "password": "Init-Pass-123", "roleIds": []int64{2},
	}).Code, 0, "管理员开通账号")
	ssoToken(t, app.ssoLogin(idp, newbie))
}

// 停用、服务账号:身份服务认,网关不认。受邀账号:激活。
func TestSSO_AccountStatusRules(t *testing.T) {
	idp := newFakeIdP(t)
	app := newSSOApp(t, idp)

	q, _ := app.repo.GetUserByEmailFold("qianyi@aegisdb.io")
	if q == nil || q.Status != "disabled" {
		t.Fatalf("前提不成立:夹具里 qianyi 应是停用账号")
	}
	if r := app.ssoLogin(idp, map[string]any{"email": "qianyi@aegisdb.io"}); r.Code == 0 {
		t.Error("网关里停用的账号不该能经 SSO 登录")
	}
	if err := app.repo.CreateUser(&model.User{Name: "bot", Email: "bot@aegisdb.io", Status: "active", Kind: model.UserKindService}); err != nil {
		t.Fatalf("造服务账号: %v", err)
	}
	if r := app.ssoLogin(idp, map[string]any{"email": "bot@aegisdb.io"}); r.Code == 0 {
		t.Error("服务账号的门只有开放接口凭据,SSO 不是它的门")
	}
	if err := app.repo.CreateUser(&model.User{Name: "invitee", Email: "invitee@aegisdb.io", Status: "invited", Kind: model.UserKindHuman}); err != nil {
		t.Fatalf("造受邀账号: %v", err)
	}
	ssoToken(t, app.ssoLogin(idp, map[string]any{"email": "invitee@aegisdb.io"}))
	inv, _ := app.repo.GetUserByEmailFold("invitee@aegisdb.io")
	eq(t, inv.Status, "active", "受邀账号经 SSO 首登后激活")
}

// 有的身份服务只在 userinfo 里给邮箱:ID Token 里没有时去 userinfo 取,sub 必须对得上。
func TestSSO_EmailFromUserInfo(t *testing.T) {
	idp := newFakeIdP(t)
	idp.stripIDToken = []string{"email", "email_verified"}
	app := newSSOApp(t, idp)
	ssoToken(t, app.ssoLogin(idp, map[string]any{"email": "chenhao@aegisdb.io"}))

	// userinfo 回答的是另一个人(sub 不同):不认。
	idp.userinfo = map[string]any{"sub": "someone-else", "email": "linwei@aegisdb.io", "email_verified": true}
	idp.as(map[string]any{"email": "chenhao@aegisdb.io"})
	eq(t, ssoErrorOf(t, app.ssoBrowse(newBrowser())), service.OIDCErrEmail, "userinfo 的 sub 对不上就不用它")
}

// 没有登录结果、state 对不上、用户在身份服务取消、换 token 失败、ID Token 不对、邮箱没验证、
// 身份服务连不上 —— 各有各的回答,要人做的事不同。
func TestSSO_FailureModesAreDistinct(t *testing.T) {
	idp := newFakeIdP(t)
	app := newSSOApp(t, idp)
	linwei := map[string]any{"email": "linwei@aegisdb.io"}

	eq(t, app.ssoPost(newBrowser(), nil).Code, resp.CodeSSONoSession, "没有登录结果:专门的码,前端据此去身份服务")

	// 回调不是这个浏览器发起的那一次:登录 CSRF 的形状
	idp.as(linwei)
	attacker := newBrowser()
	toIdP := location(t, attacker, app.srv.URL+service.OIDCStartPath)
	toCallback := location(t, attacker, toIdP)
	victim := newBrowser()
	eq(t, ssoErrorOf(t, location(t, victim, toCallback)), service.OIDCErrState, "别人的回调地址:state 对不上")
	eq(t, app.ssoPost(victim, nil).Code, resp.CodeSSONoSession, "受害者的浏览器里没有登录结果")
	// 流程 Cookie 一次性:发起的那个浏览器自己走完一次,同一个回调再来一遍就不行了
	eq(t, location(t, attacker, toCallback), app.srv.URL+"/#/login?sso=1", "发起的浏览器自己走回调:成功")
	eq(t, ssoErrorOf(t, location(t, attacker, toCallback)), service.OIDCErrState, "回调只能用一次")

	idp.deny = true
	eq(t, ssoErrorOf(t, app.ssoBrowse(newBrowser())), service.OIDCErrDenied, "用户在身份服务取消")
	idp.deny = false

	idp.failToken = true
	eq(t, ssoErrorOf(t, app.ssoBrowse(newBrowser())), service.OIDCErrExchange, "换 token 失败")
	idp.failToken = false

	for name, tamper := range map[string]func(map[string]any){
		"nonce 不对": func(c map[string]any) { c["nonce"] = "forged" },
		"受众不对":     func(c map[string]any) { c["aud"] = "another-app" },
		"颁发方不对":    func(c map[string]any) { c["iss"] = "https://evil.example.com" },
		"已过期":      func(c map[string]any) { c["exp"] = 1 },
		"多受众而 azp 不是自己": func(c map[string]any) {
			c["aud"] = []any{testClientID, "another-app"}
			c["azp"] = "another-app"
		},
	} {
		idp.idTokenTamper = tamper
		idp.as(linwei)
		eq(t, ssoErrorOf(t, app.ssoBrowse(newBrowser())), service.OIDCErrExchange, name)
	}
	idp.idTokenTamper = nil

	idp.as(map[string]any{"email": "linwei@aegisdb.io", "email_verified": false})
	eq(t, ssoErrorOf(t, app.ssoBrowse(newBrowser())), service.OIDCErrEmail, "邮箱没验证过不认")

	// 不是身份服务签的 ID Token:用另一把钥匙签,JWKS 里仍是原来的公钥
	idp.signWith = newFakeIdP(t).key
	idp.as(linwei)
	eq(t, ssoErrorOf(t, app.ssoBrowse(newBrowser())), service.OIDCErrExchange, "签名不对")
	idp.signWith = nil

	if n := auditCount(app, "command = ? AND actor_name = ?", "login failed", "sso"); n < 6 {
		t.Errorf("state 不对、换 token 失败、ID Token 不对都应写失败审计,实际 %d 条", n)
	}

	// 身份服务连不上:发起就失败,回到登录页说清楚
	idp.srv.Close()
	fresh := newSSOApp(t, idp) // 新实例:没有缓存的发现文档
	eq(t, ssoErrorOf(t, location(t, newBrowser(), fresh.srv.URL+service.OIDCStartPath)), service.OIDCErrExchange, "身份服务不可用")
}

// 登录结果是网关自己加密的:改过、伪造的一律当作没有。
func TestSSO_TicketCannotBeForged(t *testing.T) {
	idp := newFakeIdP(t).as(map[string]any{"email": "linwei@aegisdb.io"})
	app := newSSOApp(t, idp)
	b := newBrowser()
	app.ssoBrowse(b)
	u, _ := url.Parse(app.srv.URL + "/api/v1/auth/sso")
	var ticket *http.Cookie
	for _, c := range b.Jar.Cookies(u) {
		if c.Name == "aegisdb_sso" {
			ticket = c
		}
	}
	if ticket == nil {
		t.Fatal("回调应留下登录结果 Cookie")
	}
	forged := newBrowser()
	forged.Jar.SetCookies(u, []*http.Cookie{{Name: "aegisdb_sso", Value: ticket.Value[:len(ticket.Value)-6] + "AAAAAA", Path: "/api/v1/auth/sso"}})
	eq(t, app.ssoPost(forged, nil).Code, resp.CodeSSONoSession, "改过的登录结果不认")

	ssoToken(t, app.ssoPost(b, nil))
	eq(t, app.ssoPost(b, nil).Code, resp.CodeSSONoSession, "成功后登录结果即被清掉")
}

// ----------------------------------------------------------------- 绑定

// 存量账号第一次经 SSO 登录就绑定;绑定即收紧:之后只能走 SSO。没登录过 SSO 的人照常用口令。
// 管理员绑定了也保留口令应急;管理员解绑后恢复口令;SSO 关掉后绑定不再生效。
func TestSSO_BindOnFirstLoginTightensLocalLogin(t *testing.T) {
	idp := newFakeIdP(t)
	app := newSSOApp(t, idp)
	chenhao := map[string]any{"email": "ChenHao@aegisdb.io", "name": "Chen Hao"}
	ch, _ := app.repo.GetUserByEmailFold("chenhao@aegisdb.io")
	if ch == nil || app.svc.IsAdmin(ch) || ch.SSOBound {
		t.Fatalf("前提不成立:夹具里 chenhao 应是未绑定的非管理员")
	}
	// 还没经 SSO 登录过:启用了 SSO 也照常口令登录
	app.login("chenhao@aegisdb.io", "aegisdb123")

	ssoToken(t, app.ssoLogin(idp, chenhao))
	ch, _ = app.repo.GetUserByEmailFold("chenhao@aegisdb.io")
	eq(t, ch.SSOBound, true, "邮箱对上,当场绑定")
	eq(t, ch.SSOUserName, "Chen Hao", "记下身份服务里的姓名")
	if ch.SSOBoundAt == nil {
		t.Error("记下绑定时间")
	}
	if auditCount(app, "command LIKE ? AND actor_id = ?", "sso.bind %", ch.ID) != 1 {
		t.Error("绑定应写一条审计")
	}
	ssoToken(t, app.ssoLogin(idp, chenhao))
	if auditCount(app, "command LIKE ? AND actor_id = ?", "sso.bind %", ch.ID) != 1 {
		t.Error("已绑定的人再次登录不该重复写绑定审计")
	}

	r := app.do(http.MethodPost, "/api/v1/auth/login", "", map[string]any{"email": "chenhao@aegisdb.io", "password": "aegisdb123"})
	if r.Code == 0 {
		t.Fatal("已绑定 SSO 的非管理员不该能用口令登录")
	}
	if !strings.Contains(r.Msg, "SSO") {
		t.Errorf("拒绝要说清去哪儿登录,实际 %q", r.Msg)
	}
	wrong := app.do(http.MethodPost, "/api/v1/auth/login", "", map[string]any{"email": "chenhao@aegisdb.io", "password": "nope"})
	if strings.Contains(wrong.Msg, "SSO") {
		t.Error("口令错时不该泄露'这个账号绑了 SSO'")
	}

	// 管理员经 SSO 登录后同样绑定,但口令仍可用 —— 身份服务宕机时的应急门
	ssoToken(t, app.ssoLogin(idp, map[string]any{"email": "linwei@aegisdb.io"}))
	adm, _ := app.repo.GetUserByEmailFold("linwei@aegisdb.io")
	eq(t, adm.SSOBound, true, "管理员同样绑定")
	admin := app.login("linwei@aegisdb.io", "aegisdb123")

	// 解绑:恢复口令登录,并留审计
	eq(t, app.do(http.MethodPost, "/api/v1/users/"+itoa(ch.ID)+"/sso-unbind", admin, nil).Code, 0, "解绑")
	app.login("chenhao@aegisdb.io", "aegisdb123")
	if auditCount(app, "command LIKE ?", "sso.unbind %") == 0 {
		t.Error("解绑应写审计")
	}
	ro := app.login("zhaolei@aegisdb.io", "aegisdb123")
	if r := app.do(http.MethodPost, "/api/v1/users/"+itoa(ch.ID)+"/sso-unbind", ro, nil); r.Code == 0 {
		t.Error("非管理员不该能解绑")
	}

	// SSO 关掉之后绑定不再生效,所有人回到口令
	ssoToken(t, app.ssoLogin(idp, chenhao))
	_ = app.repo.SetSetting("sso.enabled", "false")
	app.login("chenhao@aegisdb.io", "aegisdb123")
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
	eq(t, ssoErrorOf(t, location(t, newBrowser(), app.srv.URL+service.OIDCStartPath)), service.OIDCErrDisabled, "未启用时发起即回到登录页")
	if r := app.ssoPost(newBrowser(), nil); r.Code == 0 {
		t.Error("未启用时 SSO 登录不该成功")
	}
	app.login("chenhao@aegisdb.io", "aegisdb123")
}

// SSO 在【设置 · 会话与安全】里配置:即改即生效,保存时整体校验并留审计,Client Secret 加密存储、不回显。
func TestSSO_ConfiguredViaSettings(t *testing.T) {
	idp := newFakeIdP(t)
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")

	// 开着但必填项不齐:拒绝,而且一项都不存
	r := app.do(http.MethodPut, "/api/v1/settings", admin, map[string]any{"sso.enabled": true, "sso.clientId": testClientID})
	if r.Code == 0 {
		t.Fatal("启用 SSO 却没填 Issuer,应当拒绝保存")
	}
	if v, _ := app.repo.GetSetting("sso.clientId"); v != "" {
		t.Errorf("校验失败时一项都不该存,sso.clientId 却被存成了 %q", v)
	}
	// 回调地址不是网关的回调接口:拒绝
	r = app.do(http.MethodPut, "/api/v1/settings", admin, map[string]any{
		"sso.enabled": true, "sso.issuer": idp.srv.URL, "sso.clientId": testClientID,
		"sso.redirectUrl": app.srv.URL + "/somewhere",
	})
	if r.Code == 0 || !strings.Contains(r.Msg, service.OIDCCallbackPath) {
		t.Fatalf("回调地址必须是网关的回调接口,实际 code=%d msg=%q", r.Code, r.Msg)
	}

	eq(t, app.do(http.MethodPut, "/api/v1/settings", admin, map[string]any{
		"sso.enabled": true, "sso.issuer": idp.srv.URL, "sso.clientId": testClientID,
		"sso.clientSecret": testClientSecret, "sso.redirectUrl": app.srv.URL + service.OIDCCallbackPath,
	}).Code, 0, "保存 SSO 设置")
	var cfg struct {
		Enabled  bool   `json:"enabled"`
		LoginURL string `json:"loginUrl"`
	}
	_ = json.Unmarshal(app.do(http.MethodGet, "/api/v1/auth/sso/config", "", nil).Data, &cfg)
	if !cfg.Enabled {
		t.Fatal("保存后应立即生效")
	}
	eq(t, cfg.LoginURL, "api/v1/auth/oidc/start", "登录入口是网关自己的发起地址(相对网关根)")
	ssoToken(t, app.ssoLogin(idp, map[string]any{"email": "linwei@aegisdb.io"}))

	stored, _ := app.repo.GetSetting("sso.clientSecret")
	if strings.Contains(stored, testClientSecret) || !strings.Contains(stored, "enc:") {
		t.Errorf("Client Secret 应加密存储,实际 %q", stored)
	}
	got := app.do(http.MethodGet, "/api/v1/settings", admin, nil)
	if strings.Contains(string(got.Data), testClientSecret) {
		t.Error("设置接口不该回显 Client Secret")
	}
	if auditCount(app, "command LIKE ?", "settings.sso %") == 0 {
		t.Error("修改 SSO 设置应写审计")
	}
	if auditCount(app, "command LIKE ?", "%"+testClientSecret+"%") != 0 {
		t.Error("审计里不该出现 Client Secret")
	}
	ro := app.login("chenhao@aegisdb.io", "aegisdb123")
	if r := app.do(http.MethodPut, "/api/v1/settings", ro, map[string]any{"sso.enabled": false}); r.Code == 0 {
		t.Error("非管理员不该能改 SSO 设置")
	}
}

// 部署环境下 Issuer 与回调地址必须是 https:保存时就挡住。
func TestSSO_SettingsRequireHTTPSOutsideDev(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	orig := service.AllowInsecureSSO
	service.AllowInsecureSSO = false
	t.Cleanup(func() { service.AllowInsecureSSO = orig })

	r := app.do(http.MethodPut, "/api/v1/settings", admin, map[string]any{
		"sso.enabled": true, "sso.issuer": "http://idp.example.com", "sso.clientId": "x",
		"sso.redirectUrl": "https://dbgw.example.com" + service.OIDCCallbackPath,
	})
	if r.Code == 0 || !strings.Contains(r.Msg, "https") {
		t.Fatalf("部署环境下 http 地址应被拒,实际 code=%d msg=%q", r.Code, r.Msg)
	}
}

// 开关开着但必填项不齐(例如有人直接改了库,或者是 SRD 时代升级上来的旧配置):当作没开,
// 已绑定的人也能用口令 —— 否则被锁在外面。
func TestSSO_EnabledWithoutIssuerIsTreatedAsOff(t *testing.T) {
	app := newTestApp(t)
	_ = app.repo.SetSetting("sso.enabled", "true")
	_ = app.repo.SetSetting("sso.baseUrl", `"https://srd.example.com"`) // 旧的 SRD 配置
	ch, _ := app.repo.GetUserByEmailFold("chenhao@aegisdb.io")
	_ = app.repo.UpdateUserFields(ch.ID, map[string]any{"sso_bound": true})
	app.login("chenhao@aegisdb.io", "aegisdb123")
}

// ----------------------------------------------------------------- 关闭 SSO、退回口令登录

// 测试中发现 SSO 有问题时的退路:在设置页关掉开关,所有人回到账号口令。
func TestSSO_TurnOffFallsBackToLocalLogin(t *testing.T) {
	idp := newFakeIdP(t)
	app := newSSOApp(t, idp)
	chenhao := map[string]any{"email": "chenhao@aegisdb.io"}
	ssoToken(t, app.ssoLogin(idp, chenhao)) // 存量账号,当场绑定
	if r := app.loginRaw("chenhao@aegisdb.io", "aegisdb123"); r.Code == 0 {
		t.Fatal("前提不成立:SSO 开着时已绑定的人不该能用口令登录")
	}

	// 管理员照常用口令进来,在设置页关掉 SSO
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	eq(t, app.do(http.MethodPut, "/api/v1/settings", admin, map[string]any{"sso.enabled": false}).Code, 0, "关闭 SSO")

	var cfg struct {
		Enabled bool `json:"enabled"`
	}
	_ = json.Unmarshal(app.do(http.MethodGet, "/api/v1/auth/sso/config", "", nil).Data, &cfg)
	if cfg.Enabled {
		t.Error("关掉后登录页不该再露出 SSO 入口")
	}
	eq(t, ssoErrorOf(t, location(t, newBrowser(), app.srv.URL+service.OIDCStartPath)), service.OIDCErrDisabled, "关掉后发起即回到登录页")
	app.login("chenhao@aegisdb.io", "aegisdb123") // 已绑定的人立即恢复口令登录

	// 再打开:绑定关系还在,已绑定的人重新只能走 SSO
	eq(t, app.do(http.MethodPut, "/api/v1/settings", admin, map[string]any{"sso.enabled": true}).Code, 0, "重新打开 SSO")
	if r := app.loginRaw("chenhao@aegisdb.io", "aegisdb123"); r.Code == 0 {
		t.Error("重新打开后,已绑定的人又该只能走 SSO")
	}
	ssoToken(t, app.ssoLogin(idp, chenhao))
}

// 部署层总闸(sso.disabled / AEGISDB_SSO_DISABLED):设置表里开着也按关闭处理 ——
// 给"SSO 出问题、偏偏又进不去设置页"的时候用。
func TestSSO_ForcedOffByDeployConfig(t *testing.T) {
	idp := newFakeIdP(t)
	app := newSSOApp(t, idp)
	chenhao := map[string]any{"email": "chenhao@aegisdb.io"}
	ssoToken(t, app.ssoLogin(idp, chenhao)) // 绑定

	// 总闸拉下之前就拿到的登录结果,拉下之后也换不出会话
	idp.as(chenhao)
	pending := newBrowser()
	app.ssoBrowse(pending)

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
	if r := app.ssoPost(pending, nil); r.Code == 0 {
		t.Error("总闸拉下时 SSO 登录不该成功")
	}
	app.login("chenhao@aegisdb.io", "aegisdb123")
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
	t.Setenv("AEGISDB_SSO_DISABLED", "1")
	cfg, _ = LoadConfig(path)
	if !cfg.SSO.Disabled {
		t.Error("AEGISDB_SSO_DISABLED=1 应拉下总闸")
	}
	if err := os.WriteFile(path, []byte("sso:\n  disabled: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AEGISDB_SSO_DISABLED", "")
	cfg, _ = LoadConfig(path)
	if !cfg.SSO.Disabled {
		t.Error("配置文件 sso.disabled: true 应拉下总闸")
	}
}

// 发现文档里的端点同样要是 https(本地开发除外):授权码和 Client Secret 是发往这些端点的。
func TestSSO_DiscoveredEndpointsMustBeHTTPS(t *testing.T) {
	idp := newFakeIdP(t).as(map[string]any{"email": "linwei@aegisdb.io"})
	app := newSSOApp(t, idp)
	orig := service.AllowInsecureSSO
	service.AllowInsecureSSO = false
	t.Cleanup(func() { service.AllowInsecureSSO = orig })
	eq(t, ssoErrorOf(t, location(t, newBrowser(), app.srv.URL+service.OIDCStartPath)), service.OIDCErrExchange,
		"假身份服务的端点是 http:部署环境下不用它")
}
