package gateway

// 元数据探查:表清单 + 每张表的每一列。
//
// 这组用例跑在**真实的 SQLite 上**,不是替身 —— 它是这个仓库里唯一能真跑的引擎,
// 而这里要验的恰恰是"SQL 写对了没有"。MySQL / PostgreSQL / Oracle 那三条只能等真机
// 验收(见 issue),但它们与这一条共用同一组返回结构和同一套上限逻辑。
//
// 钉住的三件事,对应三种会真正伤到人的错法:
//   - 列的属性(类型 / 可空 / 默认值 / 主键)要对得上。错了不会报错,只会让人照着一份
//     错的结构去改表。
//   - 视图要标成 view。把视图当表列出来,人会去 ALTER 它。
//   - sqlite_% 内部表不入清单。它们不是用户的数据,混进搜索结果里就是永久的噪音。

import (
	"database/sql"
	"path/filepath"
	"testing"

	"velagateway/internal/model"
)

func metaConn(t *testing.T) *model.Connection {
	t.Helper()
	return &model.Connection{Engine: "SQLite", Database: filepath.Join(t.TempDir(), "meta.db")}
}

func mustExec(t *testing.T, conn *model.Connection, stmts ...string) {
	t.Helper()
	db, err := sql.Open("sqlite", conn.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
}

func TestRealMetadata_SQLiteTablesAndColumns(t *testing.T) {
	conn := metaConn(t)
	mustExec(t, conn,
		`CREATE TABLE t_order (
			id      INTEGER PRIMARY KEY,
			amount  REAL NOT NULL DEFAULT 0,
			note    TEXT
		)`,
		`CREATE TABLE t_customer (id INTEGER PRIMARY KEY, name TEXT NOT NULL)`,
		`CREATE VIEW v_order_note AS SELECT id, note FROM t_order`,
	)

	tables, cols, err := RealMetadata(conn)
	if err != nil {
		t.Fatalf("探查失败: %v", err)
	}

	byName := map[string]MetaTableRow{}
	for _, tb := range tables {
		byName[tb.Table] = tb
	}
	for _, want := range []string{"t_order", "t_customer", "v_order_note"} {
		if _, ok := byName[want]; !ok {
			t.Errorf("表清单里少了 %s:%+v", want, tables)
		}
	}
	// 视图要标成 view —— 当成表列出来,人会去 ALTER 它。
	if byName["v_order_note"].Kind != "view" {
		t.Errorf("视图应当标成 view,实际 %q", byName["v_order_note"].Kind)
	}
	if byName["t_order"].Kind != "table" {
		t.Errorf("表应当标成 table,实际 %q", byName["t_order"].Kind)
	}
	// sqlite 的内部表不入清单。
	for _, tb := range tables {
		if len(tb.Table) >= 7 && tb.Table[:7] == "sqlite_" {
			t.Errorf("内部表不该进清单: %s", tb.Table)
		}
	}

	got := map[string]MetaColumnRow{}
	for _, c := range cols {
		if c.Table == "t_order" {
			got[c.Name] = c
		}
	}
	if len(got) != 3 {
		t.Fatalf("t_order 应当有 3 列,实际 %d:%+v", len(got), got)
	}
	// 主键 / 可空 / 默认值:错了都不会报错,只会让人照着一份错的结构去改表。
	if !got["id"].IsPK {
		t.Error("id 应当是主键")
	}
	if got["amount"].IsPK {
		t.Error("amount 不是主键")
	}
	if got["amount"].Nullable {
		t.Error("amount 是 NOT NULL,不该标成可空")
	}
	if !got["note"].Nullable {
		t.Error("note 没有 NOT NULL,应当可空")
	}
	if got["amount"].Default != "0" {
		t.Errorf("amount 的默认值应当是 0,实际 %q", got["amount"].Default)
	}
	if got["id"].Ordinal != 1 || got["amount"].Ordinal != 2 {
		t.Errorf("列序号应当从 1 开始按定义顺序:%+v", got)
	}
	// 库名与树里那份口径一致(singleDBName),否则同一台实例在树里和缓存里叫两个名字。
	if got["id"].Database != singleDBName(conn) {
		t.Errorf("库名口径不一致:%q vs %q", got["id"].Database, singleDBName(conn))
	}
}

// 空库不是错误。返回空清单 + nil,让调用方把"同步过、确实是空的"和"没同步过"分开。
func TestRealMetadata_EmptyDatabaseIsNotAnError(t *testing.T) {
	conn := metaConn(t)
	mustExec(t, conn, `CREATE TABLE t_tmp (id INTEGER)`, `DROP TABLE t_tmp`)

	tables, cols, err := RealMetadata(conn)
	if err != nil {
		t.Fatalf("空库不该报错: %v", err)
	}
	if len(tables) != 0 || len(cols) != 0 {
		t.Errorf("空库应当两组都空:%d 表 %d 列", len(tables), len(cols))
	}
}

// 没有凭据的模拟连接必须**报错**,不能返回空清单。
//
// 空清单读起来是"这台库很干净",而真相是"根本没连上"。这是这个功能最坏的一种谎:
// 它会让人以为某台生产库上没有那张表。
func TestRealMetadata_SimulatedConnectionErrorsInsteadOfLookingEmpty(t *testing.T) {
	conn := &model.Connection{Engine: "MySQL 8.0", Host: "10.0.0.9", Port: 3306} // 无账号 = 模拟
	if _, _, err := RealMetadata(conn); err == nil {
		t.Fatal("没有凭据时应当报错,而不是返回一份空清单")
	}
}

func TestRealMetadata_UnsupportedEngine(t *testing.T) {
	conn := &model.Connection{Engine: "Redis", Host: "h", Port: 6379, Username: "u", Password: "p"}
	if _, _, err := RealMetadata(conn); err == nil {
		t.Fatal("不支持的引擎应当报错")
	}
}
