package service

// 公司 SSO 接入(SRD Portal 授权登录)—— 只做**认证**,不做**授权**。
//
// SRD 回答"你是谁"(邮箱),网关回答"你能干什么"(菜单、能力矩阵、分层、标签)。判定链一行
// 都不因为接了 SSO 而改变。
//
// 链路(见《SRD Portal 授权登录对接文档》):
//
//	登录页「使用 SRD 授权登录」
//	  → 没有登录态:浏览器跳 {SRD}/auth/login?redirect=<网关登录页>,在 SRD 完成 Lark 授权
//	  → SRD 在公司父域(bssrvc66.com)写 CS-Token Cookie,跳回网关登录页
//	  → 登录页 POST /api/v1/auth/sso,浏览器随请求带上 CS-Token
//	  → 网关服务端解出其中的 JWT,调 SRD 的 GET /api/v1/user/oauth/userinfo(Bearer <jwt>)
//	  → 拿到 email,按邮箱映射到网关用户(没有就建一个**零权限**的),并记为已绑定
//	  → 签发网关自己的会话 JWT —— 之后每个请求都不再回 SRD
//
// JWT 由 SRD 校验(userinfo 认不认),网关不持有 SRD 的签名密钥,也不自己验签。
// 网关必须部署在 CS-Token 所在的父域之下,否则浏览器根本不会把这个 Cookie 带过来。

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"net/url"
	"strings"
	"time"
	"unicode"

	"gorm.io/gorm"

	"velagateway/internal/apimsg"
	"velagateway/internal/model"
	"velagateway/pkg/jwt"
	"velagateway/pkg/totp"
)

// SSO 的四项设置都在【设置 · 会话与安全】里,存 tbl_setting(键 sso.*),运行时生效、免重启。
//
// 不放配置文件:configs/config.yaml 在安装包里,每次升级解包都会被模板覆盖 —— 写在那儿的
// SSO 配置升级一次就没了,SSO 悄悄关掉,非管理员全部被挡在门外。设置表在数据库里,升级碰不到它。
const (
	SettingSSOEnabled    = "sso.enabled"
	SettingSSOBaseURL    = "sso.baseUrl"
	SettingSSOCookieName = "sso.cookieName"
)

// DefaultSSOCookieName 是 SRD 写在父域上的登录态 Cookie。
const DefaultSSOCookieName = "CS-Token"

// SRD 的两个固定路径,都挂在 BaseURL 下。
const (
	srdLoginPath    = "/auth/login"
	srdUserInfoPath = "/api/v1/user/oauth/userinfo"
)

// SSOSettings 是 SSO 的一份快照,每次用时现读(见 SSOConfig)。
type SSOSettings struct {
	Enabled bool
	// BaseURL 是 SRD Portal 的地址,如 https://srd.bssrvc66.com。登录入口与用户信息接口
	// 都挂在它下面。
	BaseURL string
	// LoginURL 由 BaseURL 推出:{BaseURL}/auth/login。前端再拼上 ?redirect=<回跳地址>。
	LoginURL string
	// CookieName 是 SRD 在父域上写的登录态 Cookie 名,文档里是 CS-Token。
	CookieName string
	// ForcedOff:部署配置里的总闸拉下了(见 SSOForcedOff),Enabled 因此为 false。
	ForcedOff bool
}

// AllowInsecureSSO:只有本地开发允许 SSO 地址用 http(main 按环境设置,测试夹具打开)。
// 与 AllowPrivateWebhookTargets 同一个路子:默认关,漏设的后果是"更严",不是"更松"。
var AllowInsecureSSO bool

// SSOForcedOff 是 SSO 的部署层总闸(config 的 sso.disabled / 环境变量 VELA_SSO_DISABLED)。
// 拉下时不管设置页存的是什么,SSO 一律按关闭处理:登录页只有口令表单,SSO 登录接口拒绝,
// 已绑定的人恢复口令登录。设置页的开关平时就够用;这一道是给"SSO 出了问题、偏偏又进不去
// 设置页"准备的,改配置、重启即可,不用碰数据库。
var SSOForcedOff bool

