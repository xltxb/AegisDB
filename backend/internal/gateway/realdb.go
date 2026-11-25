package gateway

import (
	"context"
	"errors"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "github.com/glebarez/go-sqlite" // sqlite (dev / tests)
	mysqldrv "github.com/go-sql-driver/mysql"
	"github.com/lib/pq"                // postgres / GaussDB(DWS) — named for NoticeHandler
	goora "github.com/sijms/go-ora/v2" // oracle (pure Go, no instant client)

	"velagateway/internal/apimsg"
	"velagateway/internal/model"
	"velagateway/pkg/crypto"
)

// dbNameRe restricts a target database/schema name to a safe identifier so it
// can never smuggle extra DSN parameters (e.g. `db?multiStatements=true` to
// bypass the single-statement approval gate). SQLite file paths are exempt.
var dbNameRe = regexp.MustCompile(`^[A-Za-z0-9_$.-]*$`)

// pqEscape quotes a value for a libpq key=value DSN: wrap in single quotes and
// backslash-escape embedded quotes/backslashes.
func pqEscape(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `'`, `\'`)
	return "'" + v + "'"
}

// engineDriver maps a connection's engine to a database/sql driver + DSN, and
// reports whether real execution is configured (credentials present, or a
// sqlite file). Supported: MySQL, TiDB, PostgreSQL/GaussDB(DWS), Oracle, SQLite.
// The stored password is decrypted here (it is AES-encrypted at rest).
// Engine families: the wire protocols this gateway can actually speak. The
// Engine field on a connection is free text (chosen in the console, or imported
// from a CSV), so it is normalised to one of these before a driver is picked.
const (
	familyMySQL    = "mysql"
	familyPostgres = "postgres"
	familyOracle   = "oracle"
	familySQLite   = "sqlite"
	// MongoDB is judged by its own dialect (see dialect.go). It has no driver
	// here yet, so it resolves to a family without being executable — a
	// connection is refused rather than silently simulated.
	familyMongo = "mongo"
)

// engineFamily maps an engine label to the wire protocol used to reach it, or ""
// when this gateway cannot drive it at all.
//
// Deciding this in one place rather than through a chain of substring checks
// inside engineDriver matters because labels overlap: a name can contain more
// than one product word, and whichever branch happened to be written first would
// silently win and pick the wrong protocol. It also gives "which engines are
// actually supported" a single answer the console can be checked against.
//
// Redis and ClickHouse deliberately resolve to "": no driver here, and the risk
// engine would have nothing meaningful to say about them. MongoDB DOES resolve to
// a family because it has its own judgement dialect (dialect.go) — but no driver
// yet, so engineDriver still reports it as not executable rather than attaching
// it to a protocol it cannot speak.
func engineFamily(engine string) string {
	e := strings.ToLower(strings.TrimSpace(engine))
	switch {
	case e == "":
		return ""
	case strings.Contains(e, "sqlite"):
		return familySQLite
	case strings.Contains(e, "oracle"):
		return familyOracle
	case strings.Contains(e, "mongo"):
		return familyMongo
	// Postgres markers are checked BEFORE the MySQL list on purpose: PolarDB ships
	// both a MySQL-compatible and a PostgreSQL-compatible edition, and both carry
	// "polardb" in the engine label. Matching "polardb" first sent PolarDB for
	// PostgreSQL to the MySQL driver, where it cannot connect at all.
	case strings.Contains(e, "postgre"), strings.Contains(e, "dws"), strings.Contains(e, "gauss"):
		return familyPostgres
	// PolarDB / Aurora (MySQL editions), TiDB and MariaDB all speak the MySQL protocol.
	case strings.Contains(e, "mysql"), strings.Contains(e, "mariadb"),
		strings.Contains(e, "tidb"), strings.Contains(e, "polardb"), strings.Contains(e, "aurora"):
		return familyMySQL
	}
	return ""
}

// IsSupportedEngine:网关能驱动的引擎(MySQL / PostgreSQL / Oracle / SQLite 四个家族)。
// 新建连接按它把关(工单 02 第 10 条):认不出的引擎存进去不会报错,而是悄悄变成一台模拟
// 连接 —— 管理员以为挂上了一台库,其实什么都没连上。MongoDB 不在其列:判定层读的是 SQL。
func IsSupportedEngine(engine string) bool {
	switch engineFamily(engine) {
	case familyMySQL, familyPostgres, familyOracle, familySQLite:
		return true
	}
	return false
}

// IsAurora:Aurora MySQL —— 只读副本走存储层复制,从库延迟的读法不同(见 osc.AuroraReplicaLagOf)。
func IsAurora(engine string) bool {
	return engineFamily(engine) == familyMySQL && strings.Contains(strings.ToLower(engine), "aurora")
}

