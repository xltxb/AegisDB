package handler

// AI 巡检报告(.scratch/inspection-report)。

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/gin-gonic/gin"

	"velagateway/internal/apimsg"
	"velagateway/internal/dto"
	"velagateway/internal/middleware"
	"velagateway/internal/service"
	"velagateway/pkg/resp"
)

// inspectionMaxBody 是一份报告请求体的上限(JSON 摘要 + 完整 HTML)。
const inspectionMaxBody = 20 << 20

// OpenUploadInspectionReport godoc
// @Summary 上传一份 AI 巡检报告(同一实例同一天再传即覆盖)
// @Router  /open/inspection-reports [post]
func (h *Handler) OpenUploadInspectionReport(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, inspectionMaxBody)
	var req dto.InspectionReportReq
	if err := c.ShouldBindJSON(&req); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			resp.Fail(c, resp.CodeTooLarge, "报告超过 20MB 上限")
			return
		}
		resp.FailErr(c, resp.CodeBadRequest, apimsg.Errorf("参数错误: %s", err))
		return
	}
	rep, replaced, err := h.Svc.UploadInspectionReport(middleware.CurrentAPIClient(c), middleware.CurrentUser(c), req)
	switch {
	case err == nil:
		resp.OK(c, gin.H{"id": rep.ID, "replaced": replaced})
	case errors.Is(err, service.ErrInspectionPathUnset):
		resp.FailErr(c, resp.CodeInspectionPathUnset, err)
	case errors.Is(err, service.ErrInspectionUnknownInstance):
		resp.FailErr(c, resp.CodeNotFound, err)
	case errors.Is(err, service.ErrInspectionInvalid):
		resp.FailErr(c, resp.CodeBadRequest, err)
	default:
		resp.FailErr(c, resp.CodeInternalError, err)
	}
}

// LatestInspectionReports godoc
// @Summary 每个实例最近一份巡检报告(没有报告的实例也列出)
// @Router  /inspection-reports/latest [get]
func (h *Handler) LatestInspectionReports(c *gin.Context) {
	rows, today, err := h.Svc.LatestInspectionReports()
	if err != nil {
		resp.Fail(c, resp.CodeInternalError, "加载失败")
		return
	}
	resp.OK(c, gin.H{"today": today, "items": rows})
}

// ListInspectionReports godoc
// @Summary 某实例的巡检报告历史
// @Router  /inspection-reports [get]
func (h *Handler) ListInspectionReports(c *gin.Context) {
	connID, _ := strconv.ParseInt(c.Query("instanceId"), 10, 64)
	if connID <= 0 {
		resp.Fail(c, resp.CodeBadRequest, "instanceId 必填")
		return
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", "30"))
	items, total, err := h.Svc.ListInspectionReports(connID, page, pageSize)
	if err != nil {
		resp.Fail(c, resp.CodeInternalError, "加载失败")
		return
	}
	resp.OK(c, gin.H{"items": items, "total": total})
}

// GetInspectionReport godoc
// @Summary 单份巡检报告的摘要与问题列表
// @Router  /inspection-reports/{id} [get]
func (h *Handler) GetInspectionReport(c *gin.Context) {
	v, err := h.Svc.GetInspectionReport(pathID(c))
	if err != nil {
		resp.Fail(c, resp.CodeNotFound, "报告不存在")
		return
	}
	resp.OK(c, v)
}

// InspectionReportHTML godoc
// @Summary 巡检报告的完整 HTML(纯文本、附件方式返回)
// @Router  /inspection-reports/{id}/html [get]
//
// 外部系统生成的 HTML 不能让浏览器当页面直接打开执行:Content-Type 用 text/plain、
// Content-Disposition: attachment、nosniff。前端取回文本后放进沙箱 iframe 显示。
// InspectionReportView 把报告当页面返回,给巡检页的 iframe 用。
//
// 报告是外部系统生成的 HTML,里面常有内联脚本和站外图表库。从前放进 srcdoc,可 srcdoc 会继承父页面的 CSP,
// 控制台收紧 script-src 之后报告里的脚本就跑不了了。所以换成一份有自己响应头的文档:CSP 里的 sandbox 与
// iframe 上的 sandbox 是两道闸,没有 allow-same-origin,脚本在一个不透明的来源里运行 —— 碰不到控制台的
// 存储和 Cookie,也发不出带会话的请求。除此之外不限制,和放在 srcdoc 里时一样。
func (h *Handler) InspectionReportView(c *gin.Context) {
	_, html, err := h.Svc.InspectionReportHTML(middleware.CurrentUser(c), pathID(c))
	if err != nil {
		c.Header("Content-Security-Policy", "sandbox; default-src 'none'; frame-ancestors 'self'")
		c.Header("X-Frame-Options", "SAMEORIGIN")
		c.Data(http.StatusNotFound, "text/plain; charset=utf-8", []byte(apimsg.Text("报告不存在或文件已丢失")))
		return
	}
	c.Header("Content-Security-Policy", "sandbox allow-scripts; frame-ancestors 'self'")
	c.Header("X-Frame-Options", "SAMEORIGIN")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, "text/html; charset=utf-8", html)
}

func (h *Handler) InspectionReportHTML(c *gin.Context) {
	r, html, err := h.Svc.InspectionReportHTML(middleware.CurrentUser(c), pathID(c))
	switch {
	case err == nil:
	case errors.Is(err, service.ErrInspectionPathUnset):
		resp.FailErr(c, resp.CodeInspectionPathUnset, err)
		return
	case errors.Is(err, service.ErrInspectionFileMissing):
		resp.FailErr(c, resp.CodeNotFound, err)
		return
	default:
		resp.Fail(c, resp.CodeNotFound, "报告不存在")
		return
	}
	name := r.InstanceName + "-" + r.ReportDate + ".html"
	c.Header("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(name))
	c.Header("X-Content-Type-Options", "nosniff")
	c.Data(http.StatusOK, "text/plain; charset=utf-8", html)
}
