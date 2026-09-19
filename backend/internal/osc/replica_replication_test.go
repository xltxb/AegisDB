package osc

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// 这一组用例要一套**真的主从**。
//
// 它们是 ADR 0011 里「一次对着有从库的实例的演练」那句话的自动化版本:限流的每一环
// 都只在复制真的存在时才成立 —— 主库报不报得出从库、心跳过不过得来、延迟涨起来时
// 拷贝停不停。这几件事在单机上全都"通过",而且是静悄悄地通过。
//
// 没有从库时跳过,并说清楚怎么搭一套(见下面的 skip 文案)。
// **跑演练或 CI 时设 VELA_OSC_EXPECT_REPLICA=1** —— 那之后跳过会变成失败,
// 省得一次本该验证主从的运行悄悄什么都没验。

// replicaAdminDSN 是连**从库**的管理连接:测试要靠它停下和恢复复制。
// 被测代码从不使用它 —— 它只用主库自报的地址加主库的凭据。
func replicaAdminDSN() string {
	if v := os.Getenv("VELA_OSC_REPLICA_ADMIN_DSN"); v != "" {
		return v
	}
	return "root@tcp(127.0.0.1:3308)/osc_test"
}

// requireReplica 确认这台实例上真挂着从库,否则跳过(或按 EXPECT 的要求失败)。
func requireReplica(t *testing.T, master *sql.DB) []replicaAddr {
	t.Helper()
	addrs, err := discoverReplicas(context.Background(), master)
	strict := os.Getenv("VELA_OSC_EXPECT_REPLICA") == "1"
	if err != nil || len(addrs) == 0 {
		msg := fmt.Sprintf("这台 MySQL 上没有挂从库(err=%v),限流的这几条用例需要一套主从。\n"+
			"搭一套(本机再起一个实例):\n"+
			"  mysqld --initialize-insecure --datadir=/tmp/osc-replica\n"+
			"  mysqld --datadir=/tmp/osc-replica --port=3308 --socket=/tmp/osc-replica.sock \\\n"+
			"    --server-id=2 --report-host=127.0.0.1 --report-port=3308 --log-bin --binlog-format=ROW \\\n"+
			"    --binlog-row-image=FULL --gtid-mode=ON --enforce-gtid-consistency=ON --mysqlx=OFF &\n"+
			"然后在从库上 CHANGE REPLICATION SOURCE TO ... SOURCE_AUTO_POSITION=1 并 START REPLICA。\n"+
			"**从库必须配 report_host**,否则主库的 SHOW REPLICAS 报不出地址,限流装不起来。\n"+
			"详见 docs/adr/0011-mysql-online-schema-change.md。", err)
		if strict {
			t.Fatalf("VELA_OSC_EXPECT_REPLICA=1 但%s", msg)
		}
		t.Skipf("%s", msg)
	}
	return addrs
}

