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

// SchemaDiffConnections 列出可以参与表结构对比的实例(调用人能访问、引擎支持)。
func (h *Handler) SchemaDiffConnections(c *gin.Context) {
	out, err := h.Svc.SchemaDiffConnections(middleware.CurrentUser(c))
	if err != nil {
		resp.Fail(c, resp.CodeInternalError, "加载失败")
		return
	}
	resp.OK(c, out)
}

// SchemaDiffDatabases 列出一个实例上可以比的库,给页面的库名下拉。读不到库列表回 0 +
// data.error(页面退回手填库名);无权访问 / 实例不存在 / 没配凭据与对比本身同样处理。
func (h *Handler) SchemaDiffDatabases(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误")
		return
	}
	names, readErr, err := h.Svc.SchemaDiffDatabases(middleware.CurrentUser(c), id)
	nameListResp(c, "databases", names, readErr, err)
}

// SchemaDiffSchemas 列出 PG / DWS 一个库(?database=)里可以比的 schema,给页面的 schema 下拉。
// 其余同 SchemaDiffDatabases;不是 PG 系回空列表。
func (h *Handler) SchemaDiffSchemas(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误")
		return
	}
	names, readErr, err := h.Svc.SchemaDiffSchemas(middleware.CurrentUser(c), id, c.Query("database"))
	nameListResp(c, "schemas", names, readErr, err)
}

// nameListResp 是库 / schema 下拉的回包:拒绝带 reasonRef;读不到回 0 + error(页面退回手填)。
func nameListResp(c *gin.Context, key string, names []string, readErr string, err error) {
	var d *service.SchemaDiffDenial
	switch {
	case errors.As(err, &d):
		code := resp.CodeBadRequest
		if d.Forbidden {
			code = resp.CodeForbidden
		}
		resp.FailData(c, code, d.Error(), gin.H{"reasonRef": d.Ref})
	case err != nil:
		resp.Fail(c, resp.CodeInternalError, "加载失败")
	case readErr != "":
		resp.OK(c, gin.H{key: []string{}, "error": readErr})
	default:
		resp.OK(c, gin.H{key: names})
	}
}

// SchemaDiff 比较两个库的表结构。无权访问一边回 40300;参数不对(实例不存在、引擎不同)回 40001;
// 读取失败回 0 + data.error(说明是哪一边)。
func (h *Handler) SchemaDiff(c *gin.Context) {
	var req dto.SchemaDiffReq
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误")
		return
	}
	out, err := h.Svc.CompareSchemas(middleware.CurrentUser(c), req)
	var d *service.SchemaDiffDenial
	switch {
	case errors.As(err, &d):
		// msg 是规范中文串;data.reasonRef 是机器可读的原因,前端按读者语言显示
		code := resp.CodeBadRequest
		if d.Forbidden {
			code = resp.CodeForbidden
		}
		resp.FailData(c, code, d.Error(), gin.H{"reasonRef": d.Ref})
	case err != nil:
		resp.Fail(c, resp.CodeInternalError, "对比失败")
	default:
		resp.OK(c, out)
	}
}
