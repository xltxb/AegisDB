package handler

// 开放接口 · 查询授权(外部审批系统审批完,调这里下发)。见 service/query_grant.go。

import (
	"errors"
	"strconv"

	"github.com/gin-gonic/gin"

	"velagateway/internal/apimsg"
	"velagateway/internal/dto"
	"velagateway/internal/middleware"
	"velagateway/internal/service"
	"velagateway/pkg/resp"
)

func grantErr(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrGrantInvalid):
		resp.FailErr(c, resp.CodeBadRequest, err)
	case errors.Is(err, service.ErrNotFound):
		resp.Fail(c, resp.CodeBadRequest, "授权或实例不存在")
	case errors.Is(err, service.ErrForbidden):
		resp.Fail(c, resp.CodeForbidden, "该凭据绑定的服务账号无权访问此实例")
	default:
		resp.FailErr(c, resp.CodeInternalError, err)
	}
}

// OpenListQuerySources 列出可授权的业务查询源。
func (h *Handler) OpenListQuerySources(c *gin.Context) {
	resp.OK(c, h.Svc.QuerySources(middleware.CurrentUser(c)))
}

// OpenListSourceDatabases 列出一个业务查询源上的库名。
func (h *Handler) OpenListSourceDatabases(c *gin.Context) {
	dbs, err := h.Svc.SourceDatabases(middleware.CurrentUser(c), pathID(c))
	if err != nil {
		grantErr(c, err)
		return
	}
	resp.OK(c, dbs)
}

// OpenUpsertQueryGrant 下发或更新一条查询授权。
func (h *Handler) OpenUpsertQueryGrant(c *gin.Context) {
	var req dto.QueryGrantReq
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.FailErr(c, resp.CodeBadRequest, apimsg.Errorf("参数错误: %s", err))
		return
	}
	v, created, err := h.Svc.UpsertQueryGrant(middleware.CurrentAPIClient(c), middleware.CurrentUser(c), req)
	if err != nil {
		grantErr(c, err)
		return
	}
	resp.OK(c, gin.H{"created": created, "grant": v})
}

// OpenRevokeQueryGrant 按 id 撤销。
func (h *Handler) OpenRevokeQueryGrant(c *gin.Context) {
	var body dto.QueryGrantRevokeReq
	_ = c.ShouldBindJSON(&body)
	out, err := h.Svc.RevokeQueryGrant(middleware.CurrentAPIClient(c), middleware.CurrentUser(c), pathID(c), "", body.Reason)
	if err != nil {
		grantErr(c, err)
		return
	}
	resp.OK(c, out)
}

// OpenRevokeQueryGrantByExternal 按外部单号撤销(只在本系统下发的授权里找)。
func (h *Handler) OpenRevokeQueryGrantByExternal(c *gin.Context) {
	var body dto.QueryGrantRevokeReq
	_ = c.ShouldBindJSON(&body)
	out, err := h.Svc.RevokeQueryGrant(middleware.CurrentAPIClient(c), middleware.CurrentUser(c), 0, c.Query("externalId"), body.Reason)
	if err != nil {
		grantErr(c, err)
		return
	}
	resp.OK(c, out)
}

// OpenListQueryGrants 查询授权 —— 只列本系统(这把凭据)下发的。
func (h *Handler) OpenListQueryGrants(c *gin.Context) {
	src, _ := strconv.ParseInt(c.Query("sourceId"), 10, 64)
	out, err := h.Svc.ListQueryGrants(service.QueryGrantQuery{
		Email: c.Query("email"), SourceID: src, Database: c.Query("database"),
		Status: c.Query("status"), ExternalID: c.Query("externalId"),
		ClientID: middleware.CurrentAPIClient(c).ID,
	})
	if err != nil {
		grantErr(c, err)
		return
	}
	resp.OK(c, out)
}
