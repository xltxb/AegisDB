package bootstrap

// 同一件事的入口,校验要一样严。
//
// 三处不一致,每一处的后果都不同:
//
//   · **建连接**不校验 policy,**改连接**校验 —— 于是一台实例可以带着一个谁也不认得
//     的 policy 建出来,而同样的值在改它的时候会被拒。policy 决定这台实例走哪条闸
//     (strict / approve-1 / audit-only),一个认不出的值在判定层读作"不是 strict",
//     也就是最松的那一档。
//   · **停用/启用**接受任意 status 串,而判定层只认 "maint" —— 别的一律当在线。
//     打错一个字,界面上显示「维护中」,而网关照常放行。
//   · **Exec** 对空 / 纯注释输入照样下发,**ExecAsync** 同样的输入直接拒。

import (
	"net/http"
	"testing"
)

func TestCreateConnection_RejectsUnknownPolicy(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")

	r := app.do(http.MethodPost, "/api/v1/connections", token, map[string]any{
		"name": "bad-policy", "engine": "MySQL 8.0", "host": "10.0.0.1:3306",
		"env": "dev", "policy": "audit-onl", "defaultRole": "developer",
	})
	if r.Code == 0 {
		t.Error("建连接不校验 policy —— 打错一个字就建出一台走最松那档闸的实例")
	}

	// 正经的 policy 照常能建。
	eq(t, app.do(http.MethodPost, "/api/v1/connections", token, map[string]any{
		"name": "good-policy", "engine": "MySQL 8.0", "host": "10.0.0.2:3306",
		"env": "dev", "policy": "audit-only", "defaultRole": "developer",
	}).Code, 0, "合法 policy 应当能建")
}

func TestToggleConnection_RejectsUnknownStatus(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	conn := app.connIDByEnv(token, "dev")

	r := app.do(http.MethodPatch, "/api/v1/connections/"+itoa(conn), token,
		map[string]any{"status": "maintenance"}) // 正确的值是 "maint"
	if r.Code == 0 {
		t.Error("停用接口接受了一个判定层不认的状态 —— 界面显示「维护中」,而网关照常放行")
	}

	for _, st := range []string{"maint", "online"} {
		eq(t, app.do(http.MethodPatch, "/api/v1/connections/"+itoa(conn), token,
			map[string]any{"status": st}).Code, 0, "合法状态 "+st)
	}
}

// 空 / 纯注释的输入:两个入口要给同一个答案。
func TestExec_EmptyInputIsRefusedLikeAsync(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	conn := app.connIDByEnv(token, "dev")

	for _, sql := range []string{"", "   ", "-- 只是一句注释", "/* 什么也没有 */"} {
		sync := app.do(http.MethodPost, "/api/v1/terminal/exec", token,
			map[string]any{"connectionId": conn, "sql": sql, "reason": "空输入"})
		async := app.do(http.MethodPost, "/api/v1/terminal/exec-async", token,
			map[string]any{"connectionId": conn, "sql": sql, "reason": "空输入"})
		if (sync.Code == 0) != (async.Code == 0) {
			t.Errorf("同一段输入 %q:同步回 %d、异步回 %d —— 同一件事两个答案",
				sql, sync.Code, async.Code)
		}
	}
}
