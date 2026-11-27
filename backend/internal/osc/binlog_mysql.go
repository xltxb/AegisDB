package osc

// 阶段三:用 go-mysql 读真实的 binlog,适配 Binlog / Stream 两个接口(见 binlog.go)。
//
// 网关以一个"从库"的身份连上去(COM_BINLOG_DUMP),从给定位点开始按序收事件。只做三件事:
//
//   - 行事件(WRITE / UPDATE / DELETE_ROWS)→ RowChange,完整前后镜像,按表的列序;
//   - 涉及本表的语句事件 → DDL=true,让迁移中止。不只是 ALTER:TRUNCATE、按语句格式记的
//     DML(某个会话 SET binlog_format=STATEMENT)也改了表却没有行镜像,一样没法准确重放;
//   - 其余事件只推进位点 —— cut-over 要知道"读到哪了",不只是"本表改到哪了"。
//
// 值的类型按 normalize 的约定(binlog.go 的 RowChange 注释):整数有符号、DECIMAL 与时间是
// 文本、字符串是列字符集的原始字节。两处要显式配置,否则对不上:
//
//   - TIMESTAMP:库里存的是 UTC 秒数,解码库按 TimestampStringLocation 格式化,不设就是网关
//     进程的本地时区 —— 而迁移会话固定 time_zone=UTC(prepareSession),差几个小时;
//   - ParseTime 保持关:要文本,不要 time.Time。
//
// DECIMAL 解码库默认就给文本(UseDecimal 关时),不经过浮点,不丢精度。

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	gomysql "github.com/go-mysql-org/go-mysql/mysql"
	"github.com/go-mysql-org/go-mysql/replication"
	gosqlmysql "github.com/go-sql-driver/mysql"

	"aegisdb/internal/apimsg"
)

// MySQLSource is how to reach the instance as a replication client. 账号要有 REPLICATION
// SLAVE / REPLICATION CLIENT(前置检查已经查过,见 preflight.go)。
type MySQLSource struct {
	Host     string
	Port     uint16
	User     string
	Password string
}

// MySQLBinlog reads one table's concurrent writes from a real MySQL binlog. 用完要 Close。
type MySQLBinlog struct {
	src    MySQLSource
	schema string
	table  string

	headOnce sync.Once
	headDB   *sql.DB
	headErr  error
}

// NewMySQLBinlog watches schema.table on the instance src.
func NewMySQLBinlog(src MySQLSource, schema, table string) *MySQLBinlog {
	return &MySQLBinlog{src: src, schema: schema, table: table}
}

// Head reads the server's current binlog position.
//
// 用**自己的**一条连接,不和迁移、也不和业务共用连接池。cut-over 是持着原表的 LOCK TABLES
// 去读位点的:这时被锁挡住的写入事务个个占着一条池里的连接,池子一满,读位点就拿不到连接,
// 而那些事务又在等这把锁 —— 互相等死。测试里真实撞上过(池子 4 条)。
func (b *MySQLBinlog) Head(ctx context.Context) (Position, error) {
	b.headOnce.Do(func() {
		cfg := gosqlmysql.NewConfig()
		cfg.User, cfg.Passwd, cfg.Net = b.src.User, b.src.Password, "tcp"
		cfg.Addr = net.JoinHostPort(b.src.Host, strconv.Itoa(int(b.src.Port)))
		cfg.Timeout = 8 * time.Second
		cfg.TLSConfig = "preferred"
		b.headDB, b.headErr = sql.Open("mysql", cfg.FormatDSN())
		if b.headErr == nil {
			b.headDB.SetMaxOpenConns(1)
		}
	})
	if b.headErr != nil {
		return Position{}, b.headErr
	}
	return binlogHead(ctx, b.headDB)
}

// Close releases the position-reading connection. 流各自 Close。
func (b *MySQLBinlog) Close() error {
	if b.headDB != nil {
		return b.headDB.Close()
	}
	return nil
}