func engineDriver(conn *model.Connection) (driver, dsn string, ok bool) {
	e := strings.ToLower(conn.Engine)
	pw, err := crypto.DecryptSecret(conn.Password)
	if err != nil {
		return "", "", false
	}
	// Reject a malformed database name for the networked engines (not SQLite, and
	// not Oracle whose field may carry a "sid/…" prefix — validated in its branch).
	if !strings.Contains(e, "sqlite") && !strings.Contains(e, "oracle") && !dbNameRe.MatchString(conn.Database) {
		return "", "", false
	}
	switch engineFamily(e) {
	case familySQLite:
		return "sqlite", conn.Database, conn.Database != ""
	case familyMySQL:
		cfg := mysqldrv.NewConfig()
		cfg.User = conn.Username
		cfg.Passwd = pw
		cfg.Net = "tcp"
		cfg.Addr = fmt.Sprintf("%s:%d", conn.Host, conn.Port)
		cfg.DBName = conn.Database
		cfg.ParseTime = true
		cfg.Loc = time.Local
		cfg.Params = map[string]string{"charset": "utf8mb4"}
		cfg.Timeout = 8 * time.Second
		// NO cfg.ReadTimeout. It is a PER-SOCKET-READ deadline shared by every
		// caller of this pooled DSN, so the 60s value it used to carry silently
		// capped any MySQL operation whose server goes quiet for a minute — a big
		// export's sort phase before the first row, or an async-channel statement
		// advertised as "30–60min+" — at 60s, regardless of the caller's own
		// budget. The driver kills the connection mid-packet and the job dies
		// with a cryptic "unexpected EOF"/"invalid connection". Every call path
		// already carries a bounded context (QueryContext/ExecContext), which is
		// the driver's supported cancellation mechanism, so per-operation budgets
		// belong there; the connect timeout above still bounds dialing.
		// Prefer TLS to the target DB (encrypt when the server supports it, fall
		// back to plaintext otherwise) so credentials/results aren't needlessly
		// sent in the clear. AllowMultiStatements stays false so the approval
		// gate's one-statement guarantee cannot be bypassed via the DSN.
		cfg.TLSConfig = "preferred"
		return "mysql", cfg.FormatDSN(), conn.Username != ""
	case familyPostgres:
		// PostgreSQL must connect to a specific database. When none is configured,
		// default to "postgres" (the maintenance DB that almost always exists) —
		// otherwise libpq defaults dbname to the USER, which usually doesn't exist
		// ("database <user> does not exist").
		dbName := strings.TrimSpace(conn.Database)
		if dbName == "" {
			dbName = "postgres"
		}
		// lib/pq does NOT support sslmode=prefer (a libpq/pgx feature), so start
		// with require (TLS) and let dialPool fall back to disable when the server
		// has no SSL — emulating "prefer": TLS if available, else plaintext.
		dsn = fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=require connect_timeout=8",
			pqEscape(conn.Host), conn.Port, pqEscape(conn.Username), pqEscape(pw), pqEscape(dbName))
		return "postgres", dsn, conn.Username != ""
	case familyOracle:
		// Oracle identifies the target DB by a SERVICE NAME (go-ora default) or a
		// SID. The "数据库名" field carries it; prefix "sid/" (or "sid:") to connect
		// by SID, e.g. "sid/ORCL". An empty/unsafe value yields empty params so
		// go-ora surfaces a clear error (translated by oracleHint on ping).
		// go-ora 的 BuildUrl 不转义 server,带 / ? # 的地址能往连接串里塞选项(TRACE FILE 能在网关主机
		// 上写文件)。保存时已校验;这里挡住校验之前存下的老数据和未保存的连接测试(2026-11 审计 V-07)。
		if !ValidHost(conn.Host) {
			return "", "", false
		}
		svc, opts := oracleTarget(conn.Database)
		return "oracle", goora.BuildUrl(conn.Host, conn.Port, svc, conn.Username, pw, opts), conn.Username != ""
	}
	return "", "", false
}

var validHostRe = regexp.MustCompile(`^[A-Za-z0-9._\-:\[\]]+$`)

// ValidHost 报告 host 是不是一个地址的样子:域名、IPv4、IPv6(可带方括号)。下划线要放行 ——
// docker-compose / k8s 的服务名常带下划线。
func ValidHost(host string) bool { return validHostRe.MatchString(host) }

// HostChecked 说明这个引擎的 Host 字段要不要做地址校验。SQLite 不连网络(字段不用),
// Mongo 的 Host 可以是逗号分隔的副本集地址。
func HostChecked(engine string) bool {
	f := engineFamily(engine)
	return f != familySQLite && f != familyMongo
}

// oracleTarget parses the Oracle "数据库名" field into go-ora connection params.
// Default: the whole value is a service name. A leading "sid/" or "sid:"
// (case-insensitive) selects SID connection instead. Empty or unsafe input
// yields empty params (go-ora then reports the missing service/SID).
func oracleTarget(field string) (service string, opts map[string]string) {
	v := strings.TrimSpace(field)
	if v == "" {
		return "", nil
	}
	low := strings.ToLower(v)
	if strings.HasPrefix(low, "sid/") || strings.HasPrefix(low, "sid:") {
		if sid := v[4:]; sid != "" && dbNameRe.MatchString(sid) {
			return "", map[string]string{"SID": sid}
		}
		return "", nil
	}
	if dbNameRe.MatchString(v) {
		return v, nil
	}
	return "", nil
}

// RealExecSupported reports whether the connection is set up for real execution.
func RealExecSupported(conn *model.Connection) bool {
	_, _, ok := engineDriver(conn)
	return ok
}

// dbPoolCache holds one *sql.DB per (driver, dsn) for the networked engines, so
// repeated executions reuse an established pool instead of paying a fresh
// connect + ping every time (L3). SQLite is intentionally NOT cached — opening a
// file is cheap, and a cached handle would keep the file open (breaking temp-dir
// cleanup on Windows and delaying releases).
var (
	dbPoolMu    sync.Mutex
	dbPoolCache = map[string]*sql.DB{}
)

// openConn returns a ready *sql.DB plus a release func the caller must defer.
// For pooled (networked) connections release is a no-op (the pool lives on); for
// SQLite it closes the one-off handle.
func openConn(conn *model.Connection) (*sql.DB, func(), error) {
	driver, dsn, ok := engineDriver(conn)
	if !ok {
		return nil, nil, apimsg.Errorf("引擎 %q 未配置真实执行(需填写连接凭据)", conn.Engine)
	}
	if driver == "sqlite" {
		db, err := dialPool(driver, dsn)
		if err != nil {
			return nil, nil, err
		}
		return db, func() { db.Close() }, nil
	}

	key := driver + "\x00" + dsn
	dbPoolMu.Lock()
	if db, found := dbPoolCache[key]; found {
		dbPoolMu.Unlock()
		return db, func() {}, nil
	}
	dbPoolMu.Unlock()

	db, err := dialPool(driver, dsn)
	if err != nil {
		return nil, nil, err
	}
	dbPoolMu.Lock()
	if existing, found := dbPoolCache[key]; found { // lost a concurrent open race
		dbPoolMu.Unlock()
		db.Close()
		return existing, func() {}, nil
	}
	dbPoolCache[key] = db
	dbPoolMu.Unlock()
	return db, func() {}, nil
}

// poolCached 说明这台实例的连接池是否已经在缓存里。
//
// 给连通性探测用:没缓存 = 这次 openConn 会新拨,而新拨的池已经在 openAndPing 里 ping
// 过一次,探测就不必再 ping 第二次 —— 否则每台实例第一轮巡检都是两次往返。
// 这里与 openConn 之间有一个窗口(另一个 goroutine 恰好在其间建了池),后果只是少 ping
// 一次刚被别人 ping 过的池,可以接受。
func poolCached(conn *model.Connection) bool {
	driver, dsn, ok := engineDriver(conn)
	if !ok || driver == "sqlite" {
		return false // SQLite 不进缓存,每次都是新开
	}
	dbPoolMu.Lock()
	defer dbPoolMu.Unlock()
	_, found := dbPoolCache[driver+"\x00"+dsn]
	return found
}

