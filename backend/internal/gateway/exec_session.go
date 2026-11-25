package gateway

// 一次执行独占的一条数据库连接。
//
// 终端的单条语句走共享的连接池(openConn):快,而且池子每台实例只有 3 条。但有几类执行不该
// 这样跑:
//
//   - **一批语句**(终端粘贴的整段、审批单里的批、审批过的脚本):从池里逐条取连接,第 1 条和
//     第 2 条可能落在两条不同的物理连接上。`SET foreign_key_checks = 0`、`BEGIN … COMMIT`、
//     临时表、会话变量 —— 这些依赖同一个会话的写法,中间就"断"了。
//   - **跑得久的执行**(审批单的 DDL、后台任务):一条跑半小时的 ALTER 占着池里 3 条中的一条,
//     同一台实例上别人的终端就只剩两条可用。
//
// 所以这类执行开一条专用连接:整个执行期间都在它上面,连接池的生命周期设置(最长存活、空闲
// 回收)对它一律不生效,执行中不会被网关这边换掉或断开;执行完 Close,连接当场断开、资源释放,
// 不回到任何池子里等空闲回收。
//
// 保活:三个驱动都用标准库的 net.Dialer 拨号,没有关掉 TCP keep-alive(Go 默认 15 秒一次;
// MySQL 驱动还显式打开),长语句执行期间网络上长时间没有数据,中间的防火墙也不会把它当空闲
// 连接掐掉。
//
// 超时 / 取消仍按每条语句的时限走 RealRun 那一套:到点由驱动取消,MySQL 系另开一条独立的
// 临时连接发 KILL QUERY(见 killquery.go)—— KILL 本来就不从池里取连接,专用连接不影响它。

import (
	"context"
	"database/sql"

	"aegisdb/internal/apimsg"
	"aegisdb/internal/model"
	"aegisdb/internal/osc"
	"aegisdb/pkg/crypto"
)

// ExecSession 是一次执行独占的连接。零值不可用,用 OpenExecSession 打开,用完必须 Close。
type ExecSession struct {
	key string // driver + dsn:只给同一台实例(同一组凭据、同一个库)的执行用
	db  *sql.DB
}

// OpenExecSession 为这台实例开一条专用连接(已经连上并 ping 过)。
func OpenExecSession(conn *model.Connection) (*ExecSession, error) {
	drv, dsn, ok := engineDriver(conn)
	if !ok {
		return nil, apimsg.Errorf("引擎 %q 未配置真实执行(需填写连接凭据)", conn.Engine)
	}
	db, err := dialPool(drv, dsn)
	if err != nil {
		return nil, err
	}
	// 只此一条,执行期间不因存活时间 / 空闲时间被换掉。Oracle 换 schema、MySQL 钉线程号时取的
	// db.Conn 拿到的也是这同一条,整个执行都是同一个会话。
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	db.SetConnMaxIdleTime(0)
	return &ExecSession{key: drv + "\x00" + dsn, db: db}, nil
}

// Close 断开连接、释放资源。重复调用无害。
func (s *ExecSession) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

type execSessionKey struct{}

// WithExecSession 让 ctx 下的执行(RealRun)都落在这条专用连接上。
func WithExecSession(ctx context.Context, s *ExecSession) context.Context {
	return context.WithValue(ctx, execSessionKey{}, s)
}

// HasExecSession 说明 ctx 上是否已经带着一条专用连接(嵌套的执行不再另开)。
func HasExecSession(ctx context.Context) bool {
	s, _ := ctx.Value(execSessionKey{}).(*ExecSession)
	return s != nil
}

// sessionDB 取 ctx 上属于这台实例的专用连接;没有,或者是别的实例的,返回 nil(走连接池)。
func sessionDB(ctx context.Context, conn *model.Connection) *sql.DB {
	s, _ := ctx.Value(execSessionKey{}).(*ExecSession)
	if s == nil {
		return nil
	}
	drv, dsn, ok := engineDriver(conn)
	if !ok || drv+"\x00"+dsn != s.key {
		return nil
	}
	return s.db
}

// OSCTableSize 读目标表的估算行数与数据 + 索引大小(在线变更判定用,见 osc.TableSize)。走连接池:
// 一次 information_schema 的单行查询。
func OSCTableSize(ctx context.Context, conn *model.Connection, schema, table string) (rows, bytes int64, ver [3]int, err error) {
	db, release, err := openConn(conn)
	if err != nil {
		return 0, 0, ver, err
	}
	defer release()
	return osc.TableSize(ctx, db, schema, table)
}

// OpenOSCTarget 为一次在线变更给出连目标库的两样东西:一个专用的 *sql.DB(引擎在上面开拷贝、重放、
// 加锁用的几个会话,不占终端共用的池子)和以复制客户端身份读 binlog 的连接参数。用完关掉 db。
func OpenOSCTarget(conn *model.Connection) (*sql.DB, osc.MySQLSource, error) {
	if engineFamily(conn.Engine) != familyMySQL {
		return nil, osc.MySQLSource{}, apimsg.Errorf("引擎 %q 不支持在线变更", conn.Engine)
	}
	drv, dsn, ok := engineDriver(conn)
	if !ok {
		return nil, osc.MySQLSource{}, apimsg.Errorf("实例 %s 未配置真实执行凭据", conn.Name)
	}
	pw, err := crypto.DecryptSecret(conn.Password)
	if err != nil {
		return nil, osc.MySQLSource{}, err
	}
	db, err := dialPool(drv, dsn)
	if err != nil {
		return nil, osc.MySQLSource{}, err
	}
	// 迁移跑几十分钟到几小时:连接不因存活 / 空闲时间被换掉;数量由引擎自己控制(拷贝、重放、
	// cut-over 各一条,加上限流探测)
	db.SetMaxOpenConns(0)
	db.SetConnMaxLifetime(0)
	db.SetConnMaxIdleTime(0)
	return db, osc.MySQLSource{Host: conn.Host, Port: uint16(conn.Port), User: conn.Username, Password: pw}, nil
}

// OpenOSCReplica 打开实例所配从库的一个小连接池,给在线变更读复制延迟用;没配从库返回 (nil, nil)。
// 端口 / 账号 / 密码留空沿用主库(见 ReplicaOf)。
func OpenOSCReplica(conn *model.Connection) (*sql.DB, error) {
	r := ReplicaOf(conn)
	if r == nil {
		return nil, nil
	}
	drv, dsn, ok := engineDriver(r)
	if !ok {
		return nil, apimsg.Errorf("从库 %s 未配置真实执行凭据", r.Host)
	}
	db, err := dialPool(drv, dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(2)
	return db, nil
}
