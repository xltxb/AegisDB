package handler

import (
	"errors"
	"time"

	"github.com/gin-gonic/gin"

	"velagateway/internal/dto"
	"velagateway/internal/middleware"
	"velagateway/internal/service"
	"velagateway/pkg/resp"
)

// ChangePassword godoc
// @Summary 自助修改密码:旧密码 + 新密码;成功后其它会话作废,返回重签的当前会话 token
// @Router  /auth/password [post]
func (h *Handler) ChangePassword(c *gin.Context) {
	var req dto.ChangePasswordReq
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误:需要原密码与新密码")
		return
	}
	u := middleware.CurrentUser(c)
	// 猜旧口令与猜登录口令是同一件事:与登录共用频控(按来源 IP + 账号)。
	ip := c.ClientIP()
	if h.loginLim.blocked(ip, u.Email, time.Now()) {
		resp.Fail(c, resp.CodeTooManyReqs, "尝试过多,请稍后再试")
		return
	}
	token, exp, err := h.Svc.ChangeOwnPassword(u, req.OldPassword, req.NewPassword)
	switch {
	case err == nil:
		h.loginLim.succeed(ip, u.Email)
		respToken, session := h.deliverSession(c, token, exp)
		resp.OK(c, dto.ChangePasswordResp{Token: respToken, Session: session, ExpiresAt: exp.Format("2006-01-02 15:04:05")})
	case errors.Is(err, service.ErrNoLocalPassword):
		resp.Fail(c, resp.CodeBadRequest, "该账号没有本地口令(SSO / 业务查询账号通过授权登录进入网关),无需也不能修改密码")
	case errors.Is(err, service.ErrOldPasswordWrong):
		h.loginLim.fail(ip, u.Email, time.Now())
		resp.Fail(c, resp.CodeBadRequest, "原密码错误")
	case errors.Is(err, service.ErrPasswordTooShort):
		resp.Fail(c, resp.CodeBadRequest, "新密码至少 8 位")
	case errors.Is(err, service.ErrPasswordUnchanged):
		resp.Fail(c, resp.CodeBadRequest, "新密码不能与原密码相同")
	case errors.Is(err, service.ErrPasswordTooCommon):
		// 口令策略(d4a7e36)新加的拒绝理由,从前落到 default 报成 50000「修改失败」,像服务器故障
		resp.Fail(c, resp.CodeBadRequest, "密码过于常见,请换一个")
	default:
		resp.Fail(c, resp.CodeInternalError, "修改失败")
	}
}
