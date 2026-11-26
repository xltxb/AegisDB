package bootstrap

// 测试存储库跑在 testpg 起的临时 PostgreSQL 上。
//
// 模板库每个测试进程只建一次:跑 pg 迁移、按开发环境播种、种测试夹具;之后每个测试用
// CREATE DATABASE … TEMPLATE 复制一份,测试结束 DROP … WITH (FORCE)(都由 testpg 管)。

import (
	"testing"

	"gorm.io/gorm"

	"aegisdb/internal/repository"
	"aegisdb/internal/testpg"
)

// seededStore 给当前测试建一个独立的、已迁移 + 播种 + 种好夹具的 PG 存储库,返回它的 DSN。
func seededStore(t *testing.T) string {
	t.Helper()
	_, dsn := testpg.FromTemplate(t, "seeded", func(dsn string) error {
		return buildSeededStore(t, dsn)
	})
	return dsn
}

// buildSeededStore 在模板库上跑迁移、播种与夹具。用一份固定配置,不受首个调用方 opts 影响。
func buildSeededStore(t *testing.T, dsn string) error {
	c := &Config{}
	c.Database.Driver = "postgres"
	c.Database.PostgresDSN = dsn
	// 迁移后的一次性回填不把"全局严格模式关"折进分层 —— 夹具一直是按"没有显式关掉
	// 严格模式"的安装来断言的(从前的 SQLite 走 AutoMigrate,根本不跑那一步)。
	c.Gateway.StrictMode = true
	db, err := OpenDB(c)
	if err != nil {
		return err
	}
	if s, e := db.DB(); e == nil {
		defer s.Close()
	}
	if err := Migrate(c, db); err != nil {
		return err
	}
	repo := repository.New(db)
	// Seed 只在本地开发时种(M6):EnvRaw 留空即开发环境。
	if err := Seed(repo, c); err != nil {
		return err
	}
	seedTestFixtures(t, repo) // demo users/connections/approvals/audit the suite asserts on
	return nil
}

// migratedStore points cfg at a fresh PG store that went through the production
// schema path (OpenDB + Migrate, as `server migrate` does) but holds no seed data,
// and opens it. cfg.Gateway.StrictMode must be off: the template is migrated
// with it off (the one-off strict-mode backfill depends on it).
func migratedStore(t *testing.T, cfg *Config) *gorm.DB {
	t.Helper()
	if cfg.Gateway.StrictMode {
		t.Fatal("migratedStore: the template is migrated with gateway.strict_mode off")
	}
	_, dsn := testpg.FromTemplate(t, "booted", func(dsn string) error {
		c := &Config{}
		c.Database.Driver = "postgres"
		c.Database.PostgresDSN = dsn
		db, err := OpenDB(c)
		if err != nil {
			return err
		}
		if s, e := db.DB(); e == nil {
			defer s.Close()
		}
		return Migrate(c, db)
	})
	cfg.Database.Driver = "postgres"
	cfg.Database.PostgresDSN = dsn
	cfg.Database.AutoMigrate = false
	db, err := OpenDB(cfg)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if sqlDB, derr := db.DB(); derr == nil {
		t.Cleanup(func() { sqlDB.Close() })
	}
	return db
}
