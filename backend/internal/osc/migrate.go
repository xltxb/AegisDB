package osc

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"aegisdb/internal/apimsg"
	"aegisdb/internal/model"
)

// Config tunes a migration. 零值字段取默认值。
type Config struct {
	ChunkSize int // 每块行数,默认 1000

	// MaxThreadsRunning 限流:实例的 Threads_running 超过它就暂停拷贝与重放,直到回落。
	// 0 = 不按负载限流。
	MaxThreadsRunning int
	ThrottleInterval  time.Duration // 限流 / 暂停时的复查间隔,默认 1s

	// 按从库复制延迟限流:ReplicaLag 读一次从库延迟(见 ReplicaLagOf),延迟超过 MaxReplicaLag
	// 就暂停拷贝与重放。读不到、或复制线程没在运行(延迟未知)同样暂停 —— 宁可停下等人处理,
	// 也不在不知道从库落后多少时继续往它灌写入。ReplicaLag 为 nil 或 MaxReplicaLag 为 0 = 不按延迟限流。
	ReplicaLag    ReplicaLagFunc
	MaxReplicaLag time.Duration

	// Resume 从检查点续跑(见 Checkpoint):不建影子表而是确认它还在、已是变更后的结构,
	// binlog 从检查点的位点开流,拷贝从检查点的键之后接着拷。nil = 从头开始。
	Resume *Checkpoint

	// cut-over 持锁等待上限。它同时是业务写入被挡住的最长时间:拿锁请求排在队里时,
	// 后来访问这张表的请求也排在它后面。宁可短、多试几次,默认 3s。
	CutOverLockTimeout time.Duration
	// 持锁之后追平 binlog 的上限,超过就放锁重来 —— 追不平说明时机不对,不是该硬等。
	CutOverDrainTimeout time.Duration // 默认 5s
	CutOverRetries      int           // 默认 10
	CutOverRetryWait    time.Duration // 默认 2s
	// 追平到什么程度才去尝试 cut-over:落后 head 的等待上限,默认 1 分钟。
	CatchUpTimeout time.Duration

	// DropOldTable 在 cut-over 之后删掉换下来的原表 _t_del。默认保留:它是这次变更
	// 唯一的回退手段,而且大表的 DROP 本身也会带来一次 IO 抖动,该由人挑时候做。
	DropOldTable bool
}

func (c Config) withDefaults() Config {
	def := func(d *time.Duration, v time.Duration) {
		if *d <= 0 {
			*d = v
		}
	}
	if c.ChunkSize <= 0 {
		c.ChunkSize = 1000
	}
	if c.CutOverRetries <= 0 {
		c.CutOverRetries = 10
	}
	def(&c.ThrottleInterval, time.Second)
	def(&c.CutOverLockTimeout, 3*time.Second)
	def(&c.CutOverDrainTimeout, 5*time.Second)
	def(&c.CutOverRetryWait, 2*time.Second)
	def(&c.CatchUpTimeout, time.Minute)
	return c
}

// Phase is where a migration is.
type Phase string

const (
	PhasePending   Phase = "pending"
	PhasePreparing Phase = "preparing"   // 建影子表、定订阅起点
	PhaseCopying   Phase = "copying"     // 分块拷贝,同时重放
	PhaseCatchUp   Phase = "catching_up" // 拷贝完成,等重放追平
	PhaseCutOver   Phase = "cutting_over"
	PhaseDone      Phase = "done"
	PhaseFailed    Phase = "failed"
	PhaseAborted   Phase = "aborted"
)

// Status is a snapshot for the UI and the audit log.
type Status struct {
	Phase           Phase     `json:"phase"`
	RowsEstimated   int64     `json:"rowsEstimated"`
	RowsCopied      int64     `json:"rowsCopied"`
	Chunks          int64     `json:"chunks"`
	RowsReplayed    int64     `json:"rowsReplayed"`
	Applied         string    `json:"applied"` // 已重放到的 binlog 位点
	Paused          bool      `json:"paused"`
	Throttled       string    `json:"throttled,omitempty"` // 非空 = 正在限流,内容是原因
	// ThrottledRef:Throttled 的机器可读身份(apiMsg),界面按读者语言显示
	ThrottledRef *model.RuleRef `json:"throttledRef,omitempty"`
	CutOverAttempts int       `json:"cutOverAttempts"`
	StartedAt       time.Time `json:"startedAt"`
	FinishedAt      time.Time `json:"finishedAt,omitempty"`
	Error           string    `json:"error,omitempty"`
}

