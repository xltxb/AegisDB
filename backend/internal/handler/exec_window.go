package handler

import (
	"errors"
	"github.com/gin-gonic/gin"
	"strconv"

	"velagateway/internal/dto"
	"velagateway/internal/middleware"
	"velagateway/internal/service"
	"velagateway/pkg/resp"
)

// 执行窗口(「班车」)的管理接口。
//
// 读是所有能进这个菜单的人都能看的 —— 一扇免审批的门开在哪、什么时候开,不该是只有
// 管理员才知道的事。
//
// 写(建/改/删)**不限管理员**:拿到 execwindow 菜单的人都能提,因为开窗口本来就要走
// 审批,而能不能改某一扇门由 canManageWindow 判(申请人自己或平台管理员)—— 把写死
// 在管理员上,等于让真正需要窗口的那批人连申请都提不了(ADR 0015 修订)。
// 每一次变动都进审计链。

// ListExecWindows godoc
// @Summary 执行窗口列表
// @Router  /exec-windows [get]
func (h *Handler) ListExecWindows(c *gin.Context) {
	ws, err := h.Svc.ListExecWindows()
	if err != nil {
		resp.Fail(c, resp.CodeInternalError, "加载失败")
		return
	}
	resp.OK(c, ws)
}

// ExecWindowAudit godoc
// @Summary 这扇执行窗口放行过的命令
// @Router  /exec-windows/{id}/audit [get]
func (h *Handler) ExecWindowAudit(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		resp.Fail(c, resp.CodeBadRequest, "参数错误")
		return
	}
	rows, err := h.Svc.AuditForWindow(middleware.CurrentUser(c), id)
	if errors.Is(err, service.ErrForbidden) {
		resp.Fail(c, resp.CodeForbidden, "需要审计查看权限")
		return
	}
	if err != nil {
		resp.Fail(c, resp.CodeInternalError, "加载失败")
		return
	}
	resp.OK(c, rows)
}

// CreateExecWindow godoc
// @Summary 新建执行窗口
// @Router  /exec-windows [post]
func (h *Handler) CreateExecWindow(c *gin.Context) {
	var req dto.ExecWindowReq
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误")
		return
	}
	w, err := h.Svc.CreateExecWindow(middleware.CurrentUser(c), req)
	if err != nil {
		respondWindowErr(c, err)
		return
	}
	resp.OK(c, w)
}

// UpdateExecWindow godoc
// @Summary 修改执行窗口
// @Router  /exec-windows/{id} [put]
func (h *Handler) UpdateExecWindow(c *gin.Context) {
	var req dto.ExecWindowReq
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误")
		return
	}
	w, err := h.Svc.UpdateExecWindow(middleware.CurrentUser(c), pathID(c), req)
	if err != nil {
		respondWindowErr(c, err)
		return
	}
	resp.OK(c, w)
}

// DeleteExecWindow godoc
// @Summary 删除执行窗口
// @Router  /exec-windows/{id} [delete]
func (h *Handler) DeleteExecWindow(c *gin.Context) {
	if err := h.Svc.DeleteExecWindow(middleware.CurrentUser(c), pathID(c)); err != nil {
		respondWindowErr(c, err)
		return
	}
	resp.OK(c, gin.H{"ok": true})
}

// respondWindowErr 把"定义写不通"和"东西不存在"分开报 —— 前者要人改表单,后者要人
// 换目标,笼统一句"操作失败"两种都救不了。
func respondWindowErr(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrWindowInvalid):
		resp.Fail(c, resp.CodeBadRequest, "窗口定义无效:检查实例/库名、时间段与时区")
	case errors.Is(err, service.ErrNotFound):
		resp.Fail(c, resp.CodeNotFound, "窗口或目标实例不存在")
	default:
		resp.Fail(c, resp.CodeInternalError, "操作失败")
	}
}
