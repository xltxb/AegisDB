package gateway

import (
	"context"
	"database/sql"
	"slices"
	"testing"

	"aegisdb/internal/model"
)

// issue #92:TiDB 的 information_schema 把系统库名按大写返回,而且库名比较区分大小写,
// 小写字面量的 NOT IN 一个也滤不掉;TiDB 还多一个 metrics_schema。
//
// 没有 TiDB 也要能钉住它:SQLite 的字符串比较同样区分大小写,附加一个名叫
// information_schema 的库,跑的就是产品里那几条 SQL 原文。
func fakeTiDBCatalog(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1) // ATTACH 只对当前连接有效
	for _, s := range []string{
		`ATTACH ':memory:' AS information_schema`,
		`CREATE TABLE information_schema.tables (table_schema TEXT, table_name TEXT, table_type TEXT, table_comment TEXT)`,
		`CREATE TABLE information_schema.columns (table_schema TEXT, table_name TEXT, ordinal_position INT, column_name TEXT,
			column_type TEXT, is_nullable TEXT, column_default TEXT, column_comment TEXT, column_key TEXT)`,
	} {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	for _, schema := range []string{
		"INFORMATION_SCHEMA", "METRICS_SCHEMA", "PERFORMANCE_SCHEMA", "mysql", "sys", // TiDB
		"information_schema", "performance_schema", // MySQL
		"Orders", // 用户库,大小写要原样保留
	} {
		if _, err := db.Exec(`INSERT INTO information_schema.tables VALUES (?, 't', 'BASE TABLE', '')`, schema); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO information_schema.columns VALUES (?, 't', 1, 'id', 'int', 'NO', NULL, '', 'PRI')`, schema); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func TestRealMetadataMySQL_SkipsSystemSchemasInAnyCase(t *testing.T) {
	tables, cols, err := realMetadataMySQL(context.Background(), fakeTiDBCatalog(t))
	if err != nil {
		t.Fatal(err)
	}
	var tableDBs, colDBs []string
	for _, r := range tables {
		tableDBs = append(tableDBs, r.Database)
	}
	for _, r := range cols {
		colDBs = append(colDBs, r.Database)
	}
	if !slices.Equal(tableDBs, []string{"Orders"}) {
		t.Errorf("表清单应只有用户库,实际 %v", tableDBs)
	}
	if !slices.Equal(colDBs, []string{"Orders"}) {
		t.Errorf("列清单应只有用户库,实际 %v", colDBs)
	}
}

// 预检读实时表名,必须与元数据探查同一个口径。
func TestLiveTableQueryMySQL_SkipsSystemSchemasInAnyCase(t *testing.T) {
	rows, err := fakeTiDBCatalog(t).Query(liveTableQuery(&model.Connection{Engine: "TiDB 7.5"}))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var schema, name string
		if err := rows.Scan(&schema, &name); err != nil {
			t.Fatal(err)
		}
		got = append(got, schema)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"Orders"}) {
		t.Errorf("应只有用户库,实际 %v", got)
	}
}