// BlockedError is returned by Run when preflight refuses the migration.
type BlockedError struct{ Blockers []Blocker }

func (e *BlockedError) Error() string {
	rs := make([]string, len(e.Blockers))
	for i, b := range e.Blockers {
		rs[i] = b.Reason
	}
	return "前置检查未通过:" + strings.Join(rs, ";")
}

// ErrAborted is the cause when an operator aborts the migration.
var ErrAborted = errors.New("迁移被中止")

// ErrInterrupted is the cause when the migration is stopped so it can be resumed
// later: 影子表留着,检查点有效(见 Interrupt)。
var ErrInterrupted = errors.New("迁移被中断,可从检查点续跑")

// Migration runs one online index change on one table.
type Migration struct {
	db     *sql.DB // 连到目标实例,默认库无所谓 —— 所有 SQL 都带库名
	binlog Binlog
	schema string
	table  string
	change Change
	cfg    Config

	// loadProbe 返回当前负载是否超限及原因;默认读 Threads_running。测试里替换。
	loadProbe func(ctx context.Context) (string, error)
	// probeMsg:probeLoad 最近一次给出的原因连同它的 ref(loadProbe 的签名只交字符串)
	probeMu  sync.Mutex
	probeMsg apimsg.M

	mu       sync.Mutex
	st       Status
	applied  Position
	paused   bool
	probedAt time.Time
	cancel   context.CancelCauseFunc

	// replayMu 让 cut-over 从重放循环手里接过 binlog 流:循环每处理一个事件就放一次锁,
	// cut-over 拿到锁后,流的剩余部分由持表锁的那条连接亲自重放。
	replayMu sync.Mutex
	cutDone  bool

	// 检查点(受 mu 保护):影子表建好、binlog 起点定下之后 ckReady;拷贝每成功一块更新一次。
	ckReady    bool
	ckLower    []any
	ckCopyDone bool
	ckShape    string // 影子表结构摘要,续跑时比对
}

// New prepares a migration. 它不碰数据库,Run 才开始。
func New(db *sql.DB, bl Binlog, schema, table string, c Change, cfg Config) *Migration {
	m := &Migration{db: db, binlog: bl, schema: schema, table: table, change: c, cfg: cfg.withDefaults()}
	m.st.Phase = PhasePending
	m.loadProbe = m.probeLoad
	return m
}

func (m *Migration) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.st
	s.Paused = m.paused
	s.Applied = m.applied.String()
	return s
}

// Pause stops copying and replaying until Resume. binlog 仍在读(流自己排队,只攒本表的改动,见 binlog_mysql.go),
// 暂停越久,恢复后要追的就越多。cut-over 不会在暂停期间开始。
func (m *Migration) Pause()  { m.setPaused(true) }
func (m *Migration) Resume() { m.setPaused(false) }

func (m *Migration) setPaused(p bool) {
	m.mu.Lock()
	m.paused = p
	m.mu.Unlock()
}

// Abort stops the migration and drops the shadow table. cut-over 一旦完成就不可中止
// —— 那时新表已经在服务了,"中止"没有意义。
func (m *Migration) Abort() {
	m.mu.Lock()
	cancel := m.cancel
	m.mu.Unlock()
	if cancel != nil {
		cancel(ErrAborted)
	}
}

// Interrupt 停下迁移但**不清理**影子表 —— 与进程直接退出时的库端状态一样,之后可以用
// Checkpoint() 续跑。Abort 才是"不要了"。
func (m *Migration) Interrupt() {
	m.mu.Lock()
	cancel := m.cancel
	m.mu.Unlock()
	if cancel != nil {
		cancel(ErrInterrupted)
	}
}

func (m *Migration) setPhase(p Phase) {
	m.mu.Lock()
	m.st.Phase = p
	m.mu.Unlock()
}