// SSOConfig 读当前生效的 SSO 设置,并补默认值。
func (s *Services) SSOConfig() SSOSettings {
	c := SSOSettings{
		Enabled:    s.settingBool(SettingSSOEnabled, false),
		BaseURL:    strings.TrimRight(strings.TrimSpace(s.settingString(SettingSSOBaseURL, "")), "/"),
		CookieName: strings.TrimSpace(s.settingString(SettingSSOCookieName, DefaultSSOCookieName)),
	}
	if c.CookieName == "" {
		c.CookieName = DefaultSSOCookieName
	}
	if c.BaseURL != "" {
		c.LoginURL = c.BaseURL + srdLoginPath
	}
	// 开关开着但地址是空的(或者被人直接改了库):当作没开。一个指向空地址的 SSO 只会让
	// 每个人都看到"SSO 服务不可用",而已绑定的人口令又被收紧了 —— 等于把他们锁在外面。
	if c.Enabled && c.BaseURL == "" {
		c.Enabled = false
	}
	if SSOForcedOff {
		c.Enabled, c.ForcedOff = false, true
	}
	return c
}

var cookieNameRe = regexp.MustCompile(`^[A-Za-z0-9_.\-]{1,64}$`)

// ValidateSSOSettings 在**保存那一刻**把配置错误挡住,而不是等第一个人登录时才发现。
//
// 地址必须是 https(allowHTTP 只给本地开发):网关要把用户的 SSO token 发给它,明文 http 上
// 这个 token 在内网任何一台机器上都能被抓走,而它能换出一个完整的网关会话。
func ValidateSSOSettings(c SSOSettings, allowHTTP bool) error {
	if !c.Enabled {
		return nil
	}
	check := func(label apimsg.M, raw string, required bool) error {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			if required {
				return apimsg.Errorf("启用 SSO 必须填写%s", label)
			}
			return nil
		}
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
			return apimsg.Errorf("%s不是合法的 http(s) 地址:%s", label, raw)
		}
		if u.Scheme == "http" && !allowHTTP {
			return apimsg.Errorf("%s必须是 https —— 网关要把用户的 SSO token 发过去,明文传输等于把会话交给内网任何一台机器", label)
		}
		return nil
	}
	if err := check(apimsg.T("SRD 授权登录地址"), c.BaseURL, true); err != nil {
		return err
	}
	if c.CookieName != "" && !cookieNameRe.MatchString(c.CookieName) {
		return apimsg.Errorf("Cookie 名只能由字母、数字、_ . - 组成:%s", c.CookieName)
	}
	return nil
}

var (
	// ErrSSODisabled:没有启用 SSO,这条路不开。
	ErrSSODisabled = errors.New("sso disabled")
	// ErrSSONoSession:请求里没有 SSO 的 token Cookie —— 人还没在 SSO 登录。这是**正常**
	// 情况(第一次打开网关),前端据此露出「使用公司 SSO 登录」按钮,不是报错。
	ErrSSONoSession = errors.New("no sso session")
	// ErrSSORejected:SSO 明确说这个 token 不认(过期、被踢下线、伪造)。
	ErrSSORejected = errors.New("sso rejected token")
	// ErrSSOUnavailable:SSO 连不上、超时、回了看不懂的东西。与 Rejected 分开,是因为
	// 要人做的事不同:前者是"重新登录 SSO",后者是"等一会儿,或者用管理员账号进来"。
	ErrSSOUnavailable = errors.New("sso unavailable")
	// ErrLocalLoginSSOOnly:这个账号已绑定 SSO,只能经 SSO 登录(管理员除外)。
	ErrLocalLoginSSOOnly = errors.New("account is bound to sso")
	// ErrSSONoAccount:SRD 认得这个人,但网关里没有给他开通账号 —— 不放进来。
	ErrSSONoAccount = errors.New("no gateway account for sso identity")
)

// SSOIdentity 是 validate 回答的"这是谁"。
type SSOIdentity struct {
	UserName   string
	Email      string
	Department string
}

// maxSSOTokenLen:一个正常 token 远小于这个值;超长的直接不认,不拿去打 SSO。
const maxSSOTokenLen = 4096

