package bootstrap

import (
	"encoding/json"
	"net/http"
	"testing"
)

// 2026-10-22:没有任何授权的人进数据查询页,拦截卡片上「去申请权限」跳到申请页。地址是设置项
// bizQuery.applyUrl,没设过时用 SRD 服务中心的申请页;可以改,可以清空(不给链接),非 http(s) 拒绝。
func TestBizQuery_ApplyURLSetting(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	applyURL := func() string {
		var d struct {
			Limits struct {
				ApplyURL string `json:"applyUrl"`
			} `json:"limits"`
		}
		_ = json.Unmarshal(app.do(http.MethodGet, "/api/v1/bizquery/grants", admin, nil).Data, &d)
		return d.Limits.ApplyURL
	}
	eq(t, applyURL(), "https://srd.bssrvc66.com/service-center/query/db-gateway-grant", "默认申请页")

	eq(t, app.do(http.MethodPut, "/api/v1/settings", admin, map[string]any{"bizQuery.applyUrl": "https://srd.example.com/apply"}).Code, 0, "改地址")
	eq(t, applyURL(), "https://srd.example.com/apply", "改过之后")

	if r := app.do(http.MethodPut, "/api/v1/settings", admin, map[string]any{"bizQuery.applyUrl": "javascript:alert(1)"}); r.Code == 0 {
		t.Error("非 http(s) 地址不应保存")
	}
	eq(t, app.do(http.MethodPut, "/api/v1/settings", admin, map[string]any{"bizQuery.applyUrl": ""}).Code, 0, "清空")
	eq(t, applyURL(), "", "清空 = 不给链接")
}
