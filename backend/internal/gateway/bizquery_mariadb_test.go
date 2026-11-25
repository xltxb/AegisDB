package gateway

import (
	"context"
	"database/sql"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"velagateway/internal/model"
)

// testMariaDB 连一个真实 MariaDB(VELA_TEST_MARIADB=host:port/user/password),没设就跳过。
func testMariaDB(t *testing.T) *model.Connection {
	t.Helper()
	v := os.Getenv("VELA_TEST_MARIADB")
	if v == "" {
		t.Skip("未设置 VELA_TEST_MARIADB=host:port/user/password,跳过真实 MariaDB 用例")
	}
	parts := strings.SplitN(v, "/", 3)
	hp := strings.SplitN(parts[0], ":", 2)
	port, _ := strconv.Atoi(hp[1])
	return &model.Connection{Name: "mariadb-it", Engine: "MariaDB 11", Host: hp[0], Port: port,
		Username: parts[1], Password: parts[2], Database: "mysql"}
}

// MariaDB 的库端时限叫 max_statement_time,超时报 1969。service.bizTimedOut 靠报错原文里的
// "max_statement_time exceeded" 认出它 —— 这里拿真库的原文把那个字符串钉住。
const mariaTimeoutText = "max_statement_time exceeded"

func TestMariaDBStatementTimeoutText(t *testing.T) {
	conn := testMariaDB(t)
	drv, dsn, _ := engineDriver(conn)
	db, err := sql.Open(drv, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	c, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.ExecContext(context.Background(), "SET SESSION max_statement_time = 0.5"); err != nil {
		t.Fatal(err)
	}
	_, err = c.ExecContext(context.Background(), "SELECT SUM(a.seq * b.seq) FROM seq_1_to_100000 a, seq_1_to_100000 b")
	if err == nil || !strings.Contains(err.Error(), mariaTimeoutText) {
		t.Fatalf("MariaDB 超时原文应含 %q,实际 %v", mariaTimeoutText, err)
	}
	t.Logf("MariaDB 原文:%v", err)
}

// 端到端:业务查询在 MariaDB 上设的是 max_statement_time(不是 MySQL 的 max_execution_time),
// 超时后库端语句停下,错误能被认成超时。
func TestMariaDBBizQuery_TimeoutStopsServer(t *testing.T) {
	conn := testMariaDB(t)
	start := time.Now()
	_, err := RealBizQuery(context.Background(), conn,
		"SELECT SUM(a.seq * b.seq) FROM seq_1_to_100000 a, seq_1_to_100000 b", 10, time.Second)
	if err == nil {
		t.Fatal("超过时限应当报错")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("时限 1 秒,实际 %v 才返回", d)
	}
	if !strings.Contains(err.Error(), mariaTimeoutText) && !strings.Contains(err.Error(), context.DeadlineExceeded.Error()) {
		t.Errorf("应是库端时限或网关超时,实际 %v", err)
	}
	t.Logf("耗时 %v,错误:%v", time.Since(start), err)
}