func (s *Services) ssoClient() *http.Client {
	return &http.Client{
		Timeout: 5 * time.Second,
		// 不跟随跳转:validate 是一个 API,它回 302 只可能是配错了地址(指到了登录页),
		// 跟过去拿到一张 HTML 页面再去解析,只会得到一个更难看懂的错误。
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// srdSessionMaxAge 与 SRD 前端的校验周期一致:封装时间超过 24 小时的登录态,SRD 自己
// 也会清掉。网关不拿它去打 userinfo,直接当作"没有登录态",让人回 SRD 重新授权。
const srdSessionMaxAge = 24 * time.Hour

// ErrSSOTokenFormat:CS-Token 存在,但解不出 JWT。与 Rejected 分开:这不是"登录态过期",
// 而是网关与 SRD 对 Cookie 编码的理解不一致,重新登录多少次都一样,要的是改网关。
var ErrSSOTokenFormat = errors.New("sso cookie format")

// srdBase64 是 SRD 的"自定义 base64":算法与标准 base64 相同,只换了字母表,填充符是 *。
//
// 出处:SRD 前端 src/assets/js/encryption.js(线上包 app.8db67ec2.js)。那里把五段混淆过的字符串
// 拼起来、每个字符减 3 得到 67 个字符,前 64 个是字母表,第 65 个(*)是填充。其中一段含一个
// 不可见字符 \x81(减 3 后是 ~),照着源码肉眼抄会漏,所以这里的值是用 node 跑 SRD 原函数打出来的。
// SRD 升级若换了字母表,sso_settings_test.go 里用 SRD 原函数生成的向量会先失败。
var srdBase64 = base64.NewEncoding("GedC6kERMS.0H3r5tXUD9o_LyVNaIf2JwWOlYZzvgjs8bpi7~TPAch14QnqKx-Fu").WithPadding('*')

// DecodeSRDToken 从 CS-Token 的 Cookie 值里取出 SRD 的 JWT。
//
// SRD 登录成功时(store 的 SET_TOKEN)写 CS-Token = srdBase64( JSON{"t_time": 秒, "token": "<jwt>"} ),
// 只有这一层,没有再套标准 base64。依次尝试:
//
//  1. Cookie 值本身就是 JWT(三段、以 eyJ 开头);
//  2. SRD 自定义 base64 —— 线上的真实形态;
//  3. 标准 / URL 安全 base64(有无填充均可)—— 兜底,也让测试与旧部署不必改。
//
// 前面先做一次 URL 解码:js-cookie 读 Cookie 时同样会解 %XX。
func DecodeSRDToken(raw string) (jwt string, issuedAt time.Time, err error) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return "", time.Time{}, ErrSSONoSession
	}
	// PathUnescape 而不是 QueryUnescape:后者把 + 当空格,而 js-cookie 让 base64 的 + 原样留在 Cookie 里。
	if strings.Contains(v, "%") {
		if u, uerr := url.PathUnescape(v); uerr == nil {
			v = u
		}
	}
	// 空格在任何一种 base64 里都不合法,出现只可能是上游把 + 解成了空格,还原回去。
	v = strings.ReplaceAll(v, " ", "+")
	v = strings.Trim(v, `"`)
	if len(v) > maxSSOTokenLen || strings.IndexFunc(v, unicode.IsControl) >= 0 {
		return "", time.Time{}, ErrSSOTokenFormat
	}
	if looksLikeJWT(v) {
		return v, time.Time{}, nil
	}
	for _, enc := range []*base64.Encoding{srdBase64, srdBase64.WithPadding(base64.NoPadding), base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		b, derr := enc.DecodeString(v)
		if derr != nil {
			continue
		}
		dec := strings.TrimSpace(string(b))
		if looksLikeJWT(dec) {
			return dec, time.Time{}, nil
		}
		var p struct {
			TTime json.Number `json:"t_time"`
			Token string      `json:"token"`
		}
		d := json.NewDecoder(strings.NewReader(dec))
		d.UseNumber()
		if d.Decode(&p) != nil || !looksLikeJWT(strings.TrimSpace(p.Token)) {
			continue
		}
		if sec, nerr := p.TTime.Int64(); nerr == nil && sec > 0 {
			issuedAt = time.Unix(sec, 0)
		}
		return strings.TrimSpace(p.Token), issuedAt, nil
	}
	return "", time.Time{}, ErrSSOTokenFormat
}

