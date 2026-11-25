package handler

import (
	"errors"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"aegisdb/internal/dto"
	"aegisdb/internal/middleware"
	"aegisdb/internal/service"
	"aegisdb/pkg/resp"
)

// SSOConfig godoc
// @Summary 登录页要不要露出 SSO 入口
// @Router  /auth/sso/config [get]
//
// 公开接口:登录页在未登录时就要知道。loginUrl 是 {SRD}/auth/login,前端在后面拼
// ?redirect=<网关登录页> 再跳过去。
func (h *Handler) SSOConfig(c *gin.Context) {
	cfg := h.Svc.SSOConfig()
	resp.OK(c, gin.H{
		"enabled":  cfg.Enabled,
		"loginUrl": cfg.LoginURL,
		// 部署配置强制关闭了 SSO:设置页据此提示"这里的开关暂不生效"。
		"forcedOff": cfg.ForcedOff,
	})
}

// SSOLogin godoc
// @Summary 用 SSO 登录态换网关会话
// @Router  /auth/sso [post]
//
// token 从 SRD 写在公司父域上的 CS-Token Cookie 里取,不从请求体里取:浏览器随请求自动
// 带上它,网关前端脚本不需要去读、也不需要知道 SRD 的 Cookie 编码。
func (h *Handler) SSOLogin(c *gin.Context) {
	ip := c.ClientIP()
	if h.loginLim.blocked(ip, "", time.Now()) {
		resp.Fail(c, resp.CodeTooManyReqs, "登录尝试过多,请稍后再试")
		return
	}
	// 取原始值,不用 c.Cookie():gin 会对它做 QueryUnescape,把 base64 里的 + 变成空格。
	// URL 解码交给 DecodeSRDToken,它知道 + 在这里是数据。
	var token string
	if ck, err := c.Request.Cookie(h.Svc.SSOConfig().CookieName); err == nil {
		token = ck.Value
	}
	// 请求体可选:已绑定 TOTP 的管理员第二步带上 mfaCode(M8);其余人发空对象。
	var req struct {
		MfaCode string `json:"mfaCode"`
	}
	_ = c.ShouldBindJSON(&req)
	jwtTok, exp, u, err := h.Svc.SSOLogin(token, strings.TrimSpace(req.MfaCode), ip)
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
		// 不计入频控:第一次打开网关的人都会走到这里,它不是一次失败的尝试。
		resp.Fail(c, resp.CodeSSONoSession, "未检测到 SSO 登录态")
		return
	case errors.Is(err, service.ErrSSOUnavailable):
		// 也不计入频控:SSO 出事不是这个人的错,把他锁在外面只会雪上加霜。
		resp.Fail(c, resp.CodeInternalError, "SRD 授权服务暂不可用,请稍后重试;紧急情况请用管理员账号登录")
		return
	case errors.Is(err, service.ErrSSOTokenFormat):
		// 不计入频控:这是网关与 SRD 对 Cookie 编码的理解不一致,不是这个人的错。
		resp.Fail(c, resp.CodeInternalError, "无法解析 SRD 登录态 Cookie,请联系网关管理员")
		return
	case errors.Is(err, service.ErrSSONoAccount):
		// 不计入频控:身份是 SRD 刚证明过的,不是在猜口令;再试多少次结论都一样。
		resp.Fail(c, resp.CodeSSONoAccount, "你还没有开通网关账号,无权限访问。请联系管理员开通后再登录")
		return
	case errors.Is(err, service.ErrUserDisabled):
		h.loginLim.fail(ip, "", time.Now())
		resp.Fail(c, resp.CodeForbidden, "该账号已在网关中停用,请联系管理员")
		return
	default:
		h.loginLim.fail(ip, "", time.Now())
		resp.Fail(c, resp.CodeUnauthorized, "SRD 登录态无效或已过期,请重新授权登录")
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
