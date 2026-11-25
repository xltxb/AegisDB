package gateway

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"aegisdb/internal/model"
)

// 导出任务被人终止 = 调用方取消了 RealQueryEach 的 context。取消必须真的让查询停下:
// 函数立即返回,目标库上的语句也结束 —— 否则"终止"只是界面上不再显示。

// SQLite:流式读取的查询在取消后下一行就停。
//
// 已知边界:glebarez/go-sqlite 只在准备语句时响应 context,逐行 step 时不理会 —— 一条要先算完
// 才出第一行的查询(如对 2 亿行的 COUNT(*))取消后仍会在库里算完。SQLite 只用于开发 / 演示;
// 生产引擎(MySQL 系、PostgreSQL、Oracle)的取消都会让目标库上的语句停下(见本文件与 killquery_test)。
func TestRealQueryEach_CancelStopsSQLite(t *testing.T) {
	file := filepath.Join(t.TempDir(), "c.db")
	db, err := sql.Open("sqlite", file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE t (n INTEGER)"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	conn := &model.Connection{Name: "sqlite-cancel", Engine: "SQLite", Database: file}

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(300*time.Millisecond, cancel)
	start := time.Now()
	err = RealQueryEach(ctx, conn,
		"WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x + 1 FROM c WHERE x < 500000000) SELECT x FROM c",
		10*time.Minute, func([]string) error { return nil }, func([]string) error { return nil })
	if err == nil {
		t.Fatal("被取消的查询应当报错")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("应是取消,实际 %v", err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("取消后应立即返回,实际 %v", d)
	}
}

// MySQL:取消后另开连接发 KILL QUERY,processlist 里那条语句结束。
func TestRealQueryEach_CancelKillsMySQLStatement(t *testing.T) {
	conn := testMySQL(t)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(500*time.Millisecond, cancel)
	start := time.Now()
	err := RealQueryEach(ctx, conn, "SELECT SLEEP(30) AS s /* export-cancel-probe */", 10*time.Minute,
		func([]string) error { return nil }, func([]string) error { return nil })
	if err == nil {
		t.Fatal("被取消的查询应当报错")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("取消后应立即返回,实际 %v", d)
	}
	time.Sleep(300 * time.Millisecond)
	if n := sleepersRunning(t, conn, "export-cancel-probe"); n != 0 {
		t.Errorf("终止后目标库上还有 %d 条语句在跑", n)
	}
}
