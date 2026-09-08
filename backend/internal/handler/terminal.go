package handler

import (
	"errors"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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
	token, exp, u, err := h.Svc.Login(req.Email, req.Password, req.MfaCode)
	if err == service.ErrMFARequired {
		// Password verified; prompt for the second factor without counting a fail.
		resp.Fail(c, resp.CodeMFARequired, "请输入 MFA 验证码")
		return
	}
	if err == service.ErrMFAInvalid {
		h.loginLim.fail(ip, time.Now())
		resp.Fail(c, resp.CodeMFARequired, "MFA 验证码错误")
		return
	}
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
	r, err := h.Svc.RiskCheck(middleware.CurrentUser(c), req.ConnectionID, req.SQL, req.Database)
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
	// Carries its own actionable message — collapsing it into the generic branch
	// below would report an oversize paste as "连接不存在".
	if errors.Is(err, service.ErrSQLTooLong) {
		resp.Fail(c, resp.CodeBadRequest, err.Error())
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

// ExecAsync submits a long-running SQL for background execution (30–60min+),
// decoupled from this request so it can't time out. Returns a jobId to poll, or
// an approval ticket when the command is gated.
func (h *Handler) ExecAsync(c *gin.Context) {
	var req dto.ExecReq
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误")
		return
	}
	r, err := h.Svc.ExecAsync(middleware.CurrentUser(c), req.ConnectionID, req.SQL, req.Reason, req.MfaCode, req.Database)
	if err == service.ErrForbidden {
		resp.Fail(c, resp.CodeForbidden, "能力矩阵禁止:命令被拒绝")
		return
	}
	if err == service.ErrMFARequired {
		resp.Fail(c, resp.CodeMFARequired, "生产操作需要 MFA 二次验证")
		return
	}
	if err == service.ErrMFAInvalid {
		resp.Fail(c, resp.CodeMFARequired, "MFA 验证码错误")
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

// ListAsyncJobs returns the caller's background jobs (newest first).
func (h *Handler) ListAsyncJobs(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	resp.OK(c, h.Svc.ListAsyncJobs(middleware.CurrentUser(c), limit))
}

// GetAsyncJob returns one background job with its streamed log (poll for progress).
func (h *Handler) GetAsyncJob(c *gin.Context) {
	j, err := h.Svc.GetAsyncJob(middleware.CurrentUser(c), pathID(c))
	if err == service.ErrForbidden {
		resp.Fail(c, resp.CodeForbidden, "无权查看该任务")
		return
	}
	if err != nil {
		resp.Fail(c, resp.CodeBadRequest, "任务不存在")
		return
	}
	resp.OK(c, j)
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
	intercepts, _ := h.Repo.CountProdInterceptions() // real PROD rule hits (0 on error)
	resp.OK(c, gin.H{
		"online":     true,
		"p50Ms":      round(metrics.Default.P50()),
		"p95Ms":      round(metrics.Default.P95()),
		"samples":    metrics.Default.Samples(),
		"intercepts": intercepts,
	})
}

// maxConsoleUploadBytes 与开放接口 (handler/openapi.go) 用同一个上限。
//
// 原先控制台这条路**没有任何上限**:`make([]byte, file.Size)` 直接按客户端声明的
// 大小分配,一个数 GB 的上传就能把进程撑爆。讽刺的是开放接口那条路早就有 15MB 上限
// 和正确的读法 —— 面向外部的入口守住了,面向内部控制台的入口反而敞着。
const maxConsoleUploadBytes = 15 << 20

// readConsoleScriptFile reads an uploaded script under an explicit cap.
//
// 不信 fh.Size:那是客户端说的。用 LimitReader 读,读满上限+1 就说明超了 ——
// 这与 handler/openapi.go 的 readOpenScriptFile 是同一套做法,两条通道在同一个
// 尺寸上拒绝。
func readConsoleScriptFile(c *gin.Context, fh *multipart.FileHeader) (string, bool) {
	if fh.Size > maxConsoleUploadBytes {
		resp.Fail(c, resp.CodeBadRequest, "脚本超过 15MB 上限")
		return "", false
	}
	f, err := fh.Open()
	if err != nil {
		resp.Fail(c, resp.CodeBadRequest, "脚本读取失败")
		return "", false
	}
	defer f.Close()
	body, rerr := io.ReadAll(io.LimitReader(f, maxConsoleUploadBytes+1))
	if rerr != nil || len(body) > maxConsoleUploadBytes {
		resp.Fail(c, resp.CodeBadRequest, "脚本读取失败或超过 15MB 上限")
		return "", false
	}
	return string(body), true
}

// ScriptUpload stores a script file (upload page) under the current user.
func (h *Handler) ScriptUpload(c *gin.Context) {
	filename := "script.sql"
	var content string
	if file, err := c.FormFile("file"); err == nil {
		body, ok := readConsoleScriptFile(c, file)
		if !ok {
			return // 拒绝已经写进响应了
		}
		filename, content = file.Filename, body
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
	// 扫描按目标实例的分层判定,所以两条分支都必须带上它。multipart 上传里它是一个
	// 表单字段;取不到就是 0,GetConnection 随即失败 —— 这正是要的方向:宁可拒绝扫描,
	// 也不要退回到某个固定分层去判,那会让报告说的和将要发生的事对不上。
	var connID int64
	if file, err := c.FormFile("file"); err == nil {
		body, ok := readConsoleScriptFile(c, file)
		if !ok {
			return // 拒绝已经写进响应了
		}
		filename, content = file.Filename, body
		connID, _ = strconv.ParseInt(c.PostForm("connectionId"), 10, 64)
	} else {
		var req dto.ScriptScanReq
		if err := c.ShouldBindJSON(&req); err != nil {
			resp.Fail(c, resp.CodeBadRequest, "参数错误")
			return
		}
		content = req.Content
		connID = req.ConnectionID
		if req.Filename != "" {
			filename = req.Filename
		}
		// Same as /scripts/execute: an already-uploaded script is read here rather
		// than re-sent. Scanning something other than the file the caller named
		// would report on a script nobody is going to run.
		if req.UploadID > 0 {
			c2, name, rerr := h.Svc.ReadUploadedScriptFor(middleware.CurrentUser(c), req.UploadID)
			if rerr != nil {
				resp.Fail(c, resp.CodeForbidden, "脚本文件不可用或不属于当前用户")
				return
			}
			content, filename = c2, name
		}
	}
	// 按**目标实例的分层**扫描 —— 报告要描述这个脚本在这台实例上会怎样。
	scan, err := h.Svc.ScanScript(connID, filename, content)
	if err != nil {
		// 目标实例或它的分层解析不出来:扫描无从判起,而空结果会被渲染成"脚本干净"。
		resp.Fail(c, resp.CodeInternalError, "脚本扫描不可用:目标实例的分层标签解析失败")
		return
	}
	resp.OK(c, scan)
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
		// Read the body from disk and IGNORE whatever the request carried.
		//
		// Two reasons. The script is already on this machine, so making the client
		// re-send megabytes is pointless. And until now the saved path and the
		// executed text came from different places with nothing checking they
		// agreed — the ticket could reference one file while a different body was
		// scanned, approved and run.
		//
		// Ownership is checked here, against the CALLER, before anything is read.
		// Only an upload id is accepted; a client-supplied path would be an
		// arbitrary file read.
		var ferr error
		saved, _, ferr = h.Svc.ScriptUploadFile(middleware.CurrentUser(c), req.UploadID)
		if ferr != nil {
			resp.Fail(c, resp.CodeForbidden, "脚本文件不可用或不属于当前用户")
			return
		}
		content, name, rerr := h.Svc.ReadUploadedScriptFor(middleware.CurrentUser(c), req.UploadID)
		if rerr != nil {
			resp.Fail(c, resp.CodeBadRequest, "读取脚本失败:"+rerr.Error())
			return
		}
		req.Content, req.Filename = content, name
	} else if strings.TrimSpace(req.Content) == "" {
		// Content stopped being a required field so an upload id could stand in for
		// it; with neither, there is nothing to scan and nothing to run.
		resp.Fail(c, resp.CodeBadRequest, "请提供脚本内容或已上传脚本的 uploadId")
		return
	} else {
		up, err := h.Svc.SaveUploadedScript(middleware.CurrentUser(c), req.ConnectionID, req.Filename, req.Content)
		if err == service.ErrScriptPathUnset {
			resp.Fail(c, resp.CodeScriptPathUnset, "请先在【系统设置 · 网关】配置上传脚本保存路径")
			return
		}
		if err != nil {
			resp.Fail(c, resp.CodeInternalError, "脚本保存失败:"+err.Error())
			return
		}
		saved = up.Path
		// The paste IS an upload now — carrying its id means a risky script's
		// approval ticket stores a bounded excerpt + digest referencing this
		// file, never the whole body (see SubmitScriptForApproval).
		req.UploadID = up.ID
	}
	scan, err := h.Svc.ScanScript(req.ConnectionID, req.Filename, req.Content)
	if err != nil {
		// 整个执行都拒掉:分层判不出来时每条语句都会扫成安全,脚本就会不经复核直接跑。
		resp.Fail(c, resp.CodeInternalError, "脚本扫描不可用:目标实例的分层标签解析失败")
		return
	}
	if scan.HasRisky {
		// whole script must be submitted for approval (created from the scan
		// result, since the `\i file` wrapper isn't itself risk-matched)
		r, err := h.Svc.SubmitScriptForApproval(middleware.CurrentUser(c), req.ConnectionID, scan.Filename, req.Content, "脚本含高危语句,整脚本提交审批", req.MfaCode, req.Database, req.UploadID)
		if err == service.ErrMFARequired {
			resp.Fail(c, resp.CodeMFARequired, "生产操作需要 MFA 二次验证")
			return
		}
		if err != nil {
			// Carry the reason. This branch used to report a bare "提交失败", so a
			// storage-level refusal (a script too large for the command column, say)
			// reached the operator as an unexplained failure with nothing to act on
			// — the sibling branch below has always included it.
			resp.Fail(c, resp.CodeBadRequest, "提交失败:"+err.Error())
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
	// retentionDays 一并给前端:归档是定时删的,谁要导四千万行,应该在排期之前就知道
	// 这个文件只在服务器上活几天,而不是下周去下载时才发现它没了。
	resp.OK(c, gin.H{"enabled": p != "", "savePath": p, "retentionDays": h.Svc.ExportRetentionDays()})
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
	job, err := h.Svc.EnqueueExportWithSensitive(middleware.CurrentUser(c), req.ConnectionID, req.SQL, req.Name, req.Database, req.IncludeSensitive)
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
	if err == service.ErrNoDatabase {
		resp.Fail(c, resp.CodeBadRequest, "请选择目标数据库(该连接未配置默认库)")
		return
	}
	if err == service.ErrExportNotReadOnly {
		resp.Fail(c, resp.CodeForbidden, "数据导出仅允许单条只读查询(SELECT/SHOW 等);修改类语句请走命令行审批流")
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

	// Reading and executing must not share a goroutine. Executing a statement
	// blocks for as long as the target database takes, and a blocked loop cannot
	// read the client's heartbeat — the browser then gets no pong inside its 5s
	// budget, concludes the socket is half-open and closes it. The effect was that
	// any statement outlasting the heartbeat interval severed its own connection
	// mid-run: the terminal showed "已断开" and the result was written to a socket
	// nobody was listening on, so the operator never learned whether their DDL had
	// applied. So: a reader goroutine answers pings immediately and hands exec
	// requests to this goroutine, which still runs them one at a time (the
	// terminal is a single-statement console).
	//
	// gorilla/websocket allows one concurrent reader and one concurrent writer, so
	// every write goes through send() under a mutex.
	var writeMu sync.Mutex
	send := func(v any) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return conn.WriteJSON(v)
	}
	execCh := make(chan wsMsg, 1)
	go func() {
		defer close(execCh)
		for {
			var msg wsMsg
			if err := conn.ReadJSON(&msg); err != nil {
				return // socket closed / unreadable: stop the executor loop too
			}
			switch msg.Type {
			case "ping":
				// app-level heartbeat: lets the client detect a half-open socket
				// (browsers can't send native WS ping frames).
				if err := send(gin.H{"type": "pong"}); err != nil {
					return
				}
			case "exec":
				select {
				case execCh <- msg:
				default:
					// A statement is already running; the console submits one at a
					// time, so this is a stray. Tell the client rather than queueing
					// work it is no longer waiting for.
					_ = send(gin.H{"type": "error", "message": "已有命令正在执行,请等待完成"})
				}
			}
		}
	}()

	for msg := range execCh {
		// Re-validate the session on every command so a mid-connection logout /
		// disable / password reset / role change takes effect immediately instead
		// of living until the socket drops (R5).
		fresh, ferr := h.Repo.GetUserByID(claims.UserID)
		if ferr != nil || fresh.Status == "disabled" || fresh.TokenVersion != claims.TokenVersion {
			send(gin.H{"type": "session_revoked", "message": "会话已失效，请重新登录"})
			return
		}
		// Re-check the terminal menu too: revoking a role's terminal access does NOT
		// bump the token version, so without this an open socket would keep executing
		// after its access was pulled (B9).
		if menus, _ := h.Repo.MenusForRoles(h.Repo.EffectiveRoleIDs(fresh)); !menus["terminal"] {
			send(gin.H{"type": "session_revoked", "message": "终端访问权限已被回收，请重新登录"})
			return
		}
		u = fresh
		r, err := h.Svc.Exec(u, msg.ConnectionID, msg.SQL, msg.Reason, msg.MfaCode, msg.Database)
		if err == service.ErrForbidden {
			send(gin.H{"type": "error", "message": "命令被拒绝:能力矩阵禁止"})
			continue
		}
		if err == service.ErrMFARequired {
			send(gin.H{"type": "mfa_required", "message": "生产操作需要 MFA 二次验证"})
			continue
		}
		if errors.Is(err, service.ErrSQLTooLong) {
			send(gin.H{"type": "error", "message": err.Error()})
			continue
		}
		if err != nil {
			send(gin.H{"type": "error", "message": "连接不存在或执行失败"})
			continue
		}
		if r.Intercepted {
			send(gin.H{"type": "intercept", "approvalNo": r.ApprovalNo, "auditId": r.AuditID, "risk": r.Risk, "rule": r.Rule, "ruleRef": r.RuleRef})
			continue
		}
		send(gin.H{"type": "output", "text": r.Output, "rows": r.Rows, "ms": r.Ms, "risk": r.Risk,
			"columns": r.Columns, "data": r.Data, "truncated": r.Truncated})
	}
}

// TranscriptExport godoc
// @Summary 记录终端会话日志导出(仅审计,文件在浏览器生成)
// @Router  /terminal/transcript-export [post]
func (h *Handler) TranscriptExport(c *gin.Context) {
	var req dto.TranscriptExportReq
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误")
		return
	}
	err := h.Svc.RecordTranscriptExport(middleware.CurrentUser(c), req)
	if err == service.ErrNotFound {
		resp.Fail(c, resp.CodeBadRequest, "连接不存在")
		return
	}
	if err == service.ErrForbidden {
		resp.Fail(c, resp.CodeForbidden, "无权访问该实例")
		return
	}
	if err != nil {
		resp.Fail(c, resp.CodeInternalError, "记录失败")
		return
	}
	resp.OK(c, gin.H{"recorded": true})
}