// srdCookieShape 描述一个 Cookie 值的形状:长度、出现了哪几类字符、首尾各 4 个字符。
// 首尾 4 个字符不足以还原登录态,但足以看出是 base64、URL 编码还是别的什么。
func srdCookieShape(v string) string {
	var classes []string
	for _, c := range []struct{ name, set string }{
		{"A-Z", "ABCDEFGHIJKLMNOPQRSTUVWXYZ"}, {"a-z", "abcdefghijklmnopqrstuvwxyz"}, {"0-9", "0123456789"},
		{"+", "+"}, {"/", "/"}, {"=", "="}, {"-", "-"}, {"_", "_"}, {"%", "%"}, {".", "."}, {"空格", " "},
	} {
		if strings.ContainsAny(v, c.set) {
			classes = append(classes, c.name)
		}
	}
	if strings.Trim(v, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/=-_%. ") != "" {
		classes = append(classes, "其他")
	}
	head, tail := v, ""
	if len(v) > 8 {
		head, tail = v[:4], v[len(v)-4:]
	}
	return fmt.Sprintf("len=%d chars=[%s] head=%q tail=%q", len(v), strings.Join(classes, " "), head, tail)
}

// looksLikeJWT:三段、每段非空且只含 base64url 字符、首段是 JSON 头(eyJ 即 `{"`)。
func looksLikeJWT(s string) bool {
	parts := strings.Split(s, ".")
	if len(parts) != 3 || !strings.HasPrefix(parts[0], "eyJ") {
		return false
	}
	for _, p := range parts {
		if p == "" || strings.Trim(p, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_=") != "" {
			return false
		}
	}
	return true
}

