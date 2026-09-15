package bootstrap

import (
	"encoding/json"
	"net/http"
	"testing"
)

// 按窗口回看放行记录,权限必须与审计本身同一把尺子。
//
// 这个入口交出来的是**别人执行过的命令原文** —— 和审计列表是同一类东西,只是换了个
// 问法。要是它自己判一套(比如"能看见执行窗口页就能看"),那它就成了绕过审计可见性
// 的后门:一个够格管窗口、却不够格看全量活动的人,能从这里把命令一条条读走。
func TestExecWindowAudit_NeedsTheSameClearanceAsTheAuditLog(t *testing.T) {
	app := newTestApp(t)

	// 先确认有审计权限的人拿得到。
	admin := app.login("linwei@vela.io", "vela123")
	if r := app.do(http.MethodGet, "/api/v1/exec-windows/1/audit", admin, nil); r.Code != 0 {
		t.Fatalf("管理员应当拿得到,实际 code=%d msg=%q", r.Code, r.Msg)
	}

	// 没有审计权限的人不该拿到。
	ro := app.login("zhaolei@vela.io", "vela123")
	r := app.do(http.MethodGet, "/api/v1/exec-windows/1/audit", ro, nil)
	if r.Code == 0 {
		var rows []map[string]any
		_ = json.Unmarshal(r.Data, &rows)
		t.Errorf("没有审计权限的人从这个入口读到了 %d 条命令记录 —— "+
			"它绕过了审计的可见性规则", len(rows))
	}
}
