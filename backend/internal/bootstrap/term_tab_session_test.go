package bootstrap

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"aegisdb/internal/gateway"
)

// Web 命令行:同一台实例开两个标签,是两条数据库连接;同一个标签里的语句始终在同一条上
// (gateway/term_session.go)。这里走完整的 HTTP 链路:/terminal/exec 带 sessionId、关标签的
// 接口、登出。用 SQLite 的临时表当"会话"的探针 —— 临时表只在建它的那条连接上可见。
func TestTerminal_EachTabGetsItsOwnConnection(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	dev := app.realSQLiteConnIn(admin, "dev", "tab-conn")
	uid := app.userIDByEmail(admin, "linwei@aegisdb.io")
	t.Cleanup(func() {
		gateway.CloseTermSessionsOf(uid)
		time.Sleep(300 * time.Millisecond) // 后台关连接,让 TempDir 能删掉 SQLite 文件
	})
	run := func(tab, sql string) apiResp {
		return app.do(http.MethodPost, "/api/v1/terminal/exec", admin, map[string]any{
			"connectionId": dev, "sql": sql, "sessionId": tab,
		})
	}

	eq(t, run("tab-a", "CREATE TEMP TABLE tab_probe (v INTEGER)").Code, 0, "标签 A 建临时表")
	eq(t, run("tab-a", "INSERT INTO tab_probe VALUES (1)").Code, 0, "标签 A 写临时表")
	if r := run("tab-a", "SELECT COUNT(*) FROM tab_probe"); r.Code != 0 || !strings.Contains(string(r.Data), `"1"`) {
		t.Fatalf("标签 A 的下一条语句应还在同一条连接上: code=%d msg=%s data=%s", r.Code, r.Msg, r.Data)
	}
	if r := run("tab-b", "SELECT COUNT(*) FROM tab_probe"); r.Code == 0 && !strings.Contains(r.Msg+string(r.Data), "no such table") {
		t.Fatalf("标签 B 是另一条连接,不该看到标签 A 的临时表: %s", r.Data)
	}
	eq(t, gateway.TermSessionCount(uid), 2, "两个标签两条连接")

	// 关掉标签 A:连接断开,临时表随之消失
	eq(t, app.do(http.MethodPost, "/api/v1/terminal/sessions/close", admin, map[string]any{"sessionId": "tab-a"}).Code, 0, "关标签")
	eq(t, gateway.TermSessionCount(uid), 1, "关掉后只剩标签 B")
	if r := run("tab-a", "SELECT COUNT(*) FROM tab_probe"); r.Code == 0 && !strings.Contains(r.Msg+string(r.Data), "no such table") {
		t.Fatalf("关掉后再用同一标识应是新连接,临时表不该还在: %s", r.Data)
	}
	if r := app.do(http.MethodPost, "/api/v1/terminal/sessions/close", admin, map[string]any{"sessionId": "../bad id"}); r.Code == 0 {
		t.Error("非法标识应被拒绝")
	}

	// 登出(空闲锁定走的也是它):这个人所有标签的连接都断开
	eq(t, app.do(http.MethodPost, "/api/v1/auth/logout", admin, nil).Code, 0, "登出")
	eq(t, gateway.TermSessionCount(uid), 0, "登出后不留任何标签连接")
}
