package bootstrap

// 2026-10-29:Oracle 自治事务函数在数据查询的只读事务里照样能写库。执行层下发前查依赖,
// 走完整条 HTTP 链:40300 + 原因、记拦截审计、表里一行不多;DESC 与日常查询照旧。

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"aegisdb/internal/gateway"
	"aegisdb/internal/model"
)

func TestBizQuery_OracleAutonomousRefused_RealOracle(t *testing.T) {
	v := os.Getenv("AEGISDB_TEST_ORACLE")
	if v == "" {
		t.Skip("未设置 AEGISDB_TEST_ORACLE,跳过真实 Oracle 用例")
	}
	p := strings.SplitN(v, "/", 4)
	hp := strings.SplitN(p[0], ":", 2)
	port, _ := strconv.Atoi(hp[1])
	direct := &model.Connection{Name: "ora-setup", Engine: "Oracle 23ai", Host: hp[0], Port: port,
		Username: p[1], Password: p[2], Database: p[3]}
	ctx := context.Background()
	run := func(s string) {
		if _, err := gateway.RealRun(ctx, direct, s, 30*time.Second); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	// 业务 schema(授权只能落在业务 schema 上,系统 schema 不在可授权的库列表里)
	cleanup := func() { _, _ = gateway.RealRun(ctx, direct, "DROP USER vzbiz CASCADE", 60*time.Second) }
	cleanup()
	t.Cleanup(cleanup)
	run(`CREATE USER vzbiz IDENTIFIED BY "Vz` + strconv.FormatInt(time.Now().UnixNano(), 36) + `" QUOTA UNLIMITED ON users`)
	run("CREATE TABLE vzbiz.vz_e2e_log (n NUMBER)")
	run(`CREATE OR REPLACE FUNCTION vzbiz.vz_e2e_auto RETURN NUMBER IS PRAGMA AUTONOMOUS_TRANSACTION; BEGIN INSERT INTO vzbiz.vz_e2e_log VALUES (1); COMMIT; RETURN 1; END;`)
	time.Sleep(3 * time.Second) // 刚建的表在只读事务里一两秒内读不了(ORA-01466)

	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	owner, _ := app.repo.GetRoleByCode("owner")
	sa := app.createServiceAccount(admin, "审批系统", []int64{owner.ID}, nil).Email
	e := &grantEnv{app: app, admin: admin, sa: sa, db: "VZBIZ"}
	e.token = app.issueClient(admin, "审批系统", sa, []string{"grant:read", "grant:write"})
	r := app.do(http.MethodPost, "/api/v1/connections", admin, map[string]any{
		"name": "biz-ora", "engine": "Oracle 23ai", "host": p[0], "env": "prod", "policy": "audit-only",
		"username": p[1], "password": p[2], "database": p[3],
	})
	if r.Code != 0 {
		t.Fatalf("create oracle conn: %s", r.Msg)
	}
	var c struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(r.Data, &c)
	e.source = c.ID
	eq(t, app.do(http.MethodPatch, "/api/v1/connections/"+itoa(e.source), admin, map[string]any{"bizQuery": true}).Code, 0, "标记业务查询源")
	grantOf(t, e.put(e.body("biz@corp.io", "OA-ORA1")))
	u, _ := app.repo.FindUserByEmailCI("biz@corp.io")
	b := &bizEnv{grantEnv: e, user: app.bizSession(u), uid: u.ID}

	var before int64
	app.repo.DB().Model(&model.AuditLog{}).Where("result = ? AND operator LIKE ?", model.ResultRejected, "查询授权 #%").Count(&before)
	r, _ = b.exec("SELECT vz_e2e_auto() FROM dual")
	if r.Code != 40300 || !strings.Contains(r.Msg, "自治事务") {
		t.Fatalf("自治事务函数应以 40300 拒绝并说明,实际 code=%d msg=%q", r.Code, r.Msg)
	}
	var after int64
	app.repo.DB().Model(&model.AuditLog{}).Where("result = ? AND operator LIKE ?", model.ResultRejected, "查询授权 #%").Count(&after)
	eq(t, after, before+1, "执行层拒绝也记一条拦截审计")
	res, err := gateway.RealRun(ctx, direct, "SELECT COUNT(*) FROM vzbiz.vz_e2e_log", 10*time.Second)
	if err != nil || len(res.Data) != 1 || res.Data[0][0] != "0" {
		t.Fatalf("被拒的查询不该写进任何一行,实际 %v %v", res.Data, err)
	}

	if r, out := b.exec("DESC vz_e2e_log"); r.Code != 0 || len(out.Rows) != 1 || out.Rows[0][0] != "N" {
		t.Errorf("DESC 应列出列 N,实际 code=%d msg=%q %+v", r.Code, r.Msg, out)
	}
	if r, out := b.exec("SELECT COUNT(*) FROM vz_e2e_log"); r.Code != 0 || out.Error != "" || len(out.Rows) != 1 {
		t.Errorf("日常查询应照常执行,实际 code=%d msg=%q %+v", r.Code, r.Msg, out)
	}
}
