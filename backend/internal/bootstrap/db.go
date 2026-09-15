package bootstrap

import (
	"fmt"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// OpenDB connects to the gateway's own PostgreSQL store. It does NOT create
// tables — schema is Migrate's job, and it is the only one that has it.
//
// There used to be a second schema source here: a boot-time GORM auto-migration
// driven by a hand-kept model list, which ran on dev/sqlite while prod applied
// migrations/*.sql. Two sources, each free to drift. db.go recorded what that
// cost: the tier_code rename landed on one side only, the model-inferred schema
// added an empty tier_code next to the populated env, and an empty value reads
// as "allow" in both rule lookups — the gateway came up ungoverned with every
// health check green. migrations/*.sql is now the only thing that defines a
// table; automigrate_guard_test.go fails if that machinery comes back.
func OpenDB(cfg *Config) (*gorm.DB, error) {
	gcfg := &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Warn)}

	db, err := gorm.Open(postgres.Open(cfg.Database.PostgresDSN), gcfg)
	if err != nil {
		return nil, fmt.Errorf("connect postgres (库是否已建? `createdb vela_gateway`): %w", err)
	}
	return db, nil
}
