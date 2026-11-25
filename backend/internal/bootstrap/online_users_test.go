package bootstrap

// 管理后台「在线用户」:谁此刻登录着、最近一次操作、开着哪些终端。
//
// 在线记录是进程里的一份(presence.Default),同一个测试进程里别的用例也会往里写 —— 这里只看
// 自己登录的那几个人。

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"velagateway/internal/presence"
)

type onlineResp struct {
	Sessions []struct {
		UserID     int64      `json:"userId"`
		Name       string     `json:"name"`
		IP         string     `json:"ip"`
		LastAction *time.Time `json:"lastAction"`
	} `json:"sessions"`
}

func (a *testApp) onlineUserIDs(token string) map[int64]bool {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/online-users", token, nil)
	if r.Code != 0 {
		a.t.Fatalf("在线用户: code=%d msg=%s", r.Code, r.Msg)
	}
	var out onlineResp
	_ = json.Unmarshal(r.Data, &out)
	ids := map[int64]bool{}
	for _, s := range out.Sessions {
		ids[s.UserID] = true
	}
	return ids
}

func (a *testApp) userIDOf(token string) int64 {
	a.t.Helper()
	var me struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(a.do(http.MethodGet, "/api/v1/auth/me", token, nil).Data, &me)
	if me.ID == 0 {
		a.t.Fatal("取不到当前用户 id")
	}
	return me.ID
}

func TestOnlineUsers_ListsLoggedInAndDropsOnLogout(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	other := app.login("zhangwei@vela.io", "vela123")
	otherID := app.userIDOf(other)
	// 在线记录是进程里共享的一份,同一进程里前面的用例可能刚给同一个用户号写过"最近活跃"、
	// 节流还没过 —— 先清掉,只看这个用例自己的操作。
	presence.Default.DropUser(otherID)
	app.do(http.MethodGet, "/api/v1/connections", other, nil) // 一次真实操作

	ids := app.onlineUserIDs(admin)
	if !ids[app.userIDOf(admin)] || !ids[otherID] {
		t.Fatalf("两个登录着的人都应在线: %v", ids)
	}

	// 用户表的"最近活跃"是真实时间(后台写库,稍等)
	deadline := time.Now().Add(3 * time.Second)
	for {
		var users []struct {
			ID         int64  `json:"id"`
			LastActive string `json:"lastActive"`
		}
		_ = json.Unmarshal(app.do(http.MethodGet, "/api/v1/users", admin, nil).Data, &users)
		got := ""
		for _, u := range users {
			if u.ID == otherID {
				got = u.LastActive
			}
		}
		if _, err := time.Parse(time.RFC3339, got); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("最近活跃应写成真实时间,实际 %q", got)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// 退出登录:立刻不在列表里
	if r := app.do(http.MethodPost, "/api/v1/auth/logout", other, nil); r.Code != 0 {
		t.Fatalf("退出: %s", r.Msg)
	}
	if app.onlineUserIDs(admin)[otherID] {
		t.Error("退出登录后不该还在在线列表里")
	}
}

func TestOnlineUsers_AdminOnly(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	roID := app.roleIDByCode(admin, "ro")
	eq(t, app.do(http.MethodPost, "/api/v1/users", admin, map[string]any{
		"email": "ro-online@vela.io", "name": "只读", "password": "initpass9", "roleIds": []int64{roID}}).Code, 0, "建只读账号")
	ro := app.login("ro-online@vela.io", "initpass9")
	if r := app.do(http.MethodGet, "/api/v1/online-users", ro, nil); r.Code != 40300 {
		t.Errorf("非管理员不能看在线用户,实际 %d", r.Code)
	}
}
