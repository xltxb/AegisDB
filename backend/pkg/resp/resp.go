// Package resp provides the unified API envelope { code, msg, data }.
package resp

import "github.com/gin-gonic/gin"

// Business error codes (see backend doc §10).
const (
	CodeOK              = 0
	CodeBadRequest      = 40001
	CodeUnauthorized    = 40100
	CodeForbidden       = 40300
	CodeNotFound        = 40400
	CodeIntercepted     = 42200 // command blocked, approval required (returns ap_no)
	CodeScriptPathUnset = 42600 // upload-script save path not configured yet
	CodeExportPathUnset = 42601 // data-export save path not configured yet
	CodeMFARequired     = 42800 // prod op needs a valid TOTP step-up code
	CodeIPBlocked       = 40301 // source IP not in the allowlist
	CodeTooManyReqs     = 42900 // too many failed login attempts (rate limited)
	// CodeOscDisabled:在线变更(ADR 0011)未启用。单独给一个码,而不是复用 403 或
	// 404 —— 界面要凭它说出「这套东西还没做完,被有意关着」,而不是让人去查权限
	// 或者怀疑路由写错了。
	CodeOscDisabled   = 42700
	CodeInternalError = 50000
)

// R is the response envelope. swagger: resp.R
type R struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data any    `json:"data,omitempty"`
}

// OK writes a success envelope.
func OK(c *gin.Context, data any) {
	c.JSON(200, R{Code: CodeOK, Msg: "ok", Data: data})
}

// Fail writes a business error with HTTP 200 (errors carried in code).
func Fail(c *gin.Context, code int, msg string) {
	c.JSON(200, R{Code: code, Msg: msg})
}

// FailStatus 写一个带**真实 HTTP 状态码**的失败信封。
//
// 「一律 HTTP 200,业务码在信封里」是对**我们自己的前端**定的约定 —— 它读 code 字段,
// 而把业务失败塞进 HTTP 状态码会让「网络出错」和「这条命令被拦下了」变成同一件事。
//
// 外部系统不一样:它按状态码判断成败。一律 200 意味着一次被拒的回调在对面看起来像
// 成功 —— 不重试、不告警,而那次审批就这么丢了。所以**给外部系统调用的端点**(目前只有
// 飞书回调)用这一个:状态码说成败,信封照旧带业务码说清是哪一种失败。
//
// 这是那条约定唯一的例外,别扩大它。
func FailStatus(c *gin.Context, httpStatus, code int, msg string) {
	c.JSON(httpStatus, R{Code: code, Msg: msg})
}

// FailData writes a business error that also carries a payload (e.g. ap_no on intercept).
func FailData(c *gin.Context, code int, msg string, data any) {
	c.JSON(200, R{Code: code, Msg: msg, Data: data})
}

// Abort writes the error and stops the middleware chain.
func Abort(c *gin.Context, code int, msg string) {
	c.AbortWithStatusJSON(200, R{Code: code, Msg: msg})
}
