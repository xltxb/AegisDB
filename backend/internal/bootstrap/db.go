package bootstrap

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"aegisdb/internal/model"
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
	case "postgres":
		db, err = gorm.Open(postgres.Open(cfg.Database.PostgresDSN), gcfg)
		if err != nil {
			return nil, fmt.Errorf("connect postgres: %w", err)
		}
	case "sqlite":
		// busy_timeout lets writers wait instead of failing "database is locked",
		// and WAL improves reader/writer concurrency (M6). Append with the right
		// separator so a path that already carries query params keeps its pragmas.
		// temp_store(MEMORY): bulk INSERT ... RETURNING needs temp tables, and the
		// default puts them in a temp directory that may not be writable (read-only
		// containers, sandboxes), failing with SQLITE_CANTOPEN (issue #92).
		dsn := cfg.Database.SQLitePath
		sep := "?"
		if strings.Contains(dsn, "?") {
			sep = "&"
		}
		dsn += sep + "_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=temp_store(MEMORY)"
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
		// This path never goes through Migrate, so the fold of the retired global
		// strict switch has to hang off it too — the same reason autoMigrate
		// carries the other backfills.
		if err := backfillStrictNoWhere(db, cfg.Gateway.StrictMode); err != nil {
			if sqlDB, derr := db.DB(); derr == nil {
				_ = sqlDB.Close()
			}
			return nil, err
		}
		// 迁移 0056 的 dev / uat 关闭,理由同上:这条路不经过 Migrate。
		if err := backfillTierMaskExport(db); err != nil {
			if sqlDB, derr := db.DB(); derr == nil {
				_ = sqlDB.Close()
			}
			return nil, err
		}
		// 迁移 0058 的 dev / uat 打开「允许自动执行」,同上。
		if err := backfillTierAutoExecute(db); err != nil {
			if sqlDB, derr := db.DB(); derr == nil {
				_ = sqlDB.Close()
			}
			return nil, err
		}
	} else if cfg.Database.AutoMigrate && sqlMigrated(cfg.Database.Driver) {
		slog.Warn("ignoring auto_migrate: schema is owned by SQL migrations — run `server migrate`/`server init`", "driver", cfg.Database.Driver)
	}
	return db, nil
}

// shouldAutoMigrate reports whether boot-time GORM AutoMigrate should run. It is
// refused for MySQL and PostgreSQL: those schemas are authoritative in
// migrations/*.sql and migrations/pg/*.sql, and a second AutoMigrate source would
// drift prod tables to model inference (B6/H13).
func shouldAutoMigrate(driver string, want bool) bool {
	return want && !sqlMigrated(driver)
}

// sqlMigrated:这个驱动的表结构由手写 SQL 迁移决定(而不是按模型 AutoMigrate)。
func sqlMigrated(driver string) bool { return driver == "mysql" || driver == "postgres" }

// allModels is the full set of GORM models, used by AutoMigrate (dev/sqlite boot
// and the migrator's sqlite fallback). Keep in sync with migrations/*.sql, which
// is authoritative for MySQL/production.
var allModels = []any{
	&model.Role{}, &model.User{}, &model.RoleMenu{}, &model.RoleMember{}, &model.QueryGrant{}, &model.QueryGrantApproval{},
	&model.RoleCapability{}, &model.Connection{}, &model.RiskCommand{},
	&model.Approval{}, &model.ApprovalStep{}, &model.AuditLog{},
	&model.WebhookConfig{}, &model.WebhookDelivery{}, &model.SchemaObject{}, &model.Setting{},
	&model.Notification{}, &model.RoleTag{}, &model.ExportJob{}, &model.OSCJob{}, &model.ScriptUpload{},
	&model.AsyncJob{}, &model.UserTag{}, &model.TerminalSnippet{},
	&model.EnvTier{}, &model.Environment{}, &model.ExecWindow{}, &model.ExecWindowDB{},
	&model.SQLReviewRule{}, &model.Pipeline{}, &model.PipelineStage{},
	&model.Release{}, &model.ReleaseStage{}, &model.APIClient{}, &model.Project{}, &model.DatabaseProject{}, &model.SensitiveColumn{},
	&model.MetaTable{}, &model.MetaColumn{}, &model.MetaSync{},
	&model.InspectionReport{},
}

// storeMovedMarker 与 storemigrate.MovedMarkerTable 同名(本包不依赖 storemigrate)。
const storeMovedMarker = "aegisdb_store_moved"

// RefuseIfStoreMoved:存储库已经搬到 PostgreSQL 后,网关却连到了旧 MySQL 上 —— 拒绝启动。
//
// 那份 MySQL 停在搬迁时刻,在它上面照常服务会接受写入,两边从此分叉(审计、工单都会丢一半)。
// 通常是 aegisdb.env 里的 AEGISDB_DB_DRIVER=postgres 丢了。真要回退到 MySQL,删掉标记表再启动。
func RefuseIfStoreMoved(cfg *Config, db *gorm.DB) error {
	if cfg.Database.Driver != "mysql" || !db.Migrator().HasTable(storeMovedMarker) {
		return nil
	}
	var row struct {
		MovedAt time.Time
		Target  string
	}
	db.Raw("SELECT moved_at, target FROM " + storeMovedMarker + " ORDER BY moved_at DESC LIMIT 1").Scan(&row)
	return fmt.Errorf("这个 MySQL 存储库已于 %s 搬到 PostgreSQL(%s),不能再在它上面启动:"+
		"检查 aegisdb.env 里的 AEGISDB_DB_DRIVER=postgres 与 AEGISDB_POSTGRES_DSN。"+
		"确实要回退到 MySQL 时,先在 MySQL 上 DROP TABLE %s(切到 PG 之后的数据不在这里)",
		row.MovedAt.Format("2006-01-02 15:04:05"), row.Target, storeMovedMarker)
}
