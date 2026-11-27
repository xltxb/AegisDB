package service

// 公司 SSO(标准 OIDC)—— 只做**认证**,不做**授权**。
//
// 身份服务回答"你是谁"(邮箱),网关回答"你能干什么"(菜单、能力矩阵、分层、标签)。判定链一行
// 都不因为接了 SSO 而改变。
//
// 链路(Authorization Code + PKCE,见 sso_oidc.go):
//
//	登录页「使用 SSO 登录」
//	  → GET /api/v1/auth/oidc/start:生成 state / nonce / PKCE,加密放进短期 Cookie,302 到身份服务
//	  → 身份服务完成登录,带 code 回到 GET /api/v1/auth/oidc/callback
//	  → 网关核对 state,在服务端用 code 换 token,按 JWKS 验 ID Token(签名、iss、aud、exp、nonce)
//	  → 取出邮箱,把"验过的身份"加密放进 5 分钟有效的一次性 Cookie,跳回登录页
//	  → 登录页 POST /api/v1/auth/sso,网关按邮箱映射到网关用户(没有就**拒绝**,不自动建号),
//	    记为已绑定,签发网关自己的会话 JWT —— 之后每个请求都不再回身份服务
//
// token 全程只在服务端:浏览器拿到的只有 code(一次性、要配 PKCE verifier 才换得动)和网关
// 自己加密的 Cookie。

import (
	"errors"
	"net/url"
	"strings"
	"time"

	"gorm.io/gorm"

	"aegisdb/internal/apimsg"
	"aegisdb/internal/model"
	"aegisdb/pkg/crypto"
	"aegisdb/pkg/jwt"
	"aegisdb/pkg/totp"
)

// SSO 的设置都在【设置 · 会话与安全】里,存 tbl_setting(键 sso.*),运行时生效、免重启。
//
// 不放配置文件:configs/config.yaml 在安装包里,每次升级解包都会被模板覆盖 —— 写在那儿的
// SSO 配置升级一次就没了,SSO 悄悄关掉,非管理员全部被挡在门外。设置表在数据库里,升级碰不到它。
const (
	SettingSSOEnabled      = "sso.enabled"
	SettingSSOIssuer       = "sso.issuer"
	SettingSSOClientID     = "sso.clientId"
	SettingSSOClientSecret = "sso.clientSecret" // 加密存储(handler 的 encryptedSettingKeys)
	SettingSSORedirectURL  = "sso.redirectUrl"
	SettingSSOScopes       = "sso.scopes"
	SettingSSOEmailClaim   = "sso.emailClaim"
)

const (
	// DefaultSSOScopes:openid 是 OIDC 的必选项;email / profile 让身份服务把邮箱和姓名放进来。
	DefaultSSOScopes = "openid email profile"
	// DefaultSSOEmailClaim:标准声明。换成别的(如 upn)由管理员担保那个字段就是邮箱。
	DefaultSSOEmailClaim = "email"
)

// SSO 的几个固定路径。Cookie 路径(handler)与回调地址校验从这里取;路由表(bootstrap.NewRouter)
// 为了让 OpenAPI 对照测试读得到,仍写字面量,由 TestSSOPathsAreRouted 钉住与这里一致(#186)。回调地址必须以 OIDCCallbackPath 结尾:登录完成后要跳回的前端地址就是从它
// 推出来的(去掉这一段即是网关的访问地址,网关可能挂在子路径下)。
const (
	APIPrefix = "/api/v1"
	// OIDCPathPrefix 下是 start 与 callback;流程 Cookie 只发给这下面。
	OIDCPathPrefix   = APIPrefix + "/auth/oidc"
	OIDCStartPath    = OIDCPathPrefix + "/start"
	OIDCCallbackPath = OIDCPathPrefix + "/callback"
	// SSOLoginPath 用回调留下的登录结果换网关会话;登录结果 Cookie 只发给它。
	SSOLoginPath = APIPrefix + "/auth/sso"
)

// SSOSettings 是 SSO 的一份快照,每次用时现读(见 SSOConfig)。
type SSOSettings struct {
	Enabled bool
	// Issuer 是身份服务的颁发方地址,{Issuer}/.well-known/openid-configuration 必须能取到。
	Issuer   string
	ClientID string
	// ClientSecret 已解密。可以为空:公开客户端只靠 PKCE。
	ClientSecret string
	// RedirectURL 是登记在身份服务上的回调地址,原样用于授权请求与换 token。
	RedirectURL string
	Scopes      []string
	EmailClaim  string
	// LoginURL 是登录页按钮要跳的地址(网关自己的 start 接口,相对网关根)。
	LoginURL string
	// ForcedOff:部署配置里的总闸拉下了(见 SSOForcedOff),Enabled 因此为 false。
	ForcedOff bool
}

