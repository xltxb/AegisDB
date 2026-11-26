package bootstrap

import (
	"fmt"
	"log/slog"

	"gorm.io/gorm"

	"aegisdb/internal/model"
	"aegisdb/internal/sqlmigrate"
	"aegisdb/migrations"
)

// Migrate brings the database schema up to date.
//
//   - MySQL (production): applies the embedded, versioned SQL migrations in
//     migrations/*.sql, tracked in the schema_migrations table. This is the
//     authoritative schema for prod and never silently ALTERs an existing table.
//   - PostgreSQL: the same, from migrations/pg/*.sql (baseline = MySQL 0001-0064).
//   - SQLite (dev/tests): the hand-written SQL is MySQL-specific, so fall back to
//     GORM AutoMigrate which understands the sqlite dialect.
func Migrate(cfg *Config, db *gorm.DB) error {
	switch cfg.Database.Driver {
	case "mysql":
		if err := sqlmigrate.Run(db, migrations.FS); err != nil {
			return err
		}
	case "postgres":
		if err := sqlmigrate.Run(db, migrations.PGFS); err != nil {
			return err
		}
	default:
		if err := autoMigrate(db); err != nil {
			return err
		}
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
	// 两条 schema 路径都要挂 —— 生产走 SQL 迁移,dev/测试走 AutoMigrate,漏掉任何
	// 一条,那条路上的库就带着一批可以被再跑一次的历史单。
	if err := backfillApprovalExecuted(db); err != nil {
		return err
	}
	// 历史审计行的两列分类:算得出来的补上,算不出来的留空(见该文件开头)。
	if err := backfillAuditClassification(db); err != nil {
		return err
	}
	// 订阅那一栏换了一维(事件类型 → 增删改查)。不迁的话是一个静默停推。
	if err := backfillWebhookActions(db); err != nil {
		return err
	}
	// 把退役的全局严格模式折进各分层。只有"显式关掉"的部署需要写库,见该函数。
	if err := backfillStrictNoWhere(db, cfg.Gateway.StrictMode); err != nil {
		return err
	}
	// 迁移 0056:dev / uat 上的脱敏与数据查询导出限制关掉一次(见该函数)。
	if err := backfillTierMaskExport(db); err != nil {
		return err
	}
	// 迁移 0058:dev / uat 上打开「允许自动执行」一次(见该函数)。
	return backfillTierAutoExecute(db)
}

// autoMigrate creates/updates every table from the GORM models (dev/sqlite).
func autoMigrate(db *gorm.DB) error {
	if err := renameRuleTierColumns(db); err != nil {
		return err
	}
	if err := db.AutoMigrate(allModels...); err != nil {
		return fmt.Errorf("auto-migrate: %w", err)
	}
	// The AutoMigrate path is dev/tests' only schema step (OpenDB calls it
	// directly), so the reference backfills have to hang off it too — see
	// seedPipelineReference.
	if err := seedPipelineReference(db); err != nil {
		return err
	}
	if err := backfillApprovalExecuted(db); err != nil {
		return err
	}
	if err := backfillAuditClassification(db); err != nil {
		return err
	}
	if err := backfillWebhookActions(db); err != nil {
		return err
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
