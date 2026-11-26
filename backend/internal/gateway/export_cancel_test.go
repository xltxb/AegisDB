package gateway

import (
	"context"
	"errors"
	"testing"
	"time"
)

// 导出任务被人终止 = 调用方取消了 RealQueryEach 的 context。取消必须让函数立即返回,
// 否则"终止"只是界面上不再显示。目标库上的语句是否结束,本文件只对 MySQL 验证(processlist);
// PostgreSQL 这条只证明客户端立即返回并报取消。

// PostgreSQL:取消后 RealQueryEach 立即返回 context.Canceled(不检查库端语句)。
func TestRealQueryEach_CancelStopsPostgreSQL(t *testing.T) {
	conn := pgTarget(t)

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(300*time.Millisecond, cancel)
	start := time.Now()
	err := RealQueryEach(ctx, conn,
		"SELECT pg_sleep(30)",
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
