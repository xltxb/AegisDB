package handler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
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

	"aegisdb/internal/apimsg"
	"aegisdb/internal/dto"
	"aegisdb/internal/gateway"
	"aegisdb/internal/metrics"
	"aegisdb/internal/middleware"
	"aegisdb/internal/model"
	"aegisdb/internal/presence"
	"aegisdb/internal/service"
	"aegisdb/pkg/resp"
)

// Login godoc
// @Summary 登录
// @Router  /auth/login [post]
func (h *Handler) Login(c *gin.Context) {
	// Throttle brute-force by source IP and by (IP, account) — see loginThrottle (R29).
	ip := c.ClientIP()
	var req dto.LoginReq
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误")
		return
	}
	acct := req.Email
	if h.loginLim.blocked(ip, acct, time.Now()) {
		resp.Fail(c, resp.CodeTooManyReqs, "登录尝试过多,请稍后再试")
		return
	}
	// ip 既用来限流,也写进审计行 —— 一次被拒的登录最该留下的就是它是从哪儿来的。
	token, exp, u, err := h.Svc.Login(req.Email, req.Password, req.MfaCode, ip)
	if err == service.ErrMFARequired {
		// 口令对了、还差第二因子。这一下**必须计入频控**:它与"邮箱或密码错误"是两种
		// 不同的回答,于是这个接口可以被当成一台口令预言机 —— 拿一本字典打过来,返回
		// 42800 的那一个就是正确口令,攻击者据此确认口令,再去别处复用(L-18)。
		//
		// 计入频控不影响正常登录:真人拿到提示就把码填上来,一次成功就清掉他自己的计数;而字典
		// 攻击每试一个口令都要吃一次计数,几次就被挡在门外。
		h.loginLim.fail(ip, acct, time.Now())
		resp.Fail(c, resp.CodeMFARequired, "请输入 MFA 验证码")
		return
	}
	if err == service.ErrMFAInvalid {
		h.loginLim.fail(ip, acct, time.Now())
		resp.Fail(c, resp.CodeMFARequired, "MFA 验证码错误")
		return
	}
	if err == service.ErrMFALocked {
		h.loginLim.fail(ip, acct, time.Now())
		resp.Fail(c, resp.CodeTooManyReqs, mfaLockedMsg)
		return
	}
	if err == service.ErrMFACodeUsed {
		// 照样计入频控:对重放者来说这就是一次失败的尝试。
		h.loginLim.fail(ip, acct, time.Now())
		resp.Fail(c, resp.CodeMFARequired, "该验证码已使用过,请等待下一个验证码")
		return
	}
	if err == service.ErrLocalLoginSSOOnly {
		// 口令是对的,只是走错了门。说"邮箱或密码错误"会让人去反复试口令、最后把自己锁住。
		// 这一句只在口令校验**通过之后**才会出现(见 service.Login),所以不泄露任何东西。
		h.loginLim.fail(ip, acct, time.Now())
		resp.Fail(c, resp.CodeForbidden, "该账号已绑定公司 SSO,请使用 SSO 登录")
		return
	}
	if err != nil {
		h.loginLim.fail(ip, acct, time.Now())
		resp.Fail(c, resp.CodeUnauthorized, "邮箱或密码错误")
		return
	}
	h.loginLim.succeed(ip, acct)
	me, _ := h.Svc.BuildMe(u)
	respToken, session := h.deliverSession(c, token, exp)
	resp.OK(c, dto.LoginResp{Token: respToken, Session: session, ExpiresAt: exp.Format("2006-01-02 15:04:05"), User: *me})
}

// deliverSession 把刚签发的令牌交给客户端:控制台写进 HttpOnly Cookie、响应体里不留令牌,别的客户端照旧放响应体。
func (h *Handler) deliverSession(c *gin.Context, token string, exp time.Time) (respToken, session string) {
	if !middleware.IsWebClient(c) {
		return token, ""
	}
	session, err := middleware.SetSessionCookies(c, h.Svc.JWT, token, exp)
	if err != nil { // 刚签发的令牌解析不了:不会发生,发生了也不能把令牌放回响应体
		return "", ""
	}
	return "", session
}

