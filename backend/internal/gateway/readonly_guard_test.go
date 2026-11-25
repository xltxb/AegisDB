package gateway

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"velagateway/internal/model"
)

func TestReadOnlyStatements(t *testing.T) {
	for _, c := range []struct {
		eng, sql string
		want     bool
	}{
		{"MySQL 8.0", "SELECT f() FROM t", true},
		{"MySQL 8.0", "SELECT 1; SHOW TABLES; DESC t", true},
		{"MySQL 8.0", "SET NAMES utf8mb4; SELECT 1", true},
		{"MySQL 8.0", "EXPLAIN DELETE FROM t", true},
		{"MySQL 8.0", "SELECT 1; UPDATE t SET a = 1", false},
		{"MySQL 8.0", "CALL p()", false},
		{"MySQL 8.0", "CREATE TABLE x (a int)", false},
		{"MySQL 8.0", "SELECT * FROM t FOR UPDATE", true}, // 加锁读按判定层的 select;只读会话里照样能加锁读
		{"PostgreSQL 16", "SELECT nextval('s')", false},
		{"PostgreSQL 16", "SET search_path TO app; SELECT 1", true},
		{"Oracle 19c", "SET SERVEROUTPUT ON\nSELECT 1 FROM dual;", true},
		{"Oracle 19c", "ALTER SESSION SET NLS_DATE_FORMAT = 'YYYY'", true},
		{"Oracle 19c", "BEGIN p; END;", false},
		{"MySQL 8.0", "", false},
	} {
		if got := ReadOnlyStatements(c.eng, c.sql); got != c.want {
			t.Errorf("%s %q:ReadOnlyStatements = %v,应为 %v", c.eng, c.sql, got, c.want)
		}
	}
}

