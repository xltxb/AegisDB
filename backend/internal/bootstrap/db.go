package bootstrap

import (
	"fmt"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"velagateway/internal/model"
)

// OpenDB connects to the gateway's own PostgreSQL store. It does NOT create
// tables — schema is Migrate's job, and it is the only one that has it.
func OpenDB(cfg *Config) (*gorm.DB, error) {
	gcfg := &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Warn)}

	db, err := gorm.Open(postgres.Open(cfg.Database.PostgresDSN), gcfg)
	if err != nil {
		return nil, fmt.Errorf("connect postgres (库是否已建? `createdb vela_gateway`): %w", err)
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
	&model.EnvTier{}, &model.Environment{}, &model.ExecWindow{},
	&model.SQLReviewRule{}, &model.Pipeline{}, &model.PipelineStage{},
	&model.Release{}, &model.ReleaseStage{}, &model.APIClient{}, &model.Project{}, &model.DatabaseProject{}, &model.SensitiveColumn{},
	&model.MetaTable{}, &model.MetaColumn{}, &model.MetaSync{},
}