// Run executes the migration to completion. 返回 nil 表示 cut-over 已完成:
// 原表名下已经是带新索引的表,旧表在 _t_del(除非 DropOldTable)。
func (m *Migration) Run(parent context.Context) (err error) {
	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(nil)
	m.mu.Lock()
	if m.cancel != nil {
		m.mu.Unlock()
		return errors.New("同一个迁移不能运行两次")
	}
	m.cancel = cancel
	m.st.StartedAt = time.Now()
	m.mu.Unlock()

	ghost := ghostName(m.table)
	shadowCreated := false
	defer func() {
		c := context.Cause(ctx)
		if err != nil && errors.Is(c, ErrAborted) {
			err = ErrAborted
		}
		interrupted := err != nil && errors.Is(c, ErrInterrupted)
		if interrupted {
			err = ErrInterrupted
		}
		m.mu.Lock()
		m.st.FinishedAt = time.Now()
		switch {
		case err == nil:
			m.st.Phase = PhaseDone
		case errors.Is(err, ErrAborted):
			m.st.Phase, m.st.Error = PhaseAborted, err.Error()
		default:
			m.st.Phase, m.st.Error = PhaseFailed, err.Error()
		}
		m.mu.Unlock()
		// 没切过去的影子表是半成品,留着只会挡住下一次(前置检查会因残留拒绝)。
		// 这是本次自己建的表,删掉它不会误伤任何人。
		if err != nil && shadowCreated && !m.cutDone && !interrupted {
			cctx, ccancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer ccancel()
			_, _ = m.db.ExecContext(cctx, "DROP TABLE IF EXISTS "+m.qualified(ghost))
		}
	}()

	m.setPhase(PhasePreparing)
	// 前置检查在这里再跑一遍,而不是信任调用方跑过:它是这个包唯一的安全阀,不能
	// 是一个可以被跳过的步骤。而且从"人点了开始"到这里,实例状态可能已经变了。
	facts, err := Gather(ctx, m.db, m.schema, m.table)
	if err != nil {
		return err
	}
	bs, _ := Preflight(facts, m.change)
	if resume := m.cfg.Resume; resume != nil {
		kept := bs[:0]
		for _, b := range bs {
			if b.Code != "leftover_ghost" {
				kept = append(kept, b)
			}
		}
		bs = kept
	}
	if len(bs) > 0 {
		return &BlockedError{Blockers: bs}
	}
	meta, _, err := loadMeta(ctx, m.db, m.schema, m.table)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.st.RowsEstimated = meta.EstRows
	m.mu.Unlock()

	resume := m.cfg.Resume
	var from Position
	var cm *colMap
	if resume != nil {
		if err := m.ghostReady(ctx, ghost, resume.GhostShape); err != nil {
			return err
		}
		if cm, err = m.mapColumns(ctx, meta, ghost, false); err != nil {
			return err
		}
		// 续跑失败时影子表同样是半成品:任务会判失败,留着只挡下一次
		shadowCreated = true
		ok, err := binlogFileExists(ctx, m.db, resume.Applied.File)
		if err != nil {
			return apimsg.Errorf("确认 binlog 是否还在: %w", err)
		}
		if !ok {
			return fmt.Errorf("%w(%s)", ErrBinlogPurged, resume.Applied.File)
		}
		from = resume.Applied
	} else {
		if _, err := m.db.ExecContext(ctx, fmt.Sprintf("CREATE TABLE %s LIKE %s", m.qualified(ghost), m.qualified(m.table))); err != nil {
			return apimsg.Errorf("创建影子表: %w", err)
		}
		shadowCreated = true
		if _, err := m.db.ExecContext(ctx, strings.Replace(m.change.alterShadowSQL(ghost), quoteIdent(ghost), m.qualified(ghost), 1)); err != nil {
			return apimsg.Errorf("在影子表上应用变更: %w", err)
		}
		// 变更后的样子以影子表的真实结构为准:对列、选定位键、新增唯一约束先查重
		if cm, err = m.mapColumns(ctx, meta, ghost, true); err != nil {
			return err
		}
	}

	copyConn, releaseCopy, err := m.session(ctx)
	if err != nil {
		return err
	}
	defer releaseCopy()
	applyConn, releaseApply, err := m.session(ctx)
	if err != nil {
		return err
	}
	defer releaseApply()

	// 拷贝、重放、cut-over 的会话都已 USE 到目标库,影子表名不必带库名。
	ap := newApplier(cm, ghost)
	cp := newCopier(cm, ghost, m.cfg.ChunkSize)
	if resume == nil {
		// binlog 起点与拷贝范围在同一把读锁下定(见 startPoint):不漏任何一行
		if from, err = m.startPoint(ctx, copyConn, cp); err != nil {
			return err
		}
	} else if err := cp.start(ctx, copyConn); err != nil {
		return err
	}
	stream, err := m.binlog.Stream(ctx, from)
	if err != nil {
		return err
	}
	defer stream.Close()
	m.mu.Lock()
	m.applied = from
	if resume != nil { // 进度接着之前的计
		m.st.RowsCopied, m.st.Chunks, m.st.RowsReplayed = resume.RowsCopied, resume.Chunks, resume.RowsReplayed
	}
	m.mu.Unlock()
	if resume != nil {
		// 最大键按现在的表重读:续跑前后新插入的行多拷一遍也无妨(INSERT IGNORE + 重放兜底)
		if resume.CopyDone {
			cp.done = true
		} else if lower, err := decodeKey(resume.Lower); err != nil {
			return err
		} else if lower != nil {
			cp.lower = lower
		}
	}
	m.mu.Lock()
	m.ckReady, m.ckLower, m.ckCopyDone = true, cp.lower, cp.done
	m.mu.Unlock()

	replayErr := make(chan error, 1)
	replayCtx, stopReplay := context.WithCancel(ctx)
	defer stopReplay()
	go func() {
		// 子 goroutine 里的 panic 父 goroutine 接不住,会直接带走整个网关进程(2026-10 安全审计
		// L-OSC4):在这里收住,当成一次重放失败交回去,迁移按失败收尾。
		defer func() {
			if r := recover(); r != nil {
				replayErr <- apimsg.Errorf("binlog 重放异常退出: %v", r)
			}
		}()
		replayErr <- m.replayLoop(replayCtx, stream, ap, applyConn)
	}()
	fail := func(e error) error { stopReplay(); <-replayErr; return e }

	m.setPhase(PhaseCopying)
	for !cp.done {
		select {
		case e := <-replayErr:
			return apimsg.Errorf("重放 binlog: %w", e)
		default:
		}
		if err := m.waitRunnable(ctx); err != nil {
			return fail(err)
		}
		n, err := cp.next(ctx, copyConn)
		if err != nil {
			return fail(err)
		}
		m.mu.Lock()
		m.st.RowsCopied += n
		m.st.Chunks++
		m.ckLower, m.ckCopyDone = cp.lower, cp.done // 与计数同一把锁:检查点里两者一致
		m.mu.Unlock()
	}

	m.setPhase(PhaseCatchUp)
	for attempt := 1; ; attempt++ {
		select {
		case e := <-replayErr:
			return apimsg.Errorf("重放 binlog: %w", e)
		default:
		}
		if err := m.waitRunnable(ctx); err != nil {
			return fail(err)
		}
		err := m.waitCaughtUp(ctx)
		if err == nil {
			m.setPhase(PhaseCutOver)
			m.mu.Lock()
			m.st.CutOverAttempts = attempt
			m.mu.Unlock()
			if err = m.cutOver(ctx, stream, ap, ghost); err == nil {
				break
			}
		}
		if ctx.Err() != nil {
			return fail(context.Cause(ctx))
		}
		if attempt >= m.cfg.CutOverRetries || !isRetryable(err) {
			return fail(apimsg.Errorf("cut-over 第 %d 次失败: %w", attempt, err))
		}
		m.setPhase(PhaseCatchUp)
		select {
		case <-ctx.Done():
			return fail(context.Cause(ctx))
		case <-time.After(m.cfg.CutOverRetryWait):
		}
	}
	stopReplay()
	<-replayErr

	if m.cfg.DropOldTable {
		if _, err := m.db.ExecContext(ctx, "DROP TABLE "+m.qualified(delName(m.table))); err != nil {
			// 变更本身已经成功了,旧表没删掉不该让整件事报失败。
			m.mu.Lock()
			m.st.Error = "变更已完成,但删除旧表失败:" + err.Error()
			m.mu.Unlock()
		}
	}
	return nil
}

