package handler

import (
	"github.com/gin-gonic/gin"

	"velagateway/internal/middleware"
	"velagateway/internal/service"
	"velagateway/pkg/resp"
)

// ExecuteApproval runs an approved ticket's command, at the initiator's request.
//
// 它是一个独立的动作,不是 approve 的副作用:审批人按下的是"我同意",执行时机由
// 发起人决定 —— 业务低峰、应用是否已停、备份是否就绪,只有他知道。
func (h *Handler) ExecuteApproval(c *gin.Context) {
	res, err := h.Svc.ExecuteApproved(middleware.CurrentUser(c), pathID(c))
	if err == service.ErrNotFound {
		resp.Fail(c, resp.CodeBadRequest, "工单不存在")
		return
	}
	if err == service.ErrForbidden {
		resp.Fail(c, resp.CodeForbidden, "无权执行该工单")
		return
	}
	if err != nil {
		// 拒绝的理由原样透出:等审批、找发起人、看发布单、重新提交 —— 四种拒绝
		// 要人做的事完全不同,笼统一句"执行失败"会让人去修错的东西。
		resp.Fail(c, resp.CodeBadRequest, err.Error())
		return
	}
	resp.OK(c, res)
}
