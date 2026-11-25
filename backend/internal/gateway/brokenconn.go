package gateway

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	mysqldrv "github.com/go-sql-driver/mysql"

	"velagateway/internal/model"
	"velagateway/internal/schemadiff"
)

// 连接池里的连接在中途被对端断开(2026-10 生产:TiDB 实例树展开报 invalid connection)。
//
// 连接池会把空闲连接留 10 分钟(见 openAndPing)。实例前面若有 HAProxy、负载均衡或防火墙,
// 它可能先于网关把空闲会话丢掉,而且不发 FIN —— 驱动复用前的存活检查看不出来,请求发出去之后
// 对端才回 RST,驱动报 invalid connection。请求已经发出,驱动(和 database/sql)都不会自己重试。
//
// 只读的元数据读取(库树、列名、对象、元数据同步、结构对比)重跑没有副作用,所以遇到这种断开
// 丢掉这台实例池里的空闲连接、换新连接重试一次。执行、导出不走这里:写语句发出去之后断开,
// 谁也说不清它执行了没有,不能替人重跑。数据查询只在用户的 SQL 发出之前断开时重试
// (见 runBizQueryUnmasked)。

// poolMaxIdle 是每个连接池保留的空闲连接数。
const poolMaxIdle = 3

// isBrokenConn 报告 err 是不是"连接在中途被对端断开" —— 而不是连不上、认证失败或超时。
func isBrokenConn(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, mysqldrv.ErrInvalidConn) || errors.Is(err, driver.ErrBadConn) ||
		errors.Is(err, io.ErrUnexpectedEOF)
}

// dropIdleConns 关掉指向 conn 所在实例(主机:端口)的每个连接池里的空闲连接。
//
// 中间设备断会话是一起断的:池里 3 条空闲连接通常全都失效了,只重试一次的话很可能拿到另一条
// 死连接。关掉空闲连接后,重试必然新拨一条。正在用的连接不受影响;同一台实例的池可能有几个
// (按库区分的 DSN),按地址一起清。
func dropIdleConns(conn *model.Connection) {
	if conn == nil {
		return
	}
	addr := fmt.Sprintf("%s:%d", conn.Host, conn.Port)              // MySQL tcp(h:p)、Oracle h:p
	pgAddr := fmt.Sprintf("host=%s port=%d ", conn.Host, conn.Port) // PostgreSQL 键值 DSN
	dbPoolMu.Lock()
	defer dbPoolMu.Unlock()
	for key, db := range dbPoolCache {
		if strings.Contains(key, addr) || strings.Contains(key, pgAddr) {
			db.SetMaxIdleConns(0) // 立即关掉空闲连接
			db.SetMaxIdleConns(poolMaxIdle)
		}
	}
}

// retryBrokenConn 跑一次只读的元数据读取 fn;连接在中途被断开时,丢掉这台实例池里的空闲连接、
// 换新连接再跑一次。只重试一次:新拨的连接也断,那就是真有问题,原样报出去。
func retryBrokenConn[T any](conn *model.Connection, fn func() (T, error)) (T, error) {
	v, err := fn()
	if !isBrokenConn(err) {
		return v, err
	}
	slog.Warn("元数据读取时连接被对端断开,换新连接重试一次", "conn", connLabel(conn), "err", err)
	dropIdleConns(conn)
	return fn()
}

// SchemaLoadError 是库树加载失败时给人看的提示:按原因说该往哪查。
//
// 从前一律是"无法连接目标数据库(检查网络/主机/端口/账号密码/数据库名)",连接中途被断开
// (invalid connection)也这么说,人就去查账号密码 —— 方向全错(2026-10 生产)。原始错误
// 总是带在后面,排查要靠它。
func SchemaLoadError(err error) string {
	switch {
	case isBrokenConn(err):
		return "读取库表列表时数据库连接中途断开,已自动重试仍失败(可能是数据库重启,或代理 / 负载均衡断开了连接),请稍后再试: " + err.Error()
	case errors.Is(err, context.DeadlineExceeded):
		return "读取库表列表超时(实例库表过多或数据库繁忙),请稍后再试: " + err.Error()
	default:
		return "无法连接目标数据库(检查网络/主机/端口/账号密码/数据库名): " + err.Error()
	}
}

func connLabel(conn *model.Connection) string {
	if conn == nil {
		return ""
	}
	return conn.Name
}

// 以下是走 retryBrokenConn 的只读元数据入口;各自的单次实现(xxxOnce)在原文件里。

// RealSchema 见 realSchemaOnce(schema.go):终端库树。
func RealSchema(conn *model.Connection) ([]SchemaGroup, error) {
	return retryBrokenConn(conn, func() ([]SchemaGroup, error) { return realSchemaOnce(conn) })
}

// TableColumns 见 tableColumnsOnce(columns.go):终端 Tab 补全的列名。
func TableColumns(conn *model.Connection, name string) ([]string, error) {
	return retryBrokenConn(conn, func() ([]string, error) { return tableColumnsOnce(conn, name) })
}

// RealObjects 见 realObjectsOnce(objects.go):库树里的视图 / 过程 / 函数等对象。
func RealObjects(conn *model.Connection, scope string) (*DbObjects, error) {
	return retryBrokenConn(conn, func() (*DbObjects, error) { return realObjectsOnce(conn, scope) })
}

// RealObjectSource 见 realObjectSourceOnce(objects.go):对象的源码 / 建表语句。
func RealObjectSource(conn *model.Connection, scope, typ, name string) (string, error) {
	return retryBrokenConn(conn, func() (string, error) { return realObjectSourceOnce(conn, scope, typ, name) })
}

// RealMetadata 见 realMetadataOnce(metadata.go):元数据同步的全部表与列。
func RealMetadata(conn *model.Connection) ([]MetaTableRow, []MetaColumnRow, error) {
	type meta struct {
		tables []MetaTableRow
		cols   []MetaColumnRow
	}
	m, err := retryBrokenConn(conn, func() (meta, error) {
		t, c, err := realMetadataOnce(conn)
		return meta{t, c}, err
	})
	return m.tables, m.cols, err
}

// RealSchemaSnapshot 见 realSchemaSnapshotOnce(schemasnapshot.go):结构对比的一侧快照。
func RealSchemaSnapshot(conn *model.Connection, database, schema string) (*schemadiff.Snapshot, error) {
	return retryBrokenConn(conn, func() (*schemadiff.Snapshot, error) { return realSchemaSnapshotOnce(conn, database, schema) })
}

// DiffDatabaseNames 见 diffDatabaseNamesOnce(schemasnapshot.go):结构对比的库名下拉。
func DiffDatabaseNames(conn *model.Connection) ([]string, error) {
	return retryBrokenConn(conn, func() ([]string, error) { return diffDatabaseNamesOnce(conn) })
}

// RealDatabaseNames 见 realDatabaseNamesOnce(bizquery.go):数据查询执行前确认实例上的全部库
// (2026-10-27 UAT-TIDB 偶发「无法确认实例 … 上的库列表:invalid connection」)。
func RealDatabaseNames(conn *model.Connection) ([]string, error) {
	return retryBrokenConn(conn, func() ([]string, error) { return realDatabaseNamesOnce(conn) })
}

// DiffSchemaNames 见 diffSchemaNamesOnce(schemasnapshot.go):结构对比的 schema 下拉。
func DiffSchemaNames(conn *model.Connection, database string) ([]string, error) {
	return retryBrokenConn(conn, func() ([]string, error) { return diffSchemaNamesOnce(conn, database) })
}
