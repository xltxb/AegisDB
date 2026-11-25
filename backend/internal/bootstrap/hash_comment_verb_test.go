package bootstrap

import (
	"net/http"
	"testing"
)

// C1(2026-10 安全审计):MySQL 把 `#` 到行尾当注释,判定层不认,以 `#` 注释开头的语句取不到
// 动词、按读级放行。前面多一行 `#` 注释,判定结果必须与不带注释时完全一样。
func TestTerminalExec_LeadingHashCommentJudgedLikeBareStatement(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@aegisdb.io", "aegisdb123")
	prod := app.connIDByEnv(token, "prod")
	for _, sql := range []string{
		"UPDATE orders SET status = 'x' WHERE id = 1;",
		"DELETE FROM orders WHERE id = 1;",
	} {
		bare := app.do(http.MethodPost, "/api/v1/terminal/exec", token, map[string]any{
			"connectionId": prod, "sql": sql, "reason": "c1",
		})
		hashed := app.do(http.MethodPost, "/api/v1/terminal/exec", token, map[string]any{
			"connectionId": prod, "sql": "# note\n" + sql, "reason": "c1",
		})
		if bare.Code == 0 {
			t.Fatalf("前提不成立:prod 上 %q 应被拦下,实际直接执行", sql)
		}
		eq(t, hashed.Code, bare.Code, "带 # 注释的 "+sql)
	}
}
