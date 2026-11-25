package osc

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// 从库延迟超阈值、复制没在运行、读不到延迟:三种都要停住拷贝,原因写清楚;回落后接着跑完。
func TestMySQL_ReplicaLagThrottlesTheCopy(t *testing.T) {
	db, schema := mysqlDB(t)
	ctx := context.Background()
	model := seedOrders(t, db, 1000)

	type reading struct {
		lag     time.Duration
		running bool
		err     error
	}
	var mu sync.Mutex
	cur := reading{lag: 30 * time.Second, running: true}
	set := func(r reading) { mu.Lock(); cur = r; mu.Unlock() }
	m := New(db, &fakeBinlog{}, schema, "orders", addCreatedIdx, Config{
		ChunkSize: 100, ThrottleInterval: 20 * time.Millisecond, MaxReplicaLag: 10 * time.Second,
		ReplicaLag: func(context.Context) (time.Duration, bool, error) {
			mu.Lock()
			defer mu.Unlock()
			return cur.lag, cur.running, cur.err
		},
	})
	done := make(chan error, 1)
	go func() { done <- m.Run(ctx) }()

	holds := func(want string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for !strings.Contains(m.Status().Throttled, want) {
			if time.Now().After(deadline) {
				t.Fatalf("应因「%s」限流,实际 %+v", want, m.Status())
			}
			time.Sleep(10 * time.Millisecond)
		}
		before := m.Status().Chunks
		time.Sleep(150 * time.Millisecond)
		if st := m.Status(); st.Chunks != before {
			t.Fatalf("限流(%s)期间不该拷贝: %+v", want, st)
		}
	}
	holds("从库延迟 30 秒,超过阈值 10 秒")
	set(reading{running: false})
	holds("从库复制未在运行")
	set(reading{err: errors.New("连接被拒绝")})
	holds("读不到从库复制延迟")

	set(reading{lag: time.Second, running: true})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if st := m.Status(); st.RowsCopied != 1000 || st.Throttled != "" {
		t.Errorf("延迟回落后应拷完 1000 行且不再限流: %+v", st)
	}
	diffOrders(t, model, readOrders(t, db, "orders"))
}

// 阈值 0 = 不按延迟限流:延迟未知也照跑。
func TestMySQL_ReplicaLagZeroThresholdIsOff(t *testing.T) {
	db, schema := mysqlDB(t)
	seedOrders(t, db, 300)
	m := New(db, &fakeBinlog{}, schema, "orders", addCreatedIdx, Config{
		ChunkSize: 100, ThrottleInterval: 20 * time.Millisecond,
		ReplicaLag: func(context.Context) (time.Duration, bool, error) { return 0, false, nil },
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := m.Run(ctx); err != nil {
		t.Fatalf("阈值为 0 时不该因延迟未知卡住: %v", err)
	}
}

// 对着一台不是从库的实例读:结果集为空,应判为"复制未在运行",不是报错。
func TestMySQL_ReplicaLagOfANonReplica(t *testing.T) {
	db, _ := mysqlDB(t)
	lag, running, err := ReplicaLagOf(db)(context.Background())
	if err != nil || running || lag != 0 {
		t.Errorf("非从库应为 running=false、无错: lag=%v running=%v err=%v", lag, running, err)
	}
}

// Aurora 的副本延迟在写节点的 information_schema.replica_host_status 里;普通 MySQL 没有这张表,
// 读不到要如实报错(迁移会停在限流里、前置检查给"读不到"的提示),不能当成"没有延迟"。
func TestMySQL_AuroraReplicaLagOnPlainMySQLIsAnError(t *testing.T) {
	db, _ := mysqlDB(t)
	if _, _, err := AuroraReplicaLagOf(db)(context.Background()); err == nil {
		t.Fatal("普通 MySQL 上没有 replica_host_status,应报错")
	}
}

func TestParseVersion_Aurora(t *testing.T) {
	for _, c := range []struct {
		v       string
		a, b, p int
	}{
		{"8.0.mysql_aurora.3.05.2", 8, 0, 23},
		{"5.7.mysql_aurora.2.11.2", 5, 7, 0},
		{"8.0.32", 8, 0, 32},
		{"8.0.36-log", 8, 0, 36},
	} {
		if a, b, p := parseVersion(c.v); a != c.a || b != c.b || p != c.p {
			t.Errorf("%s: 应为 %d.%d.%d,实际 %d.%d.%d", c.v, c.a, c.b, c.p, a, b, p)
		}
	}
}
