package osc

import (
	"context"
	"database/sql"
	"strconv"
	"time"
)

// ReplicaLagFunc 读一次从库的复制延迟。running=false 表示复制线程没在运行(延迟未知)。
type ReplicaLagFunc func(ctx context.Context) (lag time.Duration, running bool, err error)

// ReplicaLagOf 对着一台从库读复制延迟。8.0.22 起是 SHOW REPLICA STATUS / Seconds_Behind_Source,
// 更早的版本只认 SHOW SLAVE STATUS / Seconds_Behind_Master —— 先试新的再退回旧的。
//
// 结果集为空(这台根本不是从库)和延迟为 NULL(复制线程停了)都算 running=false:
// 两种情况下都不知道从库落后多少。
func ReplicaLagOf(db *sql.DB) ReplicaLagFunc {
	return func(ctx context.Context) (time.Duration, bool, error) {
		var lastErr error
		for _, q := range [][2]string{
			{"SHOW REPLICA STATUS", "Seconds_Behind_Source"},
			{"SHOW SLAVE STATUS", "Seconds_Behind_Master"},
		} {
			lag, running, err := readLag(ctx, db, q[0], q[1])
			if err == nil {
				return lag, running, nil
			}
			lastErr = err
		}
		return 0, false, lastErr
	}
}

func readLag(ctx context.Context, db *sql.DB, query, column string) (time.Duration, bool, error) {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return 0, false, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return 0, false, err
	}
	if !rows.Next() {
		return 0, false, rows.Err() // 不是从库
	}
	vals := make([]sql.NullString, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return 0, false, err
	}
	for i, c := range cols {
		if c != column {
			continue
		}
		if !vals[i].Valid {
			return 0, false, nil // NULL:复制线程没在运行
		}
		n, err := strconv.ParseInt(vals[i].String, 10, 64)
		if err != nil {
			return 0, false, err
		}
		return time.Duration(n) * time.Second, true, nil
	}
	return 0, false, nil // 结果里没有这一列:按延迟未知处理
}

// AuroraReplicaLagOf 读 Aurora MySQL 集群里只读副本的延迟。Aurora 的副本是存储层复制,不走 binlog:
// 副本上 SHOW REPLICA STATUS 是空的(会被当成"复制未在运行"一直限流),延迟要在**写节点**的
// information_schema.replica_host_status 里读 —— 每个副本一行,写节点自己那行 SESSION_ID 是
// MASTER_SESSION_ID。集群里没有副本就没有延迟可言,算正常。
func AuroraReplicaLagOf(primary *sql.DB) ReplicaLagFunc {
	return func(ctx context.Context) (time.Duration, bool, error) {
		var ms sql.NullFloat64
		err := primary.QueryRowContext(ctx, `SELECT MAX(REPLICA_LAG_IN_MILLISECONDS) FROM information_schema.replica_host_status
			WHERE SESSION_ID <> 'MASTER_SESSION_ID'`).Scan(&ms)
		if err != nil {
			return 0, false, err
		}
		if !ms.Valid {
			return 0, true, nil // 没有副本
		}
		return time.Duration(ms.Float64 * float64(time.Millisecond)), true, nil
	}
}
