package service

// 每请求的"目标数据库"该怎么落到连接上 —— 全网关唯一入口。
//
// 这条规则原本散在十来处 `conn.Database = database` 里,每处都默认"Database 就是
// 一个可以切换的库名"。对 Oracle 不成立:那个字段是 SERVICE NAME,而对象树的第
// 二层是 OWNER(all_tables 查出来的 G04 这类)。展开属主节点时属主被回传成
// "数据库"、被写进服务名,go-ora 于是拿属主去要服务:
//
//	(CONNECT_DATA=(SERVICE_NAME=G04)…) * establish * G04 * 12514
//	TNS-12514: listener does not currently know of service requested
//
// 收敛成一个函数,是因为分散的同一条规则只会以"下次又有人新写一处"的方式复发。

import (
	"strings"

	"velagateway/internal/gateway"
	"velagateway/internal/model"
)

// applyTargetDatabase points conn at the database this request targets, when the
// engine has a switchable one. Empty input is a no-op (keep the connection's
// own), never a clear.
func applyTargetDatabase(conn *model.Connection, database string) {
	if conn == nil {
		return
	}
	if database = strings.TrimSpace(database); database == "" {
		return
	}
	// Oracle: the caller's "database" is an owner, and the field it would
	// overwrite is the service name. Keep what the instance was configured with;
	// the owner already travels separately as the query scope.
	if !gateway.TargetDatabaseSwitchable(conn.Engine) {
		return
	}
	conn.Database = database
}
