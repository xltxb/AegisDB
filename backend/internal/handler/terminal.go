package handler

import (
	"math"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"velagateway/internal/dto"
	"velagateway/internal/metrics"
	"velagateway/internal/middleware"
	"velagateway/internal/service"
	"velagateway/pkg/resp"
)

// Login godoc
// @Summary 登录
// @Router  /auth/login [post]
func (h *Handler) Login(c *gin.Context) {
	// Throttle brute-force by source IP (R29).
	ip := c.ClientIP()
	if h.loginLim.blocked(ip, time.Now()) {
		resp.Fail(c, resp.CodeTooManyReqs, "登录尝试过多,请稍后再试")
		return
	}
	var req dto.LoginReq
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误")
		return
	}
	token, exp, u, err := h.Svc.Login(req.Email, req.Password)
	if err != nil {
		h.loginLim.fail(ip, time.Now())
		resp.Fail(c, resp.CodeUnauthorized, "邮箱或密码错误")
		return
	}
	h.loginLim.reset(ip)
	me, _ := h.Svc.BuildMe(u)
	resp.OK(c, dto.LoginResp{Token: token, ExpiresAt: exp.Format("2006-01-02 15:04:05"), User: *me})
}

// Logout godoc
// @Router /auth/logout [post]
func (h *Handler) Logout(c *gin.Context) {
	// Revoke the current token generation so this token (and any other issued
	// before now) can no longer be used (M1).
	if u := middleware.CurrentUser(c); u != nil {
		_ = h.Repo.BumpTokenVersion(u.ID)
	}
	resp.OK(c, gin.H{"ok": true})
}

// Me godoc
// @Router /auth/me [get]
func (h *Handler) Me(c *gin.Context) {
	u := middleware.CurrentUser(c)
	me, err := h.Svc.BuildMe(u)
	if err != nil {
		resp.Fail(c, resp.CodeInternalError, "加载失败")
		return
	}
	resp.OK(c, me)
}

// RiskCheck godoc
// @Summary 命令风险预检
// @Router  /risk/check [post]
func (h *Handler) RiskCheck(c *gin.Context) {
	var req dto.RiskCheckReq
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误")
		return
	}
	r, err := h.Svc.RiskCheck(middleware.CurrentUser(c), req.ConnectionID, req.SQL)
	if err != nil {
		resp.Fail(c, resp.CodeBadRequest, "连接不存在")
		return
	}
	resp.OK(c, r)
}

// Exec godoc
// @Summary 终端命令执行(REST 回退,WS 不可用时)
// @Router  /terminal/exec [post]
func (h *Handler) Exec(c *gin.Context) {
	var req dto.ExecReq
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误")
		return
	}
	r, err := h.Svc.Exec(middleware.CurrentUser(c), req.ConnectionID, req.SQL, req.Reason, req.MfaCode, req.Database)
	if err == service.ErrForbidden {
		resp.Fail(c, resp.CodeForbidden, "能力矩阵禁止:命令被拒绝")
		return
	}
	if err == service.ErrMFARequired {
		resp.Fail(c, resp.CodeMFARequired, "生产操作需要 MFA 二次验证")
		return
	}
	if err != nil {
		resp.Fail(c, resp.CodeBadRequest, "连接不存在")
		return
	}
	if r.Intercepted {
		resp.FailData(c, resp.CodeIntercepted, "命令被拦截,需审批", r)
		return
	}
	resp.OK(c, r)
}

// ScriptScan godoc
// @Summary 脚本扫描
// @Router  /scripts/scan [post]
// ScriptConfig godoc
// @Summary 上传脚本配置(是否已设置保存路径)
// @Router  /scripts/config [get]
func (h *Handler) ScriptConfig(c *gin.Context) {
	p := h.Svc.ScriptSavePath()
	resp.OK(c, gin.H{"enabled": p != "", "savePath": p})
}

// GatewayStats returns live gateway health: online + real request latency (p50/p95),
// measured by the latency middleware — no hard-coded numbers.
func (h *Handler) GatewayStats(c *gin.Context) {
	round := func(v float64) float64 { return math.Round(v*10) / 10 }
	resp.OK(c, gin.H{
		"online":  true,
		"p50Ms":   round(metrics.Default.P50()),
		"p95Ms":   round(metrics.Default.P95()),
		"samples": metrics.Default.Samples(),
	})
}

