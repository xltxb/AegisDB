package gateway

import (
	"context"
	"strings"
	"testing"
	"time"

	"velagateway/internal/model"
)

// 2026-11-12 UAT-DWS:PG 系 / Oracle 上的 SHOW CREATE TABLE 在数据查询里被当成"不能执行 SHOW"。
func TestBizShowCreateTable_PGAndOracle(t *testing.T) {
	cases := []struct {
		engine, q, schema, name string
	}{
		{"DWS", "show create table g_ads_order_mx_summary_lavie_report_di", "", "g_ads_order_mx_summary_lavie_report_di"},
		{"PostgreSQL 15", "SHOW CREATE TABLE Orders;", "", "orders"},
		{"GaussDB", `SHOW CREATE TABLE "Rpt"."Orders"`, "Rpt", "Orders"},
		{"DWS", "SHOW CREATE TABLE ods.orders -- 看看", "ods", "orders"},
		{"Oracle 19c", "show create table orders", "", "ORDERS"},
		{"Oracle 19c", `SHOW CREATE TABLE app."Mixed"`, "APP", "Mixed"},
	}
	for _, c := range cases {
		s, n, ok := BizShowCreateTable(c.engine, c.q)
		if !ok || s != c.schema || n != c.name {
			t.Errorf("%s %q:应认出 (%q,%q),实际 (%q,%q,%v)", c.engine, c.q, c.schema, c.name, s, n, ok)
		}
	}
	// MySQL 系照常下发库自己的 SHOW CREATE TABLE,不走网关渲染
	if _, _, ok := BizShowCreateTable("MySQL 8.0", "SHOW CREATE TABLE orders"); ok {
		t.Error("MySQL 系不该被网关接管")
	}
}

func TestBizReadOnlyCheck_ShowCreateTablePGOracle(t *testing.T) {
	for _, eng := range []string{"DWS", "PostgreSQL 15", "GaussDB"} {
		for _, q := range []string{"SHOW CREATE TABLE orders", "show create table ods.orders;"} {
			if r := BizReadOnlyCheck(eng, q, "c66_gli", nil); r != nil {
				t.Errorf("%s %q:应放行,实际 %s", eng, q, model.RenderBizReason(r))
			}
		}
		for _, c := range []struct{ q, code string }{
			{"SHOW CREATE TABLE pg_catalog.pg_authid", model.BizSystemDB},
			{"SHOW CREATE TABLE pg_authid", model.BizSystemDB},
			{"SHOW CREATE VIEW v", model.BizShowForm},
			{"SHOW search_path", model.BizShowForm},
			{"SHOW ALL", model.BizShowForm},
			{"SHOW CREATE TABLE a; SELECT 1", model.BizMulti},
		} {
			r := BizReadOnlyCheck(eng, c.q, "c66_gli", nil)
			if r == nil || r.Code != c.code {
				t.Errorf("%s %q:应以 %s 拒绝,实际 %v", eng, c.q, c.code, r)
			}
		}
	}
	dbs := []string{"APP", "HR"}
	if r := BizReadOnlyCheck("Oracle 19c", "SHOW CREATE TABLE orders", "APP", dbs); r != nil {
		t.Errorf("Oracle 本 schema 应放行,实际 %s", model.RenderBizReason(r))
	}
	for _, c := range []struct{ q, code string }{
		{"SHOW CREATE TABLE hr.employees", model.BizOtherDB},
		{"SHOW CREATE TABLE sys.user$", model.BizSystemDB},
		{"SHOW PARAMETERS", model.BizShowForm},
	} {
		r := BizReadOnlyCheck("Oracle 19c", c.q, "APP", dbs)
		if r == nil || r.Code != c.code {
			t.Errorf("Oracle %q:应以 %s 拒绝,实际 %v", c.q, c.code, r)
		}
	}
}

// 实库:PG 上 SHOW CREATE TABLE 由网关渲染,不带 schema 按 search_path 找到同一张表。
func TestPostgresBiz_ShowCreateTable(t *testing.T) {
	conn := testPostgres(t)
	raw, err := pgRaw(conn)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	for _, s := range []string{
		`DROP SCHEMA IF EXISTS vela_ddl CASCADE`, `DROP TABLE IF EXISTS public.vela_ddl_t`,
		`CREATE SCHEMA vela_ddl`,
		`CREATE TABLE vela_ddl.orders (id bigint PRIMARY KEY, amount numeric(10,2) NOT NULL DEFAULT 0)`,
		`COMMENT ON TABLE vela_ddl.orders IS '订单'`,
		`CREATE TABLE public.vela_ddl_t (k text)`,
	} {
		if _, err := raw.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	defer raw.Exec(`DROP SCHEMA IF EXISTS vela_ddl CASCADE; DROP TABLE IF EXISTS public.vela_ddl_t`)

	for q, want := range map[string]string{
		"SHOW CREATE TABLE vela_ddl.orders":  "numeric(10,2)",
		"show create table VELA_DDL.Orders;": "订单",
		"SHOW CREATE TABLE vela_ddl_t":       "vela_ddl_t",
	} {
		if r := BizReadOnlyCheck(conn.Engine, q, conn.Database, nil); r != nil {
			t.Fatalf("%q 判定应放行:%s", q, model.RenderBizReason(r))
		}
		res, err := RealBizQuery(context.Background(), conn, q, 100, 5*time.Second)
		if err != nil || len(res.Data) != 1 || len(res.Columns) != 2 {
			t.Fatalf("%q 应返回一行两列,实际 %v %v err=%v", q, res.Columns, res.Data, err)
		}
		if ddl := res.Data[0][1]; !strings.Contains(ddl, "CREATE TABLE") || !strings.Contains(ddl, want) {
			t.Errorf("%q 建表语句应含 %q,实际:\n%s", q, want, ddl)
		}
	}
	if _, err := RealBizQuery(context.Background(), conn, "SHOW CREATE TABLE no_such_tbl", 100, 5*time.Second); err == nil {
		t.Error("表不存在应报错")
	}
}

// 实库:Oracle 上 SHOW CREATE TABLE 由网关用 DBMS_METADATA 渲染(业务方自己调不了 DBMS_ 包)。
func TestOracleBiz_ShowCreateTable(t *testing.T) {
	conn := testOracle(t)
	db, release, err := openConn(conn)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	_, _ = db.Exec(`DROP TABLE vela_ddl_ora PURGE`)
	if _, err := db.Exec(`CREATE TABLE vela_ddl_ora (id NUMBER(10) PRIMARY KEY, name VARCHAR2(40))`); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DROP TABLE vela_ddl_ora PURGE`)
	for _, q := range []string{"SHOW CREATE TABLE vela_ddl_ora", "show create table system.VELA_DDL_ORA;"} {
		if r := BizReadOnlyCheck(conn.Engine, q, "SYSTEM", []string{"SYSTEM"}); r != nil && r.Code != model.BizSystemDB {
			t.Fatalf("%q 判定:%s", q, model.RenderBizReason(r))
		}
		res, err := RealBizQuery(context.Background(), conn, q, 100, 10*time.Second)
		if err != nil || len(res.Data) != 1 || !strings.Contains(res.Data[0][1], "VARCHAR2(40") {
			t.Errorf("%q 应返回建表语句,实际 %v err=%v", q, res.Data, err)
		}
	}
}
