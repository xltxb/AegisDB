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

	"velagateway/internal/model"
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

// Migrate brings the database schema up to date.
//
//   - MySQL (production): applies the embedded, versioned SQL migrations in
//     migrations/*.sql, tracked in the schema_migrations table. This is the
//     authoritative schema for prod and never silently ALTERs an existing table.
//   - SQLite (dev/tests): the hand-written SQL is MySQL-specific, so fall back to
//     GORM AutoMigrate which understands the sqlite dialect.
func Migrate(cfg *Config, db *gorm.DB) error {
	if cfg.Database.Driver == "mysql" {
		if err := RunSQLMigrations(db, migrations.FS); err != nil {
			return err
		}
	} else if err := autoMigrate(db); err != nil {
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
	return backfillEnvTiers(db)
}

// autoMigrate creates/updates every table from the GORM models (dev/sqlite).
func autoMigrate(db *gorm.DB) error {
	if err := renameRuleTierColumns(db); err != nil {
		return err
	}
	if err := db.AutoMigrate(allModels...); err != nil {
		return fmt.Errorf("auto-migrate: %w", err)
	}
	slog.Info("schema migrated (auto-migrate)")
	return nil
}

// renameRuleTierColumns renames `env` to `tier_code` on the two rule tables,
// BEFORE AutoMigrate looks at them.
//
// The order is the whole point. AutoMigrate does not rename anything: shown a
// model whose field no longer matches the column, it ADDS `tier_code` and leaves
// `env` in place, still holding the data and still part of the primary key. Every
// rule lookup would then read an empty column and find nothing — and both lookups
// spell "no rows" as permission granted. The database would come up looking
// healthy with every instance ungoverned.
//
// MySQL takes the equivalent step through migration 0016. This path is dev and
// test (sqlite), where the schema comes from the models.
func renameRuleTierColumns(db *gorm.DB) error {
	m := db.Migrator()
	for _, tbl := range []struct {
		model any
		name  string
	}{
		{&model.RoleCapability{}, "tbl_role_capability"},
		{&model.RiskCommand{}, "tbl_risk_command"},
	} {
		if !m.HasTable(tbl.model) {
			continue // fresh database: AutoMigrate creates it with the right name
		}
		// Read the real column list rather than asking about a field the model no
		// longer has. Migrator.HasColumn resolves names through the model schema
		// first, which makes it an unreliable way to ask "is the OLD column still
		// there" — precisely the question here.
		cols, err := m.ColumnTypes(tbl.model)
		if err != nil {
			return fmt.Errorf("read columns of %s: %w", tbl.name, err)
		}
		var hasEnv, hasTier bool
		for _, c := range cols {
			switch c.Name() {
			case "env":
				hasEnv = true
			case "tier_code":
				hasTier = true
			}
		}
		if !hasEnv {
			continue // already renamed, or created new
		}
		if hasTier {
			// Half-migrated: a build that added tier_code without moving the data.
			// tier_code is empty, env still holds the tier codes, and `env` is part
			// of the primary key so it cannot simply be dropped. Refuse rather than
			// start — every rule lookup would read the empty column and find
			// nothing, and nothing is how this system spells "allowed".
			return fmt.Errorf(
				"%s has both `env` and `tier_code`: the schema is half-migrated and rule lookups would read an empty column "+
					"(which reads as PERMITTED). Restore this database from backup and start again with this build", tbl.name)
		}
		if err := db.Exec("ALTER TABLE " + tbl.name + " RENAME COLUMN env TO tier_code").Error; err != nil {
			return fmt.Errorf("rename %s.env → tier_code: %w", tbl.name, err)
		}
		slog.Info("renamed rule column env → tier_code", "table", tbl.name)
	}
	return nil
}

// RunSQLMigrations applies every pending *.sql file from srcFS in lexical order,
// recording each applied version in schema_migrations. Each version is applied
// at most once (the ledger) and never concurrently (a MySQL advisory lock), so
// non-idempotent statements (ALTER/INSERT in future migrations) are safe. Do NOT
// rely on `docker-entrypoint-initdb.d` to run these files too — that would apply
// them without a ledger entry and replay them on the next `migrate`.
func RunSQLMigrations(db *gorm.DB, srcFS fs.FS) error {
	// Serialize concurrent migrators (e.g. multi-replica deploy hooks) with a
	// MySQL advisory lock so two processes can't apply the same version and
	// collide on the schema_migrations primary key (R14). SQLite is single-file
	// and needs no such lock.
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
		if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK('vela_schema_migrate', 60)").Scan(&got); err != nil {
			return fmt.Errorf("acquire migration lock: %w", err)
		}
		if got != 1 {
			return fmt.Errorf("could not acquire migration lock (another migration is running)")
		}
		// Released on the SAME connection (LIFO: runs before conn.Close). A result
		// other than 1 means the lock wasn't held at release — surface it.
		defer func() {
			var released int
			if err := conn.QueryRowContext(ctx, "SELECT RELEASE_LOCK('vela_schema_migrate')").Scan(&released); err != nil {
				slog.Warn("release migration lock failed", "err", err)
			} else if released != 1 {
				slog.Warn("migration lock not held at release (connection may have dropped)", "result", released)
			}
		}()
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
