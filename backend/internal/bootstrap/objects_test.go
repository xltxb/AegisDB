package bootstrap

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type dbObjectsResp struct {
	Functions  []string `json:"functions"`
	Procedures []string `json:"procedures"`
	Packages   []string `json:"packages"`
	Triggers   []string `json:"triggers"`
	Error      string   `json:"error"`
}

// The terminal tree's programmable-object browser must introspect a REAL target:
// a PostgreSQL connection with credentials lists its functions, and the source
// endpoint returns the stored function definition (same catalog-view approach as
// SQLcl's ddl, without shelling out to anything).
func TestObjects_RealPGFunctionListedAndSourced(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@aegisdb.io", "aegisdb123")
	connID, _ := app.newPGConn(token, "obj-pg", "dev",
		`CREATE TABLE orders (id INTEGER, status TEXT)`,
		`CREATE FUNCTION fn_orders_total() RETURNS bigint LANGUAGE sql AS 'SELECT count(*) FROM orders'`,
		`CREATE INDEX idx_orders_status ON orders (status)`,
	)
	conn := struct{ ID int64 }{connID}

	r := app.do(http.MethodGet, fmt.Sprintf("/api/v1/connections/%d/objects?scope=public", conn.ID), token, nil)
	eq(t, r.Code, 0, "list objects")
	var objs dbObjectsResp
	_ = json.Unmarshal(r.Data, &objs)
	// PostgreSQL 家族的对象浏览只有函数 / 存储过程(没有触发器这一类)。
	if len(objs.Functions) != 1 || objs.Functions[0] != "fn_orders_total" {
		t.Fatalf("expected the real function listed, got %+v", objs)
	}

	sr := app.do(http.MethodGet,
		fmt.Sprintf("/api/v1/connections/%d/object-source", conn.ID)+"?scope=public&type=function&name=fn_orders_total", token, nil)
	eq(t, sr.Code, 0, "object source")
	var src struct {
		Source string `json:"source"`
	}
	_ = json.Unmarshal(sr.Data, &src)
	if !strings.Contains(src.Source, "FUNCTION public.fn_orders_total") {
		t.Errorf("source should carry the stored definition, got %q", src.Source)
	}

	// Clicking a table in the tree shows its DDL: the stored CREATE TABLE plus
	// the table's named indexes.
	tr := app.do(http.MethodGet,
		fmt.Sprintf("/api/v1/connections/%d/object-source", conn.ID)+"?scope=public&type=table&name=orders", token, nil)
	eq(t, tr.Code, 0, "table ddl")
	var tddl struct {
		Source string `json:"source"`
	}
	_ = json.Unmarshal(tr.Data, &tddl)
	if !strings.Contains(tddl.Source, `CREATE TABLE "public"."orders"`) || !strings.Contains(tddl.Source, "idx_orders_status") {
		t.Errorf("table DDL should carry the CREATE TABLE and its indexes, got %q", tddl.Source)
	}

	// An unsafe identifier must be refused before any SQL is built from it.
	bad := app.do(http.MethodGet,
		fmt.Sprintf("/api/v1/connections/%d/object-source", conn.ID)+"?type=function&name="+url.QueryEscape("x`; DROP TABLE orders --"), token, nil)
	if bad.Code == 0 {
		t.Error("unsafe object name must be rejected")
	}
}

// A credential-less (simulated) connection still shows a demo object set, and
// its source view says so — consistent with the terminal's synthetic results.
func TestObjects_SimulatedConnectionServesDemoSet(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@aegisdb.io", "aegisdb123")
	dev := app.connIDByEnv(token, "dev")

	r := app.do(http.MethodGet, fmt.Sprintf("/api/v1/connections/%d/objects", dev), token, nil)
	eq(t, r.Code, 0, "list objects (simulated)")
	var objs dbObjectsResp
	_ = json.Unmarshal(r.Data, &objs)
	if len(objs.Functions) == 0 && len(objs.Procedures) == 0 && len(objs.Triggers) == 0 {
		t.Fatalf("simulated connection should serve a demo set, got %+v", objs)
	}

	name := ""
	typ := ""
	if len(objs.Functions) > 0 {
		name, typ = objs.Functions[0], "function"
	} else if len(objs.Procedures) > 0 {
		name, typ = objs.Procedures[0], "procedure"
	} else {
		name, typ = objs.Triggers[0], "trigger"
	}
	sr := app.do(http.MethodGet,
		fmt.Sprintf("/api/v1/connections/%d/object-source", dev)+"?type="+typ+"&name="+url.QueryEscape(name), token, nil)
	eq(t, sr.Code, 0, "object source (simulated)")
	var src struct {
		Source string `json:"source"`
	}
	_ = json.Unmarshal(sr.Data, &src)
	if !strings.Contains(src.Source, "模拟连接") {
		t.Errorf("simulated source must declare itself, got %q", src.Source)
	}
}

// `database` 参数要一路传到对象源读取:PG 的目录按库隔离,连接默认落在库 A,
// 带 database=B 时读到的必须是 B 里的表(applyTargetDatabase 的行为由 service 层
// TestApplyTargetDatabase 覆盖,这里证明它真的传到了读取那一步)。
func TestObjects_PGDatabaseParamSwitchesDatabase(t *testing.T) {
	dbA := newTargetDB(t, `CREATE TABLE tbl_in_a (id INTEGER)`)
	dbB := newTargetDB(t, `CREATE TABLE tbl_in_b (id INTEGER)`)

	app := newTestApp(t)
	token := app.login("linwei@aegisdb.io", "aegisdb123")
	conn := struct{ ID int64 }{app.pgConnAt(token, "dev", "dbparam-pg", dbA)}

	src := func(table, database string) (apiResp, string) {
		u := fmt.Sprintf("/api/v1/connections/%d/object-source", conn.ID) + "?scope=public&type=table&name=" + table
		if database != "" {
			u += "&database=" + url.QueryEscape(database)
		}
		r := app.do(http.MethodGet, u, token, nil)
		var out struct {
			Source string `json:"source"`
		}
		_ = json.Unmarshal(r.Data, &out)
		return r, out.Source
	}

	// Without database: the default (A) answers.
	r, ddl := src("tbl_in_a", "")
	eq(t, r.Code, 0, "table from default database")
	if !strings.Contains(ddl, "tbl_in_a") {
		t.Errorf("expected A's table DDL, got %q", ddl)
	}
	// B 里的表在默认库里查不到
	if r, ddl := src("tbl_in_b", ""); r.Code == 0 && strings.Contains(ddl, "CREATE TABLE") {
		t.Errorf("tbl_in_b lives in B, but the default database served it: %q", ddl)
	}
	// database=B:切到 B,读到 B 的表
	r, ddl = src("tbl_in_b", dbB)
	eq(t, r.Code, 0, "table from database B")
	if !strings.Contains(ddl, "tbl_in_b") {
		t.Errorf("expected B's table DDL, got %q", ddl)
	}
}
