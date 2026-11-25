package gateway

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"aegisdb/internal/model"
	"aegisdb/internal/schemadiff"
)

// 表结构快照:每个引擎建同样形状的一张表(主键、唯一索引、普通索引、注释、默认值、可空),
// 读回来逐项核对。真实库用例在对应环境变量设置时运行。

func tableOf(t *testing.T, s *schemadiff.Snapshot, name string) schemadiff.Table {
	t.Helper()
	for _, tb := range s.Tables {
		if strings.EqualFold(tb.Name, name) {
			return tb
		}
	}
	t.Fatalf("快照里没有 %s: %v", name, s.Tables)
	return schemadiff.Table{}
}

func colOf(t *testing.T, tb schemadiff.Table, name string) schemadiff.Column {
	t.Helper()
	for _, c := range tb.Columns {
		if strings.EqualFold(c.Name, name) {
			return c
		}
	}
	t.Fatalf("%s 里没有列 %s", tb.Name, name)
	return schemadiff.Column{}
}

func indexOf(tb schemadiff.Table, cols ...string) *schemadiff.Index {
	for i, ix := range tb.Indexes {
		if strings.EqualFold(strings.Join(ix.Columns, ","), strings.Join(cols, ",")) {
			return &tb.Indexes[i]
		}
	}
	return nil
}