// Stream starts a binlog dump from `from`.
func (b *MySQLBinlog) Stream(_ context.Context, from Position) (Stream, error) {
	if from.Pos > uint64(^uint32(0)) {
		return nil, apimsg.Errorf("binlog 位点超出范围: %s", from)
	}
	syncer := replication.NewBinlogSyncer(replication.BinlogSyncerConfig{
		ServerID: replicaServerID(),
		Flavor:   gomysql.MySQLFlavor,
		Host:     b.src.Host,
		Port:     b.src.Port,
		User:     b.src.User,
		Password: b.src.Password,
		// 见文件头:TIMESTAMP 要按 UTC 给文本,与迁移会话一致
		TimestampStringLocation: time.UTC,
		// 长时间没有写入时库端发心跳,连接不会被中间设备当成空闲掐掉,断了也能及时发现
		HeartbeatPeriod: 10 * time.Second,
		ReadTimeout:     60 * time.Second,
		// 断线不自动从中间续上:续上的位点由解码库自己记,出错时我们无从核对是否漏了事件。
		// 断了就让迁移失败(它会清理影子表),比悄悄漏掉一段写入安全。
		MaxReconnectAttempts: 1,
		DisableRetrySync:     true,
		Logger:               quietLogger,
	})
	streamer, err := syncer.StartSync(gomysql.Position{Name: from.File, Pos: uint32(from.Pos)})
	if err != nil {
		syncer.Close()
		return nil, apimsg.Errorf("订阅 binlog(%s): %w", from, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	st := &mysqlStream{
		syncer: syncer, streamer: streamer, file: from.File, schema: b.schema, table: b.table,
		maxRows: maxBacklogRows, ready: make(chan struct{}, 1), cancel: cancel, done: make(chan struct{}),
	}
	go st.pump(ctx)
	return st, nil
}

// maxBacklogRows 是读到了、还没被重放取走的本表行变更的上限。迁移暂停或限流时重放停着,
// 本表的改动只能攒在网关内存里;攒到这个数就让迁移失败,而不是把网关的内存吃光。
const maxBacklogRows = 500_000

// mysqlStream 由一个协程(pump)**不停地**把事件从连接上读走,放进自己的队列,Next 从队列里取。
//
// 不能让"读连接"跟着"重放"一起停:迁移暂停或被限流时重放不取事件,整个实例的写入照样涌进
// binlog,解码库的缓冲(1 万个事件)一满就不再读 socket,库端写不出去,net_write_timeout
// (默认 60 秒)一到就断开连接 —— 恢复后读到 "io.ReadFull ... EOF: connection was bad",
// 迁移失败。
//
// 队列只为本表的改动占内存:只推进位点的事件(别的表、无关语句)连续出现时合并成一个,只留
// 最新位点 —— 重放与 cut-over 对它们只关心"读到哪了"。
type mysqlStream struct {
	syncer   *replication.BinlogSyncer
	streamer *replication.BinlogStreamer
	file     string // 当前所在的 binlog 文件(RotateEvent 时更新);只有 pump 读写
	schema   string
	table    string
	maxRows  int

	mu    sync.Mutex
	queue []Event
	rows  int   // queue 里的行变更数
	err   error // pump 停下的原因;队列取空之后才交给 Next,不越过它前面的事件
	ready chan struct{}

	cancel context.CancelFunc
	done   chan struct{}
}

func (s *mysqlStream) pump(ctx context.Context) {
	defer close(s.done)
	for {
		be, err := s.streamer.GetEvent(ctx)
		if err != nil {
			if ctx.Err() == nil {
				s.stop(apimsg.Errorf("读取 binlog: %w", err))
			}
			return
		}
		ev, ok, err := s.convert(be)
		if err != nil {
			s.stop(err)
			return
		}
		if ok && !s.push(ev) {
			return
		}
	}
}

// push queues ev; false 表示积压超限,流已停下。
func (s *mysqlStream) push(ev Event) bool {
	s.mu.Lock()
	defer s.signal()
	defer s.mu.Unlock()
	if posOnly(ev) && len(s.queue) > 0 && posOnly(s.queue[len(s.queue)-1]) {
		s.queue[len(s.queue)-1] = Event{Pos: ev.Pos}
		return true
	}
	if posOnly(ev) {
		ev = Event{Pos: ev.Pos}
	}
	s.queue = append(s.queue, ev)
	s.rows += len(ev.Rows)
	if s.rows > s.maxRows {
		s.err = apimsg.Errorf("binlog 中积压的本表变更超过 %d 行仍未重放(迁移暂停或限流过久),为免耗尽网关内存,迁移中止", s.maxRows)
		return false
	}
	return true
}

func (s *mysqlStream) stop(err error) {
	s.mu.Lock()
	if s.err == nil {
		s.err = err
	}
	s.mu.Unlock()
	s.signal()
}

func (s *mysqlStream) signal() {
	select {
	case s.ready <- struct{}{}:
	default:
	}
}

// posOnly: 这个事件对重放只意味着位点前进。
func posOnly(ev Event) bool { return len(ev.Rows) == 0 && !ev.DDL }

// Next returns the next event. 超时返回 ctx.Err(),流仍然可用。
func (s *mysqlStream) Next(ctx context.Context) (Event, error) {
	for {
		s.mu.Lock()
		if len(s.queue) > 0 {
			ev := s.queue[0]
			s.queue[0] = Event{} // 放掉行数据的引用
			s.queue = s.queue[1:]
			s.rows -= len(ev.Rows)
			s.mu.Unlock()
			return ev, nil
		}
		err := s.err
		s.mu.Unlock()
		if err != nil {
			return Event{}, err
		}
		select {
		case <-ctx.Done():
			return Event{}, ctx.Err()
		case <-s.ready:
		}
	}
}

func (s *mysqlStream) Close() error {
	s.cancel()
	s.syncer.Close()
	<-s.done
	return nil
}

// convert turns one decoded binlog event into an Event. ok=false 表示这个事件不推进位点
// (库端开流时发的那几个伪事件:LogPos 为 0)。
func (s *mysqlStream) convert(be *replication.BinlogEvent) (Event, bool, error) {
	h := be.Header
	if re, isRotate := be.Event.(*replication.RotateEvent); isRotate {
		s.file = string(re.NextLogName)
		return Event{Pos: Position{File: s.file, Pos: re.Position}}, true, nil
	}
	if h.LogPos == 0 {
		return Event{}, false, nil
	}
	ev := Event{Pos: Position{File: s.file, Pos: uint64(h.LogPos)}}

	switch e := be.Event.(type) {
	case *replication.RowsEvent:
		kind, isRows := rowsKind(h.EventType)
		if !isRows {
			return ev, true, nil
		}
		ev.Schema, ev.Table = string(e.Table.Schema), string(e.Table.Table)
		if !strings.EqualFold(ev.Schema, s.schema) || !strings.EqualFold(ev.Table, s.table) {
			return ev, true, nil // 别的表:只要位点
		}
		rows, err := rowChanges(kind, e.Rows)
		if err != nil {
			return Event{}, false, apimsg.Errorf("binlog 位点 %s: %w", ev.Pos, err)
		}
		ev.Rows = rows
	case *replication.QueryEvent:
		if touchesTable(string(e.Query), string(e.Schema), s.schema, s.table) {
			ev.Schema, ev.Table, ev.DDL = s.schema, s.table, true
		}
	}
	return ev, true, nil
}

func rowsKind(t replication.EventType) (RowKind, bool) {
	switch t {
	case replication.WRITE_ROWS_EVENTv0, replication.WRITE_ROWS_EVENTv1, replication.WRITE_ROWS_EVENTv2:
		return RowInsert, true
	case replication.UPDATE_ROWS_EVENTv0, replication.UPDATE_ROWS_EVENTv1, replication.UPDATE_ROWS_EVENTv2:
		return RowUpdate, true
	case replication.DELETE_ROWS_EVENTv0, replication.DELETE_ROWS_EVENTv1, replication.DELETE_ROWS_EVENTv2:
		return RowDelete, true
	}
	return 0, false
}

// rowChanges turns the decoder's rows into RowChanges. UPDATE 的行是前后镜像成对出现的。
func rowChanges(kind RowKind, rows [][]any) ([]RowChange, error) {
	for _, r := range rows {
		for _, v := range r {
			if !replayableValue(v) {
				// 例如 binlog_row_value_options=PARTIAL_JSON 时的 JSON 差量:不是一个完整的值,
				// 写回去会写错。宁可中止。
				return nil, apimsg.Errorf("行镜像里有无法原样重放的值(%T)", v)
			}
		}
	}
	var out []RowChange
	switch kind {
	case RowInsert:
		for _, r := range rows {
			out = append(out, RowChange{Kind: RowInsert, After: r})
		}
	case RowDelete:
		for _, r := range rows {
			out = append(out, RowChange{Kind: RowDelete, Before: r})
		}
	case RowUpdate:
		if len(rows)%2 != 0 {
			return nil, errors.New("UPDATE 行镜像不成对")
		}
		for i := 0; i < len(rows); i += 2 {
			out = append(out, RowChange{Kind: RowUpdate, Before: rows[i], After: rows[i+1]})
		}
	}
	return out, nil
}

// replayableValue: 解码库给出的、我们知道怎么原样写回的值类型。
func replayableValue(v any) bool {
	switch v.(type) {
	case nil, int8, int16, int32, int64, int, uint8, uint16, uint32, uint64,
		float32, float64, string, []byte:
		return true
	}
	return false
}

// 不改表、也不改数据的语句。
var noOpStmt = regexp.MustCompile(`(?i)^\s*(BEGIN|COMMIT|ROLLBACK|XA\s|SAVEPOINT|RELEASE\s+SAVEPOINT|SET\s|FLUSH\s)`)

// touchesTable 说这条语句事件是否可能改动了 schema.table(结构或者数据)。宁可多报:
// 误报的代价是一次迁移中止重来,漏报的代价是按错位的列把数据写进影子表。
//
// 只看表名有没有作为一个完整的标识符出现在语句里;带了库名限定、而库名不是本库的,不算。
func touchesTable(query, defaultSchema, schema, table string) bool {
	if noOpStmt.MatchString(query) {
		return false
	}
	q := strings.ToLower(stripSQLComments(query))
	t := strings.ToLower(table)
	for i := 0; ; {
		j := strings.Index(q[i:], t)
		if j < 0 {
			return false
		}
		start, end := i+j, i+j+len(t)
		i = start + 1
		if !identBoundary(q, start-1) || !identBoundary(q, end) {
			continue
		}
		// 前面紧跟着 `库名`. 或 库名. 时,按那个库名判断
		qual := qualifierBefore(q, start)
		if qual == "" {
			if strings.EqualFold(defaultSchema, schema) || defaultSchema == "" {
				return true
			}
			continue
		}
		if strings.EqualFold(qual, schema) {
			return true
		}
	}
}

// identBoundary: q[i] 不是标识符字符(或越界)。反引号算边界。
func identBoundary(q string, i int) bool {
	if i < 0 || i >= len(q) {
		return true
	}
	c := q[i]
	return !(c == '_' || c == '$' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80)
}

// qualifierBefore 取表名前面的库名限定(`db`.`t` 或 db.t),没有返回空串。
func qualifierBefore(q string, start int) string {
	i := start - 1
	if i >= 0 && q[i] == '`' {
		i--
	}
	if i < 0 || q[i] != '.' {
		return ""
	}
	i--
	end := i + 1
	if i >= 0 && q[i] == '`' {
		end = i
		i--
		for i >= 0 && q[i] != '`' {
			i--
		}
		return q[i+1 : end]
	}
	for i >= 0 && !identBoundary(q, i) {
		i--
	}
	return q[i+1 : end]
}

var sqlComment = regexp.MustCompile(`(?s)/\*.*?\*/|--[^\n]*|#[^\n]*`)

// stripSQLComments 去掉注释,避免注释里提到表名就误报。MySQL 的 /*!80000 ... */ 版本注释
// 是会执行的,保留它的内容。
func stripSQLComments(q string) string {
	return sqlComment.ReplaceAllStringFunc(q, func(c string) string {
		if strings.HasPrefix(c, "/*!") {
			return c[3 : len(c)-2]
		}
		return " "
	})
}

// replicaServerID 给这条 binlog 连接一个 server_id。同一台库上两个复制连接用了同一个 id,
// 库端会踢掉先来的那一个 —— 所以取一个随机的大数,避开常见的人工编号段。
func replicaServerID() uint32 {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return 1_000_000_000 + binary.LittleEndian.Uint32(b[:])%1_000_000_000
}

// quietLogger 吞掉解码库自己的日志:它在每次连接 / 断开时打 INFO,网关的日志里不需要这些;
// 真正的错误会作为 error 返回给调用方。
var quietLogger = slog.New(slog.NewTextHandler(io.Discard, nil))
