package bootstrap

import (
	"net/http"
	"strings"
	"testing"
)

// L-API1(2026-10 安全审计):开放接口的失败计数按来源 IP,且任何一次成功都清零 —— 手里有
// 自己的有效凭据,每猜别人的 secret 几次就用自己的凭据调一次,计数永远到不了阈值。
// 现在猜错某把 key 的 secret 只有那把 key 成功才清零。
func TestOpenAPI_OwnCredentialDoesNotResetGuessesAgainstAnother(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	mine := app.issueClient(admin, "攻击者自己的", "zhangwei@vela.io", nil)
	victim := app.issueClient(admin, "别人的", "zhangwei@vela.io", nil)
	victimKey := victim[:strings.Index(victim, ".")]

	for i := 0; i < 12; i++ {
		app.openDo(http.MethodGet, "/api/v1/open/releases/REL-1", victimKey+".guess-"+itoa(int64(i)), nil)
		app.openDo(http.MethodGet, "/api/v1/open/releases/REL-1", mine, nil) // 用自己的凭据"清零"
	}
	eq(t, app.openDo(http.MethodGet, "/api/v1/open/releases/REL-1", victim, nil).Code, 42900,
		"别人那把 key 被猜错多次后应被锁(哪怕中间夹着自己凭据的成功调用)")
	if r := app.openDo(http.MethodGet, "/api/v1/open/releases/REL-1", mine, nil); r.Code == 42900 {
		t.Error("自己那把 key 不应受牵连")
	}
}

// 2026-10-26 回归 05:按来源 IP 的封禁只封未知 / 错误的 key。同一出口 IP 上一个配错(已删)的
// key 连续失败,IP 被封后:未知 key 照旧 429(且不再清零),有效凭据照常通过。
func TestOpenAPI_PerIPBanOnlyBlocksUnknownKeys(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	valid := app.issueClient(admin, "同出口的有效凭据", "zhangwei@vela.io", nil)

	for i := 0; i < 12; i++ {
		app.openDo(http.MethodGet, "/api/v1/open/releases/REL-1", "deletedkey"+itoa(int64(i))+".secret", nil)
	}
	// 原行为保留:封禁中的未知 key 直接 429。
	eq(t, app.openDo(http.MethodGet, "/api/v1/open/releases/REL-1", "anotherunknown.secret", nil).Code, 42900,
		"同 IP 未知 key 失败过多后,未知 key 应被封")
	// 新行为:有效凭据不受同 IP 封禁影响。
	if r := app.openDo(http.MethodGet, "/api/v1/open/releases/REL-1", valid, nil); r.Code == 42900 || r.Code == 40100 {
		t.Fatalf("有效凭据不应被同 IP 封禁拦下,got %d %s", r.Code, r.Msg)
	}
	// 有效凭据成功不清零 IP 计数:未知 key 仍被封。
	eq(t, app.openDo(http.MethodGet, "/api/v1/open/releases/REL-1", "yetanother.secret", nil).Code, 42900,
		"有效凭据成功后未知 key 仍应被封")
	// 存在的 key 猜错 secret 不受 perIP 影响,只走自己的 perKey(返回 401 而非 429)。
	validKey := valid[:strings.Index(valid, ".")]
	eq(t, app.openDo(http.MethodGet, "/api/v1/open/releases/REL-1", validKey+".wrong", nil).Code, 40100,
		"存在的 key 猜错 secret 应 40100(按 key 计数)")
}
