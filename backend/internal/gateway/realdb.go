package gateway

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	_ "github.com/glebarez/go-sqlite" // sqlite (dev / tests)
	mysqldrv "github.com/go-sql-driver/mysql"
	"github.com/lib/pq"                // postgres / GaussDB(DWS) — named for NoticeHandler
	goora "github.com/sijms/go-ora/v2" // oracle (pure Go, no instant client)

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
	// PolarDB (MySQL edition), TiDB and MariaDB all speak the MySQL protocol.
	case strings.Contains(e, "mysql"), strings.Contains(e, "mariadb"),
		strings.Contains(e, "tidb"), strings.Contains(e, "polardb"):
		return familyMySQL
	}
	return ""
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
		svc, opts := oracleTarget(conn.Database)
		return "oracle", goora.BuildUrl(conn.Host, conn.Port, svc, conn.Username, pw, opts), conn.Username != ""
	}
	return "", "", false
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
		return nil, nil, fmt.Errorf("引擎 %q 未配置真实执行(需填写连接凭据)", conn.Engine)
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
	db.SetConnMaxLifetime(5 * time.Minute)
	db.SetConnMaxIdleTime(2 * time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, oracleHint(err)
	}
	return db, nil
}

// oracleHint rewrites go-ora's cryptic "empty SID and service name" into an
// actionable message: the Oracle "数据库名" field must hold the service name
// (or "sid/你的SID" for a SID connection).
func oracleHint(err error) error {
	if err != nil && strings.Contains(err.Error(), "empty SID and service name") {
		return fmt.Errorf("Oracle 未指定目标库:请在「数据库名」填写服务名 service name(或用 sid/你的SID 指定 SID)")
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

// RealRun executes sql against the real target instance within the given timeout
// (0 falls back to 30s). A read returns the actual result set (columns + up to
// maxResultRows rows, marking Truncated if there are more); a write returns
// rows-affected.
func RealRun(conn *model.Connection, query string, timeout time.Duration) (ExecResult, error) {
	db, release, err := openConn(conn)
	if err != nil {
		return ExecResult{}, err
	}
	defer release()
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
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
	if IsRead(query) || !KnownVerb(ParseVerb(query)) {
		rows, err := db.QueryContext(ctx, query)
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
			return ExecResult{Output: "执行成功"}, nil
		}
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		data := [][]string{}
		truncated := false
		for rows.Next() {
			if len(data) >= maxResultRows {
				truncated = true
				break
			}
			if err := rows.Scan(ptrs...); err != nil {
				return ExecResult{}, err
			}
			rec := make([]string, len(cols))
			for i, v := range vals {
				rec[i] = cellString(v)
			}
			data = append(data, rec)
		}
		if err := rows.Err(); err != nil {
			return ExecResult{}, err
		}
		// 敏感字段在**离开网关之前**打码。放在这里而不是让上层各自处理:整个网关
		// 只有 RealRun 与 RealQueryEach 两处把行读出来,在这里做,新加的调用路径也
		// 天然被覆盖 —— 不会有人"忘了加脱敏"。
		masked := maskResultSet(query, cols, data)
		out := fmt.Sprintf("+ %s rows", thousands(len(data)))
		if truncated {
			out = fmt.Sprintf("+ %s+ rows (显示前 %d)", thousands(maxResultRows), maxResultRows)
		}
		return ExecResult{Output: out, Rows: len(data), Columns: cols, Data: data,
			Truncated: truncated, MaskedColumns: masked}, nil
	}
	res, err := db.ExecContext(ctx, query)
	if err != nil {
		return ExecResult{}, err
	}
	aff, _ := res.RowsAffected()
	return ExecResult{Output: fmt.Sprintf("执行成功 · %d 行受影响", aff), Rows: int(aff)}, nil
}

// RealQueryEach streams the real result set: onHeader is called once with the
// column names, then onRow once per row (as strings). No row limit — the caller
// decides how to chunk/write (see the export part-writer) and supplies the
// whole-job timeout (was hard-coded 30min here; now that the export row cap is
// configurable, a legitimate export can outlast any fixed value).
func RealQueryEach(conn *model.Connection, query string, timeout time.Duration, onHeader func([]string) error, onRow func([]string) error) error {
	return realQueryEach(conn, query, timeout, false, onHeader, onRow)
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
func RealQueryEachRaw(conn *model.Connection, query string, timeout time.Duration, onHeader func([]string) error, onRow func([]string) error) error {
	return realQueryEach(conn, query, timeout, true, onHeader, onRow)
}

func realQueryEach(conn *model.Connection, query string, timeout time.Duration, raw bool, onHeader func([]string) error, onRow func([]string) error) error {
	db, release, err := openConn(conn)
	if err != nil {
		return err
	}
	defer release()
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	err = func() error {
		rows, err := db.QueryContext(ctx, query)
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
		if raw {
			// 已批准的敏感字段导出:原值写出去。这一行是这套机制里唯一让明文通过的
			// 地方,它的授权来自一张有人签字的审批单。
			maskRow = func([]string) {}
		}
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		rec := make([]string, len(cols))
		for rows.Next() {
			if err := rows.Scan(ptrs...); err != nil {
				return err
			}
			for i, v := range vals {
				rec[i] = cellString(v)
			}
			maskRow(rec) // 敏感字段在写出去之前就已经是打码后的
			if err := onRow(rec); err != nil {
				return err
			}
		}
		return rows.Err()
	}()
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
	drv, dsn, ok := engineDriver(conn)
	if !ok {
		return 0, fmt.Errorf("引擎 %q 未配置真实执行(需填写连接凭据)", conn.Engine)
	}
	db, err := dialPool(drv, dsn)
	if err != nil {
		return 0, err
	}
	if drv == "sqlite" {
		defer db.Close() // sqlite handles are one-off (not pooled)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// A dedicated connection so the NOTICE handler is scoped to this run and not
	// left on a pooled connection for the next caller.
	sc, err := db.Conn(ctx)
	if err != nil {
		return 0, err
	}
	defer sc.Close()
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
	res, err := sc.ExecContext(ctx, query)
	if err != nil {
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
