package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"velagateway/pkg/resp"
)

// 请求体上限(2026-10 安全审计 H3)。
//
// 从前只有巡检上传、开放接口 JSON、脚本上传三处自己限了请求体,其余路由(包括不用登录的
// /auth/login)整包读进内存 —— 一个超大 JSON 就能把进程撑爆。这里按路由分档兜底:
//   - 不用登录的入口(登录、SSO、审批回调):1MB,够用且不给未登录的人留空间;
//   - 脚本上传:64MB(它自己再按 4×15MB 收紧);
//   - 其余(登录后的接口、开放接口):32MB —— SQL 文本上限 15MB,JSON 转义后可能翻倍。
// 处理器自己的上限照旧生效,这里只是最外面那一道。

const (
	bodyLimitUnauth  = 1 << 20
	bodyLimitScripts = 64 << 20
	bodyLimitDefault = 32 << 20
)

func bodyLimitFor(path string) int64 {
	switch {
	case strings.HasPrefix(path, "/api/v1/auth/"), path == "/api/v1/approvals/lark/callback":
		return bodyLimitUnauth
	case strings.HasPrefix(path, "/api/v1/scripts/"):
		return bodyLimitScripts
	default:
		return bodyLimitDefault
	}
}

// BodyLimit 给请求体套上按路由分档的上限。声明了长度且超限的当场拒绝(41300);没声明长度的
// (分块传输)读到上限即断,处理器会读到错误。
func BodyLimit() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Body == nil || c.Request.Body == http.NoBody {
			c.Next()
			return
		}
		limit := bodyLimitFor(c.Request.URL.Path)
		if c.Request.ContentLength > limit {
			resp.Abort(c, resp.CodeTooLarge, "请求体过大")
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
		c.Next()
	}
}