func (m *Migration) qualified(t string) string { return quoteIdent(m.schema) + "." + quoteIdent(t) }

// session pins a connection prepared by prepareSession. 归还时连接被丢弃而不是回到
// 池子:它带着 SET NAMES binary 与 UTC 时区(cut-over 那条还可能带着表锁),回到池子
// 里,下一个借到它的查询就会读出一堆原始字节。
// startPoint 定下 binlog 起点与拷贝范围(最大键),两者必须"无缝":起点之前提交的写入都要被拷贝
// 读到,起点之后的都在 binlog 里由重放补上。
//
// 只是先读起点、再读最大键并不够。开了 binlog 的 MySQL 提交分两步:先写 binlog,再在 InnoDB 里
// 提交使之可见。SHOW BINARY LOG STATUS 给出的位点包含"binlog 已写、还不可见"的事务 —— 它若
// 插入了一行新的最大键,读最大键时还看不到它(在拷贝范围之外),它的事件又在起点之前(不会被
// 重放):这一行永远进不了影子表。高并发写入下实测会丢行(2026-10 压测,约四十次迁移一次)。
//
// 所以在原表上短暂持一把读锁:LOCK TABLES t READ 要等这张表上所有在途的写事务(包括正处在两步
// 提交之间的)完全结束才拿得到,拿到之后也挡住新的写入。持锁期间读起点与最大键,两者对这张表
// 就是同一个时刻。锁只持有两次查询的工夫;拿锁等待有上限,超时就重试(与 cut-over 同一套参数)。
func (m *Migration) startPoint(ctx context.Context, conn *sql.Conn, cp *copier) (Position, error) {
	secs := int(m.cfg.CutOverLockTimeout.Round(time.Second) / time.Second)
	if secs < 1 {
		secs = 1
	}
	if _, err := conn.ExecContext(ctx, fmt.Sprintf("SET SESSION lock_wait_timeout = %d", secs)); err != nil {
		return Position{}, err
	}
	defer conn.ExecContext(context.Background(), "SET SESSION lock_wait_timeout = @@GLOBAL.lock_wait_timeout")
	for attempt := 1; ; attempt++ {
		_, err := conn.ExecContext(ctx, "LOCK TABLES "+quoteIdent(m.table)+" READ")
		if err == nil {
			head, err := m.binlog.Head(ctx)
			if err == nil {
				err = cp.start(ctx, conn)
			}
			if _, uerr := conn.ExecContext(context.Background(), "UNLOCK TABLES"); uerr != nil && err == nil {
				err = uerr
			}
			return head, err
		}
		if attempt >= m.cfg.CutOverRetries || !isLockConflict(err) {
			return Position{}, apimsg.Errorf("为确定拷贝起点短暂锁表: %w", err)
		}
		select {
		case <-ctx.Done():
			return Position{}, context.Cause(ctx)
		case <-time.After(m.cfg.CutOverRetryWait):
		}
	}
}

