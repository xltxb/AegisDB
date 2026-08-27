package bootstrap

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"velagateway/internal/model"
)

// OpenDB connects using the configured driver and runs AutoMigrate.
func OpenDB(cfg *Config) (*gorm.DB, error) {
	gcfg := &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Warn)}

	var db *gorm.DB
	var err error
	switch cfg.Database.Driver {
	case "mysql":
		db, err = gorm.Open(mysql.Open(cfg.Database.MySQLDSN), gcfg)
		if err != nil {
			return nil, fmt.Errorf("connect mysql (是否已启动 MySQL? 见 docker-compose.yml): %w", err)
		}
	case "sqlite":
		// busy_timeout lets writers wait instead of failing "database is locked",
		// and WAL improves reader/writer concurrency (M6). Append with the right
		// separator so a path that already carries query params keeps its pragmas.
		dsn := cfg.Database.SQLitePath
		sep := "?"
		if strings.Contains(dsn, "?") {
			sep = "&"
		}
		dsn += sep + "_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
		db, err = gorm.Open(sqlite.Open(dsn), gcfg)
		if err != nil {
			return nil, fmt.Errorf("open sqlite: %w", err)
		}
		// SQLite allows a single writer; cap the pool to serialize writes and
		// avoid lock contention across the worker pool + HTTP handlers.
		if sqlDB, derr := db.DB(); derr == nil {
			sqlDB.SetMaxOpenConns(1)
		}
	default:
		return nil, fmt.Errorf("unknown database driver: %q", cfg.Database.Driver)
	}

	if shouldAutoMigrate(cfg.Database.Driver, cfg.Database.AutoMigrate) {
		// Go through autoMigrate, NOT db.AutoMigrate directly. This used to call
		// GORM itself, so there were two places that migrated the schema and any
		// step added to the other one was silently missing here — which is exactly
		// what happened to the env→tier_code rename: boot skipped it, AutoMigrate
		// added an empty tier_code beside the populated env, and every rule lookup
		// came back empty. Both lookups read empty as permission granted, so the
		// gateway came up healthy and ungoverned.
		if err := autoMigrate(db); err != nil {
			// Nobody can close a handle they were never handed. Release the pool
			// here or a failed boot leaves the connection (and, on sqlite, a lock
			// on the database file) held for the life of the process.
			if sqlDB, derr := db.DB(); derr == nil {
				_ = sqlDB.Close()
			}
			return nil, err
		}
	} else if cfg.Database.AutoMigrate && cfg.Database.Driver == "mysql" {
		slog.Warn("ignoring auto_migrate for mysql: schema is owned by SQL migrations — run `server migrate`/`server init`")
	}
	return db, nil
}

// shouldAutoMigrate reports whether boot-time GORM AutoMigrate should run. It is
// refused for MySQL: that schema is authoritative in migrations/*.sql, and a
// second AutoMigrate source would drift prod tables to model inference (B6/H13).
func shouldAutoMigrate(driver string, want bool) bool {
	return want && driver != "mysql"
}

// allModels is the full set of GORM models, used by AutoMigrate (dev/sqlite boot
// and the migrator's sqlite fallback). Keep in sync with migrations/*.sql, which
// is authoritative for MySQL/production.
var allModels = []any{
	&model.Role{}, &model.User{}, &model.RoleMenu{}, &model.RoleMember{},
	&model.RoleCapability{}, &model.Connection{}, &model.RiskCommand{},
	&model.Approval{}, &model.ApprovalStep{}, &model.AuditLog{},
	&model.WebhookConfig{}, &model.WebhookDelivery{}, &model.SchemaObject{}, &model.Setting{},
	&model.Notification{}, &model.RoleTag{}, &model.ExportJob{}, &model.ScriptUpload{},
	&model.AsyncJob{}, &model.UserTag{}, &model.TerminalSnippet{},
	&model.EnvTier{}, &model.Environment{},
	&model.SQLReviewRule{}, &model.Pipeline{}, &model.PipelineStage{},
	&model.Release{}, &model.ReleaseStage{}, &model.APIClient{}, &model.Project{},
}