// Logout godoc
// @Router /auth/logout [post]
func (h *Handler) Logout(c *gin.Context) {
	// Revoke the current token generation so this token (and any other issued
	// before now) can no longer be used (M1).
	if u := middleware.CurrentUser(c); u != nil {
		_ = h.Repo.BumpTokenVersion(u.ID)
		// 登出(含空闲锁定)后标签都没了:它们独占的数据库连接一并断开,未提交的事务回滚。
		h.Svc.CloseTermSessionsOf(u.ID)
	}
	middleware.ClearSessionCookies(c)
	resp.OK(c, gin.H{"ok": true})
}

// SessionPolicy godoc
// @Summary 会话策略(空闲锁定开关与时长),所有登录用户可读
// @Router  /auth/session-policy [get]
func (h *Handler) SessionPolicy(c *gin.Context) {
	p := h.Svc.SessionPolicy()
	p.AdminIdleMinutes = middleware.AdminIdleMinutes(h.Repo)
	resp.OK(c, p)
}

// TermSessionClose godoc
// @Summary 关掉终端标签页时释放它独占的数据库连接
// @Router  /terminal/sessions/close [post]
func (h *Handler) TermSessionClose(c *gin.Context) {
	var req dto.TermSessionCloseReq
	if err := c.ShouldBindJSON(&req); err != nil || !gateway.ValidTermSessionID(req.SessionID) {
		resp.Fail(c, resp.CodeBadRequest, "参数错误")
		return
	}
	h.Svc.CloseTermSession(middleware.CurrentUser(c).ID, req.SessionID)
	resp.OK(c, gin.H{"ok": true})
}

// termCtx:请求带着合法的终端标签标识时,这次执行落在该标签独占的连接上。
func termCtx(c *gin.Context, sessionID string, pageSize int) context.Context {
	ctx := c.Request.Context()
	if u := middleware.CurrentUser(c); u != nil {
		if gateway.ValidTermSessionID(sessionID) {
			ctx = gateway.WithTermSession(ctx, u.ID, sessionID)
		}
		// Web 命令行的结果分批显示:每批行数来自系统设置,超出的部分暂存给本人"加载更多"
		ctx = gateway.WithResultPaging(ctx, u.ID, pageSize)
	}
	return ctx
}