// mapColumns 读影子表的真实结构,对好列、选好定位键(见 newColMap);dupCheck 时对变更新增的
// 唯一约束先在原表上查重 —— 有重复就别开始,拷到一半才撞上只是白白跑了几小时。
func (m *Migration) mapColumns(ctx context.Context, src *tableMeta, ghost string, dupCheck bool) (*colMap, error) {
	dst, ok, err := loadMeta(ctx, m.db, m.schema, ghost)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, apimsg.Errorf("影子表 %s 不在", ghost)
	}
	cm, bs := newColMap(src, dst, m.change.Renames)
	if len(bs) > 0 {
		return nil, &BlockedError{Blockers: bs}
	}
	if dupCheck {
		for _, cols := range cm.newUniques() {
			sample, err := findDuplicate(ctx, m.db, m.qualified(m.table), src, cols)
			if err != nil {
				return nil, apimsg.Errorf("新增唯一约束查重: %w", err)
			}
			if sample != "" {
				names := make([]string, len(cols))
				for i, c := range cols {
					names[i] = src.Columns[c].Name
				}
				return nil, &BlockedError{Blockers: []Blocker{{Code: "unique_duplicates",
					Reason: fmt.Sprintf("要新增唯一约束的列 (%s) 上已有重复值(例如 %s):请先清理重复数据。", strings.Join(names, ", "), clip(sample, 60))}}}
			}
		}
	}
	shape, err := ghostShape(ctx, m.db, m.schema, ghost)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.ckShape = shape
	m.mu.Unlock()
	return cm, nil
}

func (m *Migration) session(ctx context.Context) (*sql.Conn, func(), error) {
	c, err := m.db.Conn(ctx)
	if err != nil {
		return nil, nil, err
	}
	release := func() {
		_ = c.Raw(func(any) error { return driver.ErrBadConn })
		c.Close()
	}
	if err := prepareSession(ctx, c); err != nil {
		release()
		return nil, nil, err
	}
	if _, err := c.ExecContext(ctx, "USE "+quoteIdent(m.schema)); err != nil {
		release()
		return nil, nil, err
	}
	return c, release, nil
}

