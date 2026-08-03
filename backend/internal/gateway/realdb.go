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
	// PolarDB is MySQL-compatible, so it speaks the MySQL protocol.
	case strings.Contains(e, "mysql"), strings.Contains(e, "mariadb"),
		strings.Contains(e, "tidb"), strings.Contains(e, "polardb"):
		return familyMySQL
	case strings.Contains(e, "postgre"), strings.Contains(e, "dws"), strings.Contains(e, "gauss"):
		return familyPostgres
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
		cfg.ReadTimeout = 60 * time.Second
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
	if IsRead(query) {
		rows, err := db.QueryContext(ctx, query)
		if err != nil {
			return ExecResult{}, err
		}
		defer rows.Close()
		cols, err := rows.Columns()
		if err != nil {
			return ExecResult{}, err
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
		out := fmt.Sprintf("+ %s rows", thousands(len(data)))
		if truncated {
			out = fmt.Sprintf("+ %s+ rows (显示前 %d)", thousands(maxResultRows), maxResultRows)
		}
		return ExecResult{Output: out, Rows: len(data), Columns: cols, Data: data, Truncated: truncated}, nil
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
// decides how to chunk/write (see the export part-writer).
func RealQueryEach(conn *model.Connection, query string, onHeader func([]string) error, onRow func([]string) error) error {
	db, release, err := openConn(conn)
	if err != nil {
		return err
	}
	defer release()
	// generous timeout for large exports
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
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
		if err := onRow(rec); err != nil {
			return err
		}
	}
	return rows.Err()
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