// 真实 MySQL:SELECT 一个会写表的函数。只读守卫下被数据库拒绝(1792)、一行不写;没有守卫时照旧
// 能写(有写权限的人不受影响);守卫用完会话恢复原样。终端、导出、后台任务三条路都过一遍。
func TestReadOnlyGuard_MySQLFunctionWrite_RealDB(t *testing.T) {
	conn := testMySQL(t)
	ctx := context.Background()
	for _, s := range []string{"CREATE DATABASE IF NOT EXISTS vz_guard", "DROP TABLE IF EXISTS vz_guard.lg", "CREATE TABLE vz_guard.lg (n INT)",
		"DROP FUNCTION IF EXISTS vz_guard.f",
		"CREATE FUNCTION vz_guard.f() RETURNS INT DETERMINISTIC MODIFIES SQL DATA BEGIN INSERT INTO vz_guard.lg VALUES (1); RETURN 1; END"} {
		if _, err := RealRun(ctx, conn, s, 30*time.Second); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	t.Cleanup(func() { _, _ = RealRun(ctx, conn, "DROP DATABASE vz_guard", 30*time.Second) })
	c := *conn
	c.Database = "vz_guard"
	count := func() string {
		r, err := RealRun(ctx, conn, "SELECT COUNT(*) FROM vz_guard.lg", 10*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		return r.Data[0][0]
	}

	_, err := RealRun(WithReadOnlyGuard(ctx), &c, "SELECT f()", 10*time.Second)
	if err == nil || !strings.Contains(err.Error(), "1792") || !strings.Contains(err.Error(), "只读执行") {
		t.Fatalf("终端只读守卫下函数里的写应被拒绝并说明,实际 %v", err)
	}
	err = RealQueryEach(ctx, &c, "SELECT f()", 10*time.Second, func([]string) error { return nil }, func([]string) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "1792") {
		t.Fatalf("导出里函数的写应被拒绝,实际 %v", err)
	}
	if _, err := RealRunAsyncGuarded(&c, "SELECT f()", 10*time.Second, nil, true); err == nil || !strings.Contains(err.Error(), "1792") {
		t.Fatalf("只读后台任务里函数的写应被拒绝,实际 %v", err)
	}
	if n := count(); n != "0" {
		t.Fatalf("被拒的三次一行都不该写,实际 %s 行", n)
	}

	// 没有守卫(有写权限的人):照旧
	if _, err := RealRun(ctx, &c, "SELECT f()", 10*time.Second); err != nil {
		t.Fatalf("没有守卫时应照常执行,实际 %v", err)
	}
	if n := count(); n != "1" {
		t.Fatalf("没有守卫时函数应写进 1 行,实际 %s", n)
	}
	// 守卫下的普通读、导出照常
	if r, err := RealRun(WithReadOnlyGuard(ctx), &c, "SELECT COUNT(*) FROM lg", 10*time.Second); err != nil || r.Data[0][0] != "1" {
		t.Fatalf("守卫下的普通查询应照常,实际 %v %v", r.Data, err)
	}
	rows := 0
	if err := RealQueryEach(ctx, &c, "SELECT n FROM lg", 10*time.Second, func([]string) error { return nil }, func([]string) error { rows++; return nil }); err != nil || rows != 1 {
		t.Fatalf("普通导出应照常,实际 %d 行 %v", rows, err)
	}

	// 恢复:同一条连接上守卫之后会话不再只读
	db, release, err := openConn(&c)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	sc, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer sc.Close()
	_, restore, err := guardReadOnly(ctx, db, sc, &c, "SELECT 1")
	if err != nil {
		t.Fatal(err)
	}
	var on string
	_ = sc.QueryRowContext(ctx, "SELECT @@SESSION.transaction_read_only").Scan(&on)
	restore()
	var off string
	_ = sc.QueryRowContext(ctx, "SELECT @@SESSION.transaction_read_only").Scan(&off)
	if on != "1" || off != "0" {
		t.Errorf("守卫期间应只读、之后恢复:期间 %q 之后 %q", on, off)
	}
}

func TestReadOnlyGuard_PostgresFunctionWrite_RealDB(t *testing.T) {
	conn := testPostgres(t)
	ctx := context.Background()
	for _, s := range []string{"DROP TABLE IF EXISTS vz_guard_lg", "CREATE TABLE vz_guard_lg (n INT)",
		`CREATE OR REPLACE FUNCTION vz_guard_f() RETURNS int LANGUAGE plpgsql AS $$ BEGIN INSERT INTO vz_guard_lg VALUES (1); RETURN 1; END $$`} {
		if _, err := RealRun(ctx, conn, s, 30*time.Second); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	t.Cleanup(func() {
		_, _ = RealRun(ctx, conn, "DROP FUNCTION IF EXISTS vz_guard_f()", 30*time.Second)
		_, _ = RealRun(ctx, conn, "DROP TABLE IF EXISTS vz_guard_lg", 30*time.Second)
	})
	if _, err := RealRun(WithReadOnlyGuard(ctx), conn, "SELECT vz_guard_f()", 10*time.Second); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("PG 只读守卫下函数里的写应被拒绝,实际 %v", err)
	}
	if err := RealQueryEach(ctx, conn, "SELECT vz_guard_f()", 10*time.Second, func([]string) error { return nil }, func([]string) error { return nil }); err == nil {
		t.Fatal("PG 导出里函数的写应被拒绝")
	}
	r, err := RealRun(ctx, conn, "SELECT COUNT(*) FROM vz_guard_lg", 10*time.Second)
	if err != nil || r.Data[0][0] != "0" {
		t.Fatalf("一行都不该写,实际 %v %v", r.Data, err)
	}
	// 之后同一池子里的写照常(会话已恢复)
	for i := 0; i < 5; i++ {
		if _, err := RealRun(ctx, conn, "INSERT INTO vz_guard_lg VALUES (2)", 10*time.Second); err != nil {
			t.Fatalf("守卫之后的普通写应照常(会话应已恢复),实际 %v", err)
		}
	}
}

func TestReadOnlyGuard_OracleAutonomous_RealOracle(t *testing.T) {
	conn := testOracle(t)
	ctx := context.Background()
	run := func(s string) {
		if _, err := RealRun(ctx, conn, s, 30*time.Second); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	drop := func() {
		_, _ = RealRun(ctx, conn, "DROP FUNCTION vz_grd_auto", 30*time.Second)
		_, _ = RealRun(ctx, conn, "DROP TABLE vz_grd_log", 30*time.Second)
	}
	drop()
	t.Cleanup(drop)
	run("CREATE TABLE vz_grd_log (n NUMBER)")
	run(`CREATE OR REPLACE FUNCTION vz_grd_auto RETURN NUMBER IS PRAGMA AUTONOMOUS_TRANSACTION; BEGIN INSERT INTO vz_grd_log VALUES (1); COMMIT; RETURN 1; END;`)

	var refused *ErrReadOnlyRefused
	if _, err := RealRun(WithReadOnlyGuard(ctx), conn, "SELECT vz_grd_auto() FROM dual", 20*time.Second); !errors.As(err, &refused) {
		t.Fatalf("终端只读守卫下自治事务函数应被拒绝,实际 %v", err)
	}
	err := RealQueryEach(ctx, conn, "SELECT vz_grd_auto() FROM dual", 20*time.Second, func([]string) error { return nil }, func([]string) error { return nil })
	if !errors.As(err, &refused) {
		t.Fatalf("导出里自治事务函数应被拒绝,实际 %v", err)
	}
	if _, err := RealRunAsyncGuarded(conn, "SELECT vz_grd_auto() FROM dual", 20*time.Second, nil, true); !errors.As(err, &refused) {
		t.Fatalf("只读后台任务里自治事务函数应被拒绝,实际 %v", err)
	}
	r, err := RealRun(ctx, conn, "SELECT COUNT(*) FROM vz_grd_log", 10*time.Second)
	if err != nil || r.Data[0][0] != "0" {
		t.Fatalf("一行都不该写,实际 %v %v", r.Data, err)
	}
	// 守卫下的日常查询、ALTER SESSION 照常
	for _, q := range []string{"SELECT COUNT(*) FROM vz_grd_log", "SELECT SYSDATE FROM dual", "ALTER SESSION SET NLS_DATE_FORMAT = 'YYYY-MM-DD'"} {
		if _, err := RealRun(WithReadOnlyGuard(ctx), conn, q, 20*time.Second); err != nil {
			t.Errorf("守卫下 %q 应照常,实际 %v", q, err)
		}
	}
}

// 引擎标签写成 MySQL 的 TiDB(生产上疑似有):SET transaction_read_only 报 1235。守卫跳过,
// 只读用户的查询与所有人的导出照常 —— 不能因为加固把它们全弄挂。
func TestReadOnlyGuard_TiDBLabelledMySQL_StillRuns(t *testing.T) {
	v := os.Getenv("VELA_TEST_TIDB")
	if v == "" {
		t.Skip("未设置 VELA_TEST_TIDB")
	}
	p := strings.SplitN(v, "/", 3)
	hp := strings.SplitN(p[0], ":", 2)
	port, _ := strconv.Atoi(hp[1])
	pass := ""
	if len(p) > 2 {
		pass = p[2]
	}
	for _, engine := range []string{"MySQL 8.0", "TiDB 7.5"} {
		conn := &model.Connection{Name: "tidb", Engine: engine, Host: hp[0], Port: port, Username: p[1], Password: pass}
		if r, err := RealRun(WithReadOnlyGuard(context.Background()), conn, "SELECT 1", 10*time.Second); err != nil || len(r.Data) != 1 {
			t.Errorf("[%s] 守卫下的查询应照常,实际 %v %v", engine, r.Data, err)
		}
		rows := 0
		err := RealQueryEach(context.Background(), conn, "SELECT 1", 10*time.Second, func([]string) error { return nil }, func([]string) error { rows++; return nil })
		if err != nil || rows != 1 {
			t.Errorf("[%s] 导出应照常,实际 %d 行 %v", engine, rows, err)
		}
	}
}