// replayLoop applies binlog events until cut-over takes the stream over or ctx
// ends. 每个事件处理完就放一次 replayMu,Next 用短超时 —— 空闲时也不会一直攥着锁。
func (m *Migration) replayLoop(ctx context.Context, s Stream, ap *applier, conn *sql.Conn) error {
	for {
		if ctx.Err() != nil {
			return nil
		}
		if m.waitRunnable(ctx) != nil {
			return nil
		}
		m.replayMu.Lock()
		if m.cutDone {
			m.replayMu.Unlock()
			return nil
		}
		err := m.replayBatch(ctx, s, ap, conn, 200*time.Millisecond)
		m.replayMu.Unlock()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
	}
}

// 批量重放的上限:一批最多这么多行、最多攒这么久;后续事件只等 replayBatchGap,
// 没有积压时立刻收批,和逐条重放一样及时。
const (
	replayBatchRows = 500
	replayBatchSpan = 50 * time.Millisecond
	replayBatchGap  = 2 * time.Millisecond
)

// replayBatch 读一个事件(最多等 wait),再把紧跟着已经到达的事件一起放进**同一个事务**
// 重放、一次提交。
//
// 为什么要批:逐事件提交时每行一次提交、一次刷盘(双 1 配置下 redo 与 binlog 各一次),
// 实测每秒只能重放三四十行;业务每秒写入上百行的表,重放永远追不平,迁移卡在"追平中"
// 直到 cut-over 重试耗尽而失败。批量后积压越多、一批越大,追赶能力随落后程度上来;
// 不积压时每批只有一个事件,延迟与逐条重放相同。
//
// 位点只在提交成功后推进到这一批最后一个事件,与逐条重放同样是"提交了才算重放过"。
//
// 事件先收齐再开事务:重放事务和拷贝块会在影子表上撞成死锁(InnoDB 回滚其中一方),
// 这时整批回滚、用同一批事件重来 —— 流已经读过去了,手里留着这批事件才重做得了。
func (m *Migration) replayBatch(ctx context.Context, s Stream, ap *applier, conn *sql.Conn, wait time.Duration) error {
	nctx, cancel := context.WithTimeout(ctx, wait)
	ev, err := s.Next(nctx)
	cancel()
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			return nil // 这段时间没有新事件
		}
		return err
	}
	var batch []Event // 只收本表的行事件;别的表的事件只推进位点
	var rows int64
	last := ev.Pos
	budget := time.Now().Add(replayBatchSpan)
	for {
		ours := strings.EqualFold(ev.Schema, m.schema) && strings.EqualFold(ev.Table, m.table)
		if ours && ev.DDL {
			return apimsg.Errorf("迁移期间表 %s.%s 的结构被改动(binlog 位点 %s),为避免按错位的列重放,迁移中止", m.schema, m.table, ev.Pos)
		}
		if ours && len(ev.Rows) > 0 {
			batch = append(batch, ev)
			rows += int64(len(ev.Rows))
		}
		last = ev.Pos
		if rows >= replayBatchRows || time.Now().After(budget) {
			break
		}
		nctx, cancel := context.WithTimeout(ctx, replayBatchGap)
		next, err := s.Next(nctx)
		cancel()
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
				break // 没有积压了,收批
			}
			return err
		}
		ev = next
	}
	if len(batch) > 0 {
		err := retryLockConflict(ctx, func() error {
			tx, err := conn.BeginTx(ctx, nil)
			if err != nil {
				return err
			}
			for _, e := range batch {
				for _, rc := range e.Rows {
					if err := ap.apply(ctx, tx, rc); err != nil {
						tx.Rollback()
						return apimsg.Errorf("重放位点 %s 的行变更: %w", e.Pos, err)
					}
				}
			}
			return tx.Commit()
		})
		if err != nil {
			return err
		}
	}
	m.mu.Lock()
	m.applied = last
	m.st.RowsReplayed += rows
	m.mu.Unlock()
	return nil
}

