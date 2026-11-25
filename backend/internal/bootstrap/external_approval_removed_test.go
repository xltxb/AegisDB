package bootstrap

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
)

// 审批魔方(外部审批)对接已移除(#137)。这里钉住"移除"本身:
//   - 回调路由不在了(旧密钥也一样 404),单子不会被外部改动;
//   - 老调用方整包提交里带着的 approval.external.* 键仍被收下(不报错),但不再有默认值入库;
//   - 审批列表不再透出外部任务号。
func TestExternalApprovalRemoved(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@aegisdb.io", "aegisdb123")

	// 新库不再写 approval.external.* 的默认值。
	all, err := app.repo.AllSettings()
	if err != nil {
		t.Fatal(err)
	}
	for k := range all {
		if strings.HasPrefix(k, "approval.external.") {
			t.Errorf("新库不该再有 %s", k)
		}
	}

	// 老配置原样整包提交仍被收下(值合法)。
	const oldSecret = "s3cr3t-callback-key"
	eq(t, app.do(http.MethodPut, "/api/v1/settings", token, map[string]any{
		"approval.external.enabled":          true,
		"approval.external.releaseEnabled":   true,
		"approval.external.baseURL":          "https://approval.corp.io",
		"approval.external.token":            "tok-xyz",
		"approval.external.aiGroup":          "ops",
		"approval.external.callbackBaseURL":  "https://gw.corp.io",
		"approval.external.callbackSecret":   oldSecret,
		"approval.external.callbackAllowIPs": "10.1.2.3,10.9.0.0/16",
	}).Code, 0, "老的 approval.external.* 键仍被收下")

	// 这两个键没有代码再读,但新写进去的值仍必须是密文,不能以明文落库。
	stored, err := app.repo.AllSettings()
	if err != nil {
		t.Fatal(err)
	}
	for k, plain := range map[string]string{"approval.external.token": "tok-xyz", "approval.external.callbackSecret": oldSecret} {
		v, ok := stored[k]
		if !ok || v == "" {
			t.Errorf("%s 应已入库", k)
		} else if strings.Contains(v, plain) {
			t.Errorf("%s 不该以明文落库:%s", k, v)
		}
	}

	ap := app.submitProdHighRisk(token)

	// 回调路由已删:带着对的旧密钥也是 404,单子仍待审批。
	for _, url := range []string{
		app.srv.URL + "/api/v1/approvals/lark/callback",
		app.srv.URL + "/api/v1/approvals/lark/callback?secret=" + oldSecret,
	} {
		req, _ := http.NewRequest(http.MethodPost, url,
			bytes.NewReader([]byte(`{"external_task_id":"`+ap.ApNo+`","approved":true,"approver":["herbert@tbu.net"]}`)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+oldSecret)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		eq(t, res.StatusCode, http.StatusNotFound, "回调路由已移除:"+url)
	}
	eq(t, app.approvalRow(token, ap.ApNo).Status, "pending", "单子不被外部改动")

	// 即使库里(历史数据)还留着外部任务号,列表也不再透出。
	if err := app.repo.DB().Exec("UPDATE tbl_approval SET external_task_id = ? WHERE ap_no = ?", "cube-77", ap.ApNo).Error; err != nil {
		t.Fatal(err)
	}
	r := app.do(http.MethodGet, "/api/v1/approvals", token, nil)
	eq(t, r.Code, 0, "list approvals")
	if strings.Contains(string(r.Data), "externalTaskId") || strings.Contains(string(r.Data), "cube-77") {
		t.Errorf("审批列表不该再带外部任务号:%s", r.Data)
	}
}
