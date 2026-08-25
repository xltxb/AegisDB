package handler

import (
	"errors"

	"github.com/gin-gonic/gin"

	"velagateway/internal/dto"
	"velagateway/internal/middleware"
	"velagateway/internal/service"
	"velagateway/pkg/resp"
)

// 数据库规范审查规则库 (SQL review).
//
// Reads are open to anyone holding the rules menu — and the CHECK endpoint is
// open to terminal operators, because a developer being able to run the review
// against their own change before submitting it is the entire point. Only
// administrators edit the library: a rule's level decides whether a release is
// blocked, so lowering one is a policy change, not a preference.

// ListReviewRules returns the whole library.
func (h *Handler) ListReviewRules(c *gin.Context) {
	resp.OK(c, h.Svc.ListReviewRules())
}

// ReviewCatalog returns the dialect/category/level vocabulary.
func (h *Handler) ReviewCatalog(c *gin.Context) {
	resp.OK(c, h.Svc.ReviewCatalog())
}

// SaveReviewRule creates (POST) or edits (PUT /:id) one rule.
func (h *Handler) SaveReviewRule(c *gin.Context) {
	var req dto.ReviewRuleReq
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误")
		return
	}
	row, err := h.Svc.SaveReviewRule(pathID(c), req)
	if err != nil {
		// The validation messages name exactly what is wrong (bad level, malformed
		// params JSON, duplicate code) and the operator can act on every one, so
		// they are passed through rather than flattened into "保存失败".
		resp.Fail(c, reviewErrCode(err), err.Error())
		return
	}
	resp.OK(c, row)
}

// DeleteReviewRule removes a custom rule (builtins can only be disabled).
func (h *Handler) DeleteReviewRule(c *gin.Context) {
	if err := h.Svc.DeleteReviewRule(pathID(c)); err != nil {
		resp.Fail(c, reviewErrCode(err), err.Error())
		return
	}
	resp.OK(c, gin.H{"ok": true})
}

// ReviewCheck runs the library against a script, on demand.
func (h *Handler) ReviewCheck(c *gin.Context) {
	var req dto.ReviewCheckReq
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误")
		return
	}
	res, err := h.Svc.CheckSQL(middleware.CurrentUser(c), req.ConnectionID, req.Dialect, req.SQL)
	if err != nil {
		resp.Fail(c, reviewErrCode(err), err.Error())
		return
	}
	resp.OK(c, res)
}

func reviewErrCode(err error) int {
	switch {
	case errors.Is(err, service.ErrForbidden):
		return resp.CodeForbidden
	case errors.Is(err, service.ErrNotFound):
		return resp.CodeBadRequest
	default:
		return resp.CodeBadRequest
	}
}
