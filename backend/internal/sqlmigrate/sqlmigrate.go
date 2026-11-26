// Package sqlmigrate applies the embedded, versioned SQL migrations
// (migrations/*.sql for MySQL, migrations/pg/*.sql for PostgreSQL) and tracks
// them in the schema_migrations ledger. It is a leaf package so both the
// bootstrap code and test helpers can use it without an import cycle.
package sqlmigrate

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"aegisdb/pkg/sqlutil"
)

// schemaMigration is the ledger row tracking which SQL migrations have been
// applied, so re-running `migrate` / `init` only applies what's pending.
type schemaMigration struct {
	Version   string    `gorm:"column:version;primaryKey;size:255"`
	AppliedAt time.Time `gorm:"column:applied_at"`
}

func (schemaMigration) TableName() string { return "schema_migrations" }

// Run applies every pending *.sql file from srcFS in lexical order,
// recording each applied version in schema_migrations. Each version is applied
// at most once (the ledger) and never concurrently (a MySQL advisory lock), so
// non-idempotent statements (ALTER/INSERT in future migrations) are safe. Do NOT
// rely on `docker-entrypoint-initdb.d` to run these files too — that would apply
// them without a ledger entry and replay them on the next `migrate`.
func Run(db *gorm.DB, srcFS fs.FS) error {
	// Serialize concurrent migrators (e.g. multi-replica deploy hooks) with a
	// MySQL advisory lock so two processes can't apply the same version and
	// collide on the schema_migrations primary key (R14). PostgreSQL takes its own
	// advisory lock below.
	if db.Dialector.Name() == "mysql" {
		// GET_LOCK / RELEASE_LOCK are SESSION-scoped, so they must run on the SAME
		// physical connection, and the lock must stay held for the whole migration.
		// SetMaxOpenConns(1) doesn't guarantee connection IDENTITY — if the pooled
		// connection is dropped and recreated mid-migration the session lock is lost
		// silently. Instead pin ONE dedicated *sql.Conn and hold it end-to-end (B4).
		// The DDL itself may run on other pool connections; the lock only needs to
		// stay held to exclude other processes.
		sqlDB, err := db.DB()
		if err != nil {
			return fmt.Errorf("get sql.DB: %w", err)
		}
		ctx := context.Background()
		conn, err := sqlDB.Conn(ctx)
		if err != nil {
			return fmt.Errorf("pin migration connection: %w", err)
		}
		defer conn.Close() // returns the pinned connection to the pool

		var got int
		if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK('aegisdb_schema_migrate', 60)").Scan(&got); err != nil {
			return fmt.Errorf("acquire migration lock: %w", err)
		}
		if got != 1 {
			return fmt.Errorf("could not acquire migration lock (another migration is running)")
		}
		// Released on the SAME connection (LIFO: runs before conn.Close). A result
		// other than 1 means the lock wasn't held at release — surface it.
		defer func() {
			var released int
			if err := conn.QueryRowContext(ctx, "SELECT RELEASE_LOCK('aegisdb_schema_migrate')").Scan(&released); err != nil {
				slog.Warn("release migration lock failed", "err", err)
			} else if released != 1 {
				slog.Warn("migration lock not held at release (connection may have dropped)", "result", released)
			}
		}()
	}

	if db.Dialector.Name() == "postgres" {
		// 与上面 MySQL 的 GET_LOCK 同一用意、同一写法:会话级咨询锁,钉在一条专用连接上
		// 从头持有到尾。pg_advisory_lock 会无限等,所以用 try 版本轮询,60 秒拿不到就放弃。
		release, err := pgMigrationLock(db)
		if err != nil {
			return err
		}
		defer release()
	}

	if err := db.AutoMigrate(&schemaMigration{}); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	entries, err := fs.Glob(srcFS, "*.sql")
	if err != nil {
		return fmt.Errorf("list migrations: %w", err)
	}
	sort.Strings(entries)

	applied := map[string]bool{}
	var rows []schemaMigration
	if err := db.Find(&rows).Error; err != nil {
		return fmt.Errorf("read schema_migrations: %w", err)
	}
	for _, r := range rows {
		applied[r.Version] = true
	}

	pending := 0
	for _, name := range entries {
		if applied[name] {
			continue
		}
		raw, err := fs.ReadFile(srcFS, name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		stmts := splitSQLStatements(string(raw))
		for i, stmt := range stmts {
			if err := db.Exec(stmt).Error; err != nil {
				return fmt.Errorf("migration %s statement %d failed: %w", name, i+1, err)
			}
		}
		if err := db.Create(&schemaMigration{Version: name, AppliedAt: time.Now()}).Error; err != nil {
			return fmt.Errorf("record migration %s: %w", name, err)
		}
		slog.Info("migration applied", "version", name, "statements", len(stmts))
		pending++
	}
	if pending == 0 {
		slog.Info("schema up to date — no pending migrations")
	} else {
		slog.Info("migrations complete", "applied", pending)
	}
	return nil
}

// splitSQLStatements splits a migration file into statements. It is quote/comment
// aware (a `;` inside a string literal or comment won't split a statement — the
// DELIMITER-free files here still benefit once migrations carry data INSERTs).
func splitSQLStatements(sqlText string) []string {
	out := []string{}
	for _, s := range sqlutil.SplitStatements(sqlText) {
		// Skip connection-scoping statements: the runner is already connected to
		// the target database (per the DSN), and `USE <fixed-name>` would wrongly
		// switch schemas for deployments that use a non-default database name.
		up := strings.ToUpper(s)
		if strings.HasPrefix(up, "CREATE DATABASE") || strings.HasPrefix(up, "USE ") {
			continue
		}
		out = append(out, s)
	}
	return out
}

// pgMigrationLockKey 是迁移用的咨询锁键(任意常量,只要全网关一致)。
const pgMigrationLockKey = 0x61656769 // "aegi"

// pgMigrationLock 在一条钉住的连接上拿 PG 会话级咨询锁,返回释放函数。
func pgMigrationLock(db *gorm.DB) (func(), error) {
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("get sql.DB: %w", err)
	}
	ctx := context.Background()
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("pin migration connection: %w", err)
	}
	deadline := time.Now().Add(60 * time.Second)
	for {
		var got bool
		if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", pgMigrationLockKey).Scan(&got); err != nil {
			conn.Close()
			return nil, fmt.Errorf("acquire migration lock: %w", err)
		}
		if got {
			break
		}
		if time.Now().After(deadline) {
			conn.Close()
			return nil, fmt.Errorf("could not acquire migration lock (another migration is running)")
		}
		time.Sleep(500 * time.Millisecond)
	}
	return func() {
		var released bool
		if err := conn.QueryRowContext(ctx, "SELECT pg_advisory_unlock($1)", pgMigrationLockKey).Scan(&released); err != nil {
			slog.Warn("release migration lock failed", "err", err)
		} else if !released {
			slog.Warn("migration lock not held at release (connection may have dropped)")
		}
		conn.Close()
	}, nil
}