// replayOne reads at most one event (waiting up to `wait`) and applies it. 一个
// rows 事件里的多行放进同一个事务 —— 除非 inTx=false(持着 LOCK TABLES 的连接)。
func (m *Migration) replayOne(ctx context.Context, s Stream, ap *applier, conn *sql.Conn, inTx bool, wait time.Duration) error {
	nctx, cancel := context.WithTimeout(ctx, wait)
	ev, err := s.Next(nctx)
	cancel()
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			return nil // 这段时间没有新事件
		}
		return err
	}
	ours := strings.EqualFold(ev.Schema, m.schema) && strings.EqualFold(ev.Table, m.table)
	if ours && ev.DDL {
		return apimsg.Errorf("迁移期间表 %s.%s 的结构被改动(binlog 位点 %s),为避免按错位的列重放,迁移中止", m.schema, m.table, ev.Pos)
	}
	if ours && len(ev.Rows) > 0 {
		var ex execer = conn
		var tx *sql.Tx
		if inTx {
			if tx, err = conn.BeginTx(ctx, nil); err != nil {
				return err
			}
			ex = tx
		}
		for _, rc := range ev.Rows {
			if err := ap.apply(ctx, ex, rc); err != nil {
				if tx != nil {
					tx.Rollback()
				}
				return apimsg.Errorf("重放位点 %s 的行变更: %w", ev.Pos, err)
			}
		}
		if tx != nil {
			if err := tx.Commit(); err != nil {
				return err
			}
		}
	}
	m.mu.Lock()
	m.applied = ev.Pos
	if ours {
		m.st.RowsReplayed += int64(len(ev.Rows))
	}
	m.mu.Unlock()
	return nil
}

// waitRunnable blocks while the migration is paused or throttled. 负载探测结果缓存
// 一个 ThrottleInterval —— 重放循环每个事件都会走到这里,不能每次都查一遍实例。
func (m *Migration) waitRunnable(ctx context.Context) error {
	for {
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		m.mu.Lock()
		paused := m.paused
		stale := time.Since(m.probedAt) >= m.cfg.ThrottleInterval
		m.mu.Unlock()
		if !paused && stale {
			reason, err := m.loadProbe(ctx)
			if err != nil {
				// 负载拿不到时继续跑:限流是保护手段不是前提,为它停掉整个迁移不划算。
				reason = ""
			}
			m.mu.Lock()
			m.st.Throttled, m.st.ThrottledRef, m.probedAt = reason, m.probeRef(reason), time.Now()
			m.mu.Unlock()
		}
		m.mu.Lock()
		throttled := m.st.Throttled != ""
		m.mu.Unlock()
		if !paused && !throttled {
			return nil
		}
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-time.After(m.cfg.ThrottleInterval):
		}
	}
}

// probeLoad:主库负载与从库延迟,任一超限就限流,原因写进 Status.Throttled。
//
// 两种"读不到"的处理不同:主库 Threads_running 读不到时继续跑(限流是保护手段,不是前提);
// 从库延迟读不到时停下 —— 那正是从库可能已经出问题的时候,继续灌写入只会让它更糟。
func (m *Migration) probeLoad(ctx context.Context) (string, error) {
	reason, err := m.threadsRunning(ctx)
	if reason.S != "" {
		m.setProbe(reason)
		return reason.S, err
	}
	if r := m.replicaLagReason(ctx); r.S != "" {
		m.setProbe(r)
		return r.S, nil
	}
	return "", err
}

func (m *Migration) replicaLagReason(ctx context.Context) apimsg.M {
	if m.cfg.ReplicaLag == nil || m.cfg.MaxReplicaLag <= 0 {
		return apimsg.M{}
	}
	lag, running, err := m.cfg.ReplicaLag(ctx)
	switch {
	case err != nil:
		return apimsg.Sprintf("读不到从库复制延迟(%s),延迟未知", err)
	case !running:
		return apimsg.T("从库复制未在运行,延迟未知")
	case lag > m.cfg.MaxReplicaLag:
		return apimsg.Sprintf("从库延迟 %d 秒,超过阈值 %d 秒", int(lag/time.Second), int(m.cfg.MaxReplicaLag/time.Second))
	}
	return apimsg.M{}
}

func (m *Migration) setProbe(r apimsg.M) {
	m.probeMu.Lock()
	m.probeMsg = r
	m.probeMu.Unlock()
}

// probeRef:reason 是 probeLoad 刚给出的那句就用它的 ref,否则(测试替身、空串)按原句查表。
func (m *Migration) probeRef(reason string) *model.RuleRef {
	m.probeMu.Lock()
	defer m.probeMu.Unlock()
	if reason != "" && m.probeMsg.S == reason {
		return m.probeMsg.Ref
	}
	return apimsg.Lookup(reason)
}

