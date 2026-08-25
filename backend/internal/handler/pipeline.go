package handler

import (
	"errors"
	"strconv"

	"github.com/gin-gonic/gin"

	"velagateway/internal/dto"
	"velagateway/internal/middleware"
	"velagateway/internal/service"
	"velagateway/pkg/resp"
)

// 发布流程 (CI/CD) HTTP surface.
//
// Templates are configuration and therefore admin-only; releases are work, so
// anyone with the pipeline menu may raise one — and the gateway's own gate
// decides what that release is allowed to do when it executes, exactly as it
// does for the terminal.

// ListPipelines returns the flow templates with their stages.
func (h *Handler) ListPipelines(c *gin.Context) {
	resp.OK(c, h.Svc.ListPipelines())
}

// GetPipeline returns one template.
func (h *Handler) GetPipeline(c *gin.Context) {
	p, err := h.Svc.GetPipeline(pathID(c))
	if err != nil {
		resp.Fail(c, resp.CodeBadRequest, "发布流程不存在")
		return
	}
	resp.OK(c, p)
}

// SavePipeline creates (POST) or replaces (PUT /:id) a template.
func (h *Handler) SavePipeline(c *gin.Context) {
	var req dto.PipelineReq
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误")
		return
	}
	p, err := h.Svc.SavePipeline(middleware.CurrentUser(c), pathID(c), req)
	if err != nil {
		resp.Fail(c, releaseErrCode(err), err.Error())
		return
	}
	resp.OK(c, p)
}

// DeletePipeline removes a template (running releases keep their snapshot).
func (h *Handler) DeletePipeline(c *gin.Context) {
	if err := h.Svc.DeletePipeline(pathID(c)); err != nil {
		resp.Fail(c, releaseErrCode(err), err.Error())
		return
	}
	resp.OK(c, gin.H{"ok": true})
}

// ListReleases returns a page of runs.
func (h *Handler) ListReleases(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", "20"))
	resp.OK(c, h.Svc.ListReleases(middleware.CurrentUser(c),
		c.DefaultQuery("scope", "mine"), c.Query("status"), page, pageSize))
}

// GetRelease returns one run with its stages — the pipeline view's data source.
func (h *Handler) GetRelease(c *gin.Context) {
	v, err := h.Svc.GetReleaseDetail(middleware.CurrentUser(c), pathID(c))
	if err != nil {
		resp.Fail(c, releaseErrCode(err), "发布单不存在或无权查看")
		return
	}
	resp.OK(c, v)
}

// CreateRelease submits a change and queues the flow.
func (h *Handler) CreateRelease(c *gin.Context) {
	var req dto.ReleaseReq
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误")
		return
	}
	rel, err := h.Svc.SubmitRelease(middleware.CurrentUser(c), req)
	if err != nil {
		// MFA gets its own code so the console can prompt for the step-up code
		// rather than showing the refusal as a validation error.
		if errors.Is(err, service.ErrMFARequired) || errors.Is(err, service.ErrMFAInvalid) {
			resp.Fail(c, resp.CodeMFARequired, "该实例需要二次验证")
			return
		}
		resp.Fail(c, releaseErrCode(err), err.Error())
		return
	}
	resp.OK(c, rel)
}

// AbortRelease stops a run that has not begun executing.
func (h *Handler) AbortRelease(c *gin.Context) {
	if err := h.Svc.AbortRelease(middleware.CurrentUser(c), pathID(c)); err != nil {
		resp.Fail(c, releaseErrCode(err), err.Error())
		return
	}
	resp.OK(c, gin.H{"ok": true})
}

// ContinueStage is the human "继续" on a manual gate.
func (h *Handler) ContinueStage(c *gin.Context) {
	stageID, _ := strconv.ParseInt(c.Param("stageId"), 10, 64)
	if err := h.Svc.ContinueManualStage(middleware.CurrentUser(c), pathID(c), stageID); err != nil {
		resp.Fail(c, releaseErrCode(err), err.Error())
		return
	}
	resp.OK(c, gin.H{"ok": true})
}

func releaseErrCode(err error) int {
	switch {
	case errors.Is(err, service.ErrForbidden):
		return resp.CodeForbidden
	case errors.Is(err, service.ErrNotFound):
		return resp.CodeBadRequest
	default:
		return resp.CodeBadRequest
	}
}
