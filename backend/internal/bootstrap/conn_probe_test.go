package bootstrap

// 建连接时要真去探一下,并且如实回报。
//
// FR-CONN-02「保存即测试接入」。从前建连接这一路从不探测,却把 Status 写死成 online ——
// 一台刚建好、地址填错、防火墙没开的实例,在列表里显示"在线"。
//
// 这和 Executor.Test 从前那个桩是同一类谎:它曾经是 `sleep(120ms); return true`,
// 于是 88 台实例全被报成可连。那个桩已经改成真 ping 了,而**建连接**这一路还在
// 无条件说好话。
//
// 探不通不该拒绝创建 —— 先把实例登记好、再去开防火墙是正当顺序。要改的是别声称
// 它是好的:把探测结果跟着创建结果一起回给管理员,让他当场知道。

import (
	"encoding/json"
	"net/http"
	"testing"
)

type connCreated struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Probe *struct {
		OK      bool   `json:"ok"`
		Message string `json:"message"`
	} `json:"probe"`
}

func (a *testApp) createConn(token string, body map[string]any) connCreated {
	a.t.Helper()
	env := a.do(http.MethodPost, "/api/v1/connections", token, body)
	if env.Code != 0 {
		a.t.Fatalf("建连接失败 code=%d msg=%s", env.Code, env.Msg)
	}
	var out connCreated
	if err := json.Unmarshal(env.Data, &out); err != nil {
		a.t.Fatalf("decode: %v", err)
	}
	return out
}

func TestCreateConnection_ProbesAndReportsHonestly(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")

	// 一个连不上的地址:端口 1 上不会有 MySQL。
	got := app.createConn(admin, map[string]any{
		"name": "探测不通的实例", "engine": "mysql", "host": "127.0.0.1:1",
		"env": "dev", "policy": "audit-only",
		"username": "root", "password": "x", "database": "d",
	})
	if got.ID == 0 {
		t.Fatal("连不通不该拒绝创建 —— 先登记实例、再去开防火墙是正当顺序")
	}
	if got.Probe == nil {
		t.Fatal("创建结果里没有探测结论 —— 保存即测试,那个「测试」的结果得让人看见")
	}
	if got.Probe.OK {
		t.Errorf("一个连不上的地址被报成可连:%q —— 一个永远说好话的探测比没有探测更糟", got.Probe.Message)
	}
	if got.Probe.Message == "" {
		t.Error("探测失败没有说为什么,管理员无从下手")
	}
}

// 没配凭据的实例:探测要说清是"我们没配凭据",而不是"那台库是好的"。
func TestCreateConnection_NoCredentialsIsNotAGoodNews(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")

	got := app.createConn(admin, map[string]any{
		"name": "没填凭据的实例", "engine": "mysql", "host": "10.0.0.9:3306",
		"env": "dev", "policy": "audit-only",
	})
	if got.Probe == nil {
		t.Fatal("创建结果里没有探测结论")
	}
	if got.Probe.OK {
		t.Errorf("没配凭据却报可连:%q —— 把「我们没配凭据」说成「那台库是好的」是同一种谎", got.Probe.Message)
	}
}
