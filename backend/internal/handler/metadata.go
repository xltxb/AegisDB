package handler

// 元数据缓存的读接口。
//
// 三个:检索(跨实例)、看一台实例的清单、立刻同步一台。范围一律由 service 按标签
// 授权收口 —— 表名和列名本身就是信息,够不到那台实例的人不该看见它有哪些表。

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"velagateway/internal/middleware"
	"velagateway/internal/service"
	"velagateway/pkg/resp"
)

// SearchMetadata godoc
// @Summary 在元数据缓存里按表名/列名检索
// @Router  /metadata/search [get]
func (h *Handler) SearchMetadata(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "200"))
	tables, cols, err := h.Svc.SearchMetadata(middleware.CurrentUser(c), c.Query("q"), limit)
	if err != nil {
		resp.Fail(c, resp.CodeBadRequest, err.Error())
		return
	}
	resp.OK(c, gin.H{"tables": tables, "columns": cols})
}

// ConnectionMetadata godoc
// @Summary 一台实例缓存下来的表清单与最近一次同步的结果
// @Router  /connections/{id}/metadata [get]
func (h *Handler) ConnectionMetadata(c *gin.Context) {
	tables, state, err := h.Svc.ConnectionMetadata(middleware.CurrentUser(c), pathID(c), c.Query("database"))
	if err != nil {
		resp.Fail(c, metaErrCode(err), err.Error())
		return
	}
	// sync 为空表示这台实例还没同步过 —— 与"同步过但一张表都没有"不是一回事,
	// 界面要能分开说。
	resp.OK(c, gin.H{"tables": tables, "sync": state})
}

// SyncConnectionMetadata godoc
// @Summary 立刻同步一台实例的元数据
// @Router  /connections/{id}/metadata/sync [post]
func (h *Handler) SyncConnectionMetadata(c *gin.Context) {
	if err := h.Svc.SyncConnectionMetadata(middleware.CurrentUser(c), pathID(c)); err != nil {
		resp.Fail(c, metaErrCode(err), err.Error())
		return
	}
	tables, state, _ := h.Svc.ConnectionMetadata(middleware.CurrentUser(c), pathID(c), "")
	resp.OK(c, gin.H{"tables": len(tables), "sync": state})
}

func metaErrCode(err error) int {
	switch err {
	case service.ErrForbidden:
		return resp.CodeForbidden
	case service.ErrNotFound:
		return resp.CodeBadRequest
	}
	return resp.CodeBadRequest
}
