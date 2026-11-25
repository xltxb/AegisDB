package bootstrap

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
)

// 审批人要能看到脚本全文,不只是工单里的摘要。
//
// 脚本工单只存一段摘要(见 scriptExcerpt),而读上传脚本的接口只给上传者本人 —— 审批人
// 没有任何办法看到第 40 行以后的 DDL,却要为它们签字。全文按工单的可见范围开放(发起人、
// 审批链成员);给出之前校验哈希:看到的必须就是审查时、也是将来执行的那份字节。
func TestApprovalScript_ApproverReadsTheWholeScript(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	prod := app.connIDByEnv(admin, "prod")
	script := "CREATE TABLE t_a (id INT);\nCREATE TABLE t_b (id INT);\nDROP TABLE t_old;\n"
	up, path := app.uploadScript(admin, "ddl.sql", script)
	eq(t, app.do(http.MethodPost, "/api/v1/scripts/execute", admin, map[string]any{
		"connectionId": prod, "uploadId": up}).Code, 42200, "高危脚本进审批")
	id := lastApproval(t, app).ID

	approver := app.login("zhangwei@vela.io", "vela123")
	r := app.do(http.MethodGet, "/api/v1/approvals/"+itoa(id)+"/script", approver, nil)
	eq(t, r.Code, 0, "审批链成员读脚本全文")
	var body struct {
		Filename string `json:"filename"`
		Content  string `json:"content"`
	}
	_ = json.Unmarshal(r.Data, &body)
	if body.Content != script || body.Filename != "ddl.sql" {
		t.Errorf("应返回完整脚本: %+v", body)
	}

	// 既不是发起人、也不在审批链上的人看不到
	ro := app.login("zhaolei@vela.io", "vela123")
	if r := app.do(http.MethodGet, "/api/v1/approvals/"+itoa(id)+"/script", ro, nil); r.Code == 0 {
		t.Error("无关的人读到了工单的脚本全文")
	}

	// 审查之后文件被换了:不展示,说清楚
	if err := os.WriteFile(path, []byte("DROP TABLE t_everything;\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r = app.do(http.MethodGet, "/api/v1/approvals/"+itoa(id)+"/script", approver, nil)
	if r.Code == 0 || !strings.Contains(r.Msg, "修改") {
		t.Errorf("被换过的脚本不该展示: code=%d msg=%s", r.Code, r.Msg)
	}
}
