package handler

import (
	"errors"
	"strconv"

	"github.com/gin-gonic/gin"

	"aegisdb/internal/dto"
	"aegisdb/internal/middleware"
	"aegisdb/internal/service"
	"aegisdb/pkg/resp"
)

// 在网关里申请 / 直接开通数据查询授权(#173,见 service/query_grant_apply.go)。

func applyErr(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrGrantInvalid), errors.Is(err, service.ErrBadRequest):
		resp.FailErr(c, resp.CodeBadRequest, err)
	case errors.Is(err, service.ErrNotFound):
		resp.Fail(c, resp.CodeBadRequest, "实例不存在")
	case errors.Is(err, service.ErrForbidden):
		resp.Fail(c, resp.CodeForbidden, "无权申请")
	default:
		resp.FailErr(c, resp.CodeInternalError, err)
	}
}

// ApplySources godoc
// @Summary 可申请数据查询权限的业务查询源
// @Router  /bizquery/apply/sources [get]
func (h *Handler) ApplySources(c *gin.Context) {
	resp.OK(c, h.Svc.ApplySources())
}

// ApplySourceDatabases godoc
// @Summary 一个业务查询源上可申请的库
// @Router  /bizquery/apply/sources/{id}/databases [get]
func (h *Handler) ApplySourceDatabases(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	dbs, err := h.Svc.ApplySourceDatabases(id)
	if err != nil {
		applyErr(c, err)
		return
	}
	resp.OK(c, dbs)
}

// ApplyQueryGrant godoc
// @Summary 申请数据查询权限(生成一张审批单)
// @Router  /bizquery/apply [post]
func (h *Handler) ApplyQueryGrant(c *gin.Context) {
	var req dto.QueryGrantApplyReq
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误")
		return
	}
	v, err := h.Svc.ApplyQueryGrant(middleware.CurrentUser(c), req)
	if err != nil {
		applyErr(c, err)
		return
	}
	resp.OK(c, v)
}

// MyQueryGrantRequests godoc
// @Summary 我最近的数据查询权限申请
// @Router  /bizquery/apply/mine [get]
func (h *Handler) MyQueryGrantRequests(c *gin.Context) {
	resp.OK(c, h.Svc.MyQueryGrantRequests(middleware.CurrentUser(c)))
}

// CancelQueryGrantRequest godoc
// @Summary 撤回自己还在等审批的数据查询权限申请
// @Router  /bizquery/apply/{id}/cancel [post]
func (h *Handler) CancelQueryGrantRequest(c *gin.Context) {
	err := h.Svc.CancelQueryGrantRequest(middleware.CurrentUser(c), pathID(c))
	switch {
	case err == nil:
		resp.OK(c, gin.H{"ok": true})
	case errors.Is(err, service.ErrNotFound):
		resp.Fail(c, resp.CodeBadRequest, "申请不存在")
	case errors.Is(err, service.ErrAlreadyDecided):
		resp.Fail(c, resp.CodeBadRequest, "该申请已被处理,请刷新")
	default:
		resp.FailErr(c, resp.CodeBadRequest, err)
	}
}

// AdminCreateQueryGrant godoc
// @Summary [perms · admin] 直接给已有账号开通数据查询授权
// @Router  /query-grants [post]
func (h *Handler) AdminCreateQueryGrant(c *gin.Context) {
	var req dto.AdminQueryGrantReq
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误")
		return
	}
	v, err := h.Svc.AdminCreateQueryGrant(middleware.CurrentUser(c), req)
	if err != nil {
		applyErr(c, err)
		return
	}
	resp.OK(c, v)
}
