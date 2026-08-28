package handler

import (
	"github.com/gin-gonic/gin"

	"velagateway/internal/dto"
	"velagateway/internal/middleware"
	"velagateway/internal/service"
	"velagateway/pkg/resp"
)

// ListSensitiveColumns returns the sensitive-field rules.
//
// 读不限管理员:被脱敏的列在结果里是一串星号,"为什么这列看不到"必须有个能自己查到
// 的答案,否则每一次都要去问人。规则本身(哪张表的哪个字段是敏感的)不是秘密。
func (h *Handler) ListSensitiveColumns(c *gin.Context) {
	resp.OK(c, h.Svc.ListSensitiveColumns())
}

// SaveSensitiveColumn creates or edits one rule (PUT carries the id in the path).
func (h *Handler) SaveSensitiveColumn(c *gin.Context) {
	var req dto.SensitiveColumnReq
	_ = c.ShouldBindJSON(&req)
	var id int64
	if c.Param("id") != "" {
		id = pathID(c)
	}
	row, err := h.Svc.SaveSensitiveColumn(middleware.CurrentUser(c), id, req)
	if err == service.ErrNotFound {
		resp.Fail(c, resp.CodeBadRequest, "规则不存在")
		return
	}
	if err != nil {
		resp.Fail(c, resp.CodeBadRequest, err.Error())
		return
	}
	resp.OK(c, row)
}

// DeleteSensitiveColumn removes a rule — after which that column comes back in
// clear text, which is why the service writes it to the audit log.
func (h *Handler) DeleteSensitiveColumn(c *gin.Context) {
	if err := h.Svc.DeleteSensitiveColumn(middleware.CurrentUser(c), pathID(c)); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "删除失败")
		return
	}
	resp.OK(c, gin.H{"ok": true})
}
