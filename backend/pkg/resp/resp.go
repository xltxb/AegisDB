// Package resp provides the unified API envelope { code, msg, data }.
package resp

import (
	"github.com/gin-gonic/gin"

	"velagateway/internal/apimsg"
	"velagateway/internal/model"
)

// Business error codes (see backend doc §10).
const (
	CodeOK              = 0
	CodeBadRequest      = 40001
	CodeUnauthorized    = 40100
	CodeForbidden       = 40300
	CodeIntercepted     = 42200 // command blocked, approval required (returns ap_no)
	CodeScriptPathUnset = 42600 // upload-script save path not configured yet
	CodeExportPathUnset = 42601 // data-export save path not configured yet
	CodeInspectionPathUnset = 42602 // inspection-report save path not configured yet
	CodeNotFound            = 40400 // the named object does not exist (e.g. an inspection report for an unknown instance)
	CodeTooLarge            = 41300 // request body over the endpoint's size limit
	CodeMFARequired     = 42800 // prod op needs a valid TOTP step-up code
	CodePasswordRequired = 42801 // the action needs the current login password re-entered (e.g. MFA enrolment)
	CodeIPBlocked       = 40301 // source IP not in the allowlist
	CodeSSONoAccount    = 40302 // SSO identity is valid but no gateway account has been opened for it
	CodeAdminIPBlocked  = 40303 // source IP not in the Admin-console allowlist (admin APIs only)
	CodeAdminIdle       = 40304 // Admin-console session idle too long; re-enter (not a logout)
	CodeTooManyReqs     = 42900 // too many failed login attempts (rate limited)
	CodeSSONoSession    = 40101 // SSO enabled but the request carries no SSO session (not an error: show the SSO button)
	CodeInternalError   = 50000
)

// R is the response envelope. swagger: resp.R
//
// MsgRef 是 Msg 那句中文的机器可读身份(apimsg):前端按读者的语言重讲一遍,认不出就显示 Msg。
// Msg 本身永远是规范中文,开放接口的调用方与日志看到的仍是原来那句。
type R struct {
	Code   int            `json:"code"`
	Msg    string         `json:"msg"`
	MsgRef *model.RuleRef `json:"msgRef,omitempty"`
	Data   any            `json:"data,omitempty"`
}

// OK writes a success envelope.
func OK(c *gin.Context, data any) {
	c.JSON(200, R{Code: CodeOK, Msg: "ok", Data: data})
}

// Fail writes a business error with HTTP 200 (errors carried in code).
func Fail(c *gin.Context, code int, msg string) {
	c.JSON(200, R{Code: code, Msg: msg, MsgRef: apimsg.Lookup(msg)})
}

// FailErr writes err as the business error: msg is err.Error() verbatim, msgRef comes from
// the error (apimsg.Errorf / a recognised fixed sentence) so the console can translate it.
func FailErr(c *gin.Context, code int, err error) {
	c.JSON(200, R{Code: code, Msg: err.Error(), MsgRef: apimsg.RefOf(err)})
}

// FailData writes a business error that also carries a payload (e.g. ap_no on intercept).
func FailData(c *gin.Context, code int, msg string, data any) {
	c.JSON(200, R{Code: code, Msg: msg, MsgRef: apimsg.Lookup(msg), Data: data})
}

// FailDataErr is FailData with the message taken from err (see FailErr).
func FailDataErr(c *gin.Context, code int, err error, data any) {
	c.JSON(200, R{Code: code, Msg: err.Error(), MsgRef: apimsg.RefOf(err), Data: data})
}

// Abort writes the error and stops the middleware chain.
func Abort(c *gin.Context, code int, msg string) {
	c.AbortWithStatusJSON(200, R{Code: code, Msg: msg, MsgRef: apimsg.Lookup(msg)})
}

// AbortErr is Abort with the message taken from err (see FailErr).
func AbortErr(c *gin.Context, code int, err error) {
	c.AbortWithStatusJSON(200, R{Code: code, Msg: err.Error(), MsgRef: apimsg.RefOf(err)})
}