func (m *Migration) threadsRunning(ctx context.Context) (apimsg.M, error) {
	if m.cfg.MaxThreadsRunning <= 0 {
		return apimsg.M{}, nil
	}
	var name, val string
	if err := m.db.QueryRowContext(ctx, "SHOW GLOBAL STATUS LIKE 'Threads_running'").Scan(&name, &val); err != nil {
		return apimsg.M{}, err
	}
	n, _ := strconv.Atoi(val)
	if n > m.cfg.MaxThreadsRunning {
		return apimsg.Sprintf("Threads_running=%d,超过阈值 %d", n, m.cfg.MaxThreadsRunning), nil
	}
	return apimsg.M{}, nil
}

// waitCaughtUp waits until replay reaches the head observed now. 不持锁的追平:
// 让 cut-over 持锁期间要重放的只剩最后一小段。
func (m *Migration) waitCaughtUp(ctx context.Context) error {
	head, err := m.binlog.Head(ctx)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(m.cfg.CatchUpTimeout)
	for {
		m.mu.Lock()
		ok := m.applied.Compare(head) >= 0
		m.mu.Unlock()
		if ok {
			return nil
		}
		if time.Now().After(deadline) {
			return retryable{apimsg.Errorf("重放在 %s 内没有追上 binlog(目标 %s):写入太多或重放被限流", m.cfg.CatchUpTimeout, head)}
		}
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// retryable marks cut-over failures worth another attempt (锁没拿到、没追平),
// as opposed to ones that will fail the same way every time.
type retryable struct{ error }

func (r retryable) Unwrap() error { return r.error }

func isRetryable(err error) bool {
	var r retryable
	return errors.As(err, &r)
}

// cutOver swaps the shadow table in.
//
//	LOCK TABLES t WRITE, _t_gho WRITE   —— 从这一刻起原表不再有写入
//	读 binlog head                        —— 锁之前提交的写入都在它之前
//	在这条连接上把流重放到 head            —— 影子表被本连接锁着,只有它能写
//	RENAME TABLE t TO _t_del, _t_gho TO t —— 8.0.13+ 允许在 LOCK TABLES 之下做,原子
//	UNLOCK TABLES
//
// 任何一步失败都只需 UNLOCK:改名之前什么都没变,锁连接断开同样等于 UNLOCK。这就是
// 不用 gh-ost 哨兵表那套的原因 —— 没有"卡在半路"的中间状态。
func (m *Migration) cutOver(ctx context.Context, s Stream, ap *applier, ghost string) error {
	m.replayMu.Lock()
	defer m.replayMu.Unlock()

	conn, release, err := m.session(ctx)
	if err != nil {
		return err
	}
	defer release()

	secs := int(m.cfg.CutOverLockTimeout.Round(time.Second) / time.Second)
	if secs < 1 {
		secs = 1
	}
	if _, err := conn.ExecContext(ctx, fmt.Sprintf("SET SESSION lock_wait_timeout = %d", secs)); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, fmt.Sprintf("LOCK TABLES %s WRITE, %s WRITE", quoteIdent(m.table), quoteIdent(ghost))); err != nil {
		return retryable{apimsg.Errorf("拿表锁: %w", err)}
	}
	locked := true
	defer func() {
		if locked {
			uctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, _ = conn.ExecContext(uctx, "UNLOCK TABLES")
		}
	}()

	head, err := m.binlog.Head(ctx)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(m.cfg.CutOverDrainTimeout)
	for {
		m.mu.Lock()
		done := m.applied.Compare(head) >= 0
		m.mu.Unlock()
		if done {
			break
		}
		wait := time.Until(deadline)
		if wait <= 0 {
			return retryable{apimsg.Errorf("持锁 %s 内未能把重放追到 %s", m.cfg.CutOverDrainTimeout, head)}
		}
		if err := m.replayOne(ctx, s, ap, conn, false, wait); err != nil {
			return err
		}
	}

	if _, err := conn.ExecContext(ctx, fmt.Sprintf("RENAME TABLE %s TO %s, %s TO %s",
		quoteIdent(m.table), quoteIdent(delName(m.table)), quoteIdent(ghost), quoteIdent(m.table))); err != nil {
		return retryable{apimsg.Errorf("改名: %w", err)}
	}
	m.cutDone = true
	_, err = conn.ExecContext(ctx, "UNLOCK TABLES")
	locked = err != nil
	return nil
}