func TestSchemaSnapshot_SQLite(t *testing.T) {
	file := filepath.Join(t.TempDir(), "snap.db")
	raw, _ := sql.Open("sqlite", file)
	for _, s := range []string{
		`CREATE TABLE t_order (id INTEGER PRIMARY KEY, code TEXT NOT NULL UNIQUE, status INTEGER DEFAULT 0, amount REAL)`,
		`CREATE INDEX idx_status ON t_order (status, amount)`,
		`CREATE VIEW v_order AS SELECT id FROM t_order`,
	} {
		if _, err := raw.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	raw.Close()
	s, err := RealSchemaSnapshot(&model.Connection{Engine: "SQLite", Database: file}, "main", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Tables) != 2 || tableOf(t, s, "v_order").Kind != "view" {
		t.Fatalf("应读出一张表一个视图: %+v", s.Tables)
	}
	tb := tableOf(t, s, "t_order")
	if !reflect.DeepEqual(tb.PrimaryKey, []string{"id"}) {
		t.Errorf("主键: %v", tb.PrimaryKey)
	}
	if c := colOf(t, tb, "status"); !c.HasDefault || c.Default != "0" || !c.Nullable || c.Ordinal != 3 {
		t.Errorf("status 列: %+v", c)
	}
	if c := colOf(t, tb, "code"); c.Nullable {
		t.Errorf("code 应 NOT NULL: %+v", c)
	}
	if ix := indexOf(tb, "status", "amount"); ix == nil || ix.Unique || ix.Name != "idx_status" {
		t.Errorf("组合索引: %+v", tb.Indexes)
	}
	if ix := indexOf(tb, "code"); ix == nil || !ix.Unique || !ix.SystemNamed {
		t.Errorf("列上的 UNIQUE 是 SQLite 自动建的索引,应标系统命名: %+v", tb.Indexes)
	}
}

func TestSchemaSnapshot_MySQL(t *testing.T) {
	v := os.Getenv("AEGISDB_TEST_MYSQL")
	if v == "" {
		t.Skip("未设置 AEGISDB_TEST_MYSQL")
	}
	p := strings.SplitN(v, "/", 3)
	hp := strings.SplitN(p[0], ":", 2)
	raw, err := sql.Open("mysql", fmt.Sprintf("%s:%s@tcp(%s)/", p[1], p[2], p[0]))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{
		"DROP DATABASE IF EXISTS snap_test", "CREATE DATABASE snap_test",
		"CREATE TABLE snap_test.t_order (id BIGINT NOT NULL AUTO_INCREMENT, code VARCHAR(32) NOT NULL, status TINYINT(1) NOT NULL DEFAULT 0 COMMENT '状态'," +
			" amount DECIMAL(10,2) NULL, created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP," +
			" PRIMARY KEY (id), UNIQUE KEY uk_code (code), KEY idx_status (status, created_at), KEY idx_lower_code ((lower(code))), UNIQUE KEY uk_id (id)) COMMENT='订单'",
		"CREATE VIEW snap_test.v_order AS SELECT id FROM snap_test.t_order",
	} {
		if _, err := raw.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	raw.Close()
	port, _ := strconv.Atoi(hp[1])
	conn := &model.Connection{Engine: "MySQL 8.0", Host: hp[0], Port: port, Username: p[1], Password: p[2], Database: "snap_test"}
	s, err := RealSchemaSnapshot(conn, "snap_test", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Tables) != 2 || tableOf(t, s, "v_order").Kind != "view" || tableOf(t, s, "v_order").Comment != "" {
		t.Fatalf("表与视图: %+v", s.Tables)
	}
	tb := tableOf(t, s, "t_order")
	if tb.Comment != "订单" || !reflect.DeepEqual(tb.PrimaryKey, []string{"id"}) {
		t.Errorf("表注释 / 主键: %q %v", tb.Comment, tb.PrimaryKey)
	}
	if c := colOf(t, tb, "id"); c.DataType != "bigint auto_increment" && c.DataType != "bigint(20) auto_increment" {
		t.Errorf("auto_increment 要并进类型比较: %q", c.DataType)
	}
	if c := colOf(t, tb, "status"); c.DataType != "tinyint(1)" || c.Nullable || c.Default != "0" || c.Comment != "状态" {
		t.Errorf("status: %+v", c)
	}
	if c := colOf(t, tb, "created_at"); !strings.Contains(c.DataType, "on update current_timestamp") || !strings.EqualFold(c.Default, "CURRENT_TIMESTAMP") {
		t.Errorf("created_at: %+v", c)
	}
	if c := colOf(t, tb, "amount"); !c.Nullable || c.HasDefault {
		t.Errorf("amount 可空、无默认值: %+v", c)
	}
	if ix := indexOf(tb, "status", "created_at"); ix == nil || ix.Unique || ix.Name != "idx_status" {
		t.Errorf("组合索引: %+v", tb.Indexes)
	}
	if ix := indexOf(tb, "code"); ix == nil || !ix.Unique {
		t.Errorf("唯一索引: %+v", tb.Indexes)
	}
	// 函数索引读出表达式,不是一个笼统的占位
	found := false
	for _, ix := range tb.Indexes {
		if ix.Name == "idx_lower_code" && len(ix.Columns) == 1 && strings.Contains(strings.ToLower(ix.Columns[0]), "lower") {
			found = true
		}
	}
	if !found {
		t.Errorf("函数索引应读出表达式: %+v", tb.Indexes)
	}
	// 支撑主键的索引打上标记;与主键同列的另一个唯一索引不打
	for _, ix := range tb.Indexes {
		if (ix.Name == "PRIMARY") != ix.Primary {
			t.Errorf("主键索引标记不对: %+v", ix)
		}
	}
}

func TestSchemaSnapshot_Oracle(t *testing.T) {
	conn := testOracle(t)
	owner := strings.ToUpper(conn.Username)
	for _, q := range []string{
		"BEGIN EXECUTE IMMEDIATE 'DROP VIEW SNAP_V_ORDER'; EXCEPTION WHEN OTHERS THEN NULL; END;",
		"BEGIN EXECUTE IMMEDIATE 'DROP TABLE SNAP_T_ORDER PURGE'; EXCEPTION WHEN OTHERS THEN NULL; END;",
		"CREATE TABLE SNAP_T_ORDER (ID NUMBER(19) PRIMARY KEY, CODE VARCHAR2(32 CHAR) NOT NULL UNIQUE, " +
			"STATUS NUMBER(1) DEFAULT 0 NOT NULL, AMOUNT NUMBER(10,2), NOTE VARCHAR2(100), CREATED_AT DATE DEFAULT SYSDATE)",
		"CREATE INDEX SNAP_IDX_STATUS ON SNAP_T_ORDER (STATUS, CREATED_AT)",
		"CREATE INDEX SNAP_IDX_UPPER ON SNAP_T_ORDER (UPPER(NOTE))",
		"COMMENT ON TABLE SNAP_T_ORDER IS '订单'",
		"COMMENT ON COLUMN SNAP_T_ORDER.STATUS IS '状态'",
		"CREATE VIEW SNAP_V_ORDER AS SELECT ID FROM SNAP_T_ORDER",
	} {
		if _, err := RealRun(context.Background(), conn, q, 0); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	t.Cleanup(func() {
		_, _ = RealRun(context.Background(), conn, "DROP VIEW SNAP_V_ORDER", 0)
		_, _ = RealRun(context.Background(), conn, "DROP TABLE SNAP_T_ORDER PURGE", 0)
	})
	s, err := RealSchemaSnapshot(conn, owner, "")
	if err != nil {
		t.Fatal(err)
	}
	tb := tableOf(t, s, "SNAP_T_ORDER")
	if tableOf(t, s, "SNAP_V_ORDER").Kind != "view" || tb.Comment != "订单" || !reflect.DeepEqual(tb.PrimaryKey, []string{"ID"}) {
		t.Errorf("视图 / 表注释 / 主键: %q %v", tb.Comment, tb.PrimaryKey)
	}
	want := map[string]string{"ID": "NUMBER(19)", "CODE": "VARCHAR2(32 CHAR)", "STATUS": "NUMBER(1)", "AMOUNT": "NUMBER(10,2)", "NOTE": "VARCHAR2(100)", "CREATED_AT": "DATE"}
	for name, typ := range want {
		if c := colOf(t, tb, name); c.DataType != typ {
			t.Errorf("%s 类型应为 %s,实际 %s", name, typ, c.DataType)
		}
	}
	if c := colOf(t, tb, "STATUS"); c.Nullable || c.Default != "0" || c.Comment != "状态" {
		t.Errorf("STATUS: %+v", c)
	}
	if c := colOf(t, tb, "CREATED_AT"); !c.HasDefault || !strings.EqualFold(c.Default, "SYSDATE") {
		t.Errorf("CREATED_AT 默认值: %+v", c)
	}
	if ix := indexOf(tb, "STATUS", "CREATED_AT"); ix == nil || ix.Unique || ix.SystemNamed {
		t.Errorf("组合索引: %+v", tb.Indexes)
	}
	if ix := indexOf(tb, "CODE"); ix == nil || !ix.Unique || !ix.SystemNamed {
		t.Errorf("列上 UNIQUE 生成的 SYS_C 索引应标系统命名: %+v", tb.Indexes)
	}
	for _, ix := range tb.Indexes {
		if ix.Name == "SNAP_IDX_UPPER" && (len(ix.Columns) != 1 || !strings.Contains(ix.Columns[0], "UPPER")) {
			t.Errorf("函数索引应读出表达式而不是 SYS_NC 隐藏列名: %+v", ix)
		}
		if ix.Primary != (strings.Join(ix.Columns, ",") == "ID") { // 表上只有主键建在 ID 上
			t.Errorf("主键索引标记不对: %+v", ix)
		}
	}
}

func TestSchemaSnapshot_Postgres(t *testing.T) {
	v := os.Getenv("AEGISDB_TEST_PG") // host:port/user/password
	if v == "" {
		t.Skip("未设置 AEGISDB_TEST_PG")
	}
	p := strings.SplitN(v, "/", 3)
	hp := strings.SplitN(p[0], ":", 2)
	port, _ := strconv.Atoi(hp[1])
	admin := &model.Connection{Engine: "PostgreSQL 16", Host: hp[0], Port: port, Username: p[1], Password: p[2], Database: "postgres"}
	_, _ = RealRun(context.Background(), admin, "DROP DATABASE IF EXISTS snap_test", 0)
	if _, err := RealRun(context.Background(), admin, "CREATE DATABASE snap_test", 0); err != nil {
		t.Fatal(err)
	}
	conn := *admin
	conn.Database = "snap_test"
	for _, q := range []string{
		"CREATE SCHEMA app",
		"CREATE TABLE t_order (id BIGSERIAL PRIMARY KEY, code VARCHAR(32) NOT NULL UNIQUE, status SMALLINT NOT NULL DEFAULT 0, " +
			"amount NUMERIC(10,2), label VARCHAR(8) DEFAULT 'new', created_at TIMESTAMP NOT NULL DEFAULT now())",
		"CREATE INDEX idx_status ON t_order (status, created_at)",
		"CREATE INDEX idx_lower_code ON t_order (lower((code)::text))",
		"COMMENT ON TABLE t_order IS '订单'",
		"COMMENT ON COLUMN t_order.status IS '状态'",
		"CREATE TABLE app.t_log (id INT PRIMARY KEY)",
		"CREATE VIEW v_order AS SELECT id FROM t_order",
	} {
		if _, err := RealRun(context.Background(), &conn, q, 0); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	s, err := RealSchemaSnapshot(&conn, "snap_test", "")
	if err != nil {
		t.Fatal(err)
	}
	if tableOf(t, s, "app.t_log").Kind != "table" || tableOf(t, s, "v_order").Kind != "view" {
		t.Errorf("非 public schema 带前缀、视图: %+v", s.Tables)
	}
	tb := tableOf(t, s, "t_order")
	if tb.Comment != "订单" || !reflect.DeepEqual(tb.PrimaryKey, []string{"id"}) {
		t.Errorf("表注释 / 主键: %q %v", tb.Comment, tb.PrimaryKey)
	}
	if c := colOf(t, tb, "code"); c.DataType != "character varying(32)" || c.Nullable {
		t.Errorf("code: %+v", c)
	}
	if c := colOf(t, tb, "status"); c.Default != "0" || c.Comment != "状态" {
		t.Errorf("status: %+v", c)
	}
	if c := colOf(t, tb, "label"); !strings.HasPrefix(c.Default, "'new'") {
		t.Errorf("label 默认值: %+v", c)
	}
	if ix := indexOf(tb, "status", "created_at"); ix == nil || ix.Unique {
		t.Errorf("组合索引: %+v", tb.Indexes)
	}
	if ix := indexOf(tb, "code"); ix == nil || !ix.Unique {
		t.Errorf("唯一约束: %+v", tb.Indexes)
	}
	for _, ix := range tb.Indexes {
		if ix.Name == "idx_lower_code" && (len(ix.Columns) != 1 || !strings.Contains(ix.Columns[0], "lower")) {
			t.Errorf("表达式索引应读出表达式: %+v", ix)
		}
		if ix.Primary != (ix.Name == "t_order_pkey") {
			t.Errorf("主键索引标记不对: %+v", ix)
		}
	}
	// 只读一个 schema:表名不带前缀,别的 schema 不出现
	only, err := RealSchemaSnapshot(&conn, "snap_test", "app")
	if err != nil {
		t.Fatal(err)
	}
	if len(only.Tables) != 1 || only.Tables[0].Name != "t_log" {
		t.Errorf("指定 schema 应只读这一个、表名不带前缀: %+v", only.Tables)
	}
}

// 两台真实 MySQL 上执行同一段建表语句:对比必须一致(没有假差异);改一列、加一个索引后,
// 恰好报出这两处。AEGISDB_TEST_MYSQL 与 AEGISDB_TEST_MYSQL_REPLICA 各一台。
func TestSchemaSnapshot_MySQLTwoInstancesDiff(t *testing.T) {
	envs := []string{os.Getenv("AEGISDB_TEST_MYSQL"), os.Getenv("AEGISDB_TEST_MYSQL_REPLICA")}
	if envs[0] == "" || envs[1] == "" {
		t.Skip("需要 AEGISDB_TEST_MYSQL 与 AEGISDB_TEST_MYSQL_REPLICA 两台 MySQL")
	}
	ddl := []string{
		"DROP DATABASE IF EXISTS snap_diff", "CREATE DATABASE snap_diff",
		"CREATE TABLE snap_diff.t_order (id BIGINT NOT NULL AUTO_INCREMENT, code VARCHAR(32) NOT NULL, status TINYINT(1) NOT NULL DEFAULT 0," +
			" note VARCHAR(100) DEFAULT 'n/a', created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, PRIMARY KEY (id), UNIQUE KEY uk_code (code)) COMMENT='订单'",
		"CREATE TABLE snap_diff.t_user (id INT PRIMARY KEY, name VARCHAR(20))",
	}
	var snaps []*schemadiff.Snapshot
	for i, v := range envs {
		p := strings.SplitN(v, "/", 3)
		hp := strings.SplitN(p[0], ":", 2)
		raw, err := sql.Open("mysql", fmt.Sprintf("%s:%s@tcp(%s)/", p[1], p[2], p[0]))
		if err != nil {
			t.Fatal(err)
		}
		stmts := ddl
		if i == 1 {
			stmts = append(append([]string{}, ddl...),
				"ALTER TABLE snap_diff.t_order MODIFY note VARCHAR(200) DEFAULT 'n/a'",
				"CREATE INDEX idx_status ON snap_diff.t_order (status)")
		}
		for _, s := range stmts {
			if _, err := raw.Exec(s); err != nil {
				t.Fatalf("%s: %v", s, err)
			}
		}
		raw.Close()
		port, _ := strconv.Atoi(hp[1])
		s, err := RealSchemaSnapshot(&model.Connection{Engine: "MySQL 8.0", Host: hp[0], Port: port, Username: p[1], Password: p[2]}, "snap_diff", "")
		if err != nil {
			t.Fatal(err)
		}
		snaps = append(snaps, s)
	}
	r := schemadiff.DiffSchemas(snaps[0], snaps[1], schemadiff.Options{})
	if r.Summary.Same != 1 || r.Summary.Changed != 1 || len(r.OnlyLeft)+len(r.OnlyRight) != 0 {
		t.Fatalf("应一张一致、一张不同: %+v", r.Summary)
	}
	td := r.Changed[0]
	if len(td.Columns) != 1 || td.Columns[0].Column != "note" || td.Columns[0].Changes[0] != (schemadiff.Change{Attr: "type", Left: "varchar(100)", Right: "varchar(200)"}) {
		t.Errorf("应只报 note 类型: %+v", td.Columns)
	}
	if len(td.Indexes) != 1 || td.Indexes[0].Index != "idx_status" || td.Indexes[0].Status != schemadiff.OnlyRight {
		t.Errorf("应只报右边多一个 idx_status: %+v", td.Indexes)
	}
	if td.Kind != nil || td.Comment != nil || td.PrimaryKey != nil {
		t.Errorf("没改的属性不该报: %+v", td)
	}
}

// 按 schema 对比 app 与 app_uat:两边建同样的表(自增列、本 schema 的枚举类型)。不在 search_path 上
// 的对象,PG 会给默认值 / 类型带 schema 前缀 —— 那样每个自增列、枚举列都会报假差异。必须完全一致。
func TestSchemaSnapshot_PostgresSchemaPairHasNoPrefixNoise(t *testing.T) {
	v := os.Getenv("AEGISDB_TEST_PG")
	if v == "" {
		t.Skip("未设置 AEGISDB_TEST_PG")
	}
	p := strings.SplitN(v, "/", 3)
	hp := strings.SplitN(p[0], ":", 2)
	port, _ := strconv.Atoi(hp[1])
	admin := &model.Connection{Engine: "PostgreSQL 16", Host: hp[0], Port: port, Username: p[1], Password: p[2], Database: "postgres"}
	_, _ = RealRun(context.Background(), admin, "DROP DATABASE IF EXISTS snap_pair", 0)
	if _, err := RealRun(context.Background(), admin, "CREATE DATABASE snap_pair", 0); err != nil {
		t.Fatal(err)
	}
	conn := *admin
	conn.Database = "snap_pair"
	for _, sc := range []string{"app", "app_uat"} {
		for _, q := range []string{
			"CREATE SCHEMA " + sc,
			"CREATE TYPE " + sc + ".status AS ENUM ('new', 'done')",
			"CREATE TABLE " + sc + ".t_order (id SERIAL PRIMARY KEY, st " + sc + ".status NOT NULL DEFAULT 'new', code VARCHAR(8))",
			"CREATE INDEX idx_code ON " + sc + ".t_order (lower(code))",
		} {
			if _, err := RealRun(context.Background(), &conn, q, 0); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
	}
	a, err := RealSchemaSnapshot(&conn, "snap_pair", "app")
	if err != nil {
		t.Fatal(err)
	}
	b, err := RealSchemaSnapshot(&conn, "snap_pair", "app_uat")
	if err != nil {
		t.Fatal(err)
	}
	if r := schemadiff.DiffSchemas(a, b, schemadiff.Options{}); r.Summary.Same != 1 || r.Summary.Changed != 0 {
		t.Errorf("两个 schema 结构相同,不该有差异: %+v", r.Changed)
	}
	// 读完之后 search_path 没有被带回连接池:再按全库读,非 public 的表名仍带前缀
	all, err := RealSchemaSnapshot(&conn, "snap_pair", "")
	if err != nil {
		t.Fatal(err)
	}
	if tableOf(t, all, "app.t_order").Name != "app.t_order" {
		t.Error("全库读取时非 public 表名应带前缀")
	}
}

// Oracle:函数索引建在别的用户名下(索引属主 ≠ 表属主)时,也要读出表达式,不能留 SYS_NC…$。
func TestSchemaSnapshot_OracleIndexOwnedByAnotherUser(t *testing.T) {
	conn := testOracle(t)
	owner := strings.ToUpper(conn.Username)
	run := func(q string) {
		if _, err := RealRun(context.Background(), conn, q, 0); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	quiet := func(q string) { _, _ = RealRun(context.Background(), conn, q, 0) }
	quiet("BEGIN EXECUTE IMMEDIATE 'DROP USER SNAP_IDX_OWNER CASCADE'; EXCEPTION WHEN OTHERS THEN NULL; END;")
	quiet("BEGIN EXECUTE IMMEDIATE 'DROP TABLE SNAP_T_OWN PURGE'; EXCEPTION WHEN OTHERS THEN NULL; END;")
	run("CREATE USER SNAP_IDX_OWNER IDENTIFIED BY \"Snap_123\"")
	run("ALTER USER SNAP_IDX_OWNER QUOTA UNLIMITED ON USERS")
	run("CREATE TABLE SNAP_T_OWN (ID NUMBER PRIMARY KEY, CODE VARCHAR2(20))")
	run("CREATE INDEX SNAP_IDX_OWNER.SNAP_IDX_UP ON SNAP_T_OWN (UPPER(CODE))")
	t.Cleanup(func() {
		quiet("DROP TABLE SNAP_T_OWN PURGE")
		quiet("DROP USER SNAP_IDX_OWNER CASCADE")
	})
	s, err := RealSchemaSnapshot(conn, owner, "")
	if err != nil {
		t.Fatal(err)
	}
	tb := tableOf(t, s, "SNAP_T_OWN")
	found := false
	for _, ix := range tb.Indexes {
		if ix.Name == "SNAP_IDX_UP" {
			found = true
			if len(ix.Columns) != 1 || !strings.Contains(ix.Columns[0], "UPPER") {
				t.Errorf("别的用户名下的函数索引应读出表达式: %+v", ix)
			}
		}
	}
	if !found {
		t.Errorf("没读到别的用户名下的索引: %+v", tb.Indexes)
	}
}

// 库名下拉:三个引擎各自列出业务库、不列系统库。
func TestDiffDatabaseNames(t *testing.T) {
	has := func(names []string, n string) bool {
		for _, x := range names {
			if strings.EqualFold(x, n) {
				return true
			}
		}
		return false
	}
	check := func(t *testing.T, conn *model.Connection, want string, system ...string) {
		t.Helper()
		names, err := DiffDatabaseNames(conn)
		if err != nil {
			t.Fatal(err)
		}
		if !has(names, want) {
			t.Errorf("应列出 %s: %v", want, names)
		}
		for _, s := range system {
			if has(names, s) {
				t.Errorf("不应列出系统库 %s: %v", s, names)
			}
		}
	}
	t.Run("sqlite", func(t *testing.T) {
		names, err := DiffDatabaseNames(&model.Connection{Engine: "SQLite"})
		if err != nil || strings.Join(names, ",") != "main" {
			t.Fatalf("%v %v", names, err)
		}
	})
	t.Run("mysql", func(t *testing.T) {
		v := os.Getenv("AEGISDB_TEST_MYSQL")
		if v == "" {
			t.Skip("未设置 AEGISDB_TEST_MYSQL")
		}
		p := strings.SplitN(v, "/", 3)
		hp := strings.SplitN(p[0], ":", 2)
		raw, err := sql.Open("mysql", fmt.Sprintf("%s:%s@tcp(%s)/", p[1], p[2], p[0]))
		if err != nil {
			t.Fatal(err)
		}
		_, err = raw.Exec("CREATE DATABASE IF NOT EXISTS dbpick_test")
		raw.Close()
		if err != nil {
			t.Fatal(err)
		}
		port, _ := strconv.Atoi(hp[1])
		// 配的默认库不存在也能列
		check(t, &model.Connection{Engine: "MySQL 8.0", Host: hp[0], Port: port, Username: p[1], Password: p[2], Database: "no_such_db"},
			"dbpick_test", "mysql", "information_schema", "performance_schema", "sys")
	})
	t.Run("postgres", func(t *testing.T) {
		v := os.Getenv("AEGISDB_TEST_PG")
		if v == "" {
			t.Skip("未设置 AEGISDB_TEST_PG")
		}
		p := strings.SplitN(v, "/", 3)
		hp := strings.SplitN(p[0], ":", 2)
		port, _ := strconv.Atoi(hp[1])
		check(t, &model.Connection{Engine: "PostgreSQL 16", Host: hp[0], Port: port, Username: p[1], Password: p[2], Database: "postgres"},
			"postgres", "template0", "template1")
	})
	t.Run("oracle", func(t *testing.T) {
		conn := testOracle(t)
		// 登录账号自己的 schema 总在(默认比的就是它,哪怕它是系统用户);别的系统用户不列
		check(t, conn, conn.Username, "SYS", "XDB", "CTXSYS", "MDSYS")
	})
}

// schema 下拉:PG 连到指定的库,列出用户 schema(与读快照同一口径),不列系统 schema。
func TestDiffSchemaNames_Postgres(t *testing.T) {
	v := os.Getenv("AEGISDB_TEST_PG")
	if v == "" {
		t.Skip("未设置 AEGISDB_TEST_PG")
	}
	p := strings.SplitN(v, "/", 3)
	hp := strings.SplitN(p[0], ":", 2)
	port, _ := strconv.Atoi(hp[1])
	admin := &model.Connection{Engine: "PostgreSQL 16", Host: hp[0], Port: port, Username: p[1], Password: p[2], Database: "postgres"}
	_, _ = RealRun(context.Background(), admin, "DROP DATABASE IF EXISTS schpick_test", 0)
	if _, err := RealRun(context.Background(), admin, "CREATE DATABASE schpick_test", 0); err != nil {
		t.Fatal(err)
	}
	conn := *admin
	conn.Database = "schpick_test"
	for _, q := range []string{"CREATE SCHEMA app", "CREATE SCHEMA app_uat"} {
		if _, err := RealRun(context.Background(), &conn, q, 0); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	// 实例配的默认库是 postgres,按参数连到 schpick_test
	names, err := DiffSchemaNames(admin, "schpick_test")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(names, ","); got != "app,app_uat,public" {
		t.Errorf("应只列用户 schema: %s", got)
	}
	if _, err := DiffSchemaNames(&model.Connection{Engine: "MySQL 8.0"}, "x"); err == nil {
		t.Error("MySQL 没有 schema 这一层,应报错")
	}
}