// TerminalResultPage godoc
// @Summary Web 命令行"加载更多":取暂存结果的下一批
// @Router  /terminal/results/{id} [get]
func (h *Handler) TerminalResultPage(c *gin.Context) {
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	p, ok := gateway.FetchResultPage(middleware.CurrentUser(c).ID, c.Param("id"), offset, h.Svc.TerminalPageSize())
	if !ok {
		resp.Fail(c, resp.CodeBadRequest, "这份结果已过期(暂存 10 分钟),请重新执行查询")
		return
	}
	resp.OK(c, p)
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
	ctx := gateway.WithBinds(gateway.WithServerOutput(termCtx(c, req.SessionID, h.Svc.TerminalPageSize()), req.ServerOutput), req.Binds)
	r, err := h.Svc.Exec(ctx, middleware.CurrentUser(c), req.ConnectionID, req.SQL, req.Reason, req.MfaCode, req.Database)
	if err == service.ErrForbidden {
		resp.Fail(c, resp.CodeForbidden, "能力矩阵禁止:命令被拒绝")
		return
	}
	if errors.Is(err, service.ErrMFALocked) {
		resp.Fail(c, resp.CodeTooManyReqs, mfaLockedMsg)
		return
	}
	if err == service.ErrMFARequired {
		resp.Fail(c, resp.CodeMFARequired, "生产操作需要 MFA 二次验证")
		return
	}
	// Carries its own actionable message — collapsing it into the generic branch
	// below would report an oversize paste as "连接不存在".
	if errors.Is(err, service.ErrSQLTooLong) {
		resp.FailErr(c, resp.CodeBadRequest, err)
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
	if errors.Is(err, service.ErrMFALocked) {
		resp.Fail(c, resp.CodeTooManyReqs, mfaLockedMsg)
		return
	}
	if err == service.ErrTooManyJobs {
		resp.Fail(c, resp.CodeTooManyReqs, tooManyJobsMsg)
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
	// 查不出来就记一笔,别只留一个 0。
	//
	// 这一行原先是 `intercepts, _ :=`,注释写着"出错就是 0"。它确实不该让整个健康
	// 探针失败 —— 但一个 0 和"这台库上从没触发过规则"长得一模一样,于是 2026-09-10
	// 生产上那条 ERROR 1267(跨表 JOIN 的排序规则不一致,见迁移 0034)在界面上的全部
	// 表现就是"拦截次数 0",没有人会把它当成故障,只有翻 systemd 日志才看得见。
	//
	// 静默降级本身没错,错的是**降级时不出声**。
	intercepts, ierr := h.Repo.CountProdInterceptions()
	if ierr != nil {
		slog.Warn("count prod interceptions failed; reporting 0", "err", ierr)
	}
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

// consoleScriptExts 是上传通道接受的脚本扩展名:SQL 脚本、纯文本,以及 Oracle 程序包 / 存储过程的
// 惯用扩展名(网关自己下载出来的程序包就是 .pck,拿回来 \i 执行是日常操作)。
var consoleScriptExts = map[string]bool{
	".sql": true, ".txt": true,
	".pck": true, ".pkg": true, ".pks": true, ".pkb": true, ".spc": true, ".bdy": true,
	".prc": true, ".fnc": true, ".trg": true, ".typ": true, ".tps": true, ".tpb": true,
	".pls": true, ".plsql": true, ".ddl": true, ".dml": true, ".vw": true,
}

// checkConsoleScript 是脚本上传两条通道(multipart 与 JSON)共用的校验:扩展名在白名单里、
// 内容不超过 15MB。原先只有 multipart 有大小上限、哪条都不看类型,JSON 通道 .exe / .php /
// 无扩展名、30MB 都照单全收(2026-10 黑盒测试)。不通过时拒绝已写进响应。
func checkConsoleScript(c *gin.Context, filename string, size int) bool {
	if !consoleScriptExts[strings.ToLower(filepath.Ext(filepath.Base(strings.TrimSpace(filename))))] {
		resp.Fail(c, resp.CodeBadRequest, "仅支持 .sql / .txt 或 Oracle 程序包(.pck、.pkb、.pks 等)脚本文件")
		return false
	}
	if size > maxConsoleUploadBytes {
		resp.Fail(c, resp.CodeBadRequest, "脚本超过 15MB 上限")
		return false
	}
	return true
}

// ScriptUpload stores a script file (upload page) under the current user.
func (h *Handler) ScriptUpload(c *gin.Context) {
	filename := "script.sql"
	var content string
	if file, err := c.FormFile("file"); err == nil {
		if !checkConsoleScript(c, file.Filename, 0) {
			return
		}
		body, ok := readConsoleScriptFile(c, file)
		if !ok {
			return // 拒绝已经写进响应了
		}
		filename, content = file.Filename, body
	} else {
		// 请求体先封顶,免得一个超大 JSON 整个读进内存:JSON 转义会让正文膨胀(换行占 2 字节、
		// 转义的中文占 6 字节),所以给到 4 倍;精确的 15MB 在解析之后按正文长度判。
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4*maxConsoleUploadBytes)
		var req dto.ScriptScanReq
		if err := c.ShouldBindJSON(&req); err != nil {
			resp.Fail(c, resp.CodeBadRequest, "参数错误或脚本超过 15MB 上限")
			return
		}
		content = req.Content
		if strings.TrimSpace(req.Filename) != "" {
			filename = req.Filename
		}
	}
	if !checkConsoleScript(c, filename, len(content)) {
		return
	}
	up, err := h.Svc.UploadScript(middleware.CurrentUser(c), filename, content)
	if err == service.ErrScriptPathUnset {
		resp.Fail(c, resp.CodeScriptPathUnset, "请先在【系统设置 · 网关】配置上传脚本保存路径")
		return
	}
	if err != nil {
		resp.FailErr(c, resp.CodeInternalError, apimsg.Errorf("上传失败:%s", err))
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
		if errors.Is(err, service.ErrScriptReferenced) {
			resp.Fail(c, resp.CodeBadRequest, "该脚本已被审批单或发布单引用,作为执行证据保留,不能删除")
			return
		}
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

// ApprovalScript godoc
// @Summary 脚本工单所引用脚本的全文(审批人审阅用)
// @Router  /approvals/{id}/script [get]
func (h *Handler) ApprovalScript(c *gin.Context) {
	content, name, err := h.Svc.ApprovalScript(middleware.CurrentUser(c), pathID(c))
	switch {
	case err == nil:
		resp.OK(c, gin.H{"content": content, "filename": name})
	case errors.Is(err, service.ErrForbidden):
		resp.Fail(c, resp.CodeForbidden, "无权查看该工单的脚本")
	case errors.Is(err, service.ErrScriptChanged):
		resp.FailErr(c, resp.CodeBadRequest, err)
	default:
		resp.Fail(c, resp.CodeBadRequest, "脚本不可用(工单不是脚本工单,或文件已不存在)")
	}
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
	var database string // 执行窗口按库开,扫描结论随目标库变
	var progressID string
	if file, err := c.FormFile("file"); err == nil {
		body, ok := readConsoleScriptFile(c, file)
		if !ok {
			return // 拒绝已经写进响应了
		}
		filename, content = file.Filename, body
		connID, _ = strconv.ParseInt(c.PostForm("connectionId"), 10, 64)
		database = c.PostForm("database")
		progressID = c.PostForm("progressId")
	} else {
		var req dto.ScriptScanReq
		if err := c.ShouldBindJSON(&req); err != nil {
			resp.Fail(c, resp.CodeBadRequest, "参数错误")
			return
		}
		content = req.Content
		connID = req.ConnectionID
		database = req.Database
		progressID = req.ProgressID
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
	// 按**这个人**在目标实例上的完整判定扫描(ScanScriptFor):报告与执行说同一句话。
	// 上传脚本弹窗的扫描进度(script_progress.go):带了 progressId 才记
	prog := h.Svc.StartScriptProgress(middleware.CurrentUser(c), progressID)
	defer prog.Finish()
	scan, err := h.Svc.ScanScriptForCtx(service.WithScriptProgress(c.Request.Context(), prog), middleware.CurrentUser(c), connID, filename, content, database)
	if errors.Is(err, service.ErrForbidden) {
		// 与 execute、terminal 同口径:范围外的实例连扫描也不给(扫描结论本身就透露判定)
		resp.Fail(c, resp.CodeForbidden, "无权访问该实例")
		return
	}
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
			resp.FailErr(c, resp.CodeBadRequest, apimsg.Errorf("读取脚本失败:%s", rerr))
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
			resp.FailErr(c, resp.CodeInternalError, apimsg.Errorf("脚本保存失败:%s", err))
			return
		}
		saved = up.Path
		// The paste IS an upload now — carrying its id means a risky script's
		// approval ticket stores a bounded excerpt + digest referencing this
		// file, never the whole body (see SubmitScriptForApproval).
		req.UploadID = up.ID
	}
	// 整份脚本按这个人在目标实例上的**完整判定**分流(ScanScriptFor),不是只看字典:
	// 有一条被禁止 → 整份不执行;有一条要审批 → 整份送审,批准后一起执行;都放行才直接跑。
	// 只看字典时,能力矩阵才拦的语句(生产上的 CREATE TABLE)被当成安全脚本逐条直跑,
	// 第一条就只为自己建了一张单,后面的语句既没执行也没送审。
	// 执行进度(script_progress.go):先复扫(scan / judge),再逐条执行(run)
	prog := h.Svc.StartScriptProgress(middleware.CurrentUser(c), req.ProgressID)
	defer prog.Finish()
	scan, err := h.Svc.ScanScriptForCtx(service.WithScriptProgress(c.Request.Context(), prog), middleware.CurrentUser(c), req.ConnectionID, req.Filename, req.Content, req.Database)
	if errors.Is(err, service.ErrForbidden) {
		resp.Fail(c, resp.CodeForbidden, "无权访问该实例")
		return
	}
	if err != nil {
		// 整个执行都拒掉:分层判不出来时每条语句都会扫成安全,脚本就会不经复核直接跑。
		resp.Fail(c, resp.CodeInternalError, "脚本扫描不可用:目标实例的分层标签解析失败")
		return
	}
	if scan.Blocked {
		resp.FailErr(c, resp.CodeForbidden, apimsg.Errorf("脚本含被禁止的语句,整份脚本未执行 · %s", apimsg.M{S: scan.BlockedRule, Ref: scan.BlockedRuleRef}))
		return
	}
	if scan.HasRisky {
		// whole script must be submitted for approval (created from the scan
		// result, since the `\i file` wrapper isn't itself risk-matched)
		r, err := h.Svc.SubmitScriptForApproval(middleware.CurrentUser(c), req.ConnectionID, scan.Filename, req.Content, "脚本含高危语句,整脚本提交审批", req.MfaCode, req.Database, req.UploadID)
		if errors.Is(err, service.ErrMFALocked) {
			resp.Fail(c, resp.CodeTooManyReqs, mfaLockedMsg)
			return
		}
		if err == service.ErrMFARequired {
			resp.Fail(c, resp.CodeMFARequired, "生产操作需要 MFA 二次验证")
			return
		}
		if err != nil {
			// Carry the reason. This branch used to report a bare "提交失败", so a
			// storage-level refusal (a script too large for the command column, say)
			// reached the operator as an unexplained failure with nothing to act on
			// — the sibling branch below has always included it.
			resp.FailErr(c, resp.CodeBadRequest, apimsg.Errorf("提交失败:%s", err))
			return
		}
		resp.FailData(c, resp.CodeIntercepted, "脚本含高危语句,已提交审批", gin.H{"exec": r, "savedFile": savedFileName(saved)})
		return
	}
	results, err := h.Svc.ExecuteSafeScriptCtx(service.WithScriptProgress(termCtx(c, req.SessionID, h.Svc.TerminalPageSize()), prog), middleware.CurrentUser(c), req.ConnectionID, req.Content, req.MfaCode, req.Database)
	if errors.Is(err, service.ErrMFALocked) {
		resp.Fail(c, resp.CodeTooManyReqs, mfaLockedMsg)
		return
	}
	if err == service.ErrMFARequired {
		resp.Fail(c, resp.CodeMFARequired, "生产操作需要 MFA 二次验证")
		return
	}
	// 停在第几条、为什么:原样告诉人(含 Oracle 编译错误的行列),不压成一句"执行失败"
	var se *service.ScriptStmtError
	// 已执行的那几条、以及失败的那一条的结果一起带回,终端照样逐条打出来再说停在哪
	if errors.As(err, &se) {
		resp.FailDataErr(c, resp.CodeBadRequest, se, gin.H{"results": results})
		return
	}
	if err != nil {
		resp.Fail(c, resp.CodeBadRequest, "执行失败")
		return
	}
	resp.OK(c, gin.H{"executed": true, "statements": len(results), "results": results, "savedFile": savedFileName(saved)})
}

// ScriptProgress:上传脚本弹窗轮询的扫描 / 执行进度。只能查自己的;别人的、过期的、不存在的都是 40400。
func (h *Handler) ScriptProgress(c *gin.Context) {
	p, ok := h.Svc.ScriptProgressOf(middleware.CurrentUser(c), c.Param("id"))
	if !ok {
		resp.Fail(c, resp.CodeNotFound, "进度不存在或已过期")
		return
	}
	resp.OK(c, p)
}

// savedFileName:脚本存在服务器上的文件名(相对用户目录)。响应只回它,不回绝对路径 ——
// 绝对路径暴露部署目录结构,对使用者也没有用(下载走 /scripts/uploads/:id/download)。
func savedFileName(saved string) string {
	if saved == "" {
		return ""
	}
	return filepath.Base(saved)
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
	job, err := h.Svc.EnqueueExportAs(middleware.CurrentUser(c), req.ConnectionID, req.SQL, req.Name, req.Database, req.IncludeSensitive, req.Format, req.TargetTable)
	if err == service.ErrTooManyJobs {
		resp.Fail(c, resp.CodeTooManyReqs, tooManyJobsMsg)
		return
	}
	if errors.Is(err, service.ErrExportFormat) || errors.Is(err, service.ErrExportTargetTable) || errors.Is(err, service.ErrExportTargetTableBad) ||
		errors.Is(err, service.ErrExportNameRequired) || errors.Is(err, service.ErrExportNameTooLong) {
		resp.FailErr(c, resp.CodeBadRequest, err)
		return
	}
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
	var multi *service.ExportMultiStmtError
	if errors.As(err, &multi) {
		resp.FailErr(c, resp.CodeForbidden, apimsg.Errorf("数据导出一次只能提交一条语句,当前检测到 %d 条;请只保留要导出的那一条", multi.N))
		return
	}
	if err == service.ErrExportNotReadOnly {
		resp.Fail(c, resp.CodeForbidden, "数据导出仅允许单条只读查询(SELECT/SHOW 等);修改类语句请走命令行审批流")
		return
	}
	if err != nil {
		resp.FailErr(c, resp.CodeInternalError, apimsg.Errorf("提交失败:%s", err))
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

// CancelExport godoc
// @Summary 终止一个导出任务(排队中、待审批或执行中);执行中的同时终止目标库上的查询
// @Router  /export/jobs/{id}/cancel [post]
func (h *Handler) CancelExport(c *gin.Context) {
	job, err := h.Svc.CancelExport(middleware.CurrentUser(c), pathID(c))
	switch {
	case err == nil:
		resp.OK(c, job)
	case errors.Is(err, service.ErrNotFound):
		resp.Fail(c, resp.CodeBadRequest, "导出任务不存在")
	case errors.Is(err, service.ErrExportFinished):
		resp.Fail(c, resp.CodeBadRequest, "任务已经结束,无需终止")
	default:
		resp.FailErr(c, resp.CodeInternalError, apimsg.Errorf("终止失败: %s", err))
	}
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

// The credential may be carried in the Sec-WebSocket-Protocol header (see wsAuth)
// instead of the query string; advertise the subprotocols so the browser accepts
// the negotiated handshake.
//
// CheckOrigin stays open: a cookie-authenticated handshake must prove it read the
// CSRF cookie through the subprotocol, which a foreign page cannot do — that is
// what stops cross-site WebSocket hijacking, without depending on how a reverse
// proxy rewrites Host.
var upgrader = websocket.Upgrader{
	CheckOrigin:  func(r *http.Request) bool { return true },
	Subprotocols: []string{wsTokenProto, wsCSRFProto},
}

const (
	wsTokenProto = "aegisdb-token"
	wsCSRFProto  = "aegisdb-csrf"
)

// wsToken extracts the JWT for the terminal socket from the
// Sec-WebSocket-Protocol header ("aegisdb-token, <jwt>"). The credential is
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

// wsCSRF extracts the CSRF value from "aegisdb-csrf, <csrf>".
func wsCSRF(c *gin.Context) string {
	if proto := c.GetHeader("Sec-WebSocket-Protocol"); proto != "" {
		parts := strings.Split(proto, ",")
		if len(parts) == 2 && strings.TrimSpace(parts[0]) == wsCSRFProto {
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
	// ServerOutput:终端开了 SET SERVEROUTPUT ON(Oracle),执行后读回 DBMS_OUTPUT。
	ServerOutput bool `json:"serverOutput"`
	// SessionID:终端标签页的会话标识(见 gateway/term_session.go)。不跟 WS 连接绑:断线重连不该丢掉标签的事务。
	SessionID string `json:"sessionId"`
	// Binds:标签页上的 SQL*Plus 绑定变量(Oracle),见 gateway/oracle_binds.go。
	Binds []model.BindVar `json:"binds"`
}

// TerminalWS godoc
// @Summary 终端流式执行 (token via ?token=)
// @Router  /terminal/ws [get]
// newTermID 给一条终端 WebSocket 一个进程内唯一的标识(在线记录用)。
func newTermID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func (h *Handler) TerminalWS(c *gin.Context) {
	tokenStr := wsToken(c)
	fromCookie := tokenStr == ""
	if fromCookie {
		tokenStr = middleware.SessionTokenFromCookie(c)
	}
	claims, err := h.Svc.JWT.Parse(tokenStr)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	// Cookie 是浏览器自动带的,别的站点开一条 WebSocket 也会带上。终端是最敏感的执行通道,这里和写接口
	// 一样要求证明读得到 CSRF Cookie(见 upgrader 的注释)。
	if fromCookie && !middleware.CSRFMatches(h.Svc.JWT, claims.ID, wsCSRF(c)) {
		c.JSON(http.StatusForbidden, gin.H{"error": "csrf"})
		return
	}
	u, err := h.Repo.GetUserByID(claims.UserID)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "no user"})
		return
	}
	// 这条路绕开了 JWTAuth(WS 握手带不了 Authorization),所以来源地址要在这里自己盖上 ——
	// 漏了它的后果不是报错,而是**终端里跑的每一条 SQL 审计行都没有 IP**,而空在那一列是
	// 合法值(0041 之前的老行就是空),没有任何地方会提示。
	u.SessionID = claims.ID // 见 model.User.SessionID(M-6)
	middleware.BindRequestUser(c, u)
	// A non-active account's still-valid token must not open a terminal (mirror
	// the JWTAuth middleware check, which the WS route bypasses) — L-3.
	if u.Status != "active" {
		c.JSON(http.StatusForbidden, gin.H{"error": "account not active"})
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

	// 在线记录:这条路绕开了 JWTAuth,自己记一笔;开着的终端单独列出(管理后台「在线用户」)。
	middleware.TouchPresence(c, h.Repo, u, claims.IssuedAt)
	termID := newTermID()
	presence.Default.TermOpen(claims.ID, termID)
	defer presence.Default.TermClose(termID)
	instanceName := map[int64]string{} // 这条连接上用过的实例名,免得每条语句都查一次库
	nameOf := func(id int64) string {
		if n, ok := instanceName[id]; ok {
			return n
		}
		n := ""
		if cn, err := h.Repo.GetConnection(id); err == nil {
			n = cn.Env + "-" + cn.Name
		}
		instanceName[id] = n
		return n
	}

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

	// 正在执行的那条语句的取消函数。
	//
	// 操作员按 Ctrl+C 时,客户端发一帧 cancel;它由**读协程**收到 —— 执行发生在下面
	// 那个循环里,读协程与它并发(它本来就是为了在执行期间还能回心跳而存在的),
	// 所以这条路是通的。
	//
	// 取消**不等于没执行**:网关会要求目标库终止语句(MySQL 系由网关发 KILL QUERY,
	// 见 gateway/killquery.go;其它引擎靠驱动的取消请求),语句
	// 可能已经跑完了,也可能跑了一半。所以这里只负责把取消传下去,结果怎么说由
	// gateway.Executor 决定 —— 它说的是"可能已执行或部分执行,请自行核对"。
	var cancelMu sync.Mutex
	var cancelRunning context.CancelFunc
	setCancel := func(fn context.CancelFunc) {
		cancelMu.Lock()
		cancelRunning = fn
		cancelMu.Unlock()
	}

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
			case "cancel":
				// 没有正在跑的语句时按下的取消是个空动作,不必回话:客户端那一侧
				// 早已把提示打在屏幕上了,再回一条只会显得像出了错。
				cancelMu.Lock()
				fn := cancelRunning
				cancelMu.Unlock()
				if fn != nil {
					fn()
				}
			case "exec":
				select {
				case execCh <- msg:
				default:
					// A statement is already running; the console submits one at a
					// time, so this is a stray. Tell the client rather than queueing
					// work it is no longer waiting for.
					_ = send(wsNotice("error", "已有命令正在执行,请等待完成"))
				}
			}
		}
	}()

	for msg := range execCh {
		// Re-validate the session on every command so a mid-connection logout /
		// disable / password reset / role change takes effect immediately instead
		// of living until the socket drops (R5).
		// L-2:握手时验过的 exp 不会随时间自己再验一次。一条 WebSocket 可以开着好几个
		// 小时,而它是**执行通道** —— token 过期之后还能继续发语句,等于会话 TTL 对终端
		// 不生效。每条命令前重新比一次到期时间,过期就当场断开。
		if exp, eerr := claims.GetExpirationTime(); eerr != nil || exp == nil || time.Now().After(exp.Time) {
			send(wsNotice("session_revoked", "会话已过期，请重新登录"))
			return
		}
		fresh, ferr := h.Repo.GetUserByID(claims.UserID)
		if ferr != nil || fresh.Status != "active" || fresh.TokenVersion != claims.TokenVersion {
			send(wsNotice("session_revoked", "会话已失效，请重新登录"))
			return
		}
		// 每条命令都重新取一份 user 来复查会话,所以这一份也要盖上来源地址 —— 真正写审计
		// 的就是它(下面的 exec 拿的是 fresh,不是握手时那个 u)。
		fresh.SessionID = claims.ID // 同上:宽限期按会话记(M-6)
		middleware.BindRequestUser(c, fresh)
		// Re-check the terminal menu too: revoking a role's terminal access does NOT
		// bump the token version, so without this an open socket would keep executing
		// after its access was pulled (B9).
		if menus, _ := h.Repo.MenusForRoles(h.Repo.EffectiveRoleIDs(fresh)); !menus["terminal"] {
			send(wsNotice("session_revoked", "终端访问权限已被回收，请重新登录"))
			return
		}
		u = fresh
		// 每条语句一个上下文,跑完立刻清掉 —— 留着的话,下一次 Ctrl+C 会取消到
		// **下一条**语句上。
		ctx, cancel := context.WithCancel(context.Background())
		setCancel(cancel)
		presence.Default.TermExec(termID, msg.ConnectionID, nameOf(msg.ConnectionID), true)
		if gateway.ValidTermSessionID(msg.SessionID) {
			ctx = gateway.WithTermSession(ctx, u.ID, msg.SessionID)
		}
		ctx = gateway.WithResultPaging(ctx, u.ID, h.Svc.TerminalPageSize()) // 结果分批显示
		r, err := h.Svc.Exec(gateway.WithBinds(gateway.WithServerOutput(ctx, msg.ServerOutput), msg.Binds), u, msg.ConnectionID, msg.SQL, msg.Reason, msg.MfaCode, msg.Database)
		presence.Default.TermExec(termID, msg.ConnectionID, nameOf(msg.ConnectionID), false)
		setCancel(nil)
		cancel()
		if err == service.ErrForbidden {
			send(wsNotice("error", "命令被拒绝:能力矩阵禁止"))
			continue
		}
		if errors.Is(err, service.ErrMFALocked) {
			send(wsNotice("error", mfaLockedMsg))
			continue // 只跳过这一条命令,不断开终端
		}
		if err == service.ErrMFARequired {
			send(wsNotice("mfa_required", "生产操作需要 MFA 二次验证"))
			continue
		}
		if errors.Is(err, service.ErrSQLTooLong) {
			send(wsNoticeErr("error", err))
			continue
		}
		if err != nil {
			send(wsNotice("error", "连接不存在或执行失败"))
			continue
		}
		if r.Intercepted {
			send(gin.H{"type": "intercept", "approvalNo": r.ApprovalNo, "auditId": r.AuditID, "risk": r.Risk, "rule": r.Rule, "ruleRef": r.RuleRef,
				"osc": r.OSC, "notice": r.Notice, "noticeRef": r.NoticeRef})
			continue
		}
		// outputRef 与 text 一起发:text 是规范中文串(也是旧客户端唯一认得的),
		// outputRef 让界面用读者的语言重讲一遍 —— 同 intercept 分支的 rule/ruleRef。
		send(gin.H{"type": "output", "text": r.Output, "outputRef": r.OutputRef,
			"rows": r.Rows, "ms": r.Ms, "risk": r.Risk,
			"connMs": r.ConnMs, "queryMs": r.QueryMs, "fetchMs": r.FetchMs, "processMs": r.ProcessMs,
			"notice": r.Notice, "noticeRef": r.NoticeRef, "oscBlockers": r.OSCBlockers,
			"columns": r.Columns, "data": r.Data, "truncated": r.Truncated,
			"resultId": r.ResultID, "totalRows": r.TotalRows, "beyondCap": r.BeyondCap,
			"serverOutput": r.ServerOutput, "serverOutputTruncated": r.ServerOutputTruncated, "binds": r.Binds})
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

// wsNotice / wsNoticeErr 是终端 WebSocket 的提示消息:message 是规范中文,messageRef 让界面按读者的语言显示(apimsg)。
func wsNotice(typ, msg string) gin.H {
	return gin.H{"type": typ, "message": msg, "messageRef": apimsg.Lookup(msg)}
}

func wsNoticeErr(typ string, err error) gin.H {
	return gin.H{"type": typ, "message": err.Error(), "messageRef": apimsg.RefOf(err)}
}
