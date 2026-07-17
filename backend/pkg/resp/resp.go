// Package resp provides the unified API envelope { code, msg, data }.
package resp

import "github.com/gin-gonic/gin"

// Business error codes (see backend doc §10).
const (
	CodeOK              = 0
	CodeBadRequest      = 40001
	CodeUnauthorized    = 40100
	CodeForbidden       = 40300
	CodeIntercepted     = 42200 // command blocked, approval required (returns ap_no)
	CodeScriptPathUnset = 42600 // upload-script save path not configured yet
	CodeExportPathUnset = 42601 // data-export save path not configured yet
	CodeMFARequired     = 42800 // prod op needs a valid TOTP step-up code
	CodeIPBlocked       = 40301 // source IP not in the allowlist
	CodeTooManyReqs     = 42900 // too many failed login attempts (rate limited)
	CodeInternalError   = 50000
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

// FailData writes a business error that also carries a payload (e.g. ap_no on intercept).
func FailData(c *gin.Context, code int, msg string, data any) {
	c.JSON(200, R{Code: code, Msg: msg, Data: data})
}

// Abort writes the error and stops the middleware chain.
func Abort(c *gin.Context, code int, msg string) {
	c.AbortWithStatusJSON(200, R{Code: code, Msg: msg})
}
