package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"velagateway/internal/dto"
	"velagateway/internal/middleware"
	"velagateway/internal/service"
	"velagateway/pkg/resp"
)

// 执行窗口(「班车」)的管理接口。
//
// 读是所有能进这个菜单的人都能看的 —— 一扇免审批的门开在哪、什么时候开,不该是只有
// 管理员才知道的事。写(建/改/删)限管理员,并且每一次都进审计链。

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
	switch err {
	case service.ErrWindowInvalid:
		resp.Fail(c, resp.CodeBadRequest, "窗口定义无效:检查实例/库名、时间段与时区")
	case service.ErrNotFound:
		resp.Fail(c, http.StatusNotFound, "窗口或目标实例不存在")
	default:
		resp.Fail(c, resp.CodeInternalError, "操作失败")
	}
}