// ScriptUpload stores a script file (upload page) under the current user.
func (h *Handler) ScriptUpload(c *gin.Context) {
	filename := "script.sql"
	var content string
	if file, err := c.FormFile("file"); err == nil {
		filename = file.Filename
		f, _ := file.Open()
		defer f.Close()
		buf := make([]byte, file.Size)
		f.Read(buf)
		content = string(buf)
	} else {
		var req dto.ScriptScanReq
		if err := c.ShouldBindJSON(&req); err != nil {
			resp.Fail(c, resp.CodeBadRequest, "参数错误")
			return
		}
		content, filename = req.Content, req.Filename
	}
	up, err := h.Svc.UploadScript(middleware.CurrentUser(c), filename, content)
	if err == service.ErrScriptPathUnset {
		resp.Fail(c, resp.CodeScriptPathUnset, "请先在【系统设置 · 网关】配置上传脚本保存路径")
		return
	}
	if err != nil {
		resp.Fail(c, resp.CodeInternalError, "上传失败:"+err.Error())
		return
	}
	resp.OK(c, up)
}

// ScriptUploads lists the current user's own uploaded scripts.
func (h *Handler) ScriptUploads(c *gin.Context) {
	resp.OK(c, h.Svc.ListScriptUploads(middleware.CurrentUser(c)))
}

// ScriptUploadDelete removes one of the user's own uploaded scripts.
func (h *Handler) ScriptUploadDelete(c *gin.Context) {
	if err := h.Svc.DeleteScriptUpload(middleware.CurrentUser(c), pathID(c)); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "删除失败")
		return
	}
	resp.OK(c, gin.H{"ok": true})
}

// ScriptUploadDownload serves one of the user's own uploaded scripts.
func (h *Handler) ScriptUploadDownload(c *gin.Context) {
	abs, name, err := h.Svc.ScriptUploadFile(middleware.CurrentUser(c), pathID(c))
	if err != nil {
		resp.Fail(c, resp.CodeBadRequest, "文件不可用")
		return
	}
	c.FileAttachment(abs, name)
}

// ScriptUploadContent returns the content of the user's own uploaded script so
// the terminal can scan + dispatch it for execution.
func (h *Handler) ScriptUploadContent(c *gin.Context) {
	content, name, err := h.Svc.ScriptUploadContent(middleware.CurrentUser(c), pathID(c))
	if err != nil {
		resp.Fail(c, resp.CodeBadRequest, "文件不可用")
		return
	}
	resp.OK(c, gin.H{"content": content, "filename": name})
}

func (h *Handler) ScriptScan(c *gin.Context) {
	// Upload is unusable until an admin configures the save path.
	if h.Svc.ScriptSavePath() == "" {
		resp.Fail(c, resp.CodeScriptPathUnset, "请先在【系统设置 · 网关】配置上传脚本保存路径")
		return
	}
	// Accept either multipart (.sql file) or JSON { content, filename }.
	filename := "script.sql"
	var content string
	if file, err := c.FormFile("file"); err == nil {
		filename = file.Filename
		f, _ := file.Open()
		defer f.Close()
		buf := make([]byte, file.Size)
		f.Read(buf)
		content = string(buf)
	} else {
		var req dto.ScriptScanReq
		if err := c.ShouldBindJSON(&req); err != nil {
			resp.Fail(c, resp.CodeBadRequest, "参数错误")
			return
		}
		content = req.Content
		if req.Filename != "" {
			filename = req.Filename
		}
	}
	resp.OK(c, h.Svc.ScanScript(filename, content))
}

