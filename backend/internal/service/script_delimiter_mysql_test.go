package service

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"aegisdb/internal/model"
	"aegisdb/pkg/sqlutil"
)

// 2026-10 生产:Web 命令行 \i 执行带 DELIMITER $$ 的建分区脚本,第 11 条(第一个 CREATE FUNCTION)
// 报 1064。脚本按 $$ 拆对了,但每条执行时又经过一次批量分句(execCommand),函数体被分号拆碎。
// 这里在真实 MySQL 上走同一条路:整份脚本拆条,每条交给 execCommand,同一个会话。
func TestScript_MySQLDelimiterScriptRunsThroughExecCommand(t *testing.T) {
	v := os.Getenv("AEGISDB_TEST_MYSQL") // host:port/user/password
	if v == "" {
		t.Skip("未设置 AEGISDB_TEST_MYSQL")
	}
	p := strings.SplitN(v, "/", 3)
	hp := strings.SplitN(p[0], ":", 2)
	port, _ := strconv.Atoi(hp[1])
	admin, err := sql.Open("mysql", fmt.Sprintf("%s:%s@tcp(%s)/", p[1], p[2], p[0]))
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("zz_delim_%d", time.Now().UnixNano()%1_000_000)
	if _, err := admin.Exec("CREATE DATABASE " + schema); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec("DROP DATABASE " + schema)

	raw, err := os.ReadFile("../../pkg/sqlutil/testdata/mysql_add_partitions.sql")
	if err != nil {
		t.Fatal(err)
	}
	conn := &model.Connection{Name: "delim", Engine: "MySQL 8.0", Host: hp[0], Port: port, Username: p[1], Password: p[2], Database: schema}
	s := batchSvc()
	ctx, done := s.execSession(context.Background(), conn)
	defer done()
	stmts := sqlutil.SplitStatements(string(raw))
	for i, one := range stmts {
		if res := s.execCommand(ctx, conn, one, 30*time.Second); res.Err != nil {
			t.Fatalf("第 %d/%d 条失败: %s\n%.80q", i+1, len(stmts), res.Output, one)
		}
	}
	var n int
	if err := admin.QueryRow("SELECT COUNT(*) FROM information_schema.routines WHERE routine_schema = ?", schema).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("脚本末尾会清掉建的函数 / 过程,应剩 0 个,实际 %d", n)
	}
}
