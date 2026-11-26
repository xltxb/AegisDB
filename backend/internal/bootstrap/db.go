package bootstrap

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"aegisdb/internal/model"
)

// OpenDB connects using the configured driver. It never touches the schema:
// that is owned by migrations/*.sql (MySQL) and migrations/pg/*.sql (PostgreSQL),
// applied by Migrate.
func OpenDB(cfg *Config) (*gorm.DB, error) {
	gcfg := &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Warn)}

	var db *gorm.DB
	var err error
	switch cfg.Database.Driver {
	case "mysql":
		if strings.TrimSpace(cfg.Database.MySQLDSN) == "" {
			return nil, errors.New("MySQL 连接串为空:请设置 AEGISDB_MYSQL_DSN(或配置文件 database.mysql_dsn)")
		}
		db, err = gorm.Open(mysql.Open(cfg.Database.MySQLDSN), gcfg)
		if err != nil {
			return nil, fmt.Errorf("connect mysql (是否已启动 MySQL? 见 docker-compose.yml): %w", err)
		}
	case "postgres":
		// 空串交给 pgx 会回落到 libpq 默认值(本机 socket / 当前系统用户),悄悄连到不该连的库。
		if strings.TrimSpace(cfg.Database.PostgresDSN) == "" {
			return nil, errors.New("PostgreSQL 连接串为空:请设置 AEGISDB_POSTGRES_DSN(见 README「本地开发」)")
		}
		db, err = gorm.Open(postgres.Open(cfg.Database.PostgresDSN), gcfg)
		if err != nil {
			if cfg.IsLocalDev() {
				return nil, fmt.Errorf("connect postgres: 本地开发需要 PostgreSQL(SQLite 存储库已移除,见 README「本地开发」),"+
					"请先启动 PostgreSQL 并设置 AEGISDB_POSTGRES_DSN: %w", err)
			}
			return nil, fmt.Errorf("connect postgres: %w", err)
		}
	default:
		return nil, fmt.Errorf("unknown database driver: %q", cfg.Database.Driver)
	}

	if cfg.Database.AutoMigrate {
		slog.Warn("ignoring auto_migrate: schema is owned by SQL migrations — run `server migrate`/`server init`", "driver", cfg.Database.Driver)
	}
	return db, nil
}

// allModels is the full set of GORM models, kept for tests that check the models
// against the SQL migrations. migrations/*.sql is the authoritative schema.
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
	&model.ChatBinding{}, &model.ChatBindCode{}, &model.ChatCard{},
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

// RefuseIfUnmigrated:本地开发连上了一个从没迁移过的存储库(没有 schema_migrations)时拒绝启动。
// 以前的 SQLite 开发库会自己建表,PG 不会 —— 不拦的话 Seed 报一串缺表错误,网关却照常监听空库。
// 不自动迁移:建表是显式动作。生产环境行为不变。
func RefuseIfUnmigrated(cfg *Config, db *gorm.DB) error {
	if !cfg.IsLocalDev() || db.Migrator().HasTable("schema_migrations") {
		return nil
	}
	return errors.New("存储库还没有初始化:先运行 `server migrate`(见 README「本地开发」)")
}
