package osc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
)

// replicaAddr 是一个从库的地址 —— 由**主库自己报出来**的那一个。
//
// 凭据不在这里:从库借主库那一套账号密码(ADR 0011)。复制拓扑里两端账号通用是
// 常态,而让人在网关里单独登记一遍从库凭据,等于多一份会过期的密码。
type replicaAddr struct {
	Host string
	Port int
}

// parseReplicaRows 从 SHOW REPLICAS(8.0.22+)或 SHOW SLAVE HOSTS 的结果里挑出从库地址。
//
// 按列名取,不按位置数:两种语法的列名和列数都不同,而按位置取会在换个版本之后
// 悄悄把 Source_Id 当成端口。
//
// **Host 为空的行直接丢掉。** 从库不配 report_host 时主库就这么报,拿空地址去连
// 会连回网关自己所在的主机 —— 少一个从库是事实,多一个连不上的地址是错觉。
func parseReplicaRows(cols []string, rows [][]any) []replicaAddr {
	hostAt, portAt := -1, -1
	for i, c := range cols {
		switch strings.ToLower(c) {
		case "host":
			hostAt = i
		case "port":
			portAt = i
		}
	}
	if hostAt < 0 {
		return nil
	}

	var out []replicaAddr
	for _, r := range rows {
		if hostAt >= len(r) {
			continue
		}
		host := strings.TrimSpace(asString(r[hostAt]))
		if host == "" {
			continue
		}
		port := 3306
		if portAt >= 0 && portAt < len(r) {
			if n, err := strconv.Atoi(strings.TrimSpace(asString(r[portAt]))); err == nil && n > 0 {
				port = n
			}
		}
		out = append(out, replicaAddr{Host: host, Port: port})
	}
	return out
}

// replicaDSN 借主库 DSN 的凭据与连接参数,换掉地址。
//
// 走驱动自己的 ParseDSN/FormatDSN,不做字符串拼接:密码里带 @ 或 : 的账号真实存在,
// 手写的切分会把密码切断。而切断的后果不是一个显眼的报错,是**限流静默失效** ——
// 连不上的从库在这套东西里表现为"没有延迟数据"。
func replicaDSN(masterDSN string, addr replicaAddr) (string, error) {
	cfg, err := mysqldriver.ParseDSN(masterDSN)
	if err != nil {
		return "", err
	}
	cfg.Net = "tcp"
	cfg.Addr = addr.Host + ":" + strconv.Itoa(addr.Port)
	return cfg.FormatDSN(), nil
}

// lagFrom 是心跳落后此刻多久。
//
// 心跳的时间戳由**网关进程**写下,再由网关读回 —— 全程只有一个时钟参与,不把
// 网关与 MySQL 之间的时钟偏差算进延迟里。
//
// 来自未来的心跳算 0:两台机器的时钟不必一致,而一个负的延迟会被 shouldPause
// 当成"从库很健康"。
func lagFrom(beat, now time.Time) time.Duration {
	if !beat.Before(now) {
		return 0
	}
	return now.Sub(beat)
}

// heartbeatRead 读某一个从库上的那一行心跳。
//
// 做成函数而不是直接传 *sql.DB:取最坏延迟的那段逻辑不该为了测试去搭一套主从,
// 与 CopyOptions.ReplicaLag 把"读延迟"和"该不该等"分开是同一个理由。
type heartbeatRead func(context.Context) (time.Time, error)

// discoverReplicas 在主库上问它挂着哪些从库。
//
// 8.0.22 起是 SHOW REPLICAS,更早是 SHOW SLAVE HOSTS,而 8.4 又把后者删了 ——
// 依次试,与 CurrentPosition 对 SHOW BINARY LOG STATUS / SHOW MASTER STATUS 的
// 做法一致。
//
// **没有从库时返回空列表,不是错误。** 单机实例上"没有从库"是事实,而单机恰恰是
// 最不需要限流的场景。两种语法都不被接受才算错 —— 那时上层要把原因说给人听,
// 而不是当成"这台机器没有从库"。
func discoverReplicas(ctx context.Context, master *sql.DB) ([]replicaAddr, error) {
	var lastErr error
	for _, q := range []string{"SHOW REPLICAS", "SHOW SLAVE HOSTS"} {
		cols, rows, err := queryRows(ctx, master, q)
		if err != nil {
			lastErr = err
			continue
		}
		return parseReplicaRows(cols, rows), nil
	}
	return nil, fmt.Errorf("问不出从库列表:两种语法都不被接受: %w", lastErr)
}

// queryRows 把结果集整个读成列名 + 行,好让解析留在纯函数里。
// 列数和列名随 MySQL 版本变,所以按 rows.Columns() 动态收。
func queryRows(ctx context.Context, db *sql.DB, query string) ([]string, [][]any, error) {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, nil, err
	}
	var out [][]any
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, nil, err
		}
		out = append(out, vals)
	}
	return cols, out, rows.Err()
}

// lagAcross 是这组从库里**最坏**的那个延迟。
//
// 取最坏的,不是平均或第一个:限流护的是落后最多的那台,平均值会在一个从库已经
// 掉队很远时继续全速拷贝。
//
// 读不到的从库跳过 —— 一台重启中的从库不该让限流对其余几台失灵。**一个都读不到
// 才报错**:返回 0 等于替所有从库宣布"都跟上了",而那是一句没有依据的断言。
func lagAcross(ctx context.Context, now time.Time, reads []heartbeatRead) (time.Duration, error) {
	worst := time.Duration(0)
	seen := false
	var lastErr error
	for _, read := range reads {
		beat, err := read(ctx)
		if err != nil {
			lastErr = err
			continue
		}
		seen = true
		if lag := lagFrom(beat, now); lag > worst {
			worst = lag
		}
	}
	if !seen {
		if lastErr == nil {
			lastErr = errors.New("没有可读的从库")
		}
		return 0, fmt.Errorf("读不到任何一个从库的心跳: %w", lastErr)
	}
	return worst, nil
}
