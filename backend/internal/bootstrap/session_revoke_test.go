package bootstrap

import (
	"net/http"
	"testing"
)

// 管理员改了账号的状态 / 解绑了 MFA,已经发出去的会话就该作废。
//
// 状态检查本身挡得住"此刻不是 active"的请求,但挡不住**恢复**:把人置成 invited、
// 过一会儿再改回 active,他手里那个没过期的旧 token 又能用了 —— 管理员停他那一下,
// 想的正是"这个会话结束了"。MFA 解绑同理:解绑通常是因为手机丢了,丢的手机上开着的
// 会话不能继续活着。

func TestSessionRevoke_StatusRoundTripDoesNotResurrectOldToken(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	old := app.login("zhaolei@vela.io", "vela123")
	id := app.userIDByEmail(admin, "zhaolei@vela.io")

	for _, st := range []string{"invited", "active"} {
		eq(t, app.do(http.MethodPatch, "/api/v1/users/"+itoa(id), admin, map[string]any{"status": st}).Code, 0, "改状态 "+st)
	}
	if r := app.do(http.MethodGet, "/api/v1/auth/me", old, nil); r.Code == 0 {
		t.Fatal("停用再恢复之后,旧 token 仍然有效")
	}
	// 重新登录照常可用。
	eq(t, app.do(http.MethodGet, "/api/v1/auth/me", app.login("zhaolei@vela.io", "vela123"), nil).Code, 0, "重新登录")
}

func TestSessionRevoke_AdminMFAResetEndsSessions(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	old := app.login("zhaolei@vela.io", "vela123")
	id := app.userIDByEmail(admin, "zhaolei@vela.io")

	eq(t, app.do(http.MethodPost, "/api/v1/users/"+itoa(id)+"/mfa/reset", admin, nil).Code, 0, "解绑 MFA")
	if r := app.do(http.MethodGet, "/api/v1/auth/me", old, nil); r.Code == 0 {
		t.Fatal("MFA 解绑之后,旧会话仍然有效")
	}
}
