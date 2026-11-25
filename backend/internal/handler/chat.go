package handler

import (
	"errors"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"aegisdb/internal/apimsg"
	"aegisdb/internal/chat"
	"aegisdb/internal/dto"
	"aegisdb/internal/middleware"
	"aegisdb/internal/service"
	"aegisdb/pkg/resp"
)

// chatChannel 取路径里的渠道,不认识的当场拒绝。
func chatChannel(c *gin.Context) (chat.Channel, bool) {
	ch := chat.Channel(c.Param("channel"))
	if !slices.Contains(chat.Channels, ch) {
		resp.FailErr(c, resp.CodeBadRequest, service.ErrChatUnknown)
		return "", false
	}
	return ch, true
}

// ChatBindings godoc
// @Summary 本人在各个审批机器人渠道上的绑定状态(只列开着的渠道)
// @Router  /auth/chat-bindings [get]
func (h *Handler) ChatBindings(c *gin.Context) {
	resp.OK(c, h.Svc.ChatBindings(middleware.CurrentUser(c).ID))
}

// ChatBindCode godoc
// @Summary 生成一次性绑定码(10 分钟有效;启用了 MFA 的账号要带验证码)
// @Router  /auth/chat-bindings/{channel}/code [post]
func (h *Handler) ChatBindCode(c *gin.Context) {
	ch, ok := chatChannel(c)
	if !ok {
		return
	}
	var req dto.ChatBindCodeReq
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误")
		return
	}
	code, expires, err := h.Svc.NewChatBindCode(middleware.CurrentUser(c), ch, req.MfaCode)
	switch {
	case err == nil:
		resp.OK(c, dto.ChatBindCodeResp{Code: code, ExpiresAt: expires.Format(time.RFC3339), BotName: h.Svc.ChatBotName(ch)})
	case errors.Is(err, service.ErrMFALocked):
		resp.Fail(c, resp.CodeTooManyReqs, mfaLockedMsg)
	case errors.Is(err, service.ErrMFARequired) && strings.TrimSpace(req.MfaCode) == "":
		resp.Fail(c, resp.CodeMFARequired, "请输入 MFA 验证码")
	case errors.Is(err, service.ErrMFARequired):
		resp.Fail(c, resp.CodeBadRequest, "验证码错误,请重试")
	case errors.Is(err, service.ErrChatTooManyGens):
		resp.FailErr(c, resp.CodeTooManyReqs, err)
	case errors.Is(err, service.ErrChatOff), errors.Is(err, service.ErrChatNotReady),
		errors.Is(err, service.ErrChatHumanOnly), errors.Is(err, service.ErrChatUserChanged):
		resp.FailErr(c, resp.CodeBadRequest, err)
	default:
		slog.Error("generate chat bind code failed", "channel", ch, "user", middleware.CurrentUser(c).ID, "err", err)
		resp.Fail(c, resp.CodeInternalError, "生成失败")
	}
}

// ChatUnbind godoc
// @Summary 本人解除一个渠道的绑定(没绑也算成功)
// @Router  /auth/chat-bindings/{channel} [delete]
func (h *Handler) ChatUnbind(c *gin.Context) {
	ch, ok := chatChannel(c)
	if !ok {
		return
	}
	u := middleware.CurrentUser(c)
	if err := h.Svc.UnbindChat(u, u.ID, ch, apimsg.Text("你在网关里解除了绑定")); err != nil {
		slog.Error("chat unbind failed", "channel", ch, "user", u.ID, "err", err)
		resp.Fail(c, resp.CodeInternalError, "解绑失败")
		return
	}
	resp.OK(c, gin.H{"ok": true})
}

// ChatUnbindUser godoc
// @Summary 管理员解除某个用户的全部审批机器人绑定
// @Router  /users/{id}/chat-bindings [delete]
func (h *Handler) ChatUnbindUser(c *gin.Context) {
	err := h.Svc.UnbindChat(middleware.CurrentUser(c), pathID(c), "", apimsg.Text("管理员解除了绑定"))
	switch {
	case err == nil:
		resp.OK(c, gin.H{"ok": true})
	case errors.Is(err, service.ErrNotFound):
		resp.Fail(c, resp.CodeBadRequest, "用户不存在")
	default:
		slog.Error("admin chat unbind failed", "user", pathID(c), "err", err)
		resp.Fail(c, resp.CodeInternalError, "解绑失败")
	}
}

// ChatStatus godoc
// @Summary 各审批机器人渠道的连接状态(不含任何密钥)
// @Router  /settings/chat-status [get]
func (h *Handler) ChatStatus(c *gin.Context) { resp.OK(c, h.Svc.ChatStatus()) }

// ChatTest godoc
// @Summary 给自己发一张测试卡片(须先绑定)
// @Router  /settings/chat-test [post]
func (h *Handler) ChatTest(c *gin.Context) {
	var req dto.ChatTestReq
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误")
		return
	}
	if !slices.Contains(chat.Channels, req.Channel) {
		resp.FailErr(c, resp.CodeBadRequest, service.ErrChatUnknown)
		return
	}
	switch err := h.Svc.SendChatTest(c.Request.Context(), middleware.CurrentUser(c), req.Channel); {
	case err == nil:
		resp.OK(c, gin.H{"ok": true})
	case errors.Is(err, service.ErrChatTestTooOften):
		resp.FailErr(c, resp.CodeTooManyReqs, err)
	default:
		resp.FailErr(c, resp.CodeBadRequest, err)
	}
}
