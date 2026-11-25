package bootstrap

import (
	"fmt"
	"net/http"
	"testing"
)

// 登录频控不能被"自己登一次"清零。
//
// 原先计数只按来源 IP,而任何一次成功登录都会把这个 IP 的计数整个清掉。攻击者手里
// 只要有一个自己的账号,每试 4 次别人的口令就登自己一次,频控就永远到不了阈值 ——
// 口令和 TOTP 都能无限地猜。
//
// 现在分两层:**(IP, 账号)** 的失败计数,只有这个账号自己登录成功才清;**整个 IP** 的
// 失败总数(阈值更高)从不因成功而清,挡的是轮换邮箱的口令喷洒。

func (a *testApp) tryLogin(email, password string) int {
	a.t.Helper()
	return a.do(http.MethodPost, "/api/v1/auth/login", "", map[string]any{
		"email": email, "password": password,
	}).Code
}

func TestLoginRateLimit_OwnSuccessDoesNotResetAnotherAccount(t *testing.T) {
	app := newTestApp(t)
	const victim = "zhangwei@vela.io"
	for i := 0; i < 4; i++ {
		eq(t, app.tryLogin(victim, "guess"), 40100, "猜错口令")
	}
	eq(t, app.tryLogin("linwei@vela.io", "vela123"), 0, "攻击者登录自己的账号")
	eq(t, app.tryLogin(victim, "guess"), 40100, "第 5 次失败:仍被处理,并就此进入锁定")
	if code := app.tryLogin(victim, "guess"); code != 42900 {
		t.Fatalf("第 6 次猜受害者口令应被频控挡住,实际 code=%d —— 自己的成功登录清掉了别人的计数", code)
	}
}

func TestLoginRateLimit_SprayingAcrossAccountsIsCapped(t *testing.T) {
	app := newTestApp(t)
	blocked := false
	for i := 0; i < 40 && !blocked; i++ {
		blocked = app.tryLogin(fmt.Sprintf("u%d@vela.io", i), "guess") == 42900
	}
	if !blocked {
		t.Fatal("同一来源轮换 40 个邮箱各试一次,始终没被频控挡住")
	}
}

// 真人手滑几次、然后登进去了:他自己的计数应当清零,不该留着把他(或同一出口 IP 后面的
// 同事)送进锁定。
func TestLoginRateLimit_SuccessClearsOwnFailures(t *testing.T) {
	app := newTestApp(t)
	const me = "linwei@vela.io"
	for i := 0; i < 4; i++ {
		eq(t, app.tryLogin(me, "typo"), 40100, "手滑")
	}
	eq(t, app.tryLogin(me, "vela123"), 0, "登录成功")
	for i := 0; i < 4; i++ {
		eq(t, app.tryLogin(me, "typo"), 40100, "成功之后重新计数,4 次以内不该被挡")
	}
}