// ScriptExecute godoc
// @Summary 脚本执行(检出高危则转审批)
// @Router  /scripts/execute [post]
func (h *Handler) ScriptExecute(c *gin.Context) {
	var req dto.ScriptScanReq
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误")
		return
	}
	// An already-uploaded script (dispatched from the "uploaded scripts" picker)
	// must NOT be saved again — reuse its existing path. A fresh upload is saved.
	var saved string
	if req.UploadID > 0 {
		saved, _, _ = h.Svc.ScriptUploadFile(middleware.CurrentUser(c), req.UploadID)
	} else {
		p, err := h.Svc.SaveUploadedScript(middleware.CurrentUser(c), req.ConnectionID, req.Filename, req.Content)
		if err == service.ErrScriptPathUnset {
			resp.Fail(c, resp.CodeScriptPathUnset, "请先在【系统设置 · 网关】配置上传脚本保存路径")
			return
		}
		if err != nil {
			resp.Fail(c, resp.CodeInternalError, "脚本保存失败:"+err.Error())
			return
		}
		saved = p
	}
	scan := h.Svc.ScanScript(req.Filename, req.Content)
	if scan.HasRisky {
		// whole script must be submitted for approval (created from the scan
		// result, since the `\i file` wrapper isn't itself risk-matched)
		r, err := h.Svc.SubmitScriptForApproval(middleware.CurrentUser(c), req.ConnectionID, scan.Filename, req.Content, "脚本含高危语句,整脚本提交审批", req.MfaCode, req.Database)
		if err == service.ErrMFARequired {
			resp.Fail(c, resp.CodeMFARequired, "生产操作需要 MFA 二次验证")
			return
		}
		if err != nil {
			resp.Fail(c, resp.CodeBadRequest, "提交失败")
			return
		}
		resp.FailData(c, resp.CodeIntercepted, "脚本含高危语句,已提交审批", gin.H{"exec": r, "savedPath": saved})
		return
	}
	executed, err := h.Svc.ExecuteSafeScript(middleware.CurrentUser(c), req.ConnectionID, req.Content, req.MfaCode, req.Database)
	if err == service.ErrMFARequired {
		resp.Fail(c, resp.CodeMFARequired, "生产操作需要 MFA 二次验证")
		return
	}
	if err != nil {
		resp.Fail(c, resp.CodeBadRequest, "执行失败")
		return
	}
	resp.OK(c, gin.H{"executed": true, "statements": executed, "savedPath": saved})
}

// ---- Data export ----

// ExportConfig godoc
// @Summary 数据导出配置(是否已设置保存路径)
// @Router  /export/config [get]
func (h *Handler) ExportConfig(c *gin.Context) {
	p := h.Svc.ExportSavePath()
	resp.OK(c, gin.H{"enabled": p != "", "savePath": p})
}

// ExportData godoc
// @Summary 提交异步 SQL 导出任务(压缩+加密后存服务器)
// @Router  /export [post]
func (h *Handler) ExportData(c *gin.Context) {
	var req dto.ExportReq
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误")
		return
	}
	job, err := h.Svc.EnqueueExport(middleware.CurrentUser(c), req.ConnectionID, req.SQL, req.Name, req.Database)
	if err == service.ErrExportPathUnset {
		resp.Fail(c, resp.CodeExportPathUnset, "请先在【系统设置 · 网关】配置数据导出保存路径")
		return
	}
	if err == service.ErrForbidden {
		resp.Fail(c, resp.CodeForbidden, "无权访问该数据库")
		return
	}
	if err == service.ErrNotFound {
		resp.Fail(c, resp.CodeBadRequest, "连接不存在")
		return
	}
	if err != nil {
		resp.Fail(c, resp.CodeInternalError, "提交失败:"+err.Error())
		return
	}
	resp.OK(c, job)
}

// ExportJobs godoc
// @Summary 列出当前用户的导出任务(含状态与下载信息)
// @Router  /export/jobs [get]
func (h *Handler) ExportJobs(c *gin.Context) {
	resp.OK(c, h.Svc.ListExportJobs(middleware.CurrentUser(c), 50))
}

// ExportDownload godoc
// @Summary 下载已导出的加密文件
// @Router  /export/download [get]
func (h *Handler) ExportDownload(c *gin.Context) {
	abs, err := h.Svc.ResolveExportFile(middleware.CurrentUser(c), c.Query("file"))
	if err != nil {
		resp.Fail(c, resp.CodeBadRequest, "文件不可用")
		return
	}
	c.FileAttachment(abs, filepath.Base(abs))
}

// ---- WebSocket terminal ----

// The token may be carried in the Sec-WebSocket-Protocol header (see wsToken)
// instead of the query string; advertise the subprotocol so the browser accepts
// the negotiated handshake.
var upgrader = websocket.Upgrader{
	CheckOrigin:  func(r *http.Request) bool { return true },
	Subprotocols: []string{wsTokenProto},
}

const wsTokenProto = "vela-token"

