package osc

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"
)

// HeartbeatSuffix 沿用 gh-ost 给 changelog 表的后缀。
//
// 它必须与 ShadowSuffix / DelSuffix 不同:残留检查(Facts.LeftoverGhost /
// LeftoverDel)按后缀认表,心跳表要是也叫 _gho,一次正常的迁移就会把自己举报成
// "上次没清干净"。
const HeartbeatSuffix = "_ghc"

// HeartbeatName 是 t 的心跳表名。
func HeartbeatName(table string) string { return "_" + table + HeartbeatSuffix }

const defaultHeartbeatInterval = 500 * time.Millisecond

// heartbeatConfig 是起一次心跳要给的东西。
type heartbeatConfig struct {
	Schema string
	// Table 是**原表**。心跳表的名字由它推出来,一次迁移一张,不同迁移之间不互相覆盖。
	Table string
	// Interval 是两次心跳之间隔多久。它是延迟这把尺子的**刻度**:间隔 500ms 意味着
	// 测出来的延迟自带最多 500ms 的虚高,阈值定得比它还小就会一直限流。
	Interval time.Duration
	// Now 是时间戳的来源,默认 time.Now。
	//
	// 由网关进程给,不是 MySQL 的 NOW():读心跳的也是网关,全程只有一个时钟参与。
	// 用服务器时间的话,算出来的"延迟"里掺着两台机器的时钟差 —— 可能凭空限流,
	// 也可能把真实延迟抵消成 0。
	Now func() time.Time
}

// beater 是一次跑着的心跳。
type beater struct {
	db     *sql.DB
	schema string
	table  string
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
}

// startHeartbeat 建心跳表并开始写,直到 Stop 或 ctx 结束。
//
// 表已存在就**拒绝**,不复用:要么上一次没走干净,要么另一个进程正在推进同一张表的
// 迁移。后者尤其不能碰 —— 两个心跳互相覆盖,两边算出来的延迟都是错的,而它们都会
// 据此决定要不要限流。
func startHeartbeat(ctx context.Context, db *sql.DB, cfg heartbeatConfig) (*beater, error) {
	name := HeartbeatName(cfg.Table)
	exists, err := tableExists(ctx, db, cfg.Schema, name)
	if err != nil {
		return nil, fmt.Errorf("查心跳表是否存在: %w", err)
	}
	if exists {
		return nil, fmt.Errorf("心跳表 %s.%s 已存在:要么上次没清理干净,要么这张表上另有一次迁移在跑", cfg.Schema, name)
	}

	// ts 是 BIGINT 里的 Unix 微秒,不是 DATETIME。
	//
	// DATETIME 要连接带 parseTime=true 才读得回 time.Time,还要两端 loc 一致 ——
	// 而读它的是**从库**那条连接,参数由主库 DSN 借过去,不该在这里赌它带对了参数。
	// 一个整数没有这些问题。
	if _, err := db.ExecContext(ctx, fmt.Sprintf(
		"CREATE TABLE %s (id TINYINT UNSIGNED NOT NULL PRIMARY KEY, ts BIGINT NOT NULL)",
		quoteName(cfg.Schema, name))); err != nil {
		return nil, fmt.Errorf("建心跳表: %w", err)
	}

	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	interval := cfg.Interval
	if interval <= 0 {
		interval = defaultHeartbeatInterval
	}

	runCtx, cancel := context.WithCancel(ctx)
	b := &beater{db: db, schema: cfg.Schema, table: name, cancel: cancel, done: make(chan struct{})}

	beat := func() {
		// 单行,不是一张越写越长的表:一次跑几小时的迁移按 500ms 追加,结束时这张
		// 心跳表比被迁移的表还热闹,而它只有最新那一行有意义。
		_, _ = db.ExecContext(runCtx, fmt.Sprintf(
			"INSERT INTO %s (id, ts) VALUES (1, ?) ON DUPLICATE KEY UPDATE ts = VALUES(ts)",
			quoteName(cfg.Schema, name)), now().UnixMicro())
	}
	beat() // 先写一拍:读心跳的那一侧不必先等一个间隔才看到行

	go func() {
		defer close(b.done)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-t.C:
				// 写失败不重试也不上报:下一拍还会写。而**持续**写不进去的后果是
				// 安全的那一边 —— 从库上那一行停住,算出的延迟越来越大,限流把拷贝
				// 按住。拷贝本来也走同一个主库,主库出事时它一样跑不动。
				beat()
			}
		}
	}()
	return b, nil
}

// Table 是这次心跳用的表名。
func (b *beater) Table() string { return b.table }

// Stop 停止心跳并删掉那张表。可以重复调用 —— 退出路径不止一条。
func (b *beater) Stop() {
	b.once.Do(func() {
		b.cancel()
		<-b.done
		// 用一个与迁移无关的 ctx:走到这里往往正是因为迁移的 ctx 已经被取消,
		// 而"被中止"恰恰是最需要把临时表清掉的时候。
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = b.db.ExecContext(ctx, "DROP TABLE IF EXISTS "+quoteName(b.schema, b.table))
	})
}

// readHeartbeat 读一条连接上的心跳行。
//
// 这条连接通常是**从库**:主库上写下的那一拍要经过复制才到得了那里,读到的值落后
// 多少,从库就落后多少。
func readHeartbeat(ctx context.Context, db *sql.DB, schema, table string) (time.Time, error) {
	var micros int64
	err := db.QueryRowContext(ctx,
		"SELECT ts FROM "+quoteName(schema, table)+" WHERE id = 1").Scan(&micros)
	if err != nil {
		return time.Time{}, err
	}
	return time.UnixMicro(micros), nil
}
