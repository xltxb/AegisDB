package bootstrap

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// EA3:有的调用方发不了请求头,凭据只能放在 URL 里(?secret= / ?token=)。gin 默认的访问日志
// 会把 path+rawQuery 原样打出来,等于把一个能用的凭据写进谁都能读的日志,还永远不轮换。
// 访问日志必须把这几个参数的值抹掉,请求本身照常记。对任意路由都成立,不依赖哪个接口还收 ?secret=。
func TestAccessLog_QuerySecretIsRedacted(t *testing.T) {
	var logBuf bytes.Buffer
	prev := gin.DefaultWriter
	gin.DefaultWriter = &logBuf
	t.Cleanup(func() { gin.DefaultWriter = prev })

	app := newTestApp(t) // router captures DefaultWriter at construction
	token := app.login("linwei@aegisdb.io", "aegisdb123")

	const leak = "s3cr3t-callback-key"
	for _, q := range []string{"secret", "token", "access_token"} {
		eq(t, app.do(http.MethodGet, "/api/v1/approvals?page=1&"+q+"="+leak, token, nil).Code, 0, "request with ?"+q+"=")
	}

	if strings.Contains(logBuf.String(), leak) {
		t.Errorf("a query credential was written to the access log:\n%s", logBuf.String())
	}
	// The request itself must still be logged, with its non-sensitive parameters.
	if !strings.Contains(logBuf.String(), "/api/v1/approvals") || !strings.Contains(logBuf.String(), "page=1") {
		t.Errorf("request missing from the access log:\n%s", logBuf.String())
	}
}
