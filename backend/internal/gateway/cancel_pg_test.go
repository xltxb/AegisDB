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

// testPostgres 连一个真实 PostgreSQL(VELA_TEST_PG=host:port/user/password),没设就跳过。
func testPostgres(t *testing.T) *model.Connection {
	t.Helper()
	v := os.Getenv("VELA_TEST_PG")
	if v == "" {
		t.Skip("未设置 VELA_TEST_PG=host:port/user/password,跳过真实 PostgreSQL 用例")
	}
	parts := strings.SplitN(v, "/", 3)
	hp := strings.SplitN(parts[0], ":", 2)
	port, _ := strconv.Atoi(hp[1])
	return &model.Connection{Name: "pg-it", Engine: "PostgreSQL 16", Host: hp[0], Port: port,
		Username: parts[1], Password: parts[2], Database: "postgres"}
}

// pgRaw 开一条旁路连接给断言用。测试容器不开 SSL:与 openConn 的降级一致,改用 sslmode=disable。
func pgRaw(conn *model.Connection) (*sql.DB, error) {
	drv, dsn, _ := engineDriver(conn)
	return sql.Open(drv, strings.Replace(dsn, "sslmode=require", "sslmode=disable", 1))
}

// pgRunning 数库上还在跑、且语句里含 marker 的会话(排除自己这条查询)。
func pgRunning(t *testing.T, conn *model.Connection, marker string) int {
	t.Helper()
	db, err := pgRaw(conn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pg_stat_activity
		WHERE state = 'active' AND query LIKE '%' || $1 || '%' AND pid <> pg_backend_pid()`, marker).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// 终端路径(RealRun)在 PostgreSQL 上没有库端时限,超时只能靠 lib/pq 在 context 结束时发取消请求
// (CancelRequest)。钉住:网关按时返回,库端语句确实停了 —— 而不是像 MySQL 驱动那样只断连接。
func TestPostgresRealRun_TimeoutCancelsServerStatement(t *testing.T) {
	conn := testPostgres(t)
	start := time.Now()
	_, err := RealRun(context.Background(), conn, "SELECT pg_sleep(30) /* pgcancel-run */", time.Second)
	if err == nil {
		t.Fatal("超过时限应当报错")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("时限 1 秒,实际 %v 才返回", d)
	}
	time.Sleep(300 * time.Millisecond)
	if n := pgRunning(t, conn, "pgcancel-run"); n != 0 {
		t.Errorf("超时后库端语句应已停下,实际还有 %d 条在跑", n)
	}
	t.Logf("RealRun 超时:%v", err)
}

// 业务查询路径:BEGIN READ ONLY + SET LOCAL statement_timeout,库端自己按时限停下。
func TestPostgresBizQuery_StatementTimeout(t *testing.T) {
	conn := testPostgres(t)
	start := time.Now()
	_, err := RealBizQuery(context.Background(), conn, "SELECT pg_sleep(30) /* pgcancel-biz */", 10, time.Second)
	if err == nil {
		t.Fatal("超过时限应当报错")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("时限 1 秒,实际 %v 才返回", d)
	}
	time.Sleep(300 * time.Millisecond)
	if n := pgRunning(t, conn, "pgcancel-biz"); n != 0 {
		t.Errorf("超时后库端语句应已停下,实际还有 %d 条在跑", n)
	}
	t.Logf("业务查询超时:%v", err)
}

// 只读事务:写入被 PostgreSQL 自己拒绝。
func TestPostgresBizQuery_ReadOnlyTransaction(t *testing.T) {
	conn := testPostgres(t)
	db, err := pgRaw(conn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE IF NOT EXISTS biz_ro_t (n int)"); err != nil {
		t.Fatal(err)
	}
	defer db.Exec("DROP TABLE IF EXISTS biz_ro_t") //nolint:errcheck
	_, err = RealBizQuery(context.Background(), conn, "INSERT INTO biz_ro_t VALUES (1)", 10, 5*time.Second)
	if err == nil || !strings.Contains(err.Error(), "read-only transaction") {
		t.Fatalf("只读事务应拒绝写入,实际 %v", err)
	}
}
