package gateway

import (
	"context"
	"testing"
	"time"

	"aegisdb/internal/model"
	"aegisdb/pkg/sqlutil"
)

// 导出成 SQL 的验收标准只有一个:导出的 INSERT 在同结构的表里执行一遍,读回来的就是原来的
// 数据。各库最容易出错的地方各不相同 —— MySQL 的反斜杠、PostgreSQL 的 E''、Oracle 的
// TIMESTAMP 与 HEXTORAW、各家的二进制 —— 所以每个库都对着真实实例走一遍。

func sqlDumpRoundTrip(t *testing.T, conn *model.Connection, setup []string, cols string) {
	t.Helper()
	ctx := context.Background()
	run := func(q string) {
		t.Helper()
		if res, err := RealRun(ctx, conn, q, 30*time.Second); err != nil || res.Err != nil {
			t.Fatalf("%s: %v %v", q, err, res.Err)
		}
	}
	for _, q := range setup {
		run(q)
	}
	read := func(table string) [][]Cell {
		t.Helper()
		var out [][]Cell
		var header []string
		err := RealQueryEachCellsRaw(ctx, conn, "SELECT "+cols+" FROM "+table+" ORDER BY id", 30*time.Second,
			func(h []string) error { header = h; return nil },
			func(r []Cell) error { out = append(out, append([]Cell(nil), r...)); return nil })
		if err != nil {
			t.Fatalf("read %s: %v", table, err)
		}
		_ = header
		return out
	}
	src := read("vx_src")
	var header []string
	_ = RealQueryEachCellsRaw(ctx, conn, "SELECT "+cols+" FROM vx_src WHERE 1=0", 30*time.Second,
		func(h []string) error { header = h; return nil }, func([]Cell) error { return nil })
	dump := InsertStatements(conn.Engine, "vx_dst", header, src)
	for _, st := range sqlutil.SplitStatements(dump) {
		run(st)
	}
	dst := read("vx_dst")
	if len(dst) != len(src) {
		t.Fatalf("导回 %d 行,应为 %d 行\n%s", len(dst), len(src), dump)
	}
	for i := range src {
		for j := range src[i] {
			a, b := src[i][j], dst[i][j]
			if a.Null != b.Null || a.Text != b.Text {
				t.Errorf("行 %d 列 %s 不一致:源 %+v 导回 %+v\n%s", i+1, header[j], a, b, dump)
			}
		}
	}
}

func TestSQLDump_RoundTrip_MySQL(t *testing.T) {
	conn := tcMySQL(t)
	conn.Database = "tc_a"
	rawExec(t, conn, "CREATE DATABASE IF NOT EXISTS tc_a")
	def := "(id INT PRIMARY KEY, s VARCHAR(100), n VARCHAR(100), e VARCHAR(10), z VARCHAR(50), b VARBINARY(10), ts DATETIME, d DECIMAL(10,2))"
	sqlDumpRoundTrip(t, conn, []string{
		"DROP TABLE IF EXISTS vx_src", "DROP TABLE IF EXISTS vx_dst",
		"CREATE TABLE vx_src " + def, "CREATE TABLE vx_dst " + def,
		`INSERT INTO vx_src VALUES (1, 'O''Brien C:\\new\\n', NULL, '', '张三', X'00FF27', '2026-10-08 09:30:00', 12.50)`,
	}, "id, s, n, e, z, b, ts, d")
}

func TestSQLDump_RoundTrip_Postgres(t *testing.T) {
	conn := testPostgres(t)
	def := "(id INT PRIMARY KEY, s TEXT, n TEXT, e TEXT, z TEXT, b BYTEA, ts TIMESTAMP, d NUMERIC(10,2), bo BOOLEAN)"
	sqlDumpRoundTrip(t, conn, []string{
		"DROP TABLE IF EXISTS vx_src", "DROP TABLE IF EXISTS vx_dst",
		"CREATE TABLE vx_src " + def, "CREATE TABLE vx_dst " + def,
		`INSERT INTO vx_src VALUES (1, E'O''Brien C:\\new\\n', NULL, '', '张三', '\x00ff27'::bytea, '2026-10-08 09:30:00', 12.50, true)`,
	}, "id, s, n, e, z, b, ts, d, bo")
}

func TestSQLDump_RoundTrip_Oracle(t *testing.T) {
	conn := testOracle(t)
	def := "(id NUMBER PRIMARY KEY, s VARCHAR2(100), n VARCHAR2(100), z NVARCHAR2(50), b RAW(10), ts TIMESTAMP, d NUMBER(10,2))"
	drop := func(tb string) string {
		return "BEGIN EXECUTE IMMEDIATE 'DROP TABLE " + tb + "'; EXCEPTION WHEN OTHERS THEN NULL; END;"
	}
	sqlDumpRoundTrip(t, conn, []string{
		drop("vx_src"), drop("vx_dst"),
		"CREATE TABLE vx_src " + def, "CREATE TABLE vx_dst " + def,
		`INSERT INTO vx_src VALUES (1, 'O''Brien C:\new', NULL, N'张三', HEXTORAW('00FF27'), TIMESTAMP '2026-10-08 09:30:00', 12.5)`,
	}, "id, s, n, z, b, ts, d")
}
