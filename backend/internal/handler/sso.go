package handler

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"aegisdb/internal/dto"
	"aegisdb/internal/middleware"
	"aegisdb/internal/service"
	"aegisdb/pkg/resp"
)

// 两枚 Cookie(内容由网关加密,见 service/sso_oidc.go):
//
//   - oidcFlowCookie:start → callback 之间的 state / nonce / PKCE verifier,只发给 …/api/v1/auth/oidc。
//   - ssoTicketCookie:callback → POST /auth/sso 之间验过的身份,只发给 …/api/v1/auth/sso。
//
// 都是 HttpOnly(前端脚本读不到)、SameSite=Lax —— 身份服务跳回来是一次跨站的顶层 GET,Strict 下
// 浏览器不会带上流程 Cookie。
const (
	oidcFlowCookie  = "aegisdb_oidc_flow"
	oidcFlowPath    = service.OIDCPathPrefix
	ssoTicketCookie = "aegisdb_sso"
	ssoTicketPath   = service.SSOLoginPath
)

// cookiePath 是浏览器眼里的 Cookie 路径:回调地址里网关的路径前缀 + suffix。网关挂在反向代理的
// 子路径下(浏览器看到的是 /gw/api/v1/…,网关收到的是 /api/v1/…)时,写成 /api/v1/… 的 Cookie
// 浏览器永远不会带回来。前缀取自回调地址 —— 它就是浏览器实际访问网关的地址。
func cookiePath(cfg service.SSOSettings, suffix string) string {
	u, err := url.Parse(cfg.GatewayBaseURL())
	if err != nil {
		return suffix
	}
	return strings.TrimRight(u.Path, "/") + suffix
}

func setSSOCookie(c *gin.Context, name, value, path string, maxAge time.Duration, secure bool) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name: name, Value: value, Path: path, MaxAge: int(maxAge.Seconds()),
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode,
	})
}

func clearSSOCookie(c *gin.Context, name, path string, secure bool) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name: name, Value: "", Path: path, MaxAge: -1,
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode,
	})
}

// secureFor:回调地址是 https 时 Cookie 只走 https。看配置而不是看这次请求:网关常在反向代理
// 后面,请求到网关时已经是 http 了。
func secureFor(cfg service.SSOSettings) bool {
	u, err := url.Parse(cfg.RedirectURL)
	return err == nil && u.Scheme == "https"
}

// loginPageURL 是网关登录页(hash 路由),带上一个查询参数。base 为空(SSO 没配)时用相对网关根的地址。
func loginPageURL(cfg service.SSOSettings, key, value string) string {
	return strings.TrimRight(cfg.GatewayBaseURL(), "/") + "/#/login?" + key + "=" + url.QueryEscape(value)
}

// SSOConfig godoc
// @Summary 登录页要不要露出 SSO 入口
// @Router  /auth/sso/config [get]
//
// 公开接口:登录页在未登录时就要知道。loginUrl 是网关自己的 OIDC 发起地址(相对网关根)。
func (h *Handler) SSOConfig(c *gin.Context) {
	cfg := h.Svc.SSOConfig()
	resp.OK(c, gin.H{
		"enabled":  cfg.Enabled,
		"loginUrl": strings.TrimPrefix(cfg.LoginURL, "/"),
		// 部署配置强制关闭了 SSO:设置页据此提示"这里的开关暂不生效"。
		"forcedOff": cfg.ForcedOff,
	})
}

// OIDCStart godoc
// @Summary 发起 SSO 登录:302 到身份服务
// @Router  /auth/oidc/start [get]
func (h *Handler) OIDCStart(c *gin.Context) {
	cfg := h.Svc.SSOConfig()
	authURL, flow, err := h.Svc.OIDCStart()
	if err != nil {
		reason := service.OIDCErrExchange
		if errors.Is(err, service.ErrSSODisabled) {
			reason = service.OIDCErrDisabled
		} else {
			slog.Warn("sso: 发起登录失败", "err", err)
		}
		c.Redirect(http.StatusFound, loginPageURL(cfg, "sso_error", reason))
		return
	}
	setSSOCookie(c, oidcFlowCookie, flow, cookiePath(cfg, oidcFlowPath), 10*time.Minute, secureFor(cfg))
	c.Redirect(http.StatusFound, authURL)
}

// OIDCCallback godoc
// @Summary SSO 回调:换 token、验 ID Token,跳回登录页
// @Router  /auth/oidc/callback [get]
//
// 成功:写登录结果 Cookie,跳回 /#/login?sso=1,由登录页 POST /auth/sso 换会话。
// 失败:跳回 /#/login?sso_error=<原因>(disabled / state / denied / exchange / email)。
func (h *Handler) OIDCCallback(c *gin.Context) {
	cfg := h.Svc.SSOConfig()
	secure := secureFor(cfg)
	ip := c.ClientIP()
	// 与登录接口共用按 IP 的频控:回调是公开的,每次 state 对不上都写一条失败审计,不限的话
	// 有人可以拿它刷审计表。被挡住时不碰流程 Cookie。
	if h.loginLim.blocked(ip, "", time.Now()) {
		c.Redirect(http.StatusFound, loginPageURL(cfg, "sso_error", service.OIDCErrExchange))
		return
	}
	flow, _ := c.Cookie(oidcFlowCookie)
	// 流程 Cookie 一次性:不论成败都清掉,同一个 state 不能用第二次。
	clearSSOCookie(c, oidcFlowCookie, cookiePath(cfg, oidcFlowPath), secure)
	ticket, err := h.Svc.OIDCCallback(flow, c.Query("state"), c.Query("code"), c.Query("error"), ip)
	if err != nil {
		reason := service.OIDCErrExchange
		var oe *service.OIDCError
		if errors.As(err, &oe) {
			reason = oe.Reason
		}
		// state 对不上、换 token / 验签失败计入频控;用户在身份服务取消、没有邮箱不算 ——
		// 那不是在试探网关。
		if reason == service.OIDCErrState || reason == service.OIDCErrExchange {
			h.loginLim.fail(ip, "", time.Now())
		}
		if reason != service.OIDCErrDenied && reason != service.OIDCErrDisabled {
			slog.Warn("sso: 回调失败", "reason", reason, "err", err)
		}
		c.Redirect(http.StatusFound, loginPageURL(cfg, "sso_error", reason))
		return
	}
	setSSOCookie(c, ssoTicketCookie, ticket, cookiePath(cfg, ssoTicketPath), service.SSOTicketTTL, secure)
	c.Redirect(http.StatusFound, loginPageURL(cfg, "sso", "1"))
}

