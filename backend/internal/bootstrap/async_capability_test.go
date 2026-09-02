package bootstrap

// H-1(白盒审计):异步执行通道判定的是**原始串**,不是拆分后的语句。
//
// SplitStatements(";UPDATE …") 只拆出一条,于是原实现保留了对原串的判定;而
// verbRe 匹配不到前导分隔符后面的关键字 → 动词为空 → 按 select 归类 → 只读角色
// 在 PROD 写库,完全绕过能力矩阵。
//
// 同步 Exec 早就无条件拆分,它的注释(ER3)白纸黑字写着"绝不可对原始串判定"。
// 两条通道通往同一个执行器,判定就必须是同一套 —— 这一组测试钉的就是这件事。

import (
	"net/http"
	"testing"

	"velagateway/pkg/resp"
)

// 关掉 write 之后,前导分隔符不能把一条 UPDATE 偷渡进异步通道。
func TestAsyncExec_LeadingSeparatorCannotSmuggleAWrite(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	dev := app.connIDByEnv(token, "dev")
	roleID := app.roleIDByCode(token, "admin")

	// 把 write 在所有分层上关掉:此后任何写都必须被拦,不管从哪条通道进来。
	eq(t, app.do(http.MethodPut, "/api/v1/roles/"+itoa(roleID)+"/capabilities", token, map[string]any{
		// write 与 ddl 都关掉:CREATE 走的是 ddl 那一格,只关 write 它照样合法通过。
		"matrix": map[string]any{
			"write": map[string]string{"prod": "deny", "staging": "deny", "dev": "deny"},
			"ddl":   map[string]string{"prod": "deny", "staging": "deny", "dev": "deny"},
		},
	}).Code, 0, "deny write")

	// 先确认同步通道确实拦得住 —— 它是这条测试的参照物。
	eq(t, app.do(http.MethodPost, "/api/v1/terminal/exec", token, map[string]any{
		"connectionId": dev, "sql": ";UPDATE t_user SET name='x'",
	}).Code, resp.CodeForbidden, "同步通道拦住了带前导分隔符的写")

	// 异步通道必须给出同样的答案。
	smuggles := []string{
		";UPDATE t_user SET name='x'",
		"/*x*/;INSERT INTO t_user VALUES(1)",
		";CREATE TABLE evil(a int)",
		"  ;  DELETE FROM t_user",
	}
	for _, sql := range smuggles {
		r := app.do(http.MethodPost, "/api/v1/terminal/exec-async", token, map[string]any{
			"connectionId": dev, "sql": sql, "reason": "smuggle",
		})
		if r.Code != resp.CodeForbidden {
			t.Errorf("异步通道放行了一条被矩阵禁止的写: %q → code=%d msg=%s", sql, r.Code, r.Msg)
		}
	}
}

// 反方向:修完之后,正常的读仍然走得通。把洞堵上不能把通道一起堵死。
func TestAsyncExec_OrdinaryReadsStillGoThrough(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	dev := app.connIDByEnv(token, "dev")

	for _, sql := range []string{"SELECT run_long_proc()", "SELECT 1;", "  SELECT 2  "} {
		r := app.do(http.MethodPost, "/api/v1/terminal/exec-async", token, map[string]any{
			"connectionId": dev, "sql": sql, "reason": "ok",
		})
		eq(t, r.Code, 0, "正常的读应当照常提交: "+sql)
	}
}

// 多语句仍按最严的那一条判 —— 这是原来就有的行为,不能在修 H-1 时丢掉。
func TestAsyncExec_MultiStatementStillJudgedByTheStrictest(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	dev := app.connIDByEnv(token, "dev")
	roleID := app.roleIDByCode(token, "admin")

	eq(t, app.do(http.MethodPut, "/api/v1/roles/"+itoa(roleID)+"/capabilities", token, map[string]any{
		// write 与 ddl 都关掉:CREATE 走的是 ddl 那一格,只关 write 它照样合法通过。
		"matrix": map[string]any{
			"write": map[string]string{"prod": "deny", "staging": "deny", "dev": "deny"},
			"ddl":   map[string]string{"prod": "deny", "staging": "deny", "dev": "deny"},
		},
	}).Code, 0, "deny write")

	r := app.do(http.MethodPost, "/api/v1/terminal/exec-async", token, map[string]any{
		"connectionId": dev, "sql": "SELECT 1; UPDATE t_user SET name='x'", "reason": "mixed",
	})
	if r.Code != resp.CodeForbidden {
		t.Errorf("一读一写的批次应当按写来判,got code=%d msg=%s", r.Code, r.Msg)
	}
}

// 空输入/纯注释不该被当成一次可执行的提交。
func TestAsyncExec_BlankInputIsRefused(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	dev := app.connIDByEnv(token, "dev")

	for _, sql := range []string{"   ", "-- just a comment", "/* only a comment */"} {
		r := app.do(http.MethodPost, "/api/v1/terminal/exec-async", token, map[string]any{
			"connectionId": dev, "sql": sql, "reason": "blank",
		})
		if r.Code == 0 {
			t.Errorf("空输入不该产生一个后台任务: %q", sql)
		}
	}
}
