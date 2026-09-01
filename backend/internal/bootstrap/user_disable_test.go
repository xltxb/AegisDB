package bootstrap

// 停用账户是一把没有回程的闸。
//
// 停用之后:登录被拒(Login 只放行 active)、在手的 token 每个请求都被中间件按库里
// 的最新状态挡下、WebSocket 握手和逐条命令也各复查一次。这是对的 —— 停用就该立刻
// 生效,而不是等 token 过期。
//
// 但正因为它这么彻底,把**最后一个管理员**停掉就没有任何路可以走回来:重新启用要调
// 管理员接口,而调它需要一个还能登录的管理员。剩下的唯一办法是有人去改数据库。
//
// 下面这两条钉的就是这件事:闸可以关,但不能把钥匙一起关在里面。

import (
	"encoding/json"
	"net/http"
	"testing"
)

// 管理员不能停用自己 —— 哪怕还有别的管理员在。
//
// 这一条不是为了防死锁(那是下一条),而是因为它几乎总是误点:状态徽章就在用户
// 列表里自己那一行上,点下去的后果是当场把自己踢出控制台。
func TestUserDisable_AdminCannotDisableThemselves(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	me := app.userIDByEmail(admin, "linwei@vela.io")

	r := app.do(http.MethodPatch, "/api/v1/users/"+itoa(me), admin, map[string]any{"status": "disabled"})
	if r.Code == 0 {
		t.Fatal("管理员把自己停用了 —— 这一下会当场把自己踢出控制台")
	}
	// 拒绝之后账户必须还是好的,不能停在一个半停用的状态里。
	if app.userStatus(admin, "linwei@vela.io") != "active" {
		t.Error("被拒的停用不该真的落库")
	}
}

// 最后一个管理员不能被停用 —— 停了就再没有人能把任何人启用回来。
func TestUserDisable_TheLastAdminCannotBeDisabled(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")

	// 先造第二个管理员,证明"有两个的时候可以停用其中一个"。
	adminRole := app.roleIDByCode(admin, "admin")
	zw := app.userIDByEmail(admin, "zhangwei@vela.io")
	eq(t, app.do(http.MethodPatch, "/api/v1/users/"+itoa(zw), admin, map[string]any{
		"roleIds": []int64{adminRole},
	}).Code, 0, "把张伟提成管理员")

	// 两个管理员在,停用其中一个是允许的。
	eq(t, app.do(http.MethodPatch, "/api/v1/users/"+itoa(zw), admin, map[string]any{
		"status": "disabled",
	}).Code, 0, "还有别的管理员时,停用是允许的")

	// 现在只剩 linwei 一个管理员了。任何人都不能把他停掉 —— 包括他自己。
	me := app.userIDByEmail(admin, "linwei@vela.io")
	if r := app.do(http.MethodPatch, "/api/v1/users/"+itoa(me), admin, map[string]any{
		"status": "disabled",
	}); r.Code == 0 {
		t.Fatal("最后一个管理员被停用了 —— 从此没有人能启用任何账户,只能去改数据库")
	}
	if app.userStatus(admin, "linwei@vela.io") != "active" {
		t.Error("被拒的停用不该真的落库")
	}
}

func (a *testApp) userStatus(token, email string) string {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/users", token, nil)
	var users []struct {
		Email  string `json:"email"`
		Status string `json:"status"`
	}
	_ = json.Unmarshal(r.Data, &users)
	for _, u := range users {
		if u.Email == email {
			return u.Status
		}
	}
	a.t.Fatalf("user %s not found", email)
	return ""
}
