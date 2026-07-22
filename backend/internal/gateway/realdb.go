package gateway

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	_ "github.com/glebarez/go-sqlite" // sqlite (dev / tests)
	mysqldrv "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"           // postgres / GaussDB(DWS)
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
func engineDriver(conn *model.Connection) (driver, dsn string, ok bool) {
	e := strings.ToLower(conn.Engine)
	pw, err := crypto.DecryptSecret(conn.Password)
	if err != nil {
		return "", "", false
	}
	// Reject a malformed database name for the networked engines (not SQLite).
	if !strings.Contains(e, "sqlite") && !dbNameRe.MatchString(conn.Database) {
		return "", "", false
	}
	switch {
	case strings.Contains(e, "sqlite"):
		return "sqlite", conn.Database, conn.Database != ""
	case strings.Contains(e, "tidb") || strings.Contains(e, "mysql") || strings.Contains(e, "mariadb"):
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
	case strings.Contains(e, "postgre") || strings.Contains(e, "dws") || strings.Contains(e, "gauss"):
		// lib/pq does NOT support sslmode=prefer (a libpq/pgx feature), so start
		// with require (TLS) and let dialPool fall back to disable when the server
		// has no SSL — emulating "prefer": TLS if available, else plaintext.
		dsn = fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=require connect_timeout=8",
			pqEscape(conn.Host), conn.Port, pqEscape(conn.Username), pqEscape(pw), pqEscape(conn.Database))
		return "postgres", dsn, conn.Username != ""
	case strings.Contains(e, "oracle"):
		return "oracle", goora.BuildUrl(conn.Host, conn.Port, conn.Database, conn.Username, pw, nil), conn.Username != ""
	}
	return "", "", false
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
		return nil, err
	}
	return db, nil
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
