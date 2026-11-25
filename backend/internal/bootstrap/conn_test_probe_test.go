package bootstrap

// 「测试连通性 / 批量巡检」必须真的连,并且失败要以**失败**回。
//
// 两个成因叠在一起,才让巡检把 88 台实例全报成"可连":
//
//   1. Executor.Test 是个 `sleep(120ms); return true` 的桩,从不拨号;
//   2. 即便它返回 false,handler 用的是 resp.OK —— 信封 code 恒为 0,而前端判的
//      正是 code,于是永远进"成功"分支。
//
// 这条用例钉的是第 2 条:一个够不到的目标,接口必须回非零 code。少了它,即便探测
// 修好了,界面照样一片绿。

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestConnectionProbe_UnreachableTargetIsAFailure(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")

	// 一台指向本机关闭端口的实例:立刻被拒,不必等满 dial 超时。
	r := app.do(http.MethodPost, "/api/v1/connections", admin, map[string]any{
		"name": "probe-unreachable", "engine": "MySQL 8.0", "host": "127.0.0.1", "port": 1,
		"env": "dev", "policy": "audit-only", "database": "d",
		"username": "u", "password": "p",
	})
	if r.Code != 0 {
		t.Fatalf("建连接失败: code=%d msg=%s", r.Code, r.Msg)
	}
	var conn struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(r.Data, &conn)

	got := app.do(http.MethodPost, "/api/v1/connections/"+itoa(conn.ID)+"/test", admin, nil)
	if got.Code == 0 {
		t.Fatal("连不上的实例被报成了成功 —— 巡检会把它显示成「可连」")
	}
	if got.Msg == "" {
		t.Error("失败要带原因,否则人不知道该查网络还是查账号")
	}
}

// 没配凭据的演示实例同样不能报成功。
//
// 从前它回的是"已接入网关" —— 把"我们没配凭据"说成了"那台库是好的"。
func TestConnectionProbe_SimulatedConnectionIsNotSuccess(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	oracle := app.newOracleConn(admin, "probe-sim") // 无账号密码

	got := app.do(http.MethodPost, "/api/v1/connections/"+itoa(oracle)+"/test", admin, nil)
	if got.Code == 0 {
		t.Fatal("没有凭据的实例不该报成功")
	}
}