// AllowInsecureSSO:只有本地开发允许 SSO 地址用 http(main 按环境设置,测试夹具打开)。
// 与 AllowPrivateWebhookTargets 同一个路子:默认关,漏设的后果是"更严",不是"更松"。
var AllowInsecureSSO bool

// SSOForcedOff 是 SSO 的部署层总闸(config 的 sso.disabled / 环境变量 AEGISDB_SSO_DISABLED)。
// 拉下时不管设置页存的是什么,SSO 一律按关闭处理:登录页只有口令表单,SSO 登录接口拒绝,
// 已绑定的人恢复口令登录。设置页的开关平时就够用;这一道是给"SSO 出了问题、偏偏又进不去
// 设置页"准备的,改配置、重启即可,不用碰数据库。
var SSOForcedOff bool

// SSOConfig 读当前生效的 SSO 设置,并补默认值。
func (s *Services) SSOConfig() SSOSettings {
	c := SSOSettings{
		Enabled:     s.settingBool(SettingSSOEnabled, false),
		// 不去掉结尾的 /:有的身份服务(如 Auth0)的 issuer 就带着它,而发现文档里的 issuer 必须与
		// 这里逐字相同,ID Token 的 iss 也是。
		Issuer:      strings.TrimSpace(s.settingString(SettingSSOIssuer, "")),
		ClientID:    strings.TrimSpace(s.settingString(SettingSSOClientID, "")),
		RedirectURL: strings.TrimSpace(s.settingString(SettingSSORedirectURL, "")),
		Scopes:      parseScopes(s.settingString(SettingSSOScopes, DefaultSSOScopes)),
		EmailClaim:  strings.TrimSpace(s.settingString(SettingSSOEmailClaim, DefaultSSOEmailClaim)),
		LoginURL:    OIDCStartPath,
	}
	if raw := strings.TrimSpace(s.settingString(SettingSSOClientSecret, "")); raw != "" {
		if plain, err := crypto.DecryptSecret(raw); err == nil {
			c.ClientSecret = plain
		}
	}
	if c.EmailClaim == "" {
		c.EmailClaim = DefaultSSOEmailClaim
	}
	// 开关开着但必填项不齐(或者被人直接改了库,或者是从 SRD 时代升级上来的旧配置):当作没开。
	// 一个缺了地址的 SSO 只会让每个人都看到"SSO 服务不可用",而已绑定的人口令又被收紧了 ——
	// 等于把他们锁在外面。
	if c.Enabled && (c.Issuer == "" || c.ClientID == "" || c.RedirectURL == "") {
		c.Enabled = false
	}
	if SSOForcedOff {
		c.Enabled, c.ForcedOff = false, true
	}
	return c
}

// parseScopes 按空白或逗号切开,去重,并保证 openid 在里面 —— 没有它就不是 OIDC,身份服务
// 不会发 ID Token。
func parseScopes(raw string) []string {
	out := []string{"openid"}
	seen := map[string]bool{"openid": true}
	for _, f := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' }) {
		if f = strings.TrimSpace(f); f != "" && !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	return out
}

// GatewayBaseURL 是回调地址去掉 OIDCCallbackPath 之后的部分 —— 网关(前端)的访问地址。
// 回调完成后跳回 {base}/#/login。
func (c SSOSettings) GatewayBaseURL() string {
	return strings.TrimSuffix(c.RedirectURL, OIDCCallbackPath)
}