func openReplicaAdmin(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("mysql", replicaAdminDSN())
	if err != nil {
		t.Fatalf("打不开从库管理连接: %v", err)
	}
	if err := db.Ping(); err != nil {
		t.Skipf("连不上从库的管理连接(DSN=%s): %v —— 停/开复制线程要用它,"+
			"可以用 VELA_OSC_REPLICA_ADMIN_DSN 指一个", replicaAdminDSN(), err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestReplication_MasterReportsTheReplicaAndThrottleComesUp(t *testing.T) {
	// 限流能不能装起来,第一步就卡在主库报不报得出从库。报不出的时候这套东西
	// 不报错、不变慢,只是**不限流** —— 所以这一步必须有人盯着。
	db := openMySQL(t)
	addrs := requireReplica(t, db)
	for _, a := range addrs {
		if a.Host == "" || a.Port == 0 {
			t.Fatalf("主库报出的从库地址不完整:%+v —— 从库大概没配 report_host", a)
		}
	}

	table := makeTable(t, db, "CREATE TABLE `%s` (id BIGINT PRIMARY KEY)")
	th := newThrottle(context.Background(), throttleConfig{
		Master: db, MasterDSN: mysqlDSN(), Schema: "osc_test", Table: table,
		MaxLag: 30 * time.Second, HeartbeatInterval: 100 * time.Millisecond,
		ProbeTimeout: 10 * time.Second,
	})
	defer th.Close()

	if !th.Enabled() {
		t.Fatalf("有从库、复制也通,限流却没装起来:%s", th.Note())
	}
	if !strings.Contains(th.Note(), "已启用") {
		t.Errorf("留痕是 %q,期望它说已启用", th.Note())
	}

	// 心跳真的穿过复制到了从库:读回来的是一个刚写下不久的时间。
	lag, err := th.LagFunc()(context.Background())
	if err != nil {
		t.Fatalf("读从库延迟失败: %v", err)
	}
	if lag > 10*time.Second {
		t.Errorf("延迟读成了 %v —— 复制正常时不该这么大,读到的可能是一拍陈年心跳", lag)
	}
}

func TestReplication_CopyStopsWhileTheReplicaIsBehindAndResumesAfter(t *testing.T) {
	// 这条是整件事的落脚点:**延迟涨上去,拷贝就得停下来**。
	//
	// 前面所有用例都可以在单机上绿 —— 心跳写得动、延迟算得出、shouldPause 判得对。
	// 但"拷贝真的会因为从库落后而停住"只有让一个真从库落后才验得到,而让它落后的
	// 办法就是把 SQL 线程停掉。
	master := openMySQL(t)
	requireReplica(t, master)
	admin := openReplicaAdmin(t)

	const rows = 3000
	table := makeTable(t, master, "CREATE TABLE `%s` (id BIGINT PRIMARY KEY, memo VARCHAR(32))")
	var vals []string
	for i := 1; i <= rows; i++ {
		vals = append(vals, fmt.Sprintf("(%d,'x')", i))
	}
	mustExec(t, master, fmt.Sprintf("INSERT INTO `%s` (id, memo) VALUES %s", table, strings.Join(vals, ",")))

	shadow, err := CreateShadow(context.Background(), master, "osc_test", table, "ADD INDEX idx_memo (memo)")
	if err != nil {
		t.Fatalf("建影子表失败: %v", err)
	}
	t.Cleanup(func() {
		_, _ = master.ExecContext(context.Background(), "DROP TABLE IF EXISTS `"+shadow+"`")
	})

	th := newThrottle(context.Background(), throttleConfig{
		Master: master, MasterDSN: mysqlDSN(), Schema: "osc_test", Table: table,
		MaxLag: time.Second, HeartbeatInterval: 100 * time.Millisecond,
		ProbeTimeout: 10 * time.Second,
	})
	defer th.Close()
	if !th.Enabled() {
		t.Fatalf("限流没装起来,这条用例测不了它:%s", th.Note())
	}

	// 把从库按住。恢复放在 Cleanup 里:用例中途失败也不能留下一个停着的从库。
	mustExec(t, admin, "STOP REPLICA SQL_THREAD")
	resumed := false
	resume := func() {
		if !resumed {
			resumed = true
			mustExec(t, admin, "START REPLICA SQL_THREAD")
		}
	}
	t.Cleanup(resume)

	// 等到延迟确实超过阈值再开始拷贝 —— 否则测的是"拷贝比延迟涨得快"这种运气。
	waitUntilLagExceeds(t, th, time.Second, 10*time.Second)

	var copied atomic.Int64
	done := make(chan CopyResult, 1)
	errc := make(chan error, 1)
	go func() {
		res, err := CopyAll(context.Background(), master, "osc_test", table, shadow, CopyOptions{
			ChunkSize: 20, ReplicaLag: th.LagFunc(), MaxLag: time.Second,
			ThrottleInterval: 200 * time.Millisecond,
			OnProgress:       func(p Progress) { copied.Store(p.Copied) },
		})
		errc <- err
		done <- res
	}()

	// 从库落后着的这段时间里,一行都不该被搬走:限流的检查在**每一块之前**,
	// 而第一块也算一块。
	time.Sleep(2 * time.Second)
	if n := copied.Load(); n != 0 {
		t.Errorf("从库落后 %v 以上时仍然拷了 %d 行 —— 限流没有按住拷贝", time.Second, n)
	}
	select {
	case res := <-done:
		t.Fatalf("拷贝在从库还落后着的时候就跑完了(%d 行) —— 限流形同虚设", res.Copied)
	default:
	}

	// 放开从库,拷贝应当自己接着跑完。
	//
	// "停得住"只是一半:停完不能自己恢复的话,这个限流等于把迁移永久卡死,
	// 而那比不限流更糟 —— 它会让人把限流关掉。
	resume()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("从库追上之后拷贝报错: %v", err)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("从库已经恢复,拷贝却没有在 60s 内跑完 —— 限流没有解除")
	}
	res := <-done
	if res.Copied != rows {
		t.Errorf("最终拷了 %d 行,期望 %d 行", res.Copied, rows)
	}
}

// waitUntilLagExceeds 一直等到从库延迟真的超过阈值。
func waitUntilLagExceeds(t *testing.T, th *throttle, threshold, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		lag, err := th.LagFunc()(context.Background())
		if err == nil && lag > threshold {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("等了 %v,从库延迟仍然没有超过 %v(最后一次读到 %v,err=%v)——"+
				"复制可能根本没有被停住,那样这条用例测不到限流", timeout, threshold, lag, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
