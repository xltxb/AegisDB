package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"aegisdb/internal/dto"
	"aegisdb/internal/middleware"
	"aegisdb/internal/service"
	"aegisdb/pkg/resp"
)

// 执行窗口(「班车」)的管理接口。
//
// 读是所有能进这个菜单的人都能看的 —— 一扇免审批的门开在哪、什么时候开,不该是只有
// 管理员才知道的事。写(建/改/删)限管理员,并且每一次都进审计链。

// ListExecWindows godoc
// @Summary 执行窗口列表
// @Router  /exec-windows [get]
func (h *Handler) ListExecWindows(c *gin.Context) {
	u := middleware.CurrentUser(c)
	// scope=active:此刻开着的窗口,所有人的 —— 用户后台总览的「执行窗口生效中」面板(人人可读)
	if c.Query("scope") == "active" {
		ws, err := h.Svc.ListActiveExecWindows()
		if err != nil {
			resp.Fail(c, resp.CodeInternalError, "加载失败")
			return
		}
		resp.OK(c, ws)
		return
	}
	// scope=scheduled:已批准、还没到点的窗口,所有人的 —— 总览「执行窗口排期」(人人可读,2026-10-19)
	if c.Query("scope") == "scheduled" {
		ws, err := h.Svc.ListScheduledExecWindows()
		if err != nil {
			resp.Fail(c, resp.CodeInternalError, "加载失败")
			return
		}
		resp.OK(c, ws)
		return
	}
	// scope=reviewable:用户后台班车页的「全部窗口」页签,只给有回看权限的人(含已过期,只读;
	// 2026-10-26 回归排查 08)。没有回看权限 40300。
	if c.Query("scope") == "reviewable" {
		ws, err := h.Svc.ListReviewableExecWindows(u)
		if errors.Is(err, service.ErrForbidden) {
			resp.Fail(c, resp.CodeForbidden, "没有回看执行窗口的权限")
			return
		}
		if err != nil {
			resp.Fail(c, resp.CodeInternalError, "加载失败")
			return
		}
		resp.OK(c, ws)
		return
	}
	// scope=all:管理后台的全局列表,只给平台管理员;默认只列自己申请的(工单 07)
	all := c.Query("scope") == "all"
	if all && !h.Svc.IsPlatformAdmin(u) {
		resp.Fail(c, resp.CodeForbidden, "仅平台管理员可查看所有人的执行窗口")
		return
	}
	if all && !middleware.AdminIPAllowed(h.Repo, c) { // 全局列表是管理后台的接口(工单 08)
		resp.Fail(c, resp.CodeAdminIPBlocked, "当前 IP 不在管理后台白名单内")
		return
	}
	if all && !middleware.AdminSessionFresh(h.Repo, c) { // 管理后台空闲超时(工单 09)
		resp.Fail(c, resp.CodeAdminIdle, "管理后台已空闲超时,请重新进入")
		return
	}
	ws, err := h.Svc.ListExecWindowsFor(u, all)
	if err != nil {
		resp.Fail(c, resp.CodeInternalError, "加载失败")
		return
	}
	resp.OK(c, ws)
}

// ExecWindowStatements godoc
// @Summary 回看窗口期里执行过的 SQL
// @Router  /exec-windows/{id}/statements [get]
//
// 这一页**不按人收窄**:打得开它的人,看得见那段窗口期里所有人跑过的语句原文,而不
// 只是自己的。与审计页分道是刻意的 —— 一扇免审批的门没有签字的人,一份只剩自己那两
// 条的回看,读起来和"没出什么事"一模一样(理由写在 service.WindowStatements)。
//
// 门槛因此挪到"谁打得开这一页":execwindow 菜单的持有者,或监督角色。那条判断在
// service(canReviewWindow)—— 路由只认得菜单键,认不出角色。
func (h *Handler) ExecWindowStatements(c *gin.Context) {
	res, err := h.Svc.WindowStatements(middleware.CurrentUser(c), pathID(c))
	if err != nil {
		respondWindowErr(c, err)
		return
	}
	resp.OK(c, res)
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
	case service.ErrWindowSpan:
		resp.FailErr(c, resp.CodeBadRequest, service.ErrWindowSpan)
	case service.ErrWindowTooManyDBs:
		resp.FailErr(c, resp.CodeBadRequest, service.ErrWindowTooManyDBs)
	case service.ErrNotFound:
		resp.Fail(c, http.StatusNotFound, "窗口或目标实例不存在")
	case service.ErrForbidden:
		// 说清是"这一页不归你看",而不是"操作失败" —— 后者会让人以为是系统出了岔子,
		// 转头再点十次。
		resp.Fail(c, resp.CodeForbidden, "无权查看:回看限执行窗口菜单的持有者与监督角色")
	default:
		resp.Fail(c, resp.CodeInternalError, "操作失败")
	}
}

// DisableExecWindow godoc
// @Summary 停用执行窗口(立即不再放行,不走审批)
// @Router  /exec-windows/{id}/disable [post]
func (h *Handler) DisableExecWindow(c *gin.Context) {
	w, err := h.Svc.DisableExecWindow(middleware.CurrentUser(c), pathID(c))
	switch {
	case errors.Is(err, service.ErrNotFound):
		resp.Fail(c, resp.CodeBadRequest, "窗口不存在")
	case errors.Is(err, service.ErrForbidden):
		resp.Fail(c, resp.CodeForbidden, "只有申请人或平台管理员可以停用这个窗口")
	case err != nil:
		resp.Fail(c, resp.CodeInternalError, "停用失败")
	default:
		resp.OK(c, w)
	}
}
