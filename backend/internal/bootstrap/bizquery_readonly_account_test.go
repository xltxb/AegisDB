package bootstrap

// 2026-10-29:TiDB 没有从库,想让数据查询只用一个只读账号 —— 从库地址填主库地址、另填只读账号。
// 原先一律报"从库地址与主库相同,那就不是从库"。现在:同地址必须换账号且带口令;查询只用这个账号,
// 连不上 / 口令错照实报错,不回落主库账号(回落等于只读账号白配)。

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"aegisdb/internal/model"
)

func TestBizQuery_SameAddressReadOnlyAccount_RealMySQL(t *testing.T) {
	p := mysqlParts(t, "AEGISDB_TEST_MYSQL")
	raw, err := sql.Open("mysql", fmt.Sprintf("%s:%s@tcp(%s)/", p[1], p[2], p[0]))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	const roPw = "VzRo#2026pw"
	for _, s := range []string{
		"CREATE DATABASE IF NOT EXISTS biz_ro",
		"CREATE TABLE IF NOT EXISTS biz_ro.marker (v VARCHAR(20))",
		"CREATE TABLE IF NOT EXISTS biz_ro.secret (v VARCHAR(20))",
		"DELETE FROM biz_ro.marker", "INSERT INTO biz_ro.marker VALUES ('hello')",
		"DROP USER IF EXISTS 'vz_ro'@'%'",
		"CREATE USER 'vz_ro'@'%' IDENTIFIED BY '" + roPw + "'",
		"GRANT SELECT ON biz_ro.marker TO 'vz_ro'@'%'",
	} {
		if _, err := raw.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	defer raw.Exec("DROP USER IF EXISTS 'vz_ro'@'%'")

	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	owner, _ := app.repo.GetRoleByCode("owner")
	sa := app.createServiceAccount(admin, "审批系统", []int64{owner.ID}, nil).Email
	e := &grantEnv{app: app, admin: admin, sa: sa, db: "biz_ro"}
	e.token = app.issueClient(admin, "审批系统", sa, []string{"grant:read", "grant:write"})
	base := map[string]any{
		"name": "biz-tidb-like", "engine": "MySQL 8.0", "host": p[0], "env": "prod", "policy": "audit-only",
		"username": p[1], "password": p[2], "database": "biz_ro",
	}
	r := app.do(http.MethodPost, "/api/v1/connections", admin, base)
	if r.Code != 0 {
		t.Fatalf("create conn: %s", r.Msg)
	}
	var c struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(r.Data, &c)
	e.source = c.ID
	app.do(http.MethodPatch, "/api/v1/connections/"+itoa(e.source), admin, map[string]any{"bizQuery": true})
	grantOf(t, e.put(e.body("biz@corp.io", "OA-RO1")))
	u, _ := app.repo.FindUserByEmailCI("biz@corp.io")
	b := &bizEnv{grantEnv: e, user: app.bizSession(u), uid: u.ID}

	setReplica := func(host, user, pw string) apiResp {
		body := map[string]any{}
		for k, v := range base {
			body[k] = v
		}
		body["password"] = ""
		body["replicaHost"], body["replicaUsername"], body["replicaPassword"] = host, user, pw
		return app.do(http.MethodPut, "/api/v1/connections/"+itoa(e.source), admin, body)
	}
	type served struct {
		Rows   [][]string `json:"rows"`
		Served string     `json:"served"`
		Error  string     `json:"error"`
	}
	query := func(q string) (apiResp, served) {
		r := b.app.do(http.MethodPost, "/api/v1/bizquery/exec", b.user, map[string]any{"sourceId": b.source, "database": b.db, "sql": q})
		var out served
		_ = json.Unmarshal(r.Data, &out)
		return r, out
	}

	// 同地址:不换账号、换成同一个账号、换了账号却不给口令 —— 都不行
	for _, c := range []struct{ user, pw, want string }{
		{"", "", "只读账号"},
		{p[1], p[2], "只读账号"},
		{"vz_ro", "", "口令"},
	} {
		if r := setReplica(p[0], c.user, c.pw); r.Code != 40001 || !strings.Contains(r.Msg, c.want) {
			t.Errorf("同地址 user=%q:应以 40001 拒绝并提到 %q,实际 %d %s", c.user, c.want, r.Code, r.Msg)
		}
	}

	// 同地址 + 只读账号:放行;查询只用这个账号,结果与审计注明
	eq(t, setReplica(p[0], "vz_ro", roPw).Code, 0, "同地址配只读账号")
	if r, out := query("SELECT v FROM marker"); r.Code != 0 || len(out.Rows) != 1 || out.Rows[0][0] != "hello" || out.Served != "readonlyAccount" {
		t.Fatalf("应经只读账号查到数据并标注,实际 code=%d msg=%q %+v", r.Code, r.Msg, out)
	}
	var a model.AuditLog
	app.repo.DB().Where("operator LIKE ?", "查询授权 #%").Order("id DESC").First(&a)
	if !strings.Contains(a.Operator, "只读账号") {
		t.Errorf("审计应注明只读账号,实际 %q", a.Operator)
	}
	// 只读账号没授权的表查不到:证明真的是用它在查,不是 root
	if _, out := query("SELECT v FROM secret"); out.Error == "" || out.Served != "readonlyAccount" {
		t.Errorf("只读账号无权的表应报错,实际 %+v", out)
	}
	// 再编辑一次、口令留空:沿用已存的
	eq(t, setReplica(p[0], "vz_ro", "").Code, 0, "口令留空沿用")

	// 口令错:照实报错,不回落主库账号
	eq(t, setReplica(p[0], "vz_ro", "wrong-pw").Code, 0, "改成错的口令")
	// (取库列表这一步就连不上:按"确认不了库列表"拒绝,同样不碰主库账号)
	r, out := query("SELECT v FROM marker")
	if out.Served == "primaryFallback" || len(out.Rows) != 0 {
		t.Fatalf("只读账号连不上不能回落主库账号,实际 %+v", out)
	}
	if r.Code == 0 && out.Error == "" || !strings.Contains(r.Msg+out.Error, "Access denied") {
		t.Errorf("口令错应照实报错(Access denied),实际 code=%d msg=%q %+v", r.Code, r.Msg, out)
	}
}
