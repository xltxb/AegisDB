package gateway

// 超时 / 取消时,让**目标库**真的停下来。
//
// 网关对每条语句都有执行时限,终端里也能按 Ctrl+C 取消。两者走的都是 context:到时间
// 或被取消,context 结束,驱动收到信号。但各驱动收到信号后做的事不一样:
//
//   - lib/pq(PostgreSQL 系):另开连接发 CancelRequest,库端语句被取消。
//   - go-ora(Oracle):发 break。
//   - go-sql-driver/mysql(MySQL / MariaDB / TiDB / PolarDB):**只关掉本端的网络连接**。
//     库端那个线程并不知道,会继续把语句跑完 —— 一条慢查询在网关这边"已超时",
//     在库上却还在吃 CPU 和 IO,直到它想往一个已经断开的连接写结果时才停。
//
// 所以 MySQL 系要自己补上:执行前把语句钉在一条独占连接上并记下它的 CONNECTION_ID(),
// context 一结束,另开一条**独立的**临时连接发 KILL QUERY <id>。
//
// 用独立连接而不是连接池:池子只有 3 条,执行中的那条还被占着,池满时 KILL 会排队等到
// 超时 —— 恰好在最需要它的时候发不出去。
//
// 竞态:语句正常跑完、连接即将还回池子的那一刻,如果 KILL 恰好发出,它会打到这条连接
// 接下来执行的**别人的**语句上。所以 stop() 要等进行中的 KILL 做完,并且调用方必须
// 先 stop() 再把连接还回池子(defer 的顺序:先 defer sc.Close(),再 defer stop())。

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"velagateway/internal/apimsg"
	"velagateway/internal/model"
)

// killTimeout 发 KILL 的预算:连上、发出、拿到回执。
const killTimeout = 5 * time.Second

// killable 报告这台实例是否需要网关自己发 KILL(即 MySQL 协议系)。
func killable(conn *model.Connection) bool {
	return conn != nil && engineFamily(conn.Engine) == familyMySQL
}

// mysqlThreadID 读出这条独占连接在库端的线程号。
func mysqlThreadID(ctx context.Context, sc *sql.Conn) (int64, error) {
	var id int64
	if err := sc.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&id); err != nil {
		return 0, err
	}
	return id, nil
}

// killStatement 是 KILL 的语句。TiDB 用 KILL TIDB QUERY:6.1 以前的 TiDB 不接受
// 不带 TIDB 的 KILL(除非开了 compatible-kill-query),6.1 以后两种都接受。
func killStatement(engine string, id int64) string {
	if strings.Contains(strings.ToLower(engine), "tidb") {
		return fmt.Sprintf("KILL TIDB QUERY %d", id)
	}
	return fmt.Sprintf("KILL QUERY %d", id)
}

// killQueryFn 发出 KILL。包级变量,测试里可以替换成记录调用的假实现。
var killQueryFn = killQuery

func killQuery(conn *model.Connection, id int64) error {
	drv, dsn, ok := engineDriver(conn)
	if !ok {
		return apimsg.Errorf("未配置真实执行凭据")
	}
	db, err := sql.Open(drv, dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), killTimeout)
	defer cancel()
	_, err = db.ExecContext(ctx, killStatement(conn.Engine, id))
	return err
}

// watchKill 在 ctx 结束时对线程 id 发 KILL。返回的 stop 必须在语句结束后、连接还回池子
// 之前调用:它会等正在进行的 KILL 做完,之后不会再发。
func watchKill(ctx context.Context, conn *model.Connection, id int64) (stop func()) {
	var (
		mu       sync.Mutex
		finished bool
		done     = make(chan struct{})
	)
	go func() {
		select {
		case <-ctx.Done():
			mu.Lock()
			defer mu.Unlock()
			if finished {
				return
			}
			if err := killQueryFn(conn, id); err != nil {
				// 线程已经结束("Unknown thread id")是正常的:语句恰好在这时跑完了。
				// 其余失败要留痕 —— 那意味着库端语句可能还在跑。
				slog.Warn("超时/取消后终止目标库语句失败", "instance", conn.Name, "thread", id, "err", err)
			} else {
				slog.Info("已终止目标库上的语句", "instance", conn.Name, "thread", id, "reason", ctx.Err())
			}
		case <-done:
		}
	}()
	return func() {
		mu.Lock()
		finished = true
		mu.Unlock()
		close(done)
	}
}

// pinForKill 为 MySQL 系实例取一条独占连接、记下线程号、开始看守。其它引擎原样返回 db,
// 靠驱动自己的取消机制。
//
// 调用方:
//
//	runner, sc, stop, err := pinForKill(ctx, db, conn)
//	if sc != nil { defer sc.Close() }   // 先登记 Close
//	defer stop()                        // 再登记 stop —— 它先执行
func pinForKill(ctx context.Context, db *sql.DB, conn *model.Connection) (sqlRunner, *sql.Conn, func(), error) {
	if !killable(conn) {
		return db, nil, func() {}, nil
	}
	sc, err := db.Conn(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	id, err := mysqlThreadID(ctx, sc)
	if err != nil {
		sc.Close()
		return nil, nil, nil, err
	}
	return sc, sc, watchKill(ctx, conn, id), nil
}