// wsToken extracts the JWT for the terminal socket from the
// Sec-WebSocket-Protocol header ("vela-token, <jwt>"). The credential is
// deliberately NOT read from the query string — that would put the token into
// gin's access logs and any Referer header (R17).
func wsToken(c *gin.Context) string {
	if proto := c.GetHeader("Sec-WebSocket-Protocol"); proto != "" {
		parts := strings.Split(proto, ",")
		if len(parts) == 2 && strings.TrimSpace(parts[0]) == wsTokenProto {
			return strings.TrimSpace(parts[1])
		}
	}
	return ""
}

type wsMsg struct {
	Type         string `json:"type"`
	ConnectionID int64  `json:"connectionId"`
	SQL          string `json:"sql"`
	Reason       string `json:"reason"`
	MfaCode      string `json:"mfaCode"`
	Database     string `json:"database"`
}

// TerminalWS godoc
// @Summary 终端流式执行 (token via ?token=)
// @Router  /terminal/ws [get]
func (h *Handler) TerminalWS(c *gin.Context) {
	tokenStr := wsToken(c)
	claims, err := h.Svc.JWT.Parse(tokenStr)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	u, err := h.Repo.GetUserByID(claims.UserID)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "no user"})
		return
	}
	// A disabled account's still-valid token must not open a terminal (mirror the
	// JWTAuth middleware check, which the WS route bypasses).
	if u.Status == "disabled" {
		c.JSON(http.StatusForbidden, gin.H{"error": "account disabled"})
		return
	}
	// Reject a revoked session generation (logout / password reset) — M1.
	if claims.TokenVersion != u.TokenVersion {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "session revoked"})
		return
	}
	// The WS route bypasses MenuGuard, so enforce the terminal menu here — a role
	// without terminal access must not execute via this channel (R4).
	if menus, _ := h.Repo.MenusForRoles(h.Repo.EffectiveRoleIDs(u)); !menus["terminal"] {
		c.JSON(http.StatusForbidden, gin.H{"error": "no terminal menu"})
		return
	}
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	for {
		var msg wsMsg
		if err := conn.ReadJSON(&msg); err != nil {
			return
		}
		if msg.Type == "ping" {
			// app-level heartbeat: lets the client detect a half-open socket
			// (browsers can't send native WS ping frames).
			conn.WriteJSON(gin.H{"type": "pong"})
			continue
		}
		if msg.Type != "exec" {
			continue
		}
		// Re-validate the session on every command so a mid-connection logout /
		// disable / password reset / role change takes effect immediately instead
		// of living until the socket drops (R5).
		fresh, ferr := h.Repo.GetUserByID(claims.UserID)
		if ferr != nil || fresh.Status == "disabled" || fresh.TokenVersion != claims.TokenVersion {
			conn.WriteJSON(gin.H{"type": "session_revoked", "message": "会话已失效，请重新登录"})
			return
		}
		// Re-check the terminal menu too: revoking a role's terminal access does NOT
		// bump the token version, so without this an open socket would keep executing
		// after its access was pulled (B9).
		if menus, _ := h.Repo.MenusForRoles(h.Repo.EffectiveRoleIDs(fresh)); !menus["terminal"] {
			conn.WriteJSON(gin.H{"type": "session_revoked", "message": "终端访问权限已被回收，请重新登录"})
			return
		}
		u = fresh
		r, err := h.Svc.Exec(u, msg.ConnectionID, msg.SQL, msg.Reason, msg.MfaCode, msg.Database)
		if err == service.ErrForbidden {
			conn.WriteJSON(gin.H{"type": "error", "message": "命令被拒绝:能力矩阵禁止"})
			continue
		}
		if err == service.ErrMFARequired {
			conn.WriteJSON(gin.H{"type": "mfa_required", "message": "生产操作需要 MFA 二次验证"})
			continue
		}
		if err != nil {
			conn.WriteJSON(gin.H{"type": "error", "message": "连接不存在或执行失败"})
			continue
		}
		if r.Intercepted {
			conn.WriteJSON(gin.H{"type": "intercept", "approvalNo": r.ApprovalNo, "auditId": r.AuditID, "risk": r.Risk, "rule": r.Rule})
			continue
		}
		conn.WriteJSON(gin.H{"type": "output", "text": r.Output, "rows": r.Rows, "ms": r.Ms, "risk": r.Risk,
			"columns": r.Columns, "data": r.Data, "truncated": r.Truncated})
	}
}