// dialPool opens, configures and verifies a connection pool. For PostgreSQL it
// emulates sslmode=prefer: if the initial TLS (sslmode=require) ping fails because
// the server has no SSL, it retries once with sslmode=disable (plaintext).
func dialPool(driver, dsn string) (*sql.DB, error) {
	db, err := openAndPing(driver, dsn)
	if err != nil && driver == "postgres" && strings.Contains(dsn, "sslmode=require") && isPgNoSSL(err) {
		db, err = openAndPing(driver, strings.Replace(dsn, "sslmode=require", "sslmode=disable", 1))
	}
	return db, err
}

// openAndPing opens a pool, applies limits, and verifies connectivity.
func openAndPing(driver, dsn string) (*sql.DB, error) {
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(3)
	// 空闲连接留 10 分钟、3 条都可以留着。从前 2 分钟就回收(而且默认只留 2 条空闲):隔几分钟
	// 再执行一次,就要重新建连 —— TLS 握手,库端没开 skip_name_resolve 时还要对网关做反向 DNS,
	// 一次建连就能花掉好几秒,看起来像"这条 SQL 在网关上特别慢"。留得久一点不怕连接被悄悄
	// 断掉:驱动的 TCP keep-alive 默认开着(15 秒),MySQL 驱动复用前还会检查连接是否已断。
	db.SetMaxIdleConns(poolMaxIdle)
	db.SetConnMaxLifetime(30 * time.Minute)
	db.SetConnMaxIdleTime(10 * time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, oracleHint(err)
	}
	return db, nil
}

// pingTimeout 一次连通性探测的上限。
//
// 比 openAndPing 里那 8 秒的建连预算略宽:探测要连**并且**确认对面还应答,而巡检
// 一次可能连几十台,每一台卡满都得有个头。
const pingTimeout = 10 * time.Second

// RealPing 真的连一次目标库,并确认对面还应答。
//
// 这个函数存在的理由:在它之前,"测试连通性"走的是 Executor.Test —— 一个睡 120 毫秒
// 然后无条件返回"已接入网关"的模拟实现。于是批量巡检把 88 台实例全报成可连,而其中
// 有的连 dial 都超时。一个永远说好话的巡检比没有巡检更糟:没有巡检时人会自己去试,
// 有一个说"可连"的巡检时人不会。
//
// **Ping 而不是只 openConn**:连接池是缓存的(见 openConn),第二次拿到的是缓存里
// 那个 *sql.DB,它不会重新拨号 —— 只靠 openConn 成功来判断可连,等于在问"我们以前
// 连上过吗"。PingContext 才是"现在还连得上吗"。
func RealPing(conn *model.Connection) (bool, string) {
	// 分类的版本在 conncheck.go 的 Check 里;这里只是把它压回 (bool, string)。
	// 没配凭据仍然报不可连 —— 说"已接入网关"是把"我们没配凭据"说成"那台库是好的"。
	r := Check(conn)
	return r.OK(), r.Msg
}

// oracleHint rewrites go-ora's cryptic "empty SID and service name" into an
// actionable message: the Oracle "数据库名" field must hold the service name
// (or "sid/你的SID" for a SID connection).
func oracleHint(err error) error {
	if err != nil && strings.Contains(err.Error(), "empty SID and service name") {
		return apimsg.Errorf("Oracle 未指定目标库:请在「数据库名」填写服务名 service name(或用 sid/你的SID 指定 SID)")
	}
	return err
}

// isPgNoSSL reports whether a Postgres connect error is "the server has no SSL",
// so a TLS attempt can safely retry in plaintext.
func isPgNoSSL(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "ssl is not enabled on the server")
}

// maxResultRows caps how many result rows a terminal read returns for display, so
// a `SELECT *` on a huge table can't flood the socket or the xterm buffer. Exports
// (RealQueryEach) are unbounded — this only applies to the interactive terminal.
const maxResultRows = 200

// sqlRunner is what RealRun executes through: either the pool (*sql.DB) or one
// pinned connection (*sql.Conn). Both carry these two methods with identical
// signatures, which is what lets the Oracle schema switch pin a session without
// the rest of the function knowing.
type sqlRunner interface {
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error)
}

// oracleTargetSchema returns the schema this execution should run in, or "".
//
// 只对 Oracle 生效,并且名字必须是一个规规矩矩的标识符 —— 它要拼进
// ALTER SESSION SET CURRENT_SCHEMA,不能带引号、分号或空格。校验不过就当没选,
// 让语句在登录用户自己的 schema 里跑(而不是把一段可疑文本拼进 SQL)。
func oracleTargetSchema(conn *model.Connection) string {
	if conn == nil || !IsOracleEngine(conn.Engine) {
		return ""
	}
	s := strings.TrimSpace(conn.TargetSchema)
	if s == "" || !schemaIdentRe.MatchString(s) {
		return ""
	}
	return s
}

// schemaIdentRe 比 objectIdentRe 严:不允许点和连字符。这里拼的是一个 schema 名,
// 不是可能带库名前缀的对象名。
var schemaIdentRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_$#]*$`)

