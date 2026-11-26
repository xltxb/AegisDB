package gateway

// 终端标签连接的建连:和 dialPool 一样(PostgreSQL 的 sslmode=prefer 模拟、8 秒 ping、Oracle 报错
// 提示),只多一件事 —— 数这条会话上**物理连接**被建立过几次。
//
// 标签连接是一个只允许 1 条连接的 *sql.DB。连接断了(网络闪断、语句超时 / Ctrl+C 后驱动丢弃了它),
// database/sql 会在下一次使用时**悄悄**新建一条,而旧连接上没提交的事务已被数据库回滚、会话变量和
// 临时表也没了。人还以为自己在 BEGIN 里,接下来的 DML 自动提交、ROLLBACK 什么也撤不回(2026-10 审查)。
// 数一数建连次数,变多了就在那条语句的回执里明说"会话已断开并重连"。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"strings"
	"sync/atomic"
	"time"
)

// countingConnector 包一层底层驱动的 Connector:每成功建立一条物理连接就 +1。
type countingConnector struct {
	base driver.Connector
	n    *atomic.Int64
}

func (c countingConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.base.Connect(ctx)
	if err == nil {
		c.n.Add(1)
	}
	return conn, err
}

func (c countingConnector) Driver() driver.Driver { return c.base.Driver() }

// dsnConnector:驱动没有实现 DriverContext 时的退路。
type dsnConnector struct {
	d   driver.Driver
	dsn string
}

func (c dsnConnector) Connect(context.Context) (driver.Conn, error) { return c.d.Open(c.dsn) }
func (c dsnConnector) Driver() driver.Driver                       { return c.d }

func openCounted(drv, dsn string, n *atomic.Int64) (*sql.DB, error) {
	probe, err := sql.Open(drv, dsn) // 只为拿到注册的驱动;不建连
	if err != nil {
		return nil, err
	}
	d := probe.Driver()
	_ = probe.Close()
	var base driver.Connector = dsnConnector{d: d, dsn: dsn}
	if dc, ok := d.(driver.DriverContext); ok {
		if base, err = dc.OpenConnector(dsn); err != nil {
			return nil, err
		}
	}
	db := sql.OpenDB(countingConnector{base: base, n: n})
	// 只此一条,执行期间不因存活时间 / 空闲时间被换掉(见 ExecSession)。
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	db.SetConnMaxIdleTime(0)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, oracleHint(err)
	}
	return db, nil
}

// dialTermDB 开一条标签连接,建连次数记进 n。
func dialTermDB(drv, dsn string, n *atomic.Int64) (*sql.DB, error) {
	db, err := openCounted(drv, dsn, n)
	if err != nil && drv == "postgres" && strings.Contains(dsn, "sslmode=require") && isPgNoSSL(err) {
		n.Store(0)
		db, err = openCounted(drv, strings.Replace(dsn, "sslmode=require", "sslmode=disable", 1), n)
	}
	return db, err
}
