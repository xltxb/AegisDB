package bootstrap

import (
	"net/http"
	"testing"

	"aegisdb/internal/service"

	"github.com/gin-gonic/gin"
)

// SSO 的路径同时决定 Cookie 发给谁:路由必须真挂在 service 里那几个路径上,Cookie 才带得回来(#186)。
func TestSSOPathsAreRouted(t *testing.T) {
	app := newTestApp(t)
	routes := map[string]bool{}
	for _, r := range app.srv.Config.Handler.(*gin.Engine).Routes() {
		routes[r.Method+" "+r.Path] = true
	}
	for _, want := range []string{
		http.MethodGet + " " + service.OIDCStartPath,
		http.MethodGet + " " + service.OIDCCallbackPath,
		http.MethodPost + " " + service.SSOLoginPath,
	} {
		if !routes[want] {
			t.Errorf("没有路由 %s", want)
		}
	}
}
