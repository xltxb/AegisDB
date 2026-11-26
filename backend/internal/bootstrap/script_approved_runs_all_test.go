package bootstrap

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// 批准之后执行脚本工单,脚本里的**每一条**语句都要执行 —— 不是只有第一条 DDL。
func TestApprovedScript_RunsEveryStatement(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	dev, prod := app.sameDBConns(admin, "script-all")

	var b strings.Builder
	b.WriteString("DROP TABLE IF EXISTS t_old;\n\n")
	tables := []string{"t_s1", "t_s2", "t_s3"}
	for _, tb := range tables {
		var cols []string
		for i := 0; i < 20; i++ {
			cols = append(cols, fmt.Sprintf("  c%d INTEGER", i))
		}
		b.WriteString("CREATE TABLE " + tb + " (\n  id INTEGER PRIMARY KEY,\n" + strings.Join(cols, ",\n") + "\n);\n\n")
	}
	up, _ := app.uploadScript(admin, "ddl.sql", b.String())
	eq(t, app.do(http.MethodPost, "/api/v1/scripts/execute", admin, map[string]any{
		"connectionId": prod, "uploadId": up}).Code, 42200, "含 DROP 的脚本在 prod 上进审批")
	id := lastApproval(t, app).ID
	approver := app.login("zhangwei@aegisdb.io", "aegisdb123")
	eq(t, app.do(http.MethodPost, "/api/v1/approvals/"+itoa(id)+"/approve", approver, nil).Code, 0, "审批通过")

	r := app.do(http.MethodPost, "/api/v1/approvals/"+itoa(id)+"/execute", admin, nil)
	t.Logf("execute: code=%d msg=%s data=%s", r.Code, r.Msg, r.Data)
	t.Logf("result: %s", lastApproval(t, app).Result)
	for _, tb := range tables {
		if !app.tableExists(admin, dev, tb) {
			t.Errorf("表 %s 没有建出来", tb)
		}
	}
}