// SSOLogin godoc
// @Summary 用 SSO 登录结果换网关会话
// @Router  /auth/sso [post]
//
// 登录结果在回调写下的 HttpOnly Cookie 里,浏览器随请求带上;前端脚本不需要、也读不到它。
func (h *Handler) SSOLogin(c *gin.Context) {
	ip := c.ClientIP()
	if h.loginLim.blocked(ip, "", time.Now()) {
		resp.Fail(c, resp.CodeTooManyReqs, "登录尝试过多,请稍后再试")
		return
	}
	cfg := h.Svc.SSOConfig()
	secure := secureFor(cfg)
	ticket, _ := c.Cookie(ssoTicketCookie)
	// 请求体可选:已绑定 TOTP 的管理员第二步带上 mfaCode(M8);其余人发空对象。
	var req struct {
		MfaCode string `json:"mfaCode"`
	}
	_ = c.ShouldBindJSON(&req)
	jwtTok, exp, u, err := h.Svc.SSOLogin(ticket, strings.TrimSpace(req.MfaCode), ip)
	// 登录结果只用一次:除了"还要输 TOTP"(下一步还得用它),一律清掉。
	if !errors.Is(err, service.ErrMFARequired) && !errors.Is(err, service.ErrMFAInvalid) && !errors.Is(err, service.ErrMFACodeUsed) {
		clearSSOCookie(c, ssoTicketCookie, cookiePath(cfg, ssoTicketPath), secure)
	}
	switch {
	case err == nil:
	case errors.Is(err, service.ErrMFARequired):
		// 不计入频控:SSO 已证明了身份,这只是在要第二步。
		resp.Fail(c, resp.CodeMFARequired, "请输入 MFA 验证码")
		return
	case errors.Is(err, service.ErrMFAInvalid):
		h.loginLim.fail(ip, "", time.Now())
		resp.Fail(c, resp.CodeMFARequired, "MFA 验证码错误")
		return
	case errors.Is(err, service.ErrMFACodeUsed):
		h.loginLim.fail(ip, "", time.Now())
		resp.Fail(c, resp.CodeMFARequired, "该验证码已使用过,请等待下一个验证码")
		return
	case errors.Is(err, service.ErrMFALocked):
		resp.Fail(c, resp.CodeTooManyReqs, mfaLockedMsg)
		return
	case errors.Is(err, service.ErrSSODisabled):
		resp.Fail(c, resp.CodeForbidden, "未启用 SSO 登录")
		return
	case errors.Is(err, service.ErrSSONoSession):
		// 不计入频控:没有登录结果是正常情况(还没去身份服务,或者结果已过期)。
		resp.Fail(c, resp.CodeSSONoSession, "未检测到 SSO 登录结果")
		return
	case errors.Is(err, service.ErrSSONoAccount):
		// 不计入频控:身份是身份服务刚证明过的,不是在猜口令;再试多少次结论都一样。
		resp.Fail(c, resp.CodeSSONoAccount, "你还没有开通网关账号,无权限访问。请联系管理员开通后再登录")
		return
	case errors.Is(err, service.ErrUserDisabled):
		h.loginLim.fail(ip, "", time.Now())
		resp.Fail(c, resp.CodeForbidden, "该账号已在网关中停用,请联系管理员")
		return
	default:
		h.loginLim.fail(ip, "", time.Now())
		resp.Fail(c, resp.CodeUnauthorized, "SSO 登录失败,请重新登录")
		return
	}
	// 不清 IP 计数:成功登录清掉的只能是**这个账号自己**的失败(见 loginThrottle),而 SSO
	// 这条路只按 IP 计数 —— 拿自己的 SSO 登一次,不该替人把门槛归零。
	me, _ := h.Svc.BuildMe(u)
	respToken, session := h.deliverSession(c, jwtTok, exp)
	resp.OK(c, dto.LoginResp{Token: respToken, Session: session, ExpiresAt: exp.Format("2006-01-02 15:04:05"), User: *me})
}

// SSOUnbind godoc
// @Summary 解除一个账号的 SSO 绑定(恢复口令登录)
// @Router  /users/{id}/sso-unbind [post]
func (h *Handler) SSOUnbind(c *gin.Context) {
	if err := h.Svc.SSOUnbind(middleware.CurrentUser(c), pathID(c)); err != nil {
		if errors.Is(err, service.ErrNotFound) {
			resp.Fail(c, resp.CodeBadRequest, "用户不存在")
			return
		}
		resp.Fail(c, resp.CodeInternalError, "解绑失败")
		return
	}
	resp.OK(c, gin.H{"ok": true})
}
