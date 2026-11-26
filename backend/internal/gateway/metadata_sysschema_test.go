package gateway

import (
	"context"
	"database/sql"
	"slices"
	"testing"

	"aegisdb/internal/model"
	"aegisdb/internal/testpg"
)

// issue #92:TiDB 的 information_schema 把系统库名按大写返回,而且库名比较区分大小写,
// 小写字面量的 NOT IN 一个也滤不掉;TiDB 还多一个 metrics_schema。
//
// 没有 TiDB 也要能钉住它:PostgreSQL 的字符串比较同样区分大小写。在一次性的临时库里
// 把自带的 information_schema 删掉,换成同名、同列名的普通表,跑的就是产品里那几条 SQL 原文。
func fakeTiDBCatalog(t *testing.T) *sql.DB {
	t.Helper()
	conn := pgTarget(t)
	db, err := sql.Open("pgx", testpg.Server(t).DSN(conn.Database))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	for _, s := range []string{
		// MySQL 的 IFNULL,产品里的几条 SQL 在用。
		`CREATE FUNCTION public.ifnull(text, text) RETURNS text LANGUAGE sql IMMUTABLE AS 'SELECT COALESCE($1, $2)'`,
		`DROP SCHEMA information_schema CASCADE`,
		`CREATE SCHEMA information_schema`,
		`CREATE TABLE information_schema.tables (table_schema TEXT, table_name TEXT, table_type TEXT, table_comment TEXT)`,
		`CREATE TABLE information_schema.schemata (schema_name TEXT)`,
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
		if _, err := db.Exec(`INSERT INTO information_schema.tables VALUES ($1, 't', 'BASE TABLE', '')`, schema); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO information_schema.schemata VALUES ($1)`, schema); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO information_schema.columns VALUES ($1, 't', 1, 'id', 'int', 'NO', NULL, '', 'PRI')`, schema); err != nil {
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
	assertOnlyUserSchema(t, liveTableQuery(&model.Connection{Engine: "TiDB 7.5"}))
}

// 对象树也是同一个口径:表清单。
func TestSchemaIntrospectMySQL_SkipsSystemSchemasInAnyCase(t *testing.T) {
	for _, engine := range []string{"TiDB 7.5", "MySQL 8.0", "MariaDB 11"} {
		query, _ := schemaIntrospectQuery(&model.Connection{Engine: engine})
		assertOnlyUserSchema(t, query)
	}
}

// 对象树补空库名的那一条。
func TestMySQLSchemaNames_SkipsSystemSchemasInAnyCase(t *testing.T) {
	got, err := mysqlSchemaNames(context.Background(), fakeTiDBCatalog(t))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"Orders"}) {
		t.Errorf("应只有用户库,实际 %v", got)
	}
}

// assertOnlyUserSchema 跑一条"库名, 表名"的查询,结果应只剩用户库。
func assertOnlyUserSchema(t *testing.T, query string) {
	t.Helper()
	rows, err := fakeTiDBCatalog(t).Query(query)
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
