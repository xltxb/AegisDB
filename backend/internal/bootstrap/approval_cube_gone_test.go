package bootstrap

import (
	"bytes"
	"net/http"
	"testing"
)

// 回调端点能驱动一张生产工单走向终态。整条对接删掉之后,这扇门必须真的不在了 ——
// 不是「鉴权失败」,是「没有这个路由」。
func TestApprovalCube_CallbackRouteIsGone(t *testing.T) {
	app := newTestApp(t)
	res, err := http.Post(app.srv.URL+"/api/v1/approvals/lark/callback", "application/json",
		bytes.NewReader([]byte(`{"external_task_id":"AP-1","approved":true}`)))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	eq(t, res.StatusCode, http.StatusNotFound, "the callback route no longer exists")
}

// 旧版前端(或缓存着旧页面的浏览器)保存设置时还会带着 approval.external.* —— 静默丢弃,
// 不能让它们重新长回库里。
func TestSettings_LegacyApprovalCubeKeysAreDropped(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	app.setSettings(admin, map[string]any{
		"approval.external.enabled": true, "approval.external.token": "tok",
	})
	for k := range settingRows(t, app.repo.DB()) {
		if len(k) >= 18 && k[:18] == "approval.external." {
			t.Errorf("legacy key %s was stored", k)
		}
	}
}
