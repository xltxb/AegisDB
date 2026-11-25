package bootstrap

// 在线变更的判定(.scratch/osc-product 工单 01):生产 + MySQL + 单个索引变更 + 大表 → 走在线变更;
// 大表上做不了的 DDL → 照原路径但提示;其余照原路径。对着真 MySQL(AEGISDB_OSC_TEST_DSN)。

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"aegisdb/internal/service"
)

// oscTestConns 在测试库上建一大一小两张表,并在网关里登记生产 / 开发两条连接。
func (a *testApp) oscTestConns(token string) (prod, dev int64, schema string) {
	a.t.Helper()
	dsn := os.Getenv("AEGISDB_OSC_TEST_DSN")
	if dsn == "" {
		a.t.Skip("未设置 AEGISDB_OSC_TEST_DSN")
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		a.t.Fatal(err)
	}
	raw, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		a.t.Fatal(err)
	}
	defer raw.Close()
	schema = fmt.Sprintf("oscr_%d", time.Now().UnixNano()%1_000_000_000)
	for _, q := range []string{
		"CREATE DATABASE " + schema,
		"CREATE TABLE " + schema + ".big (id INT PRIMARY KEY, a INT, b VARCHAR(20))",
		"CREATE TABLE " + schema + ".small (id INT PRIMARY KEY, a INT)",
	} {
		if _, err := raw.Exec(q); err != nil {
			a.t.Fatal(err)
		}
	}
	a.t.Cleanup(func() {
		r, _ := sql.Open("mysql", cfg.FormatDSN())
		r.Exec("DROP DATABASE " + schema)
		r.Close()
	})
	var vals []string
	for i := 1; i <= 300; i++ {
		vals = append(vals, fmt.Sprintf("(%d, %d, 'x')", i, i))
	}
	if _, err := raw.Exec("INSERT INTO " + schema + ".big VALUES " + strings.Join(vals, ",")); err != nil {
		a.t.Fatal(err)
	}
	raw.Exec("INSERT INTO " + schema + ".small VALUES (1, 1)")
	raw.Exec("ANALYZE TABLE " + schema + ".big, " + schema + ".small") // TABLE_ROWS 是统计值,刷新一下
	prod = a.addConn(token, "osc-prod", "MySQL 8.0", "prod", cfg.Addr, schema, cfg.User, cfg.Passwd)
	dev = a.addConn(token, "osc-dev", "MySQL 8.0", "dev", cfg.Addr, schema, cfg.User, cfg.Passwd)
	return prod, dev, schema
}

func TestOSCRoute(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	prod, dev, schema := app.oscTestConns(admin)
	// 阈值调小:300 行算大表,5 行不算;字节阈值调到不会触发
	eq(t, app.do(http.MethodPut, "/api/v1/settings", admin, map[string]any{"osc.minRows": 100, "osc.minBytes": 1 << 40}).Code, 0, "设阈值")

	route := func(connID int64, sql string) service.OSCDecision {
		t.Helper()
		conn, err := app.repo.GetConnection(connID)
		if err != nil {
			t.Fatal(err)
		}
		return app.svc.OSCRoute(context.Background(), conn, sql)
	}
	cases := []struct {
		conn int64
		sql  string
		want service.OSCRouteKind
	}{
		{prod, "ALTER TABLE big ADD INDEX idx_a (a)", service.OSCOnline},
		{prod, "CREATE INDEX idx_a ON " + schema + ".big (a)", service.OSCOnline},
		{prod, "ALTER TABLE small ADD INDEX idx_a (a)", service.OSCNormal},
		// 工单 08:原生 INPLACE / COPY 的都走在线变更
		{prod, "ALTER TABLE big ADD UNIQUE INDEX u (a)", service.OSCOnline},
		{prod, "ALTER TABLE big MODIFY b VARCHAR(40)", service.OSCOnline},
		{prod, "ALTER TABLE big CONVERT TO CHARACTER SET utf8mb4", service.OSCOnline},
		{prod, "ALTER TABLE big ENGINE=InnoDB", service.OSCOnline},
		// 原生秒完成的(INSTANT / 只改元数据):照原路径、不提示
		{prod, "ALTER TABLE big DROP INDEX idx_none", service.OSCNormal},
		{prod, "ALTER TABLE big ADD COLUMN c INT", service.OSCNormal},
		{prod, "ALTER TABLE big RENAME COLUMN b TO bb", service.OSCNormal},
		// 在线变更做不了的:照原路径并提示
		{prod, "ALTER TABLE big PARTITION BY HASH(id) PARTITIONS 2", service.OSCBigDirect},
		{prod, "ALTER TABLE big ADD CONSTRAINT fk FOREIGN KEY (a) REFERENCES small (id)", service.OSCBigDirect},
		{prod, "SELECT * FROM big", service.OSCNormal},
		{prod, "ALTER TABLE no_such ADD INDEX i (a)", service.OSCNormal},
		{dev, "ALTER TABLE big ADD INDEX idx_a (a)", service.OSCNormal}, // 非生产
	}
	for _, c := range cases {
		d := route(c.conn, c.sql)
		if d.Kind != c.want {
			t.Errorf("%q: 应为 %d,实际 %d (rows=%d err=%v unsupported=%q)", c.sql, c.want, d.Kind, d.Rows, d.SizeErr, d.Parsed.Unsupported)
		}
	}
	if d := route(prod, "ALTER TABLE no_such ADD INDEX i (a)"); d.SizeErr == nil {
		t.Error("表不存在时应带上读数据量的错误(写审计用)")
	}
	if d := route(prod, "ALTER TABLE big ADD INDEX idx_a (a)"); d.Rows < 100 || d.Schema != schema || d.Table != "big" {
		t.Errorf("判定应带上库表与估算行数: %+v", d)
	}
}