// RealRun executes sql against the real target instance within the given timeout
// (0 falls back to 30s). A read returns the actual result set (columns + up to
// maxResultRows rows, marking Truncated if there are more); a write returns
// rows-affected.
// RealRun 执行一条语句。
//
// ctx 是**调用方的**上下文,不是这里凭空造的:终端要能在语句跑到一半时把它取消掉
// (操作员按了 Ctrl+C)。timeout 仍然叠在它上面 —— 取消和超时是两回事,一个是人说
// 停,一个是等太久了。
func RealRun(ctx context.Context, conn *model.Connection, query string, timeout time.Duration) (ExecResult, error) {
	started := time.Now()
	ph := &phaseRec{}
	ctx = context.WithValue(ctx, phaseKey{}, ph)
	// 这次执行开了专用连接(一批语句、审批单、脚本)就用它,整个执行同一个会话;否则走连接池。
	db := sessionDB(ctx, conn)
	var lease *termLease
	if db == nil && HasTermSession(ctx) {
		// 终端标签页:落在这个标签独占的连接上(见 term_session.go)
		l, err := acquireTermDB(ctx, conn)
		switch {
		case errors.Is(err, errTermConnFull):
			// 这台实例的独占连接已满:直接报错,不退回共享连接池。退回去的话,这个标签开的事务 / 锁 /
			// 会话变量会跟着连接回到池里,被别人的终端、导出、审批借到(2026-10-26 讨论 11,恢复 10-12 的做法)。
			return ExecResult{}, apimsg.Errorf("这台实例的终端会话已满(%d 条),请关闭闲置的终端标签后重试", MaxTermSessionsPerConn)
		case errors.Is(err, ErrTermSessionLimit):
			return ExecResult{}, apimsg.Errorf("你打开的终端标签占用的数据库连接已达上限(%d 条),请关闭不用的标签后重试", MaxTermSessionsPerUser)
		case errors.Is(err, ErrTermSessionClosed):
			return ExecResult{}, apimsg.Errorf("这个终端标签已关闭,语句没有执行")
		case err != nil:
			return ExecResult{}, err
		default:
			lease = l
			defer l.Release()
			db = l.sess.db
		}
	}
	if db == nil {
		pooled, release, err := openConn(conn)
		if err != nil {
			return ExecResult{}, err
		}
		defer release()
		db = pooled
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	res, err := realRun(ctx, db, conn, query)
	switch {
	case lease != nil && lease.Reconnected():
		// 这条语句期间标签的连接被换过:原来的会话已经没了 —— 明说,不让人以为自己还在事务里
		res.Notice = "数据库会话已断开并重新连接:之前未提交的事务已被数据库回滚,会话变量、临时表也已失效"
		res.NoticeRef = model.NewRuleRef("termSessionReset")
	}
	ph.fill(&res, started)
	if total := time.Since(started); total >= slowExecLog {
		st := db.Stats()
		slog.Warn("目标库执行偏慢", "instance", conn.Name, "totalMs", total.Milliseconds(),
			"connMs", res.ConnMs, "queryMs", res.QueryMs, "fetchMs", res.FetchMs, "processMs", res.ProcessMs,
			"poolInUse", st.InUse, "poolIdle", st.Idle, "poolWaitCount", st.WaitCount, "poolWaitMs", st.WaitDuration.Milliseconds())
	}
	// 超时或被取消时,驱动报出来的常常不是 context 的错误,而是被切断的连接:
	// "invalid connection"、"unexpected EOF"。包一层,让上层能 errors.Is 出这是超时 / 取消,
	// 而不是把它说成"数据库执行失败"。
	if err != nil && ctx.Err() != nil && !errors.Is(err, ctx.Err()) {
		err = fmt.Errorf("%w (%v)", ctx.Err(), err)
	}
	return res, err
}

// oracleSchemaRestorer:切 schema 之前调用。读出这条连接**当前**的 CURRENT_SCHEMA,返回一个把它
// 切回去的函数,归还连接池之前调用(defer);读不出来或切不回来,就丢掉这条连接。
//
// 从前切完不切回:连接带着 CURRENT_SCHEMA 回到池里,下一个**没选 schema**的请求借到它 ——
// 可能是别人的终端语句、一次导出、一张审批单的执行 —— 不带 owner 的语句就落在上一个人选的
// schema 上。那个 schema 被删掉之后,借到它的人建表报 ORA-01129(2026-10 测试发现)。
//
// 切回的是**原来的** schema,不是登录用户(SESSION_USER):不少库给应用账号配了登录触发器,登录时
// 就 ALTER SESSION SET CURRENT_SCHEMA 到业务属主。切回 SESSION_USER 的话,这条连接再回到池里,
// 没选 schema 的请求就落在一个空 schema 上,不带 owner 的表一张都找不到(2026-10 审查)。
func oracleSchemaRestorer(ctx context.Context, sc *sql.Conn) func() {
	drop := func() { _ = sc.Raw(func(any) error { return driver.ErrBadConn }) }
	var orig string
	if err := sc.QueryRowContext(ctx, "SELECT SYS_CONTEXT('USERENV', 'CURRENT_SCHEMA') FROM dual").Scan(&orig); err != nil || orig == "" || strings.Contains(orig, `"`) {
		return drop
	}
	return func() {
		rctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := sc.ExecContext(rctx, `ALTER SESSION SET CURRENT_SCHEMA = "`+orig+`"`); err != nil {
			drop()
		}
	}
}

func realRun(ctx context.Context, db *sql.DB, conn *model.Connection, query string) (ExecResult, error) {
	// Oracle 换 schema:必须钉住**同一个物理会话**。
	//
	// ALTER SESSION 改的是会话状态,而 db 是连接池 —— 在池上先发 ALTER SESSION 再发
	// 查询,两条语句可能落在不同的物理连接上,切了等于没切。db.Conn 取一条独占连接,
	// 两条语句都走它,用完**先把 schema 切回来**再归还(见 oracleSchemaRestorer)。
	//
	// 与 RealRunAsync 里为 NOTICE 处理器取独占连接是同一个理由。
	var runner sqlRunner = db
	if schema := oracleTargetSchema(conn); schema != "" {
		sc, cerr := db.Conn(ctx)
		if cerr != nil {
			return ExecResult{}, cerr
		}
		defer sc.Close()
		defer oracleSchemaRestorer(ctx, sc)() // 先于 Close;切换之前就读出原 schema
		if _, aerr := sc.ExecContext(ctx, `ALTER SESSION SET CURRENT_SCHEMA = "`+schema+`"`); aerr != nil {
			return ExecResult{}, apimsg.Errorf("切换 schema 到 %s 失败: %w", schema, aerr)
		}
		runner = sc
	} else {
		// MySQL 系:超时 / 取消时要网关自己发 KILL QUERY,驱动不会(见 killquery.go)。
		r, sc, stop, perr := pinForKill(ctx, db, conn)
		if perr != nil {
			return ExecResult{}, perr
		}
		if sc != nil {
			defer sc.Close()
		}
		defer stop() // 先于 sc.Close 执行:等进行中的 KILL 做完,再把连接还回池子
		runner = r
	}

	// SET SERVEROUTPUT ON(终端带来的开关):DBMS_OUTPUT 的缓冲区在会话上,打开、执行、读回必须
	// 落在同一条物理连接上(见 oracle_serveroutput.go)。
	var outConn *sql.Conn
	if serverOutputOn(ctx) && engineFamily(conn.Engine) == familyOracle {
		sc, pinned := runner.(*sql.Conn)
		if !pinned {
			c, cerr := db.Conn(ctx)
			if cerr != nil {
				return ExecResult{}, cerr
			}
			defer c.Close()
			sc, runner = c, c
		}
		if err := enableServerOutput(ctx, sc); err != nil {
			return ExecResult{}, apimsg.Errorf("打开 DBMS_OUTPUT 失败: %w", err)
		}
		outConn = sc
	}
	// Oracle 的 EXPLAIN PLAN 把计划写进会话级的 PLAN_TABLE,随后要在同一会话里读回来
	if _, pinned := runner.(*sql.Conn); !pinned && engineFamily(conn.Engine) == familyOracle && isOracleExplainPlan(query) {
		c, cerr := db.Conn(ctx)
		if cerr != nil {
			return ExecResult{}, cerr
		}
		defer c.Close()
		runner = c
	}
	// 只读用户的语句:会话设成数据库层面只读,函数里夹带的写由数据库自己拒绝(readonly_guard.go)
	if readOnlyGuarded(ctx) {
		r, restore, gerr := guardReadOnly(ctx, db, runner, conn, query)
		if gerr != nil {
			return ExecResult{}, gerr
		}
		defer restore()
		runner = r
	}
	res, err := runStatement(ctx, runner, conn, query)
	if readOnlyGuarded(ctx) {
		err = explainReadOnly(err)
	}
	err = maskErrorText(query, err, activeSensitiveRules(ctx)) // 报错里的敏感值(H5,见 sensitive_error.go)
	if outConn != nil {
		// 出错时也读:报错之前 PUT_LINE 的内容正是排查要看的东西。用一个新的短时限,
		// 原来那个可能已经因为超时结束了。
		octx, ocancel := context.WithTimeout(context.Background(), 10*time.Second)
		lines, truncated, oerr := collectServerOutput(octx, outConn)
		ocancel()
		// 读回出错(多半是超时):已经读到的行照样给人看,标成截断 —— 从前整个丢掉,人看到的是
		// "没有输出",而实际是"输出很多、没读完"。
		if oerr == nil || len(lines) > 0 {
			res.ServerOutput, res.ServerOutputTruncated = lines, truncated
		}
	}
	return res, err
}

// runStatement 在 runner 上执行一条语句(客户端命令先翻译成 SQL)。
// slowExecLog:总耗时超过它就写一条带分段与连接池状态的日志,排查"为什么这条这么慢"用。
const slowExecLog = 3 * time.Second

// phaseRec 记一次执行的分段时间点(见 ExecResult.ConnMs)。经 ctx 带进 runStatement,免得为了
// 计时把每一层的签名都改一遍。
type phaseRec struct{ ready, sent, fetched, done time.Time }
type phaseKey struct{}

func phaseMark(ctx context.Context, f func(*phaseRec)) {
	if p, _ := ctx.Value(phaseKey{}).(*phaseRec); p != nil {
		f(p)
	}
}

func (p *phaseRec) fill(res *ExecResult, started time.Time) {
	ms := func(a, b time.Time) int {
		if a.IsZero() || b.IsZero() || b.Before(a) {
			return 0
		}
		return int(b.Sub(a).Milliseconds())
	}
	res.ConnMs = ms(started, p.ready)
	res.QueryMs = ms(p.ready, p.sent)
	if p.fetched.IsZero() {
		res.FetchMs = ms(p.sent, p.done)
		return
	}
	res.FetchMs = ms(p.sent, p.fetched)
	res.ProcessMs = ms(p.fetched, p.done)
}

func runStatement(ctx context.Context, runner sqlRunner, conn *model.Connection, query string) (ExecResult, error) {
	// 到这里连接已经拿到并就绪(连接池取出 / 新建、MySQL 记下线程号、Oracle 切 schema)
	now := time.Now()
	phaseMark(ctx, func(p *phaseRec) { p.ready = now })
	defer phaseMark(ctx, func(p *phaseRec) {
		p.done = time.Now()
		if p.sent.IsZero() {
			p.sent = p.done
		}
	})
	// 客户端命令翻译成等价 SQL:SQL*Plus 的 SHOW/DESC/EXEC、psql 的反斜杠、
	// mysql 客户端结尾的 \G。它们都不是 SQL —— 是说给客户端听的话,由客户端解释掉,
	// 从来不会发到服务端。网关走驱动,不翻译就是一个语法错误。
	//
	// 位置是关键:这里已经在**判定之后**了。高危字典与规范审查匹配的是文本里的那个
	// 词,先翻译再判定的话,一条写着 SHOW 的规则就再也匹配不到东西 —— 那是绕过。
	// 所以判定看到的永远是用户输入的原文,只有交给驱动的这一份被改写。
	query = clientCommandSQL(conn.Engine, query)
	// EXPLAIN 只取执行计划,绝不真的执行被解释的语句(见 explain_plan.go)。判定看的仍是原文
	// —— 判定之后才改写,所以 EXPLAIN ANALYZE DELETE 照样按 DELETE 审批,只是最终只取计划。
	if engineFamily(conn.Engine) == familyOracle && isOracleExplainPlan(query) {
		return oracleTerminalExplainPlan(ctx, runner, query)
	}
	notice, noticeRef := "", (*model.RuleRef)(nil)
	if q, changed, perr := planOnlySQL(conn.Engine, query); perr != nil {
		return ExecResult{}, perr
	} else if changed {
		query, notice, noticeRef = q, planOnlyNotice, model.NewRuleRef("explainPlanOnly")
	}
	// 三条路,而不是两条:
	//
	//   已知的读  → Query,取结果集
	//   已知的写  → Exec,拿影响行数(写操作最该看到的就是这个)
	//   认不出来  → **问数据库**:走 Query,数据库返回了列就把行显示出来,没有列
	//               就如实说"执行成功"
	//
	// 第三条是这次加的。每个引擎都有自己的方言关键字(DWS 的 EXPLAIN PERFORMANCE、
	// SQLite 的 PRAGMA、MySQL 里能返回结果集的 CALL),动词表永远补不完 —— 而"这条
	// 语句返回不返回行"根本不必猜:执行一次就知道了。此前认不出来一律按写处理,
	// 语句照样在服务端跑了,结果集却被丢掉,用户看到的就是"命令没有反应"。
	//
	// 注意这里**没有** Query 失败后回退 Exec 的逻辑,这是有意的:Query 报错时无法
	// 判断语句到底执行了没有,再跑一次就可能是重复执行 —— 对一条 INSERT 来说,
	// 重复执行比报错严重得多。报错就照实报错。
	//
	// EXPLAIN 开头的一律取结果集:它的产出就是执行计划。判定为了看清风险,把 EXPLAIN
	// UPDATE 拆成 UPDATE(见 ParseVerb),但"要不要取结果集"不能跟着拆 —— 否则走了
	// Exec,计划被丢掉,终端只显示"执行成功 · 0 行受影响"(2026-10 生产反馈)。
	//
	// 绑定变量(SQL*Plus 的 VARIABLE,见 oracle_binds.go):带了已声明的 :name 的 PL/SQL 块
	// 改走 Exec —— 只有 Exec 能把 OUT 参数的新值带回来。
	bp := planOracleBinds(ctx, conn.Engine, query)
	if (IsRead(query) || isExplain(query) || !KnownVerb(ParseVerb(query))) && !(bp != nil && bp.plsql) {
		rows, err := runner.QueryContext(ctx, query, bp.queryArgs()...)
		sent := time.Now()
		phaseMark(ctx, func(p *phaseRec) { p.sent = sent })
		if err != nil {
			return ExecResult{}, err
		}
		defer rows.Close()
		cols, err := rows.Columns()
		if err != nil {
			return ExecResult{}, err
		}
		// 数据库说"这条语句没有结果集"——那它就不是查询,如实回话即可。
		// 走到这里说明语句已经执行过了,不能再 Exec 一次。
		if len(cols) == 0 {
			// 走这条路的是动词认不出的语句(PL/SQL 块、方言命令):驱动不报影响行数,如实标成未知
			return ExecResult{Output: "执行成功", RowsUnknown: true}, nil
		}
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		data := [][]string{}
		truncated := false
		// Web 命令行(带 WithResultPaging)最多读 maxCachedRows 行 / maxCachedBytes 字节,第一批之外的
		// 暂存给"加载更多"(见 result_cache.go);别的调用方照旧只读 maxResultRows 行。
		paging, paged := pagingFrom(ctx)
		limit := maxResultRows
		if paged {
			limit = maxCachedRows
		}
		bytes := 0
		// Web 命令行读到一半撞上执行超时:把已经读到的行交出去,并说明是被超时截断的。从前(fbe10b8 起
		// 读取上限从 200 提到 5 万行)整条报错、一行不返回 —— Oracle 上不带条件的 SELECT * 以前秒出
		// 200 行,改成要读满才返回后就可能什么都看不到(2026-10-26 讨论 13)。只认超时;人为取消、
		// 驱动报错照旧报错。驱动在超时那一刻可能从 Scan 报,也可能从 rows.Err() 报,两处都认。
		timedOut := false
		partialOnTimeout := func() bool {
			if paged && len(data) > 0 && errors.Is(ctx.Err(), context.DeadlineExceeded) {
				timedOut, truncated = true, true
				return true
			}
			return false
		}
		for rows.Next() {
			if len(data) >= limit || (paged && bytes >= maxCachedBytes) {
				truncated = true
				break
			}
			if err := rows.Scan(ptrs...); err != nil {
				if partialOnTimeout() {
					break
				}
				return ExecResult{}, err
			}
			rec := make([]string, len(cols))
			for i, v := range vals {
				rec[i] = cellString(v)
			}
			bytes += rowMemSize(rec) // 按实际占用估算,不只是文本(见 result_cache.go)
			data = append(data, rec)
		}
		if err := rows.Err(); err != nil && !timedOut && !partialOnTimeout() {
			return ExecResult{}, err
		}
		// 敏感字段在**离开网关之前**打码。放在这里而不是让上层各自处理:整个网关
		// 只有 RealRun 与 RealQueryEach 两处把行读出来,在这里做,新加的调用路径也
		// 天然被覆盖 —— 不会有人"忘了加脱敏"。
		fetched := time.Now()
		phaseMark(ctx, func(p *phaseRec) { p.fetched = fetched })
		var masked []string
		if !maskingOff(ctx) { // 分层关掉了脱敏(masking_switch.go)
			masked = maskResultSet(query, cols, data) // 打码在暂存之前:暂存里没有原值
		}
		if paged {
			// 读到的全部行数;超出读取上限(beyondCap)时真实总数更多,只能说"至少这么多"
			total, beyondCap := len(data), truncated
			res := ExecResult{Columns: cols, Notice: notice, NoticeRef: noticeRef, MaskedColumns: masked,
				TotalRows: total, BeyondCap: beyondCap}
			if total > paging.pageSize {
				res.ResultID = storeResult(paging.userID, cols, data[paging.pageSize:], paging.pageSize)
				data = data[:paging.pageSize]
			}
			res.Data, res.Rows = data, len(data)
			res.Truncated = res.ResultID != "" || beyondCap
			res.Output = fmt.Sprintf("+ %s rows", thousands(total))
			if beyondCap {
				res.Output = fmt.Sprintf("+ %s+ rows", thousands(total))
			}
			if timedOut {
				msg := fmt.Sprintf("读取结果时达到执行超时,只返回已读到的 %s 行;需要完整结果请加条件缩小范围,或用数据导出", thousands(total))
				if res.Notice != "" {
					res.Notice += ";" + msg // 已有一条提示(会话重连等):原样保留它的 ref,这句只进原文
				} else {
					res.Notice = msg
					res.NoticeRef = model.NewRuleRef("termReadTimeout", "n", thousands(total))
				}
			}
			return res, nil
		}
		out := fmt.Sprintf("+ %s rows", thousands(len(data)))
		if truncated {
			out = fmt.Sprintf("+ %s+ rows (显示前 %d)", thousands(maxResultRows), maxResultRows)
		}
		return ExecResult{Output: out, Rows: len(data), Columns: cols, Data: data, Notice: notice, NoticeRef: noticeRef,
			Truncated: truncated, MaskedColumns: masked}, nil
	}
	res, err := runner.ExecContext(ctx, query, bp.execArgs()...)
	if err != nil {
		return ExecResult{}, err
	}
	if bp != nil && bp.plsql {
		// PL/SQL 块:驱动不报影响行数(与上面 Query 那条路的说法一致),带回绑定变量的新值
		return ExecResult{Output: "执行成功", RowsUnknown: true, Binds: bp.updated()}, nil
	}
	// Oracle 建存储程序编译失败也返回成功,要回读 all_errors 才知道(见 oracle_create_errors.go)
	if cerr := checkOracleCreate(ctx, runner, conn.Engine, query); cerr != nil {
		return ExecResult{}, cerr
	}
	aff, _ := res.RowsAffected()
	out := ExecResult{Output: fmt.Sprintf("执行成功 · %d 行受影响", aff), Rows: int(aff),
		OutputRef: model.NewRuleRef(model.OutExecAffected, "n", strconv.FormatInt(aff, 10))}
	if bp != nil { // DML 的 RETURNING … INTO :x
		out.Binds = bp.updated()
	}
	return out, nil
}

// RealQueryEach streams the real result set: onHeader is called once with the
// column names, then onRow once per row (as strings). No row limit — the caller
// decides how to chunk/write (see the export part-writer) and supplies the
// whole-job timeout (was hard-coded 30min here; now that the export row cap is
// configurable, a legitimate export can outlast any fixed value).
func RealQueryEach(ctx context.Context, conn *model.Connection, query string, timeout time.Duration, onHeader func([]string) error, onRow func([]string) error) error {
	return realQueryEach(ctx, conn, query, timeout, false, onHeader, textRows(onRow))
}

// RealQueryEachCells is RealQueryEach with each value TYPED (see Cell): NULL kept
// apart from '', numbers, times and binary told apart — what an SQL export needs
// to write literals that read back as the original data. Masking is the same.
func RealQueryEachCells(ctx context.Context, conn *model.Connection, query string, timeout time.Duration, onHeader func([]string) error, onRow func([]Cell) error) error {
	return realQueryEach(ctx, conn, query, timeout, false, onHeader, onRow)
}

// RealQueryEachCellsRaw is RealQueryEachCells with masking OFF — the same single
// bypass as RealQueryEachRaw, for an approved raw-value export in SQL format.
func RealQueryEachCellsRaw(ctx context.Context, conn *model.Connection, query string, timeout time.Duration, onHeader func([]string) error, onRow func([]Cell) error) error {
	return realQueryEach(ctx, conn, query, timeout, true, onHeader, onRow)
}

// textRows adapts a string-row callback to the typed stream: each cell's text, a
// NULL read as "" — the CSV export's long-standing behaviour.
func textRows(onRow func([]string) error) func([]Cell) error {
	var rec []string
	return func(cells []Cell) error {
		if cap(rec) < len(cells) {
			rec = make([]string, len(cells))
		}
		rec = rec[:len(cells)]
		for i, c := range cells {
			rec[i] = c.Text
		}
		return onRow(rec)
	}
}

// RealQueryEachRaw is RealQueryEach with masking OFF.
//
// 这是整套脱敏机制**唯一**的旁路,所以它有一个刺眼的名字,而不是在 RealQueryEach
// 上加一个默认 false 的参数 —— 后者会让"这一次到底脱没脱敏"藏在一个布尔值里,
// 读代码的人看不出这行调用是不是把身份证号原样写进了 CSV。
//
// 唯一的合法调用方是导出 worker,而且只在任务已经因"包含敏感字段"被批准之后。
// 调用点自己会再核一次审批状态(见 sensitiveExportApproved):这道旁路不能只靠
// "调用方应该先检查"来守 —— 它守的东西一旦漏了就是明文数据流出去。
func RealQueryEachRaw(ctx context.Context, conn *model.Connection, query string, timeout time.Duration, onHeader func([]string) error, onRow func([]string) error) error {
	return realQueryEach(ctx, conn, query, timeout, true, onHeader, textRows(onRow))
}

func realQueryEach(parent context.Context, conn *model.Connection, query string, timeout time.Duration, raw bool, onHeader func([]string) error, onRow func([]Cell) error) (err error) {
	defer func() { err = maskErrorText(query, err, activeSensitiveRules(parent)) }() // H5
	db, release, err := openConn(conn)
	if err != nil {
		return err
	}
	defer release()
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	// parent 是调用方的取消信号(导出任务被人终止);timeout 是整个任务的时限。两者任一到了,
	// 查询都会停 —— MySQL 系另发 KILL QUERY(见 pinForKill),目标库上的语句一起停下。
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	// 与 RealRun 同样的理由:Oracle 选了 schema 就得钉住会话再切,否则导出读到的是
	// 登录用户自己的 schema —— 而终端里看到的是另一个,同一条 SQL 两个结果。
	var runner sqlRunner = db
	if schema := oracleTargetSchema(conn); schema != "" {
		sc, cerr := db.Conn(ctx)
		if cerr != nil {
			return cerr
		}
		defer sc.Close()
		defer oracleSchemaRestorer(ctx, sc)() // 先于 Close:切回原 schema 再还给池子
		if _, aerr := sc.ExecContext(ctx, `ALTER SESSION SET CURRENT_SCHEMA = "`+schema+`"`); aerr != nil {
			return apimsg.Errorf("切换 schema 到 %s 失败: %w", schema, aerr)
		}
		runner = sc
	} else {
		// 导出超时同样要让目标库停下来 —— 一条跑了半小时的导出查询,超时后还在库上跑。
		r, sc, stop, perr := pinForKill(ctx, db, conn)
		if perr != nil {
			return perr
		}
		if sc != nil {
			defer sc.Close()
		}
		defer stop()
		runner = r
	}
	// 导出按定义是读:不论谁导,会话都设成数据库层面只读(readonly_guard.go)。从前不在只读事务里,
	// SELECT 一个会写表的函数,导出一次就写一次。
	gr, restore, gerr := guardReadOnly(ctx, db, runner, conn, query)
	if gerr != nil {
		return gerr
	}
	defer restore()
	runner = gr
	err = func() error {
		rows, err := runner.QueryContext(ctx, query)
		if err != nil {
			return err
		}
		defer rows.Close()
		cols, err := rows.Columns()
		if err != nil {
			return err
		}
		if err := onHeader(cols); err != nil {
			return err
		}
		// 导出走的是流式路径,一行都不会落进内存里的大数组 —— 所以脱敏也必须是
		// 逐行的。列集合在表头就定下来了,打码目标算一次,之后每行都过同一次改写。
		//
		// 导出尤其不能漏:一份 CSV 落到磁盘、发进聊天工具,比终端上看一眼跑得远得多。
		maskRow, _ := maskStream(query, cols)
		if raw || maskingOff(ctx) {
			// 已批准的敏感字段导出,或分层关掉了脱敏:原值写出去。这一行是这套机制里唯一让明文通过的
			// 地方,它的授权来自一张有人签字的审批单。
			maskRow = func([]string) {}
		}
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		// 列的声明类型:驱动把很多值都交成 []byte(MySQL 的数字也是),只看 Go 类型分不出
		// 数字和文本、二进制和文本 —— 导出成 SQL 时这决定了加不加引号、写不写十六进制。
		var typeNames []string
		if cts, cerr := rows.ColumnTypes(); cerr == nil {
			for _, ct := range cts {
				typeNames = append(typeNames, strings.ToUpper(ct.DatabaseTypeName()))
			}
		}
		rec := make([]string, len(cols))
		cells := make([]Cell, len(cols))
		for rows.Next() {
			// 每一行都看一眼取消信号:导出被人终止时,流式读取在下一行就停,不必等驱动 ——
			// 有的驱动(glebarez/go-sqlite)只在准备语句时响应 context,逐行读取时不理会。
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := rows.Scan(ptrs...); err != nil {
				return err
			}
			for i, v := range vals {
				rec[i] = cellString(v)
				tn := ""
				if i < len(typeNames) {
					tn = typeNames[i]
				}
				cells[i] = Cell{Null: v == nil, Kind: cellKind(v, tn)}
			}
			maskRow(rec) // 敏感字段在写出去之前就已经是打码后的
			for i := range cells {
				if cells[i].Kind != KindText && rec[i] != cellString(vals[i]) {
					cells[i].Kind = KindText // 打码改写过的格子是一段文本,不再是数字 / 时间 / 二进制
				}
				cells[i].Text = rec[i]
			}
			if err := onRow(cells); err != nil {
				return err
			}
		}
		return rows.Err()
	}()
	err = explainReadOnly(err)
	// When OUR deadline fired, the driver may surface the killed connection as a
	// cryptic transport error ("unexpected EOF", "invalid connection") instead of
	// the context error, depending on where mid-packet the cut landed. Rewrap so
	// callers can errors.Is the timeout and report it as one.
	if err != nil && ctx.Err() != nil {
		return fmt.Errorf("%w (%v)", ctx.Err(), err)
	}
	return err
}

// RealRunAsync executes a long-running statement on a dedicated connection with a
// caller-supplied timeout, streaming server NOTICE messages (PostgreSQL/DWS
// RAISE NOTICE) to onNotice as they arrive — this is how a 30–60min procedure's
// progress log reaches the async-job viewer. Notices are only captured for the
// pg-family driver; other engines run without live logs. Returns rows-affected
// (best-effort; 0 for statements that don't report it).
func RealRunAsync(conn *model.Connection, query string, timeout time.Duration, onNotice func(string)) (int64, error) {
	return RealRunAsyncGuarded(conn, query, timeout, onNotice, false)
}

// RealRunAsyncGuarded 是 RealRunAsync,readOnly 时在数据库层面只读执行(只读用户提交的后台任务,
// 见 readonly_guard.go)。
func RealRunAsyncGuarded(conn *model.Connection, query string, timeout time.Duration, onNotice func(string), readOnly bool) (int64, error) {
	drv, dsn, ok := engineDriver(conn)
	if !ok {
		return 0, apimsg.Errorf("引擎 %q 未配置真实执行(需填写连接凭据)", conn.Engine)
	}
	db, err := dialPool(drv, dsn)
	if err != nil {
		return 0, err
	}
	// 这是专为这次任务新拨的池,不在 openConn 的缓存里:跑完必须关掉。从前只有 sqlite 关了,
	// 网络引擎每跑一个后台任务就留下一个没人管的池(连接等空闲回收才断,池对象永不释放)。
	// 执行期间不因存活时间被换掉;跑完当场断开。
	defer db.Close()
	db.SetConnMaxLifetime(0)
	db.SetConnMaxIdleTime(0)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// A dedicated connection so the NOTICE handler is scoped to this run and not
	// left on a pooled connection for the next caller.
	sc, err := db.Conn(ctx)
	if err != nil {
		return 0, err
	}
	defer sc.Close()
	// 异步执行动辄几十分钟,超时后更不能让它在库上接着跑(见 killquery.go)。
	if killable(conn) {
		id, terr := mysqlThreadID(ctx, sc)
		if terr != nil {
			return 0, terr
		}
		defer watchKill(ctx, conn, id)() // 先于 sc.Close 执行
	}
	if drv == "postgres" && onNotice != nil {
		_ = sc.Raw(func(dc any) error {
			if c, ok := dc.(driver.Conn); ok {
				pq.SetNoticeHandler(c, func(n *pq.Error) {
					if n != nil {
						onNotice(strings.TrimSpace(n.Message))
					}
				})
			}
			return nil
		})
		// Reset the handler before the conn returns to the pool.
		defer sc.Raw(func(dc any) error {
			if c, ok := dc.(driver.Conn); ok {
				pq.SetNoticeHandler(c, nil)
			}
			return nil
		})
	}
	if readOnly {
		_, restore, gerr := guardReadOnly(ctx, db, sc, conn, query)
		if gerr != nil {
			return 0, gerr
		}
		defer restore()
	}
	res, err := sc.ExecContext(ctx, query)
	if readOnly {
		err = explainReadOnly(err)
	}
	if err != nil {
		if ctx.Err() != nil && !errors.Is(err, ctx.Err()) {
			err = fmt.Errorf("%w (%v)", ctx.Err(), err)
		}
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

func cellString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case []byte:
		return string(x)
	case time.Time:
		return x.Format("2006-01-02 15:04:05")
	default:
		return fmt.Sprintf("%v", x)
	}
}

// cellKind classifies one scanned value for an SQL literal: by its Go type where
// the driver tells (time.Time, bool, numbers), else by the column's declared type
// name — the MySQL driver hands numbers over as []byte. A wrong guess toward
// "number" is caught later: SQLLiteral quotes anything that does not parse as one.
func cellKind(v any, typeName string) CellKind {
	switch v.(type) {
	case time.Time:
		return KindTime
	case bool:
		return KindBool
	case int64, int32, int, float64, float32:
		return KindNumber
	}
	switch {
	case typeName == "":
		return KindText
	case strings.Contains(typeName, "BLOB"), strings.Contains(typeName, "BINARY"),
		typeName == "BYTEA", typeName == "RAW", typeName == "LONG RAW", typeName == "IMAGE":
		return KindBinary
	case typeName == "BOOL", typeName == "BOOLEAN":
		return KindBool
	case strings.Contains(typeName, "INT") && !strings.Contains(typeName, "INTERVAL") && !strings.Contains(typeName, "POINT"),
		strings.Contains(typeName, "DEC"), strings.Contains(typeName, "NUMERIC"), strings.Contains(typeName, "NUMBER"),
		strings.Contains(typeName, "FLOAT"), strings.Contains(typeName, "DOUBLE"), typeName == "REAL", typeName == "MONEY":
		return KindNumber
	case strings.Contains(typeName, "TIMESTAMP"), strings.Contains(typeName, "DATE"), typeName == "TIME":
		return KindTime
	}
	return KindText
}

// isExplain:EXPLAIN 开头,或 DESC / DESCRIBE 后面跟着语句(MySQL 系的同义词)。它总是返回结果集。
func isExplain(query string) bool {
	s := strings.TrimSpace(StripComments(query))
	switch w := strings.ToUpper(firstWord(s)); w {
	case "EXPLAIN":
		return true
	case "DESC", "DESCRIBE":
		return explainHead(strings.TrimSpace(s[len(w):]))
	}
	return false
}