// ssoValidate 从 CS-Token 取出 JWT,拿去问 SRD"这是谁"。
func (s *Services) ssoValidate(baseURL, cookie string) (*SSOIdentity, error) {
	token, issuedAt, err := DecodeSRDToken(cookie)
	if errors.Is(err, ErrSSOTokenFormat) {
		// 只记形状不记内容:Cookie 里是能直接用的 SRD 登录态。有了这行,对照 SRD 的
		// encryption.js 就能判断编码差在哪,不必让用户去浏览器里抠 Cookie。
		slog.Warn("sso: CS-Token 解不开", "shape", srdCookieShape(cookie))
	}
	if err != nil {
		return nil, err
	}
	if !issuedAt.IsZero() && time.Since(issuedAt) > srdSessionMaxAge {
		return nil, ErrSSONoSession
	}
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(baseURL, "/")+srdUserInfoPath, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSSOUnavailable, err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	res, err := s.ssoClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSSOUnavailable, err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))

	switch {
	case res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden:
		return nil, ErrSSORejected
	case res.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("%w: http %d", ErrSSOUnavailable, res.StatusCode)
	}
	var env struct {
		Code int             `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, apimsg.Errorf("%w: 响应不是 JSON", ErrSSOUnavailable)
	}
	if env.Code != 200 {
		// 业务码非 200:SRD 看懂了请求,只是不认这个 token(过期、未生效、伪造)。
		return nil, ErrSSORejected
	}
	id, ok := parseSSOIdentity(env.Data)
	if !ok {
		return nil, apimsg.Errorf("%w: 响应里没有可用的邮箱", ErrSSOUnavailable)
	}
	return id, nil
}

// parseSSOIdentity 从 userinfo 的 data 里取身份:{username, email, internal_department, department, ...}。
//
// 部门取 internal_department(公司内部的部门),为空时退回 department —— 不因为 SRD 少给了一个
// 字段就把网关里的部门清空。部门字段不是字符串时忽略它:部门只是展示信息,为它拒绝一次登录不值得。
func parseSSOIdentity(raw json.RawMessage) (*SSOIdentity, bool) {
	var one struct {
		UserName     string          `json:"username"`
		Email        string          `json:"email"`
		Department   json.RawMessage `json:"department"`
		InternalDept json.RawMessage `json:"internal_department"`
	}
	if err := json.Unmarshal(raw, &one); err != nil {
		return nil, false
	}
	email := normEmailForCompare(one.Email)
	if email == "" || !strings.Contains(email, "@") {
		return nil, false
	}
	var dept string
	_ = json.Unmarshal(one.InternalDept, &dept)
	if strings.TrimSpace(dept) == "" {
		_ = json.Unmarshal(one.Department, &dept)
	}
	return &SSOIdentity{
		UserName:   strings.TrimSpace(one.UserName),
		Email:      email,
		Department: strings.TrimSpace(dept),
	}, true
}

// SSOLogin 用 SSO 的 token 换一个网关会话。
//
// 映射规则只有一条:**邮箱**。网关里有这个邮箱的账号就是这个人;没有就拒绝 ——
// SSO 只证明了"这个邮箱的主人来了",不证明"他该进网关"。账号由管理员在后台开通
// (新建用户或发邀请)。从前首登自动建零权限号,结果是谁都能在网关里留下一个账号。
//
// 网关自己的 TOTP 一般不在这里再要:登录这一步由 SSO 负责(它有自己的 MFA),生产操作的步进
// 验证是按操作、按实例的另一道闸。例外是**已绑定 TOTP 的平台管理员**:SSO 一旦失守,损失最大的
// 就是这类账号,所以他们走 SSO 也要输码(2026-10 安全审计 M8,见 ssoAdminMFA)。
func (s *Services) SSOLogin(token, mfaCode, clientIP string) (string, time.Time, *model.User, error) {
	cfg := s.SSOConfig()
	if !cfg.Enabled {
		return "", time.Time{}, nil, ErrSSODisabled
	}
	id, err := s.ssoValidate(cfg.BaseURL, token)
	if err != nil {
		if !errors.Is(err, ErrSSONoSession) {
			// 没有登录态是正常情况,不记;被拒、连不上、Cookie 解不开都要留痕 —— 可能是有人
			// 拿着过期或伪造的 token 在试,也可能是 SRD 出了事,事后都要能查到。
			s.auditLoginFail("sso", clientIP)
		}
		return "", time.Time{}, nil, err
	}

	u, err := s.Repo.GetUserByEmailFold(id.Email)
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound), err == nil && !sameEmail(u.Email, id.Email):
		s.auditLoginFail(id.Email, clientIP)
		return "", time.Time{}, nil, ErrSSONoAccount
	case err != nil:
		return "", time.Time{}, nil, err
	}
	u.ClientIP = clientIP
	u.AuditSource = model.AuditSourceAuth // SSO 登录同样在鉴权中间件之前

	// 服务账号的门只有开放接口凭据一扇,SSO 不是它的门 —— 与本地登录同一条规矩。
	if u.Kind == model.UserKindService {
		s.auditLoginFail(id.Email, clientIP)
		return "", time.Time{}, nil, ErrInvalidCredentials
	}
	switch u.Status {
	case "active":
	case "invited":
		// 受邀账号:管理员已经在等这个人来了,SSO 又证明了来的就是这个邮箱的主人 ——
		// 这正是邀请要完成的事。激活并留痕。
		if err := s.Repo.UpdateUserFields(u.ID, map[string]any{"status": "active"}); err != nil {
			return "", time.Time{}, nil, err
		}
		u.Status = "active"
		s.recordAudit(u, nil, "sso.activate 受邀账号经 SSO 首次登录,已激活", model.RiskMid, model.ResultExecuted, "", "login")
	default:
		// disabled 以及任何别的状态:管理员停掉的账号,SSO 认不认都不放进来。
		s.auditLoginFail(id.Email, clientIP)
		return "", time.Time{}, nil, ErrUserDisabled
	}

	if err := s.ssoAdminMFA(u, mfaCode, clientIP); err != nil {
		return "", time.Time{}, nil, err
	}

	// 绑定:这个人刚刚经 SRD 证明了自己是这个邮箱的主人 —— 这正是"按邮箱匹配上"的那一下。
	// 绑定之后他只能经 SSO 登录(管理员除外,见 Login),与管理员后台解绑是一对。
	if !u.SSOBound {
		now := time.Now()
		if err := s.Repo.UpdateUserFields(u.ID, map[string]any{
			"sso_bound": true, "sso_user_name": fitCol(id.UserName, 128), "sso_bound_at": now,
		}); err != nil {
			return "", time.Time{}, nil, err
		}
		u.SSOBound, u.SSOUserName, u.SSOBoundAt = true, fitCol(id.UserName, 128), &now
		s.recordAudit(u, nil, "sso.bind 经 SRD 授权登录,按邮箱绑定(此后只能经 SSO 登录,管理员除外)",
			model.RiskMid, model.ResultExecuted, "", "login")
	}

	// 部门归 SSO 管:变了就跟上。姓名不动 —— 管理员可能在网关里改过它,SSO 的
	// user_name 常常只是邮箱前缀,用它覆盖掉一个认真起的名字是倒退。
	if id.Department != "" && id.Department != u.Dept {
		if err := s.Repo.UpdateUserFields(u.ID, map[string]any{"dept": fitCol(id.Department, 64)}); err == nil {
			u.Dept = fitCol(id.Department, 64)
		}
	}
	return s.issueSession(u, "login via sso", jwt.AuthMethodSSO)
}

// ssoAdminMFA:已绑定 TOTP 的平台管理员经 SSO 登录也要输码(M8)。缺码回 ErrMFARequired(前端
// 据此亮出输码框);错码计入 MFA 失败计数(与步进验证共用,H4),码同样一次性消费。
func (s *Services) ssoAdminMFA(u *model.User, mfaCode, clientIP string) error {
	if !u.MFAEnabled || u.MFASecret == "" || !s.IsAdmin(u) {
		return nil
	}
	if mfaCode == "" {
		return ErrMFARequired
	}
	if s.mfaFails.locked(u.ID, time.Now()) {
		return ErrMFALocked
	}
	ok, counter := totp.ValidateWithCounter(u.MFASecret, mfaCode, time.Now())
	if !ok {
		s.auditLoginFail(u.Email, clientIP)
		return s.mfaFailed(u, "sso.login", ErrMFAInvalid)
	}
	if !s.Repo.ConsumeMFACounter(u.ID, int64(counter)) {
		s.auditLoginFail(u.Email, clientIP)
		return ErrMFACodeUsed
	}
	s.mfaFails.succeed(u.ID)
	return nil
}

// issueSession 签发网关会话并记一条登录审计。本地登录与 SSO 登录共用这一段 ——
// 会话怎么签(TTL、代次、角色码)只该有一个答案。authMethod 记进 token(jwt.Claims.AuthMethod),
// SSO 登录传 jwt.AuthMethodSSO,本地口令登录传空。
func (s *Services) issueSession(u *model.User, auditCmd, authMethod string) (string, time.Time, *model.User, error) {
	role, _ := s.Repo.GetRole(u.RoleID)
	roleCode := ""
	if role != nil {
		roleCode = role.Code
	}
	token, exp, err := s.JWT.IssueSession(u.ID, u.RoleID, roleCode, u.Name, u.TokenVersion, s.SessionTTL(), authMethod)
	if err != nil {
		return "", time.Time{}, nil, err
	}
	s.recordAudit(u, nil, auditCmd, model.RiskLow, model.ResultExecuted, "", "login")
	return token, exp, u, nil
}

// fitCol 把字符串截到列宽以内(按字符,不是字节),不加省略号。
//
// 不用 clip:它截断后会补一个「…」,64 个字再加 1 个就是 65,写进 size:64 的列在 MySQL
// 严格模式下直接报错 —— 一个名字过长的人第一次用 SSO 登录,得到的是一次 500。
func fitCol(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// AuditAdminAction 供 handler 记一条管理动作审计(例如改 SSO 设置)。
func (s *Services) AuditAdminAction(actor *model.User, detail string) { s.auditAdminAction(actor, detail) }