// 生产 = 分层开了生产横幅,与「需要 MFA」无关。不启用 MFA 的部署会把 MFA 关掉,曾因此让
// 生产大表加索引整个绕开在线变更(2026-10 端到端验收)。
func TestOSCRoute_ProductionMeansTheDangerBanner(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	prod, _, _ := app.oscTestConns(admin)
	eq(t, app.do(http.MethodPut, "/api/v1/settings", admin, map[string]any{"osc.minRows": 100, "osc.minBytes": 1 << 40}).Code, 0, "设阈值")
	setFlags := func(banner, mfa bool) {
		t.Helper()
		tier, err := app.repo.GetEnvTier("prod")
		if err != nil {
			t.Fatal(err)
		}
		tier.DangerBanner, tier.RequireMFA = banner, mfa
		eq(t, app.do(http.MethodPut, "/api/v1/env-tiers/prod", admin, tier).Code, 0, "改分层开关")
	}
	route := func() service.OSCRouteKind {
		t.Helper()
		conn, _ := app.repo.GetConnection(prod)
		return app.svc.OSCRoute(context.Background(), conn, "ALTER TABLE big ADD INDEX idx_a (a)").Kind
	}
	setFlags(true, false)
	if k := route(); k != service.OSCOnline {
		t.Errorf("横幅开、MFA 关:仍是生产,应走在线变更,实际 %v", k)
	}
	setFlags(false, true)
	if k := route(); k != service.OSCNormal {
		t.Errorf("横幅关:不是生产,应照原路径,实际 %v", k)
	}
}

// 在线变更只用于 MySQL、PolarDB(MySQL 版)、Aurora MySQL(2026-10 用户定)。TiDB / MariaDB 协议相同,但从前会被送进在线
// 变更、在前置检查被拦下 —— 生产大表上的 DDL 既不走在线变更、也执行不了。现在照原路径、不提示。
func TestOSCRoute_OnlyMySQL(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	_, _, schema := app.oscTestConns(admin)
	eq(t, app.do(http.MethodPut, "/api/v1/settings", admin, map[string]any{"osc.minRows": 100, "osc.minBytes": 1 << 40}).Code, 0, "设阈值")
	cfg, _ := mysql.ParseDSN(os.Getenv("AEGISDB_OSC_TEST_DSN"))
	for _, c := range []struct {
		engine string
		want   service.OSCRouteKind
	}{
		{"MySQL 8.0", service.OSCOnline},
		{"MySQL", service.OSCOnline},
		{"TiDB", service.OSCNormal},
		{"MariaDB 10.11", service.OSCNormal},
		{"PolarDB", service.OSCOnline},           // PolarDB 的 MySQL 版可以用在线变更
		{"PolarDB MySQL 8.0", service.OSCOnline},
		{"PolarDB for PostgreSQL", service.OSCNormal}, // PG 版不是
		{"Aurora MySQL", service.OSCOnline},
		{"aurora", service.OSCOnline},
		{"Aurora PostgreSQL", service.OSCNormal},
	} {
		id := app.addConn(admin, "osc-eng-"+strings.ReplaceAll(c.engine, " ", "-"), c.engine, "prod", cfg.Addr, schema, cfg.User, cfg.Passwd)
		conn, _ := app.repo.GetConnection(id)
		for _, sql := range []string{"ALTER TABLE big ADD INDEX idx_a (a)", "ALTER TABLE big PARTITION BY HASH(id) PARTITIONS 2"} {
			want := c.want
			if want == service.OSCOnline && strings.Contains(sql, "PARTITION") {
				want = service.OSCBigDirect
			}
			if d := app.svc.OSCRoute(context.Background(), conn, sql); d.Kind != want {
				t.Errorf("%s %q: 应为 %d,实际 %d", c.engine, sql, want, d.Kind)
			}
		}
	}
}

// Aurora 的从库延迟在写节点上读,不看连接上配置的从库:前置检查里给的是"读不到"(普通 MySQL 没有
// replica_host_status),而不是"未配置从库"。
func TestOSCReplica_AuroraReadsLagOnTheWriter(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	_, _, schema := app.oscTestConns(admin)
	eq(t, app.do(http.MethodPut, "/api/v1/settings", admin, map[string]any{"osc.minRows": 100, "osc.minBytes": 1 << 40}).Code, 0, "设阈值")
	cfg, _ := mysql.ParseDSN(os.Getenv("AEGISDB_OSC_TEST_DSN"))
	id := app.addConn(admin, "osc-aurora", "Aurora MySQL", "prod", cfg.Addr, schema, cfg.User, cfg.Passwd)
	codes := app.oscWarningCodes(id)
	if !hasCode(codes, "replica_unreadable") || hasCode(codes, "replica_none") {
		t.Errorf("Aurora 应在写节点读副本延迟(这里读不到): %v", codes)
	}
}
