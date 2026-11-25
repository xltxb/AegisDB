package gateway

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"velagateway/internal/model"
)

// 专用连接:整个执行同一个会话;执行完当场断开;超时取消照样生效。

func testMySQLConn(t *testing.T) (*model.Connection, *sql.DB) {
	t.Helper()
	v := os.Getenv("VELA_TEST_MYSQL") // host:port/user/password
	if v == "" {
		t.Skip("未设置 VELA_TEST_MYSQL")
	}
	p := strings.SplitN(v, "/", 3)
	hp := strings.SplitN(p[0], ":", 2)
	port, _ := strconv.Atoi(hp[1])
	admin, err := sql.Open("mysql", fmt.Sprintf("%s:%s@tcp(%s)/", p[1], p[2], p[0]))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close() })
	return &model.Connection{Name: "sess", Engine: "MySQL 8.0", Host: hp[0], Port: port, Username: p[1], Password: p[2], Database: "mysql"}, admin
}

func firstCell(t *testing.T, r ExecResult) string {
	t.Helper()
	if len(r.Data) == 0 || len(r.Data[0]) == 0 {
		t.Fatalf("没有结果: %+v", r)
	}
	return r.Data[0][0]
}

func TestExecSession_OneSessionThenDisconnected(t *testing.T) {
	conn, admin := testMySQLConn(t)
	sess, err := OpenExecSession(conn)
	if err != nil {
		t.Fatal(err)
	}
	if n := sess.db.Stats().MaxOpenConnections; n != 1 {
		t.Fatalf("专用连接只能有一条,实际上限 %d", n)
	}
	ctx := WithExecSession(context.Background(), sess)

	// 三条语句各走一次 RealRun(与批处理逐条执行一样),会话变量与线程号都得连得上
	if _, err := RealRun(ctx, conn, "SET @vela_sess = 42", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	a, err := RealRun(ctx, conn, "SELECT CONNECTION_ID()", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	b, err := RealRun(ctx, conn, "SELECT @vela_sess, CONNECTION_ID()", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	id := firstCell(t, a)
	if got := firstCell(t, b); got != "42" {
		t.Errorf("同一个会话里设的变量应当读得到,实际 %q", got)
	}
	if b.Data[0][1] != id {
		t.Errorf("整个执行应在同一条连接上:%s → %s", id, b.Data[0][1])
	}

	// 执行完 Close:库端这个线程当场没了(不回池子等空闲回收)
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		var n int
		if err := admin.QueryRow("SELECT COUNT(*) FROM information_schema.processlist WHERE id = ?", id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Close 之后线程 %s 仍在库上 —— 连接没有断开", id)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestExecSession_TimeoutStillKillsAndSessionStaysUsable(t *testing.T) {
	conn, _ := testMySQLConn(t)
	sess, err := OpenExecSession(conn)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	ctx := WithExecSession(context.Background(), sess)

	start := time.Now()
	if _, err := RealRun(ctx, conn, "SELECT SLEEP(5)", time.Second); err == nil {
		t.Fatal("1 秒时限的 SLEEP(5) 应当超时")
	}
	if el := time.Since(start); el > 4*time.Second {
		t.Errorf("超时应当按时生效,实际等了 %v", el)
	}
	// 超时之后这次执行后面的语句照样能跑
	r, err := RealRun(ctx, conn, "SELECT 7", 5*time.Second)
	if err != nil || firstCell(t, r) != "7" {
		t.Fatalf("超时之后专用连接应仍可用: %v %+v", err, r)
	}
}

func TestExecSession_OtherInstanceFallsBackToPool(t *testing.T) {
	conn, _ := testMySQLConn(t)
	sess, err := OpenExecSession(conn)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	ctx := WithExecSession(context.Background(), sess)
	other := *conn
	other.Database = "information_schema" // 不同的库 = 不同的 DSN,不能借用这条专用连接
	if sessionDB(ctx, &other) != nil {
		t.Error("别的实例 / 库的执行不能落在这条专用连接上")
	}
	if sessionDB(ctx, conn) == nil {
		t.Error("同一台实例的执行应当用这条专用连接")
	}
}

// 耗时分段:库端慢就落在 QueryMs,取连接那段不该被算进去。
func TestRealRun_PhaseTimings(t *testing.T) {
	conn, _ := testMySQLConn(t)
	if _, err := RealRun(context.Background(), conn, "SELECT 1", 5*time.Second); err != nil { // 先把池子建好
		t.Fatal(err)
	}
	start := time.Now()
	r, err := RealRun(context.Background(), conn, "SELECT SLEEP(1)", 5*time.Second)
	took := int(time.Since(start).Milliseconds())
	if err != nil {
		t.Fatal(err)
	}
	if r.QueryMs < 900 {
		t.Errorf("SLEEP(1) 的时间应算在库端执行(QueryMs),实际 %+v", r)
	}
	if r.ConnMs > 500 {
		t.Errorf("池子里已有连接,取连接不该花这么久: %dms", r.ConnMs)
	}
	if r.ConnMs+r.QueryMs+r.FetchMs > took+5 {
		t.Errorf("分段之和不该超过总耗时 %dms: %+v", took, r)
	}
}
