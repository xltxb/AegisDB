package bootstrap

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"aegisdb/pkg/resp"
	"aegisdb/pkg/totp"
)

// H4(2026-10 安全审计):生产操作的 MFA 步进验证与 /auth/mfa/disable 没有失败次数限制 ——
// 拿到别人登录态的人可以一直猜 6 位码。按用户计失败次数:5 次失败锁 15 分钟,锁定期间连对的码
// 也不认,并写审计。

func wrongCode(secret string) string {
	n, _ := strconv.Atoi(totp.Code(secret, time.Now()))
	return fmt.Sprintf("%06d", (n+500000)%1000000) // 与当前有效码一定不同
}

func TestMFA_StepUpLocksAfterRepeatedFailures(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@aegisdb.io", "aegisdb123")
	secret := app.setupMFA(token)
	prod := app.connIDByEnv(token, "prod")
	exec := func(code string) int {
		return app.do(http.MethodPost, "/api/v1/terminal/exec", token, map[string]any{"connectionId": prod, "sql": "SELECT 1", "mfaCode": code}).Code
	}
	for i := 1; i <= 5; i++ {
		eq(t, exec(wrongCode(secret)), resp.CodeMFARequired, fmt.Sprintf("第 %d 次错码", i))
	}
	// 锁定后,对的码也不认
	eq(t, exec(totp.Code(secret, time.Now())), resp.CodeTooManyReqs, "锁定期间正确的码也拒绝")

	r := app.do(http.MethodGet, "/api/v1/audit?pageSize=50", token, nil)
	if !strings.Contains(string(r.Data), "mfa.locked") {
		t.Error("锁定应写审计 mfa.locked")
	}
}

func TestMFA_DisableCountsFailuresToo(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@aegisdb.io", "aegisdb123")
	secret := app.setupMFA(token)
	for i := 1; i <= 5; i++ {
		r := app.do(http.MethodPost, "/api/v1/auth/mfa/disable", token, map[string]any{"code": wrongCode(secret)})
		if r.Code == 0 {
			t.Fatalf("第 %d 次错码不该关掉 MFA", i)
		}
	}
	r := app.do(http.MethodPost, "/api/v1/auth/mfa/disable", token, map[string]any{"code": totp.Code(secret, time.Now())})
	eq(t, r.Code, resp.CodeTooManyReqs, "锁定期间不能用对的码关掉 MFA")
}

func TestMFA_SuccessResetsTheCounter(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@aegisdb.io", "aegisdb123")
	secret := app.setupMFA(token)
	prod := app.connIDByEnv(token, "prod")
	exec := func(code string) int {
		return app.do(http.MethodPost, "/api/v1/terminal/exec", token, map[string]any{"connectionId": prod, "sql": "SELECT 1", "mfaCode": code}).Code
	}
	for i := 0; i < 4; i++ {
		exec(wrongCode(secret))
	}
	eq(t, exec(totp.Code(secret, time.Now())), 0, "第 5 次给对的码:放行并清零")
}

// 2026-10-26 回归:前端每条生产命令第一次都不带码发,收到 42800 才弹输码框。这一下不能算输错 ——
// 从前每弹一次框先扣一次额度,5 次就锁 15 分钟。
func TestMFA_EmptyCodeIsNotAFailure(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@aegisdb.io", "aegisdb123")
	secret := app.setupMFA(token)
	prod := app.connIDByEnv(token, "prod")
	exec := func(code string) int {
		return app.do(http.MethodPost, "/api/v1/terminal/exec", token, map[string]any{"connectionId": prod, "sql": "SELECT 1", "mfaCode": code}).Code
	}
	for i := 1; i <= 8; i++ {
		eq(t, exec(""), resp.CodeMFARequired, fmt.Sprintf("第 %d 次不带码:要求输码", i))
	}
	for i := 1; i <= 8; i++ {
		r := app.do(http.MethodPost, "/api/v1/auth/mfa/disable", token, map[string]any{"code": ""})
		if r.Code == 0 || r.Code == resp.CodeTooManyReqs {
			t.Fatalf("第 %d 次不带码关 MFA:应要求输码,实际 %d", i, r.Code)
		}
	}
	eq(t, exec(totp.Code(secret, time.Now())), 0, "之后给对的码照常放行(没被锁)")
	// 真输错照旧计数:见 TestMFA_StepUpLocksAfterRepeatedFailures
}

// L-AUTH4(2026-10 安全审计):绑定 MFA 不要求当前口令 —— 拿到别人会话的人给还没绑定的账号
// 绑上自己的验证器,此后本人反而被挡在外面。有本地口令的账号必须出示当前登录密码。
func TestMFASetup_RequiresCurrentPassword(t *testing.T) {
	app := newTestApp(t)
	token := app.login("chenhao@aegisdb.io", "aegisdb123")
	eq(t, app.do(http.MethodPost, "/api/v1/auth/mfa/setup", token, nil).Code, 42801, "不带密码:要求出示")
	eq(t, app.do(http.MethodPost, "/api/v1/auth/mfa/setup", token, map[string]any{"password": "wrong-pass"}).Code, 42801, "密码错误")
	eq(t, app.do(http.MethodPost, "/api/v1/auth/mfa/setup", token, map[string]any{"password": "aegisdb123"}).Code, 0, "密码正确")
}
