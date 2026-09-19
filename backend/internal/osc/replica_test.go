package osc

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// 从库的地址从主库自己报出来(SHOW REPLICAS),凭据借主库那一套 —— 见 ADR 0011。
// 这一层是纯函数:解析与拼 DSN 不该为了测试去搭一套主从。
//
// 真要有从库才能验的那部分(发现得到、延迟读得出、限流真的按住了拷贝)在
// replica_replication_test.go 里,那些用例要一套真复制拓扑。

func TestParseReplicaRows_TakesHostAndPortByColumnName(t *testing.T) {
	// MySQL 8.0.22+ 的 SHOW REPLICAS 与更早的 SHOW SLAVE HOSTS 列名不同,列的顺序
	// 也不保证。按名字取,不按位置数 —— 与 scanPosition 对 SHOW MASTER STATUS 的
	// 立场一致。
	cols := []string{"Server_Id", "Host", "Port", "Source_Id", "Replica_UUID"}
	rows := [][]any{
		{"2", "replica-a", "3306", "1", "uuid-a"},
		{"3", "replica-b", "3308", "1", "uuid-b"},
	}

	got := parseReplicaRows(cols, rows)

	want := []replicaAddr{{Host: "replica-a", Port: 3306}, {Host: "replica-b", Port: 3308}}
	if len(got) != len(want) {
		t.Fatalf("发现 %d 个从库,期望 %d 个:%+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("第 %d 个从库是 %+v,期望 %+v", i, got[i], want[i])
		}
	}
}

func TestParseReplicaRows_SkipsReplicaThatReportsNoHost(t *testing.T) {
	// 从库不配 report_host 时,主库这一行的 Host 是空的 —— 拿它去连会连到自己身上。
	// 这正是"发现不到从库"最常见的原因,它必须表现为**少一个从库**,而不是多一个
	// 连不上的地址。
	cols := []string{"Server_Id", "Host", "Port"}
	rows := [][]any{
		{"2", "", "3306"},
		{"3", "replica-b", "3306"},
	}

	got := parseReplicaRows(cols, rows)

	if len(got) != 1 || got[0].Host != "replica-b" {
		t.Fatalf("没配 report_host 的那一行应当被跳过,实际发现:%+v", got)
	}
}

func TestParseReplicaRows_DefaultsMissingPortTo3306(t *testing.T) {
	cols := []string{"Host", "Port"}
	rows := [][]any{{"replica-a", ""}}

	got := parseReplicaRows(cols, rows)

	if len(got) != 1 || got[0].Port != 3306 {
		t.Fatalf("端口缺失时应当默认 3306,实际:%+v", got)
	}
}

func TestReplicaDSN_KeepsCredentialsAndParameters(t *testing.T) {
	// 借的是主库那一套凭据和连接参数,换掉的只有地址。parseTime 这类参数丢了,
	// 心跳那一列读回来就不是 time.Time,延迟算不出来。
	master := "vela:velapass@tcp(127.0.0.1:3306)/osc_test?parseTime=true&charset=utf8mb4"

	got, err := replicaDSN(master, replicaAddr{Host: "replica-a", Port: 3308})
	if err != nil {
		t.Fatalf("拼从库 DSN 失败: %v", err)
	}

	for _, want := range []string{"vela:velapass@", "replica-a:3308", "/osc_test", "parseTime=true", "charset=utf8mb4"} {
		if !strings.Contains(got, want) {
			t.Errorf("从库 DSN %q 里缺了 %q", got, want)
		}
	}
	if strings.Contains(got, "127.0.0.1:3306") {
		t.Errorf("从库 DSN %q 还指着主库的地址", got)
	}
}

func TestReplicaDSN_SurvivesPasswordWithAtSign(t *testing.T) {
	// 密码里带 @ 的账号是真实存在的。按第一个 @ 切 DSN 会把密码切断,拼出来的
	// 从库 DSN 连不上 —— 而连不上在这套东西里表现为"没有延迟数据",也就是
	// **限流静默失效**,不是一个显眼的报错。
	master := "vela:pa@ss:word@tcp(127.0.0.1:3306)/osc_test"

	got, err := replicaDSN(master, replicaAddr{Host: "replica-a", Port: 3306})
	if err != nil {
		t.Fatalf("拼从库 DSN 失败: %v", err)
	}

	if !strings.Contains(got, "vela:pa@ss:word@") {
		t.Errorf("从库 DSN %q 没有原样带上密码", got)
	}
}

