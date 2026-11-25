package bootstrap

import (
	"gorm.io/gorm"

	"velagateway/internal/model"
)

// tierAutoExecuteGuard marks that dev / uat have been switched on once (migration 0058).
const tierAutoExecuteGuard = "envtier.autoExecute.backfilled"

// backfillTierAutoExecute turns 「允许自动执行」 ON for the dev and uat tiers, exactly once (2026-10-30).
//
// Migration 0058 adds the column defaulting to FALSE, so every tier — built-in or custom — starts
// with every release execution waiting for a human click. Development and rehearsal tiers are where
// that click is pure friction; prod, staging (pre-release) and gli (legal) stay off.
//
// Once, guarded by a setting row, for the same reason as backfillTierMaskExport: an operator who later
// turns it off for uat must not see it come back at the next restart.
func backfillTierAutoExecute(db *gorm.DB) error {
	var done int64
	if err := db.Model(&model.Setting{}).Where("k = ?", tierAutoExecuteGuard).Count(&done).Error; err != nil {
		return err
	}
	if done > 0 {
		return nil
	}
	if err := db.Model(&model.EnvTier{}).Where("code IN ?", []string{model.EnvDev, model.EnvUat}).
		Update("auto_execute", true).Error; err != nil {
		return err
	}
	return db.Create(&model.Setting{K: tierAutoExecuteGuard, V: "1"}).Error
}
