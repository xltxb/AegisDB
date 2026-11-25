package bootstrap

import (
	"database/sql"
	"fmt"
	"encoding/json"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/glebarez/go-sqlite"
)

type dbObjectsResp struct {
	Functions  []string `json:"functions"`
	Procedures []string `json:"procedures"`
	Packages   []string `json:"packages"`
	Triggers   []string `json:"triggers"`
	Error      string   `json:"error"`
}

// The terminal tree's programmable-object browser must introspect a REAL target:
// a SQLite connection with credentials lists its triggers, and the source
// endpoint returns the stored CREATE TRIGGER text (same catalog-view approach as
// SQLcl's ddl, without shelling out to anything).
func TestObjects_RealSqliteTriggerListedAndSourced(t *testing.T) {
	dbfile := filepath.Join(t.TempDir(), "obj.db")
	raw, err := sql.Open("sqlite", dbfile)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if _, err := raw.Exec(`CREATE TABLE orders (id INTEGER, status TEXT)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := raw.Exec(`CREATE TRIGGER trg_orders_touch AFTER UPDATE ON orders BEGIN SELECT 1; END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	if _, err := raw.Exec(`CREATE INDEX idx_orders_status ON orders (status)`); err != nil {
		t.Fatalf("create index: %v", err)
	}
	raw.Close()

	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	cr := app.do(http.MethodPost, "/api/v1/connections", token, map[string]any{
		"name": "obj-sqlite", "engine": "SQLite", "host": "localhost:0",
		"env": "dev", "policy": "audit-only", "database": dbfile,
	})
	eq(t, cr.Code, 0, "create real connection")
	var conn struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(cr.Data, &conn)

	r := app.do(http.MethodGet, fmt.Sprintf("/api/v1/connections/%d/objects", conn.ID), token, nil)
	eq(t, r.Code, 0, "list objects")
	var objs dbObjectsResp
	_ = json.Unmarshal(r.Data, &objs)
	if len(objs.Triggers) != 1 || objs.Triggers[0] != "trg_orders_touch" {
		t.Fatalf("expected the real trigger listed, got %+v", objs)
	}

	sr := app.do(http.MethodGet,
		fmt.Sprintf("/api/v1/connections/%d/object-source", conn.ID)+"?type=trigger&name=trg_orders_touch", token, nil)
	eq(t, sr.Code, 0, "object source")
	var src struct {
		Source string `json:"source"`
	}
	_ = json.Unmarshal(sr.Data, &src)
	if !strings.Contains(src.Source, "CREATE TRIGGER trg_orders_touch") {
		t.Errorf("source should carry the stored definition, got %q", src.Source)
	}

	// Clicking a table in the tree shows its DDL: the stored CREATE TABLE plus
	// the table's named indexes.
	tr := app.do(http.MethodGet,
		fmt.Sprintf("/api/v1/connections/%d/object-source", conn.ID)+"?type=table&name=orders", token, nil)
	eq(t, tr.Code, 0, "table ddl")
	var tddl struct {
		Source string `json:"source"`
	}
	_ = json.Unmarshal(tr.Data, &tddl)
	if !strings.Contains(tddl.Source, "CREATE TABLE orders") || !strings.Contains(tddl.Source, "idx_orders_status") {
		t.Errorf("table DDL should carry the CREATE TABLE and its indexes, got %q", tddl.Source)
	}

	// An unsafe identifier must be refused before any SQL is built from it.
	bad := app.do(http.MethodGet,
		fmt.Sprintf("/api/v1/connections/%d/object-source", conn.ID)+"?type=trigger&name="+url.QueryEscape("x`; DROP TABLE orders --"), token, nil)
	if bad.Code == 0 {
		t.Error("unsafe object name must be rejected")
	}
}

// A credential-less (simulated) connection still shows a demo object set, and
// its source view says so — consistent with the terminal's synthetic results.
func TestObjects_SimulatedConnectionServesDemoSet(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
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

// 这条测试原先拿两个 SQLite 文件模拟 PG 的"一个连接下两个库",证明 `database` 参数
// 一路传到了元数据读取(PG 的目录按库隔离)。但 SQLite 的 database 就是文件路径,
// 允许请求改它 = 让网关打开服务器上任意文件(2026-10 安全审计 H2)。现在反过来证明:
// SQLite 连接只认管理员配置的文件,传别的路径不起作用。PG 的按库切换走同一个入口
// applyTargetDatabase,由 service 层 TestApplyTargetDatabase 覆盖。
func TestObjects_SQLiteIgnoresDatabaseParam(t *testing.T) {
	mk := func(file, table string) string {
		raw, err := sql.Open("sqlite", file)
		if err != nil {
			t.Fatalf("open sqlite: %v", err)
		}
		if _, err := raw.Exec(`CREATE TABLE ` + table + ` (id INTEGER)`); err != nil {
			t.Fatalf("create table: %v", err)
		}
		raw.Close()
		return file
	}
	dir := t.TempDir()
	fileA := mk(filepath.Join(dir, "a.db"), "tbl_in_a")
	fileB := mk(filepath.Join(dir, "b.db"), "tbl_in_b")

	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	cr := app.do(http.MethodPost, "/api/v1/connections", token, map[string]any{
		"name": "dbparam-sqlite", "engine": "SQLite", "host": "localhost:0",
		"env": "dev", "policy": "audit-only", "database": fileA,
	})
	eq(t, cr.Code, 0, "create connection defaulting to database A")
	var conn struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(cr.Data, &conn)

	// Without database: the default (A) answers.
	ra := app.do(http.MethodGet,
		fmt.Sprintf("/api/v1/connections/%d/object-source", conn.ID)+"?type=table&name=tbl_in_a", token, nil)
	eq(t, ra.Code, 0, "table from default database")

	// database=B 的路径:不得打开 B —— B 里的表查不到
	rb := app.do(http.MethodGet,
		fmt.Sprintf("/api/v1/connections/%d/object-source", conn.ID)+
			"?type=table&name=tbl_in_b&database="+url.QueryEscape(fileB), token, nil)
	if rb.Code == 0 {
		t.Errorf("传别的文件路径不应打开那个文件,却读到了 tbl_in_b")
	}
	// 同样带着 database=B,读到的仍是配置的 A
	ra2 := app.do(http.MethodGet,
		fmt.Sprintf("/api/v1/connections/%d/object-source", conn.ID)+
			"?type=table&name=tbl_in_a&database="+url.QueryEscape(fileB), token, nil)
	eq(t, ra2.Code, 0, "仍读配置的文件 A")
	var src struct {
		Source string `json:"source"`
	}
	_ = json.Unmarshal(ra2.Data, &src)
	if !strings.Contains(src.Source, "tbl_in_a") {
		t.Errorf("expected A's table DDL, got %q", src.Source)
	}
}
