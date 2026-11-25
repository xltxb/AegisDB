package gateway

// 超时 / 取消后,目标库上的语句要真的停下来(见 killquery.go)。
//
// 两层:
//   - 看守逻辑:不连库,用假的 KILL 记录调用,钉住"该发时发、不该发时不发、还连接前等 KILL 做完"。
//   - 真实 MySQL:设置了 VELA_TEST_MYSQL(host:port/user/password)才跑。起一条 SLEEP(30),
//     2 秒超时后到 processlist 里确认它已经不在了。没有这个环境变量时跳过 ——
//     CI 里没有 MySQL,但这一条是这个修复唯一能证明"库端真的停了"的地方,本地要跑一次。

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"velagateway/internal/model"
)

type fakeKill struct {
	mu    sync.Mutex
	calls []int64
	block chan struct{} // 非 nil 时,KILL 卡在这里直到被关闭
}

func (f *fakeKill) install(t *testing.T) {
	t.Helper()
	orig := killQueryFn
	killQueryFn = func(_ *model.Connection, id int64) error {
		f.mu.Lock()
		f.calls = append(f.calls, id)
		b := f.block
		f.mu.Unlock()
		if b != nil {
			<-b
		}
		return nil
	}
	t.Cleanup(func() { killQueryFn = orig })
}

func (f *fakeKill) n() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.calls) }

var mysqlConn = &model.Connection{Name: "t", Engine: "MySQL 8.0"}

func TestWatchKill_KillsOnCancel(t *testing.T) {
	f := &fakeKill{}
	f.install(t)
	ctx, cancel := context.WithCancel(context.Background())
	stop := watchKill(ctx, mysqlConn, 42)
	cancel()
	deadline := time.Now().Add(2 * time.Second)
	for f.n() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	stop()
	if f.n() != 1 || f.calls[0] != 42 {
		t.Fatalf("取消后应对线程 42 发一次 KILL,实际 %v", f.calls)
	}
}

func TestWatchKill_NoKillAfterNormalFinish(t *testing.T) {
	f := &fakeKill{}
	f.install(t)
	ctx, cancel := context.WithCancel(context.Background())
	stop := watchKill(ctx, mysqlConn, 7)
	stop()   // 语句正常跑完
	cancel() // 之后 context 才结束(RealRun 里 defer cancel 在 stop 之后执行)
	time.Sleep(50 * time.Millisecond)
	if f.n() != 0 {
		t.Fatalf("语句跑完后不该再发 KILL —— 那条连接回到池子会被别人用,实际 %v", f.calls)
	}
}

// stop 必须等进行中的 KILL 做完才返回:否则连接先回了池子,KILL 打到别人的语句上。
func TestWatchKill_StopWaitsForInFlightKill(t *testing.T) {
	f := &fakeKill{block: make(chan struct{})}
	f.install(t)
	ctx, cancel := context.WithCancel(context.Background())
	stop := watchKill(ctx, mysqlConn, 9)
	cancel()
	for f.n() == 0 {
		time.Sleep(2 * time.Millisecond)
	}
	var returned atomic.Bool
	go func() { stop(); returned.Store(true) }()
	time.Sleep(50 * time.Millisecond)
	if returned.Load() {
		t.Fatal("KILL 还没做完,stop 就返回了")
	}
	close(f.block)
	deadline := time.Now().Add(time.Second)
	for !returned.Load() && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if !returned.Load() {
		t.Fatal("KILL 做完后 stop 应当返回")
	}
}

func TestKillStatement(t *testing.T) {
	if got := killStatement("MySQL 8.0", 12); got != "KILL QUERY 12" {
		t.Errorf("MySQL: %q", got)
	}
	if got := killStatement("TiDB 7.5", 12); got != "KILL TIDB QUERY 12" {
		t.Errorf("TiDB: %q", got)
	}
	if !killable(&model.Connection{Engine: "PolarDB MySQL"}) || killable(&model.Connection{Engine: "PostgreSQL 15"}) ||
		killable(&model.Connection{Engine: "Oracle 19c"}) || killable(&model.Connection{Engine: "SQLite"}) {
		t.Error("只有 MySQL 协议系需要网关自己发 KILL")
	}
}

// ---------------------------------------------------------------- 真实 MySQL

// testMySQL 从 VELA_TEST_MYSQL=host:port/user/password 取一台可用的 MySQL。
func testMySQL(t *testing.T) *model.Connection {
	t.Helper()
	v := os.Getenv("VELA_TEST_MYSQL")
	if v == "" {
		t.Skip("未设置 VELA_TEST_MYSQL=host:port/user/password,跳过真实 MySQL 用例")
	}
	parts := strings.SplitN(v, "/", 3)
	hp := strings.SplitN(parts[0], ":", 2)
	port, _ := strconv.Atoi(hp[1])
	return &model.Connection{Name: "mysql-it", Engine: "MySQL 8.0", Host: hp[0], Port: port,
		Username: parts[1], Password: parts[2], Database: "mysql"}
}

// sleepersRunning 数一下库上还在跑的 SLEEP(<marker>) 语句。
func sleepersRunning(t *testing.T, conn *model.Connection, marker string) int {
	t.Helper()
	drv, dsn, _ := engineDriver(conn)
	db, err := sql.Open(drv, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	err = db.QueryRow(`SELECT COUNT(*) FROM information_schema.PROCESSLIST
		WHERE COMMAND = 'Query' AND INFO LIKE ? AND INFO NOT LIKE '%PROCESSLIST%'`, "%"+marker+"%").Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func waitGone(t *testing.T, conn *model.Connection, marker string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if sleepersRunning(t, conn, marker) == 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("超时 / 取消 3 秒后,库上的 %s 仍在执行 —— 目标库没有被要求停下", marker)
}

func TestMySQLTimeoutKillsServerSideQuery(t *testing.T) {
	conn := testMySQL(t)
	marker := fmt.Sprintf("/* vela-timeout-%d */", time.Now().UnixNano())
	res := (&Executor{}).Run(context.Background(), conn, "SELECT SLEEP(30) "+marker, 2*time.Second)
	if res.OutputRef == nil || res.OutputRef.Code != model.OutExecTimeout {
		t.Fatalf("应当报执行超时,实际 %+v / %q", res.OutputRef, res.Output)
	}
	waitGone(t, conn, marker)
}

func TestMySQLCancelKillsServerSideQuery(t *testing.T) {
	conn := testMySQL(t)
	marker := fmt.Sprintf("/* vela-cancel-%d */", time.Now().UnixNano())
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(1500*time.Millisecond, cancel) // 相当于 1.5 秒后按下 Ctrl+C
	res := (&Executor{}).Run(ctx, conn, "SELECT SLEEP(30) "+marker, 60*time.Second)
	if res.OutputRef == nil || res.OutputRef.Code != model.OutExecCancelled {
		t.Fatalf("应当报已取消,实际 %+v / %q", res.OutputRef, res.Output)
	}
	waitGone(t, conn, marker)
}

// 连接池复用:KILL 之后,同一个池子上的下一条语句照常执行,不会被误杀。
func TestMySQLNextQueryAfterKillIsUnaffected(t *testing.T) {
	conn := testMySQL(t)
	_ = (&Executor{}).Run(context.Background(), conn, "SELECT SLEEP(10) /* vela-prev */", time.Second)
	for i := 0; i < 5; i++ {
		res := (&Executor{}).Run(context.Background(), conn, "SELECT 1 + 1", 5*time.Second)
		if res.Err != nil {
			t.Fatalf("第 %d 条后续语句失败: %v", i+1, res.Err)
		}
	}
}
