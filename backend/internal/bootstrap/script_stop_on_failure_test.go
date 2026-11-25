package bootstrap

import (
	"net/http"
	"strings"
	"testing"
)

// \i 脚本里一条语句被目标库拒绝:停在这一条,说清是第几条、为什么;后面的不执行。
//
// 从前脚本执行不看每条的结果,失败的语句照样计数,终端显示"脚本执行成功 · N 条语句"。
// 生产上 \i PK_AGENT.pck 报成功,包体却是 INVALID(那一例是 Oracle 编译错误,
// 见 gateway/oracle_create_errors.go;这里用 SQLite 的普通执行错误钉住脚本这一层)。
func TestScript_StopsAtTheFirstFailingStatement(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	dev := app.realSQLiteConnIn(admin, "dev", "script-stop")
	app.setSettings(admin, map[string]any{"script.savePath": t.TempDir()})

	r := app.do(http.MethodPost, "/api/v1/scripts/execute", admin, map[string]any{
		"connectionId": dev, "filename": "mixed.sql",
		"content": "CREATE TABLE s_ok (id INTEGER);\nINSERT INTO s_no_such_table VALUES (1);\nCREATE TABLE s_after (id INTEGER);\n",
	})
	if r.Code == 0 {
		t.Fatalf("第 2 条失败,脚本不该报成功: %s", r.Data)
	}
	if !strings.Contains(r.Msg, "已执行 1/3 条后失败") || !strings.Contains(r.Msg, "第 2 条") || !strings.Contains(r.Msg, "s_no_such_table") {
		t.Errorf("要说清停在第几条、为什么,实际 %q", r.Msg)
	}
	if !app.tableExists(admin, dev, "s_ok") {
		t.Error("失败之前的第 1 条应已执行")
	}
	if app.tableExists(admin, dev, "s_after") {
		t.Error("失败之后的语句不该执行")
	}
}
