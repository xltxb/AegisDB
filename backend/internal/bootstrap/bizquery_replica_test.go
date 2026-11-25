package bootstrap

// 数据源配了从库:业务方的数据查询(执行、库表下拉、判定用的库名)都由从库提供;从库连不上
// 时回落主库,结果与审计里注明。真实库用例需要两台 MySQL:
//
//	AEGISDB_TEST_MYSQL=host:port/user/password          (主库)
//	AEGISDB_TEST_MYSQL_REPLICA=host:port/user/password  (从库)
//
// 两台库里放**不同的标记数据**,查出来是哪一份,就知道打到了哪一台。

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"aegisdb/internal/model"
)

func mysqlParts(t *testing.T, env string) []string {
	t.Helper()
	v := os.Getenv(env)
	if v == "" {
		t.Skipf("未设置 %s,跳过真实从库用例", env)
	}
	p := strings.SplitN(v, "/", 3)
	if len(p) != 3 {
		t.Fatalf("%s 格式应为 host:port/user/password", env)
	}
	return p
}

func seedMarker(t *testing.T, p []string, marker string, extra ...string) {
	t.Helper()
	raw, err := sql.Open("mysql", fmt.Sprintf("%s:%s@tcp(%s)/", p[1], p[2], p[0]))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	stmts := append([]string{
		"CREATE DATABASE IF NOT EXISTS biz_rep",
		"CREATE TABLE IF NOT EXISTS biz_rep.marker (v VARCHAR(20))",
		"DELETE FROM biz_rep.marker",
		"INSERT INTO biz_rep.marker VALUES ('" + marker + "')",
	}, extra...)
	for _, s := range stmts {
		if _, err := raw.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
}

func TestBizQuery_Replica(t *testing.T) {
	primary := mysqlParts(t, "AEGISDB_TEST_MYSQL")
	replica := mysqlParts(t, "AEGISDB_TEST_MYSQL_REPLICA")
	seedMarker(t, primary, "primary", "DROP TABLE IF EXISTS biz_rep.only_on_replica")
	seedMarker(t, replica, "replica", "CREATE TABLE IF NOT EXISTS biz_rep.only_on_replica (id INT)")

	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	owner, _ := app.repo.GetRoleByCode("owner")
	sa := app.createServiceAccount(admin, "审批系统", []int64{owner.ID}, nil).Email
	e := &grantEnv{app: app, admin: admin, sa: sa, db: "biz_rep"}
	e.token = app.issueClient(admin, "审批系统", sa, []string{"grant:read", "grant:write"})
	base := map[string]any{
		"name": "biz-primary", "engine": "MySQL 8.0", "host": primary[0], "env": "prod", "policy": "audit-only",
		"username": primary[1], "password": primary[2], "database": "biz_rep",
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
	grantOf(t, e.put(e.body("biz@corp.io", "OA-R1")))
	u, _ := app.repo.FindUserByEmailCI("biz@corp.io")
	b := &bizEnv{grantEnv: e, user: app.bizSession(u), uid: u.ID}

	setReplica := func(host, user, pw string) apiResp {
		body := map[string]any{}
		for k, v := range base {
			body[k] = v
		}
		body["password"] = "" // 主库密码保持不变
		body["replicaHost"], body["replicaUsername"], body["replicaPassword"] = host, user, pw
		return app.do(http.MethodPut, "/api/v1/connections/"+itoa(e.source), admin, body)
	}
	type served struct {
		Rows         [][]string `json:"rows"`
		Served       string     `json:"served"`
		ReplicaError string     `json:"replicaError"`
	}
	query := func() served {
		r, _ := b.exec("SELECT v FROM marker")
		if r.Code != 0 {
			t.Fatalf("查询应成功: %d %s", r.Code, r.Msg)
		}
		var out served
		_ = json.Unmarshal(r.Data, &out)
		return out
	}
	lastOperator := func() string {
		var a model.AuditLog
		app.repo.DB().Where("operator LIKE ?", "查询授权 #%").Order("id DESC").First(&a)
		return a.Operator
	}

	// 没配从库:打主库,不标注
	if out := query(); len(out.Rows) != 1 || out.Rows[0][0] != "primary" || out.Served != "" {
		t.Fatalf("没配从库应查主库且不标注,实际 %+v", out)
	}

	// 与主库同一个地址:不是从库
	if r := setReplica(primary[0], "", ""); r.Code != 40001 {
		t.Errorf("从库地址与主库相同应拒绝,实际 %d %s", r.Code, r.Msg)
	}

	// 配上从库(单独的账号密码):查询、库表下拉都走从库,审计注明
	eq(t, setReplica(replica[0], replica[1], replica[2]).Code, 0, "配从库")
	if out := query(); len(out.Rows) != 1 || out.Rows[0][0] != "replica" || out.Served != "replica" {
		t.Fatalf("配了从库应查从库,实际 %+v", out)
	}
	if op := lastOperator(); !strings.Contains(op, "从库") || strings.Contains(op, "主库") {
		t.Errorf("审计应注明由从库提供,实际 %q", op)
	}
	var tables struct {
		Tables []string `json:"tables"`
	}
	_ = json.Unmarshal(app.do(http.MethodGet, "/api/v1/bizquery/tables?sourceId="+itoa(e.source)+"&database=biz_rep", b.user, nil).Data, &tables)
	if !strings.Contains(strings.Join(tables.Tables, ","), "only_on_replica") {
		t.Errorf("库表下拉应来自从库,实际 %v", tables.Tables)
	}
	// 数据源列表里看得到从库配置,看不到从库密码
	var conns []map[string]any
	_ = json.Unmarshal(app.do(http.MethodGet, "/api/v1/connections", admin, nil).Data, &conns)
	for _, cn := range conns {
		if int64(cn["id"].(float64)) == e.source {
			if cn["replicaHost"] == "" || cn["replicaPassword"] != nil {
				t.Errorf("列表应带从库地址、不带从库密码: %v", cn)
			}
		}
	}

	// 从库连不上:回落主库,结果与审计里注明,而且不能等太久
	eq(t, setReplica("127.0.0.1:1", "", "").Code, 0, "改成连不上的从库")
	start := time.Now()
	out := query()
	if len(out.Rows) != 1 || out.Rows[0][0] != "primary" || out.Served != "primaryFallback" || out.ReplicaError == "" {
		t.Fatalf("从库连不上应回落主库并注明,实际 %+v", out)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("回落耗时 %v,探从库的时限太长", d)
	}
	if op := lastOperator(); !strings.Contains(op, "由主库提供") {
		t.Errorf("审计应注明回落主库,实际 %q", op)
	}
	// 再查一次:探测结果有缓存,直接回落,不再等
	start = time.Now()
	if out := query(); out.Served != "primaryFallback" {
		t.Errorf("第二次也应回落,实际 %+v", out)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("从库已知不可用,第二次不该再等探测,实际 %v", d)
	}

	// 去掉从库:回到主库,不标注
	eq(t, setReplica("", "", "").Code, 0, "去掉从库")
	if out := query(); out.Rows[0][0] != "primary" || out.Served != "" {
		t.Errorf("去掉从库后应查主库且不标注,实际 %+v", out)
	}
}
