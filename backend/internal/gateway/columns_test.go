package gateway

import (
	"database/sql"
	"os"
	"strconv"
	"strings"
	"testing"

	"aegisdb/internal/model"
)

func TestSplitQualified(t *testing.T) {
	for in, want := range map[string][2]string{
		"orders":             {"", "orders"},
		"shop.orders":        {"shop", "orders"},
		"`shop`.`orders`":    {"shop", "orders"},
		`"public"."Orders"`:  {"public", "Orders"},
		" db.订单 ":            {"db", "订单"},
	} {
		q, tb := SplitQualified(in)
		if q != want[0] || tb != want[1] {
			t.Errorf("SplitQualified(%q) = %q, %q; want %q, %q", in, q, tb, want[0], want[1])
		}
	}
}

// tcMySQL 连真实 MySQL(AEGISDB_TEST_MYSQL=host:port/user/password),没设就跳过。
// 不叫 testMySQL:别的分支(killquery_test.go)已有同名函数,合并后会重复定义。
func tcMySQL(t *testing.T) *model.Connection {
	t.Helper()
	v := os.Getenv("AEGISDB_TEST_MYSQL")
	if v == "" {
		t.Skip("未设置 AEGISDB_TEST_MYSQL,跳过")
	}
	parts := strings.SplitN(v, "/", 3)
	hp := strings.SplitN(parts[0], ":", 2)
	port, _ := strconv.Atoi(hp[1])
	return &model.Connection{Name: "mysql-it", Engine: "MySQL 8.0", Host: hp[0], Port: port, Username: parts[1], Password: parts[2], Database: "mysql"}
}

func rawExec(t *testing.T, conn *model.Connection, stmts ...string) {
	t.Helper()
	drv, dsn, _ := engineDriver(conn)
	db, err := sql.Open(drv, strings.Replace(dsn, "sslmode=require", "sslmode=disable", 1))
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

// MySQL:同名表在两个库里各有一张,只返回会话所在库(或限定名指定库)的列,保留大小写。
func TestTableColumns_MySQLFiltersByDatabase(t *testing.T) {
	conn := tcMySQL(t)
	rawExec(t, conn,
		"CREATE DATABASE IF NOT EXISTS tc_a", "CREATE DATABASE IF NOT EXISTS tc_b",
		"DROP TABLE IF EXISTS tc_a.orders", "DROP TABLE IF EXISTS tc_b.orders",
		"CREATE TABLE tc_a.orders (id INT, CustomerName VARCHAR(10))",
		"CREATE TABLE tc_b.orders (id INT, secret VARCHAR(10))")
	c := *conn
	c.Database = "tc_a"
	cols, err := TableColumns(&c, "orders")
	if err != nil || strings.Join(cols, ",") != "id,CustomerName" {
		t.Errorf("会话在 tc_a:应是 [id CustomerName],实际 %v %v", cols, err)
	}
	cols, err = TableColumns(&c, "tc_b.orders")
	if err != nil || strings.Join(cols, ",") != "id,secret" {
		t.Errorf("限定名 tc_b.orders:应是 [id secret],实际 %v %v", cols, err)
	}
}

// PostgreSQL:不加引号的名字折成小写;schema 限定。
func TestTableColumns_Postgres(t *testing.T) {
	v := os.Getenv("AEGISDB_TEST_PG")
	if v == "" {
		t.Skip("未设置 AEGISDB_TEST_PG,跳过")
	}
	parts := strings.SplitN(v, "/", 3)
	hp := strings.SplitN(parts[0], ":", 2)
	port, _ := strconv.Atoi(hp[1])
	conn := &model.Connection{Name: "pg-it", Engine: "PostgreSQL 16", Host: hp[0], Port: port, Username: parts[1], Password: parts[2], Database: "postgres"}
	rawExec(t, conn, "CREATE SCHEMA IF NOT EXISTS tc_s", "DROP TABLE IF EXISTS tc_s.orders", "CREATE TABLE tc_s.orders (id int, amount numeric)")
	cols, err := TableColumns(conn, "tc_s.Orders")
	if err != nil || strings.Join(cols, ",") != "id,amount" {
		t.Errorf("tc_s.Orders:应是 [id amount],实际 %v %v", cols, err)
	}
}

// Oracle:不加引号的名字折成大写;owner 限定,不限定时是当前 schema。
func TestTableColumns_Oracle(t *testing.T) {
	v := os.Getenv("AEGISDB_TEST_ORACLE")
	if v == "" {
		t.Skip("未设置 AEGISDB_TEST_ORACLE=host:port/user/password/service,跳过")
	}
	parts := strings.SplitN(v, "/", 4)
	hp := strings.SplitN(parts[0], ":", 2)
	port, _ := strconv.Atoi(hp[1])
	conn := &model.Connection{Name: "ora-it", Engine: "Oracle 23ai", Host: hp[0], Port: port, Username: parts[1], Password: parts[2], Database: parts[3]}
	drv, dsn, _ := engineDriver(conn)
	db, err := sql.Open(drv, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, _ = db.Exec("DROP TABLE tc_orders")
	if _, err := db.Exec("CREATE TABLE tc_orders (id NUMBER, amount NUMBER)"); err != nil {
		t.Fatal(err)
	}
	defer db.Exec("DROP TABLE tc_orders") //nolint:errcheck
	cols, err := TableColumns(conn, "tc_orders")
	if err != nil || strings.Join(cols, ",") != "ID,AMOUNT" {
		t.Errorf("tc_orders:应是 [ID AMOUNT],实际 %v %v", cols, err)
	}
	cols, err = TableColumns(conn, strings.ToLower(parts[1])+".tc_orders")
	if err != nil || len(cols) != 2 {
		t.Errorf("owner 限定:应取到 2 列,实际 %v %v", cols, err)
	}
}
