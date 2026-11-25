package handler

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/gin-gonic/gin"

	"aegisdb/internal/dto"
	"aegisdb/internal/middleware"
	"aegisdb/internal/service"
	"aegisdb/pkg/resp"
)

// MyQueryGrants 当前用户自己生效中 / 未到开始时间的查询授权(数据查询页左侧)。
func (h *Handler) MyQueryGrants(c *gin.Context) {
	resp.OK(c, gin.H{"grants": h.Svc.MyQueryGrants(middleware.CurrentUser(c)), "limits": h.Svc.BizQueryLimits()})
}

// BizTables 列出授权库里的表(数据查询页的"表"下拉)。判定拒绝与执行接口同一套回答(40300 + reasonRef)。
func (h *Handler) BizTables(c *gin.Context) {
	sourceID, _ := strconv.ParseInt(c.Query("sourceId"), 10, 64)
	out, err := h.Svc.BizTables(middleware.CurrentUser(c), sourceID, c.Query("database"))
	if err != nil {
		bizFail(c, err)
		return
	}
	resp.OK(c, out)
}

// BizColumns:数据查询页自动补全用的列名(service.BizColumns)。
func (h *Handler) BizColumns(c *gin.Context) {
	sourceID, _ := strconv.ParseInt(c.Query("sourceId"), 10, 64)
	out, err := h.Svc.BizColumns(middleware.CurrentUser(c), sourceID, c.Query("database"), c.Query("table"))
	if errors.Is(err, service.ErrBadRequest) {
		resp.Fail(c, resp.CodeBadRequest, "表名不合法")
		return
	}
	if err != nil {
		bizFail(c, err)
		return
	}
	resp.OK(c, out)
}

// bizFail 把业务查询的失败写成信封:判定拒绝 40300、并发已满 42900(两者 msg 是规范中文串,
// data.reasonRef 是机器可读的原因,见 model.RenderBizReason);导出时执行失败回 0 + data.error
// (与执行接口"做了,但没做成"一致);其余 50000。
func bizFail(c *gin.Context, err error) {
	var d *service.BizQueryDenial
	var f *service.BizQueryFailure
	switch {
	case errors.As(err, &d):
		code := resp.CodeForbidden
		if errors.Is(err, service.ErrBizQueryBusy) {
			code = resp.CodeTooManyReqs
		}
		resp.FailData(c, code, d.Error(), gin.H{"reasonRef": d.Ref})
	case errors.As(err, &f):
		resp.OK(c, gin.H{"error": f.Msg, "errorRef": f.Ref, "timedOut": f.TimedOut})
	default:
		resp.FailErr(c, resp.CodeInternalError, err)
	}
}

// BizQueryExec 执行一条业务查询。判定层面的拒绝(没授权、不是只读语句)回 40300,并发已满回
// 42900;执行层面的失败(超时、库报错)回 0,原因在 data.error —— 前者是"你不能这么做",
// 后者是"做了,但没做成"。
//
// 用请求自己的 context:浏览器关掉页面或网络断开,查询随之取消,MySQL 系还会发 KILL QUERY。
func (h *Handler) BizQueryExec(c *gin.Context) {
	var req dto.BizQueryReq
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误")
		return
	}
	out, err := h.Svc.BizQueryExec(c.Request.Context(), middleware.CurrentUser(c), req)
	if err != nil {
		bizFail(c, err)
		return
	}
	resp.OK(c, out)
}

// BizQueryExport 把一次业务查询的结果下载为 CSV。成功是文件本体;失败仍是 JSON 信封(错误码同
// BizQueryExec;查询本身失败时 code 为 0、原因在 data.error),前端按 Content-Type 区分。
func (h *Handler) BizQueryExport(c *gin.Context) {
	var req dto.BizQueryReq
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误")
		return
	}
	name, body, err := h.Svc.BizQueryExport(c.Request.Context(), middleware.CurrentUser(c), req)
	if err != nil {
		bizFail(c, err)
		return
	}
	c.Header("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(name))
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, "text/csv; charset=utf-8", body)
}

// ListQueryGrants 授权总表(控制台):谁、哪个库、到期时间、来源系统。可按邮箱、部门、实例、库、状态筛选。
func (h *Handler) ListQueryGrants(c *gin.Context) {
	src, _ := strconv.ParseInt(c.Query("sourceId"), 10, 64)
	out, err := h.Svc.ListQueryGrants(service.QueryGrantQuery{
		Email: c.Query("email"), Dept: c.Query("dept"), SourceID: src, Database: c.Query("database"), Status: c.Query("status"),
	})
	if err != nil {
		resp.FailErr(c, resp.CodeInternalError, err)
		return
	}
	resp.OK(c, gin.H{"items": out, "limits": h.Svc.BizQueryLimits()})
}

// RevokeQueryGrant 管理员撤销一条查询授权,立即生效,写审计。
func (h *Handler) RevokeQueryGrant(c *gin.Context) {
	var body dto.QueryGrantRevokeReq
	_ = c.ShouldBindJSON(&body)
	v, err := h.Svc.AdminRevokeQueryGrant(middleware.CurrentUser(c), pathID(c), body.Reason)
	if err != nil {
		if errors.Is(err, service.ErrNotFound) {
			resp.Fail(c, resp.CodeBadRequest, "授权不存在")
			return
		}
		resp.FailErr(c, resp.CodeInternalError, err)
		return
	}
	resp.OK(c, v)
}