// ValidateSSOSettings 在**保存那一刻**把配置错误挡住,而不是等第一个人登录时才发现。
//
// 地址必须是 https(allowHTTP 只给本地开发):授权码和 token 走这些地址,明文 http 上它们在
// 内网任何一台机器上都能被抓走,而它们能换出一个完整的网关会话。
func ValidateSSOSettings(c SSOSettings, allowHTTP bool) error {
	if !c.Enabled {
		return nil
	}
	check := func(label apimsg.M, raw string) error {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return apimsg.Errorf("启用 SSO 必须填写%s", label)
		}
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.RawQuery != "" || u.Fragment != "" {
			return apimsg.Errorf("%s不是合法的 http(s) 地址:%s", label, raw)
		}
		if u.Scheme == "http" && !allowHTTP {
			return apimsg.Errorf("%s必须是 https —— 授权码和 token 要经过它,明文传输等于把会话交给内网任何一台机器", label)
		}
		return nil
	}
	if err := check(apimsg.T("Issuer 地址"), c.Issuer); err != nil {
		return err
	}
	if strings.TrimSpace(c.ClientID) == "" {
		return apimsg.Errorf("启用 SSO 必须填写 Client ID")
	}
	if err := check(apimsg.T("回调地址"), c.RedirectURL); err != nil {
		return err
	}
	if !strings.HasSuffix(strings.TrimSpace(c.RedirectURL), OIDCCallbackPath) {
		return apimsg.Errorf("回调地址必须以 %s 结尾", OIDCCallbackPath)
	}
	if strings.ContainsAny(c.EmailClaim, " \t\n") {
		return apimsg.Errorf("邮箱字段名不能含空白:%s", c.EmailClaim)
	}
	return nil
}

var (
	// ErrSSODisabled:没有启用 SSO,这条路不开。
	ErrSSODisabled = errors.New("sso disabled")
	// ErrSSONoSession:请求里没有(有效的)SSO 登录结果 —— 人还没去身份服务登录,或者那次
	// 结果已过期。这是**正常**情况,前端据此去身份服务,不是报错。
	ErrSSONoSession = errors.New("no sso session")
	// ErrSSOUnavailable:身份服务连不上、超时、回了看不懂的东西。要人做的事是"等一会儿,
	// 或者用管理员账号进来"。
	ErrSSOUnavailable = errors.New("sso unavailable")
	// ErrLocalLoginSSOOnly:这个账号已绑定 SSO,只能经 SSO 登录(管理员除外)。
	ErrLocalLoginSSOOnly = errors.New("account is bound to sso")
	// ErrSSONoAccount:身份服务认得这个人,但网关里没有给他开通账号 —— 不放进来。
	ErrSSONoAccount = errors.New("no gateway account for sso identity")
)

// SSOIdentity 是身份服务验过的"这是谁"。
type SSOIdentity struct {
	UserName string
	Email    string
}

// SSOLogin 用 OIDC 回调留下的登录结果(ticket,见 OIDCCallback)换一个网关会话。
//
// 映射规则只有一条:**邮箱**。网关里有这个邮箱的账号就是这个人;没有就拒绝 ——
// SSO 只证明了"这个邮箱的主人来了",不证明"他该进网关"。账号由管理员在后台开通
// (新建用户或发邀请)。从前首登自动建零权限号,结果是谁都能在网关里留下一个账号。
//
// 网关自己的 TOTP 一般不在这里再要:登录这一步由 SSO 负责(它有自己的 MFA),生产操作的步进
// 验证是按操作、按实例的另一道闸。例外是**已绑定 TOTP 的平台管理员**:SSO 一旦失守,损失最大的
// 就是这类账号,所以他们走 SSO 也要输码(2026-10 安全审计 M8,见 ssoAdminMFA)。
func (s *Services) SSOLogin(ticket, mfaCode, clientIP string) (string, time.Time, *model.User, error) {
	cfg := s.SSOConfig()
	if !cfg.Enabled {
		return "", time.Time{}, nil, ErrSSODisabled
	}
	// 身份在回调时已经验过(签名、颁发方、受众、有效期、nonce),这里只解开网关自己加密的
	// 结果。解不开或过期一律当作"没有登录态":它只可能是过期了,或者不是网关发的。
	id, err := s.openSSOTicket(ticket, cfg)
	if err != nil {
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

	// 绑定:这个人刚刚经身份服务证明了自己是这个邮箱的主人 —— 这正是"按邮箱匹配上"的那一下。
	// 绑定之后他只能经 SSO 登录(管理员除外,见 Login),与管理员后台解绑是一对。
	if !u.SSOBound {
		now := time.Now()
		if err := s.Repo.UpdateUserFields(u.ID, map[string]any{
			"sso_bound": true, "sso_user_name": fitCol(id.UserName, 128), "sso_bound_at": now,
		}); err != nil {
			return "", time.Time{}, nil, err
		}
		u.SSOBound, u.SSOUserName, u.SSOBoundAt = true, fitCol(id.UserName, 128), &now
		s.recordAudit(u, nil, "sso.bind 经 SSO 登录,按邮箱绑定(此后只能经 SSO 登录,管理员除外)",
			model.RiskMid, model.ResultExecuted, "", "login")
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
