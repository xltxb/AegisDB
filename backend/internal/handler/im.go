package handler

import (
	"errors"
	"strings"

	"github.com/gin-gonic/gin"

	"velagateway/internal/middleware"
	"velagateway/internal/repository"
	"velagateway/internal/service"
	"velagateway/pkg/resp"
)

// imFail 把 IM 相关的错误说成人话。
func imFail(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrNotFound):
		resp.Fail(c, resp.CodeNotFound, "渠道或用户不存在")
	case errors.Is(err, service.ErrIMChannelOff):
		resp.Fail(c, resp.CodeBadRequest, "该渠道未启用交互审批,机器人不在运行,请联系管理员")
	case errors.Is(err, repository.ErrIMIdentityTaken):
		resp.Fail(c, resp.CodeBadRequest, "该平台账号已绑定到其他网关账户")
	case errors.Is(err, service.ErrBadRequest):
		resp.Fail(c, resp.CodeBadRequest, "平台账号 ID 不能为空,且不超过 128 个字符")
	default:
		resp.Fail(c, resp.CodeInternalError, "操作失败")
	}
}

func (h *Handler) MyIMIdentities(c *gin.Context) {
	resp.OK(c, h.Svc.IMIdentities(middleware.CurrentUser(c).ID))
}

func (h *Handler) IssueMyIMBindCode(c *gin.Context) {
	ch := c.Param("channel")
	code, exp, err := h.Svc.IssueIMBindCode(middleware.CurrentUser(c), ch)
	if err != nil {
		imFail(c, err)
		return
	}
	// 码只在这里出现一次。库里只有它的哈希,没有第二个接口能再取。
	resp.OK(c, gin.H{"code": code, "expiresAt": exp, "botName": h.Svc.IM.BotName(ch)})
}

func (h *Handler) UnbindMyIM(c *gin.Context) {
	if err := h.Svc.UnbindOwnIM(middleware.CurrentUser(c), c.Param("channel")); err != nil {
		imFail(c, err)
		return
	}
	resp.OK(c, gin.H{"ok": true})
}

func (h *Handler) UserIMIdentities(c *gin.Context) {
	resp.OK(c, h.Svc.IMIdentities(pathID(c)))
}

func (h *Handler) BindUserIM(c *gin.Context) {
	var body struct {
		PlatformUserID string `json:"platformUserId"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误")
		return
	}
	if err := h.Svc.AdminBindIM(middleware.CurrentUser(c), pathID(c), c.Param("channel"), strings.TrimSpace(body.PlatformUserID)); err != nil {
		imFail(c, err)
		return
	}
	resp.OK(c, gin.H{"ok": true})
}

func (h *Handler) UnbindUserIM(c *gin.Context) {
	if err := h.Svc.AdminUnbindIM(middleware.CurrentUser(c), pathID(c), c.Param("channel")); err != nil {
		imFail(c, err)
		return
	}
	resp.OK(c, gin.H{"ok": true})
}

// TestIMNotify 往某渠道的通知群发一条样例。只用已保存的配置 —— 界面先保存再测。
func (h *Handler) TestIMNotify(c *gin.Context) {
	ok, msg := h.Svc.TestIMNotify(c.Param("channel"))
	resp.OK(c, gin.H{"ok": ok, "message": msg})
}