func TestLagFrom_TreatsFutureHeartbeatAsZero(t *testing.T) {
	// 心跳的时间戳由网关进程生成,但读它的是从库上的那一行 —— 两边时钟不必一致,
	// 而一个"负延迟"会被 shouldPause 当成从库很健康。它本来就该是 0。
	now := time.Now()

	if lag := lagFrom(now.Add(2*time.Second), now); lag != 0 {
		t.Errorf("来自未来的心跳算出的延迟是 %v,期望 0", lag)
	}
}

func TestLagFrom_IsTheDistanceBehindNow(t *testing.T) {
	now := time.Now()

	if lag := lagFrom(now.Add(-3*time.Second), now); lag != 3*time.Second {
		t.Errorf("延迟算成了 %v,期望 3s", lag)
	}
}

func TestDiscoverReplicas_ReportsNoneOnAStandaloneInstanceWithoutFailing(t *testing.T) {
	// 单机实例上「没有从库」是事实,不是故障。这里返回错误的话,一个本来跑得好好的
	// 迁移会在单机上被限流层拖下水 —— 而单机恰恰是最不需要限流的场景。
	db := openMySQL(t)

	got, err := discoverReplicas(context.Background(), db)
	if err != nil {
		t.Fatalf("在单机实例上发现从库不该报错,实际: %v", err)
	}
	if len(got) != 0 {
		t.Skipf("这台实例上挂着 %d 个从库,不是单机:%+v", len(got), got)
	}
}

func TestLagAcross_TakesTheWorstReplica(t *testing.T) {
	// 限流要护住的是**最慢的那个**从库。取平均或取第一个,都会在一个从库已经落后
	// 很远时继续全速拷贝。
	now := time.Now()
	reads := []heartbeatRead{
		func(context.Context) (time.Time, error) { return now.Add(-1 * time.Second), nil },
		func(context.Context) (time.Time, error) { return now.Add(-9 * time.Second), nil },
		func(context.Context) (time.Time, error) { return now.Add(-3 * time.Second), nil },
	}

	lag, err := lagAcross(context.Background(), now, reads)
	if err != nil {
		t.Fatalf("取延迟失败: %v", err)
	}
	if lag != 9*time.Second {
		t.Errorf("延迟取成了 %v,期望最坏的那个 9s", lag)
	}
}

func TestLagAcross_SkipsTheReplicaItCannotRead(t *testing.T) {
	// 一个从库连不上(重启、网络、被摘掉)不该让限流失灵 —— 剩下那些还读得到的
	// 仍然要护住。
	now := time.Now()
	reads := []heartbeatRead{
		func(context.Context) (time.Time, error) { return time.Time{}, errors.New("connection refused") },
		func(context.Context) (time.Time, error) { return now.Add(-4 * time.Second), nil },
	}

	lag, err := lagAcross(context.Background(), now, reads)
	if err != nil {
		t.Fatalf("有一个从库读不到就整个失败了: %v", err)
	}
	if lag != 4*time.Second {
		t.Errorf("延迟取成了 %v,期望还读得到的那个 4s", lag)
	}
}

func TestLagAcross_FailsWhenNoReplicaCanBeRead(t *testing.T) {
	// 一个都读不到时必须报错,不能返回 0。
	//
	// 返回 0 是在说"从库都跟上了" —— 那是一句没有依据的断言,而拷贝会照着它全速跑。
	// 报错的后果由 waitForReplica 决定(它放行,并把判断交给监控),但那是它的立场,
	// 不该由这里替它编一个数据出来。
	now := time.Now()
	reads := []heartbeatRead{
		func(context.Context) (time.Time, error) { return time.Time{}, errors.New("connection refused") },
		func(context.Context) (time.Time, error) { return time.Time{}, errors.New("unknown table") },
	}

	if _, err := lagAcross(context.Background(), now, reads); err == nil {
		t.Error("所有从库都读不到,却返回了一个延迟值")
	}
}
