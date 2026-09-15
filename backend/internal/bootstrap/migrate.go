package bootstrap

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"velagateway/migrations"
	"velagateway/pkg/sqlutil"
)

// schemaMigration is the ledger row tracking which SQL migrations have been
// applied, so re-running `migrate` / `init` only applies what's pending.
type schemaMigration struct {
	Version   string    `gorm:"column:version;primaryKey;size:255"`
	AppliedAt time.Time `gorm:"column:applied_at"`
}

func (schemaMigration) TableName() string { return "schema_migrations" }

// Migrate brings the database schema up to date. It applies the embedded,
// versioned SQL migrations in migrations/*.sql, tracked in the
// schema_migrations table. That SQL is the one authoritative schema — in every
// environment — and it never silently ALTERs an existing table.
func Migrate(cfg *Config, db *gorm.DB) error {
	if err := RunSQLMigrations(db, migrations.FS); err != nil {
		return err
	}
	// Reference DATA that a later release introduced has to be backfilled here
	// too, not only from Seed: production upgrades run `migrate` while `seed` is
	// a first-install-only step (config.prod.yaml keeps it off). A capability or
	// dictionary row that is merely ABSENT reads as "allow", so shipping an
	// environment without backfilling it leaves that environment unregulated
	// (ED1). Idempotent, so it is safe on every run.
	if err := backfillGliEnv(db); err != nil {
		return err
	}
	// Same reasoning for the tier/environment split: without these rows no
	// environment resolves to a tier, and connection edits would be refused
	// outright on an upgraded install.
	if err := backfillEnvTiers(db); err != nil {
		return err
	}
	// …and for the release pipeline + review library: an absent menu key reads as
	// denied for everyone, and an empty rule library passes every script.
	if err := seedPipelineReference(db); err != nil {
		return err
	}
	// 历史审批单的执行时刻:不回填的话,旧行为下已经跑过的命令会重新变成"可执行"。
	// 从前 schema 有两条路(生产走 SQL 迁移,dev/测试走 GORM 自动建表),这个回填两条
	// 都得挂,漏掉任何一条,那条路上的库就带着一批可以被再跑一次的历史单。现在只剩
	// 这一条路,而 serve / migrate / init 三个入口全都经过它。
	if err := backfillApprovalExecuted(db); err != nil {
		return err
	}
	// 历史行里非规范大小写的邮箱。三条建号路径今天都折叠了,所以这只对升级上来的
	// 库有事可做;留着不修的代价是一颗哑雷,见该函数。
	if err := backfillLowerEmail(db); err != nil {
		return err
	}
	// 把退役的全局严格模式折进各分层。只有"显式关掉"的部署需要写库,见该函数。
	return backfillStrictNoWhere(db, cfg.Gateway.StrictMode)
}

// RunSQLMigrations applies every pending *.sql file from srcFS in lexical order,
// recording each applied version in schema_migrations. Each version is applied
// at most once (the ledger) and never concurrently (a PostgreSQL advisory lock),
// so non-idempotent statements (ALTER/INSERT in future migrations) are safe. Do
// NOT rely on `docker-entrypoint-initdb.d` to run these files too — that would
// apply them without a ledger entry and replay them on the next `migrate`.
func RunSQLMigrations(db *gorm.DB, srcFS fs.FS) error {
	// Serialize concurrent migrators (e.g. multi-replica deploy hooks) with a
	// PostgreSQL advisory lock so two processes can't apply the same version and
	// collide on the schema_migrations primary key (R14). There is only one
	// dialect now, so the lock is unconditional — no branch to fall past.
	//
	// pg_advisory_lock / pg_advisory_unlock are SESSION-scoped, so they must run
	// on the SAME physical connection, and the lock must stay held for the whole
	// migration. SetMaxOpenConns(1) doesn't guarantee connection IDENTITY — if the
	// pooled connection is dropped and recreated mid-migration the session lock is
	// lost silently. Instead pin ONE dedicated *sql.Conn and hold it end-to-end
	// (B4). The DDL itself may run on other pool connections; the lock only needs
	// to stay held to exclude other processes.
	//
	// The lock is keyed by hashtext() of a fixed name rather than a literal
	// number. Advisory locks are scoped to the DATABASE, not to the schema and not
	// to the whole cluster (pg_locks carries the database oid; a session in another
	// database takes the same key freely). So the people who can collide with this
	// key are whatever else runs inside vela_gateway — and a hand-picked integer is
	// a collision waiting for the next person who picks the same one.
	//
	// Two properties of hashtext() worth knowing, neither of which changes the
	// choice: it returns int4, so widening to bigint spends only 32 bits of the
	// 64-bit key space; and it is an undocumented internal function whose output is
	// not guaranteed stable across PostgreSQL major versions. The latter is
	// harmless here because the expression is evaluated SERVER-side — every
	// migrator talking to one server derives the same key, which is the only
	// agreement this lock needs.
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

	// 用 try 版本而不是 pg_advisory_lock:后者无限阻塞,会把「另一个迁移正在跑」
	// 变成一次没有任何输出的挂起。try 立即返回 boolean,外面包一个有上限的重试,
	// 保住 MySQL 版 GET_LOCK(..., 60) 的等待语义。
	const lockSQL = `SELECT pg_try_advisory_lock(hashtext('vela_schema_migrate')::bigint)`
	deadline := time.Now().Add(60 * time.Second)
	var got bool
	for {
		if err := conn.QueryRowContext(ctx, lockSQL).Scan(&got); err != nil {
			return fmt.Errorf("acquire migration lock: %w", err)
		}
		if got {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("could not acquire migration lock (another migration is running)")
		}
		time.Sleep(500 * time.Millisecond)
	}
	// Released on the SAME connection (LIFO: runs before conn.Close). A false
	// result means the lock wasn't held at release — surface it.
	defer func() {
		var released bool
		if err := conn.QueryRowContext(ctx,
			`SELECT pg_advisory_unlock(hashtext('vela_schema_migrate')::bigint)`).Scan(&released); err != nil {
			slog.Warn("release migration lock failed", "err", err)
		} else if !released {
			slog.Warn("migration lock not held at release (connection may have dropped)")
		}
	}()

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
