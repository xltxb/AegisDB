package gateway

import (
	"context"
	"strings"
	"time"

	"aegisdb/internal/model"
)

// replicaProbeTimeout 探从库的预算。比巡检的 pingTimeout 短得多:探测在每次数据查询的路径上,
// 从库挂了的时候,多等的每一秒都是业务方在等。
const replicaProbeTimeout = 3 * time.Second

// ReplicaOf 返回一个指向该数据源从库的连接副本;没配从库返回 nil。库名、引擎、目标 schema 都沿用主库;端口、账号、密码留空也沿用主库。
//
// 返回的是副本:调用方改它不会改到主库那份,连接池也按 DSN 各自独立。
func ReplicaOf(conn *model.Connection) *model.Connection {
	if conn == nil || conn.ReplicaHost == "" {
		return nil
	}
	r := *conn
	r.Host = conn.ReplicaHost
	if conn.ReplicaPort > 0 {
		r.Port = conn.ReplicaPort
	}
	if conn.ReplicaUsername != "" {
		r.Username = conn.ReplicaUsername
		// 换了账号却没另给密码:沿用主库密码(同一套账号体系常见)。
		if conn.ReplicaPassword != "" {
			r.Password = conn.ReplicaPassword
		}
	}
	return &r
}

// ReplicaIsReadOnlyAccount:配的"从库"与主库是同一个地址,只是换了账号 —— 没有主从之分的架构
// (TiDB 等)用它把数据查询限定在一个只读账号上(2026-10-29)。这种配置连不上时不能回落主库:
// 回落用的是主库账号,只读账号就白配了。
func ReplicaIsReadOnlyAccount(conn *model.Connection) bool {
	return conn != nil && conn.ReplicaHost != "" && strings.EqualFold(conn.ReplicaHost, conn.Host) &&
		(conn.ReplicaPort == 0 || conn.ReplicaPort == conn.Port)
}

// ProbeReplica 在短时限内确认从库连得上。
func ProbeReplica(r *model.Connection) error {
	db, release, err := openConn(r)
	if err != nil {
		return err
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), replicaProbeTimeout)
	defer cancel()
	return db.PingContext(ctx)
}
