package bootstrap

import (
	"encoding/json"
	"net/http"
	"testing"
)

// M4(2026-10 安全审计):编辑连接时口令留空 = 沿用旧口令。地址也一起改了,网关就会拿存着的
// 口令去连新地址 —— 能改连接配置的人把地址指向自己的机器,就能收到口令明文(看不到口令的人
// 借此拿到它)。地址(主机 / 端口)变了必须重新输入口令;从库同理。
func TestConnection_AddressChangeRequiresPassword(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	cr := app.do(http.MethodPost, "/api/v1/connections", admin, map[string]any{
		"name": "rebind", "engine": "MySQL 8.0", "host": "10.0.0.1:3306",
		"env": "staging", "policy": "strict", "username": "u", "password": "secret", "database": "app",
	})
	eq(t, cr.Code, 0, "create connection")
	var conn struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(cr.Data, &conn)
	put := func(host, password string, extra map[string]any) apiResp {
		body := map[string]any{
			"name": "rebind", "engine": "MySQL 8.0", "host": host,
			"env": "staging", "policy": "strict", "username": "u", "password": password, "database": "app",
		}
		for k, v := range extra {
			body[k] = v
		}
		return app.do(http.MethodPut, "/api/v1/connections/"+itoa(conn.ID), admin, body)
	}

	eq(t, put("10.0.0.1:3306", "", nil).Code, 0, "地址没变:口令留空照旧沿用")
	if put("10.6.6.6:3306", "", nil).Code == 0 {
		t.Error("主机变了、口令留空,不应沿用旧口令")
	}
	if put("10.0.0.1:3307", "", nil).Code == 0 {
		t.Error("端口变了、口令留空,不应沿用旧口令")
	}
	eq(t, put("10.6.6.6:3306", "secret2", nil).Code, 0, "地址变了且重新输入了口令")

	// 从库:有自己的账号时,从库地址变了要重新输入从库口令
	eq(t, put("10.6.6.6:3306", "", map[string]any{
		"replicaHost": "10.6.6.7:3306", "replicaUsername": "ro", "replicaPassword": "ro-secret",
	}).Code, 0, "首次配置从库并输入从库口令")
	if put("10.6.6.6:3306", "", map[string]any{
		"replicaHost": "10.6.6.8:3306", "replicaUsername": "ro", "replicaPassword": "",
	}).Code == 0 {
		t.Error("从库地址变了、从库口令留空,不应沿用旧的从库口令")
	}

	// 从库沿用主库账号:从库地址变了、主库口令也留空 —— 拒绝,而且说的是人话(不带 bad request 前缀)
	r := put("10.6.6.6:3306", "", map[string]any{"replicaHost": "10.6.6.9:3306", "replicaUsername": ""})
	if r.Code == 0 {
		t.Error("从库地址变了、沿用主库账号且主库口令留空,不应沿用")
	}
	if r.Msg != "更新失败:从库地址改了且沿用主库账号,请重新输入主库口令(已保存的口令不会发往新地址)" {
		t.Errorf("提示应直接说原因,实际 %q", r.Msg)
	}
	eq(t, put("10.6.6.6:3306", "secret2", map[string]any{"replicaHost": "10.6.6.9:3306", "replicaUsername": ""}).Code, 0,
		"从库地址变了、沿用主库账号,重新输入了主库口令")
}
