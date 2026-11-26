package bootstrap

import (
	"fmt"

	"gorm.io/gorm"

	"aegisdb/internal/sqlmigrate"
	"aegisdb/migrations"
)

// Migrate brings the database schema up to date.
//
//   - MySQL (production): applies the embedded, versioned SQL migrations in
//     migrations/*.sql, tracked in the schema_migrations table. This is the
//     authoritative schema for prod and never silently ALTERs an existing table.
//   - PostgreSQL: the same, from migrations/pg/*.sql (baseline = MySQL 0001-0064).
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
		return fmt.Errorf("unknown database driver: %q", cfg.Database.Driver)
	}
	// Reference DATA that a later release introduced has to be backfilled here
	// too, not only from Seed: production upgrades run `migrate` while `seed` is
	// a first-install-only step (config.prod.yaml keeps it off). A capability or
	// dictionary row that is merely ABSENT reads as "allow", so shipping an
	// environment without backfilling it leaves that environment unregulated
	// (ED1). Idempotent, so it is safe on every run.
	//
	// The tier/environment split: without these rows no environment resolves to
	// a tier, and connection edits would be refused outright on an upgraded
	// install. It also mirrors the gli/uat rule rows (backfillGliEnv).
	if err := backfillEnvTiers(db); err != nil {
		return err
	}
	// …and for the release pipeline + review library: an absent menu key reads as
	// denied for everyone, and an empty rule library passes every script.
	if err := seedPipelineReference(db); err != nil {
		return err
	}
	// 历史审批单的执行时刻:不回填的话,旧行为下已经跑过的命令会重新变成"可执行"。
	// 不回填的话,那批历史单会带着"可以被再跑一次"的状态留在库里。
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
