package handler

import (
	"errors"
	"strconv"

	"github.com/gin-gonic/gin"

	"velagateway/internal/middleware"
	"velagateway/internal/model"
	"velagateway/internal/service"
	"velagateway/pkg/resp"
)

// oscJobView 是一个在线变更任务给界面看的样子:任务本身 + 调用人能不能控制它。
type oscJobView struct {
	*model.OSCJob
	CanControl bool `json:"canControl"`
}

// OSCJob 查看一个在线变更任务(进度、警告、结局)。够得到那台实例的人都能看。
func (h *Handler) OSCJob(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误")
		return
	}
	u := middleware.CurrentUser(c)
	j, err := h.Svc.OSCJobFor(u, id)
	switch {
	case errors.Is(err, service.ErrNotFound):
		resp.Fail(c, resp.CodeBadRequest, "任务不存在")
	case errors.Is(err, service.ErrForbidden):
		resp.Fail(c, resp.CodeForbidden, "无权查看")
	case err != nil:
		resp.Fail(c, resp.CodeInternalError, "加载失败")
	default:
		resp.OK(c, oscJobView{OSCJob: j, CanControl: h.Svc.CanControlOSC(u, j)})
	}
}

// OSCControl 暂停 / 继续 / 中止一个正在跑的在线变更(有这台实例 DDL 权限的人)。
func (h *Handler) OSCControl(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误")
		return
	}
	err = h.Svc.ControlOSC(middleware.CurrentUser(c), id, c.Param("action"))
	switch {
	case err == nil:
		resp.OK(c, gin.H{"ok": true})
	case errors.Is(err, service.ErrNotFound):
		resp.Fail(c, resp.CodeBadRequest, "任务不存在")
	case errors.Is(err, service.ErrForbidden):
		resp.Fail(c, resp.CodeForbidden, "无权操作这个在线变更")
	case errors.Is(err, service.ErrBadRequest):
		resp.Fail(c, resp.CodeBadRequest, "不支持的操作")
	default:
		resp.FailErr(c, resp.CodeBadRequest, err)
	}
}
