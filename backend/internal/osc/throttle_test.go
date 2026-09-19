package osc

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

// throttle 把三件事组装起来:主库自报从库、心跳写进主库、从从库把它读回来。
//
// 这一层最要紧的不是"限流能开起来",是**开不起来的时候必须说出口**。一个看起来
// 开着、实际一次都没生效的限流,比明说没有更危险 —— 人会照着它去点那颗按钮。

func TestThrottle_NotEnabledWhenTheInstanceHasNoReplica(t *testing.T) {
	db := openMySQL(t)
	table := makeTable(t, db, "CREATE TABLE `%s` (id BIGINT PRIMARY KEY)")

	th := newThrottle(context.Background(), throttleConfig{
		Master: db, MasterDSN: mysqlDSN(), Schema: "osc_test", Table: table,
		MaxLag: 30 * time.Second,
		discover: func(context.Context, *sql.DB) ([]replicaAddr, error) {
			return nil, nil
		},
	})
	defer th.Close()

	if th.LagFunc() != nil {
		t.Error("没有从库却给出了延迟来源 —— 那会让拷贝按着一个编出来的数字限流")
	}
	if !strings.Contains(th.Note(), "未启用") {
		t.Errorf("留痕是 %q,它必须说得出限流没开", th.Note())
	}
	// 没有从库就没有人会读这行心跳。还去建一张表、还每 500ms 写一次,
	// 是白搭一张表和一份写入。
	exists, err := tableExists(context.Background(), db, "osc_test", HeartbeatName(table))
	if err != nil {
		t.Fatalf("查心跳表失败: %v", err)
	}
	if exists {
		t.Error("没有从库,却仍然建了心跳表")
	}
}

func TestThrottle_FallsBackWhenTheHeartbeatNeverReachesTheReplica(t *testing.T) {
	// 从库发现得到、也连得上,但心跳到不了 —— 复制过滤器把这张表排除了,或者复制
	// 本身断着。这时限流**必须退化成不限流并说出来**:留着一个永远读不到心跳的
	// 延迟来源,算出的延迟会一路变大,把拷贝永久按死在限流里。
	db := openMySQL(t)
	table := makeTable(t, db, "CREATE TABLE `%s` (id BIGINT PRIMARY KEY)")

	th := newThrottle(context.Background(), throttleConfig{
		Master: db, MasterDSN: mysqlDSN(), Schema: "osc_test", Table: table,
		MaxLag: 30 * time.Second, ProbeTimeout: 300 * time.Millisecond,
		discover: func(context.Context, *sql.DB) ([]replicaAddr, error) {
			return []replicaAddr{{Host: "replica-that-is-not-there", Port: 3306}}, nil
		},
		openReplica: func(string) (*sql.DB, error) {
			return nil, errors.New("connection refused")
		},
	})
	defer th.Close()

	if th.LagFunc() != nil {
		t.Error("心跳到不了从库,却仍然给出了延迟来源")
	}
	if !strings.Contains(th.Note(), "未启用") {
		t.Errorf("留痕是 %q,它必须说得出限流没开", th.Note())
	}
}

func TestThrottle_EnabledWhenAReplicaReadsTheHeartbeatBack(t *testing.T) {
	// 这里的"从库"是主库自己 —— 测的是组装:发现 → 连上 → 心跳读得回来 → 启用。
	// 复制那一段(心跳真的经过 binlog 到达另一台机器)由 replication_test.go 里
	// 那套真主从覆盖,拿自环冒充复制只会让这条用例说谎。
	db := openMySQL(t)
	table := makeTable(t, db, "CREATE TABLE `%s` (id BIGINT PRIMARY KEY)")

	th := newThrottle(context.Background(), throttleConfig{
		Master: db, MasterDSN: mysqlDSN(), Schema: "osc_test", Table: table,
		MaxLag: 30 * time.Second, ProbeTimeout: 3 * time.Second,
		discover: func(context.Context, *sql.DB) ([]replicaAddr, error) {
			return []replicaAddr{{Host: "self", Port: 3306}}, nil
		},
		openReplica: func(string) (*sql.DB, error) { return sql.Open("mysql", mysqlDSN()) },
	})
	defer th.Close()

	lag := th.LagFunc()
	if lag == nil {
		t.Fatalf("从库读得到心跳,限流却没开起来:%s", th.Note())
	}
	if !strings.Contains(th.Note(), "已启用") {
		t.Errorf("留痕是 %q,它该说限流开着", th.Note())
	}

	got, err := lag(context.Background())
	if err != nil {
		t.Fatalf("读延迟失败: %v", err)
	}
	// 读的是自己刚写下的那一拍,延迟只该是毫秒级。这条断言钉的是"读到的确实是
	// 这次迁移的心跳",而不是某个陈年数值。
	if got > 5*time.Second {
		t.Errorf("延迟读成了 %v,自环读自己的心跳不该有这么大", got)
	}
}

func TestThrottle_CloseTakesTheHeartbeatTableWithIt(t *testing.T) {
	db := openMySQL(t)
	table := makeTable(t, db, "CREATE TABLE `%s` (id BIGINT PRIMARY KEY)")

	th := newThrottle(context.Background(), throttleConfig{
		Master: db, MasterDSN: mysqlDSN(), Schema: "osc_test", Table: table,
		MaxLag: 30 * time.Second, ProbeTimeout: 3 * time.Second,
		discover: func(context.Context, *sql.DB) ([]replicaAddr, error) {
			return []replicaAddr{{Host: "self", Port: 3306}}, nil
		},
		openReplica: func(string) (*sql.DB, error) { return sql.Open("mysql", mysqlDSN()) },
	})
	th.Close()

	exists, err := tableExists(context.Background(), db, "osc_test", HeartbeatName(table))
	if err != nil {
		t.Fatalf("查心跳表失败: %v", err)
	}
	if exists {
		t.Error("限流器关掉了,心跳表还留在库里")
	}
}

func TestThrottle_NotEnabledWhenThresholdIsZero(t *testing.T) {
	// 阈值 0 是"没开限流"(见 shouldPause)。那就连心跳都不必写 —— 没有人会用它。
	db := openMySQL(t)
	table := makeTable(t, db, "CREATE TABLE `%s` (id BIGINT PRIMARY KEY)")

	th := newThrottle(context.Background(), throttleConfig{
		Master: db, MasterDSN: mysqlDSN(), Schema: "osc_test", Table: table, MaxLag: 0,
		discover: func(context.Context, *sql.DB) ([]replicaAddr, error) {
			t.Error("阈值为 0 时不该去问从库列表")
			return nil, nil
		},
	})
	defer th.Close()

	if th.LagFunc() != nil {
		t.Error("阈值为 0,却给出了延迟来源")
	}
	exists, err := tableExists(context.Background(), db, "osc_test", HeartbeatName(table))
	if err != nil {
		t.Fatalf("查心跳表失败: %v", err)
	}
	if exists {
		t.Error("阈值为 0,却仍然建了心跳表")
	}
}
