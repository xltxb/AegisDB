package osc

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"
)

const defaultProbeTimeout = 5 * time.Second

// throttleConfig 是给一次迁移装一套限流所需的东西。
type throttleConfig struct {
	Master    *sql.DB
	MasterDSN string
	Schema    string
	Table     string
	// MaxLag 是能容忍的延迟上限。0 = 不限流,那样连心跳都不必写。
	MaxLag time.Duration
	// HeartbeatInterval 是心跳的刻度,默认 500ms。
	HeartbeatInterval time.Duration
	// ProbeTimeout 是"心跳到得了从库吗"这次探测等多久,默认 5s。
	ProbeTimeout time.Duration

	// 下面两个只为测试留:退化路径(从库连不上、心跳到不了)不该为了被测到
	// 而真去拔一台从库的网线。
	discover    func(context.Context, *sql.DB) ([]replicaAddr, error)
	openReplica func(dsn string) (*sql.DB, error)
}

// throttle 是一次迁移的限流装置:主库自报从库、心跳写进主库、从库把它读回来。
//
// 它只有两种结局,而**两种都要说得出口**:开起来了(几个从库、什么阈值),或者没开
// 起来(为什么)。一个看起来开着、实际一次都没生效的限流比明说没有更危险 ——
// 人会照着它去按下那颗按钮。
type throttle struct {
	note  string
	lag   func(context.Context) (time.Duration, error)
	beat  *beater
	conns []*sql.DB
	once  sync.Once
}

// newThrottle 装一套限流。
//
// **没有 error 返回,因为它不会因为装不起来而失败** —— 装不起来就是不限流,理由
// 写进 Note,由上层落库并显示给人。签名照着这件事写,省得调用方去处理一个永远是
// nil 的错误,也省得后来的人以为"装不起来"会在那里被拦住。
//
// 这是 ADR 0011 对未知的一贯立场(拿不到磁盘余量不拦):把"没有从库"当成故障,
// 会让单机实例上一个本来最安全的迁移反而做不成。
func newThrottle(ctx context.Context, cfg throttleConfig) *throttle {
	if cfg.MaxLag <= 0 {
		return &throttle{note: "未启用:配置里的 osc.max_lag_seconds 是负数,限流被明确关掉了"}
	}

	discover := cfg.discover
	if discover == nil {
		discover = discoverReplicas
	}
	open := cfg.openReplica
	if open == nil {
		open = func(dsn string) (*sql.DB, error) { return sql.Open("mysql", dsn) }
	}

	addrs, err := discover(ctx, cfg.Master)
	if err != nil {
		return &throttle{note: fmt.Sprintf("未启用:问不出从库列表(%v)", err)}
	}
	if len(addrs) == 0 {
		// 单机实例,或者从库没配 report_host。两者都表现为"主库报不出从库",
		// 而后者是要人去 my.cnf 里补一行的 —— 所以这句话得把两种可能都说出来。
		return &throttle{note: "未启用:主库上没有发现从库(单机实例,或从库没配 report_host)"}
	}

	// 心跳只在真有从库时才写:没有人读的话,那是白搭一张表和一份持续写入。
	beat, err := startHeartbeat(ctx, cfg.Master, heartbeatConfig{
		Schema: cfg.Schema, Table: cfg.Table, Interval: cfg.HeartbeatInterval,
	})
	if err != nil {
		return &throttle{note: fmt.Sprintf("未启用:心跳表建不出来(%v)", err)}
	}

	t := &throttle{beat: beat}
	var reads []heartbeatRead
	for _, a := range addrs {
		dsn, err := replicaDSN(cfg.MasterDSN, a)
		if err != nil {
			continue
		}
		db, err := open(dsn)
		if err != nil {
			continue
		}
		t.conns = append(t.conns, db)
		reads = append(reads, func(ctx context.Context) (time.Time, error) {
			return readHeartbeat(ctx, db, cfg.Schema, beat.Table())
		})
	}

	// 探一次:心跳真的到得了从库吗?
	//
	// 到不了的原因是沉默的 —— 复制过滤器把这个库排除了、复制断着、账号连不上。
	// 不探的话,限流会带着一个永远读不到的心跳跑下去,算出的延迟一路变大,把拷贝
	// 永久按死在限流里,而界面上写着"限流已启用"。
	if n := probeReplicas(ctx, reads, cfg.ProbeTimeout); n == 0 {
		t.Close()
		return &throttle{note: fmt.Sprintf(
			"未启用:心跳没能到达 %d 个从库中的任何一个(复制断了?复制过滤器排除了这个库?账号连不上?)",
			len(addrs))}
	}

	t.lag = func(ctx context.Context) (time.Duration, error) {
		return lagAcross(ctx, time.Now(), reads)
	}
	t.note = fmt.Sprintf("已启用:%d 个从库,阈值 %s", len(reads), cfg.MaxLag)
	return t
}

// probeReplicas 报告有几个从库在超时之内读到了心跳。
//
// 复制本来就有延迟,所以这里要等、要重试,而不是读一次就下结论 —— 刚建好的心跳表
// 到达从库也需要时间。
func probeReplicas(ctx context.Context, reads []heartbeatRead, timeout time.Duration) int {
	if timeout <= 0 {
		timeout = defaultProbeTimeout
	}
	deadline := time.Now().Add(timeout)
	for {
		n := 0
		for _, read := range reads {
			if _, err := read(ctx); err == nil {
				n++
			}
		}
		if n > 0 || time.Now().After(deadline) || ctx.Err() != nil {
			return n
		}
		select {
		case <-ctx.Done():
			return 0
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// LagFunc 是交给 CopyOptions.ReplicaLag 的那个函数。
//
// **没开起来时返回 nil**,不是一个恒返回 0 的函数:0 是在说"从库都跟上了",
// 而 nil 让 waitForReplica 明确地不限流。二者在拷贝速度上看不出区别,在
// "我们知不知道从库状况"上是两回事。
func (t *throttle) LagFunc() func(context.Context) (time.Duration, error) { return t.lag }

// Note 是落库并显示给人的那句话:限流开着(几个从库、什么阈值),或者没开(为什么)。
func (t *throttle) Note() string { return t.note }

// Enabled 是同一件事的布尔面,给落库和界面用 —— 省得谁去解析 Note 那句话。
func (t *throttle) Enabled() bool { return t.lag != nil }

// Close 收掉心跳表和那些从库连接。可以重复调用。
func (t *throttle) Close() {
	t.once.Do(func() {
		if t.beat != nil {
			t.beat.Stop()
		}
		for _, db := range t.conns {
			_ = db.Close()
		}
	})
}
