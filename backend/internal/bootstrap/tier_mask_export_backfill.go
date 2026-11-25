package bootstrap

import (
	"gorm.io/gorm"

	"aegisdb/internal/model"
)

// tierMaskExportGuard marks that dev / uat have been switched off once (migration 0056).
const tierMaskExportGuard = "envtier.maskExport.backfilled"

// backfillTierMaskExport turns sensitive-field masking and the data-query export restriction OFF on
// the dev and uat tiers, exactly once (2026-10-29).
//
// Migration 0056 adds both columns defaulting to TRUE, so every tier — built-in or custom — starts
// protected. Development and rehearsal data does not need the protection, which is the point of the
// switch; prod, staging (pre-release) and gli (legal) hold production data or copies of it and stay on.
//
// Once, guarded by a setting row, for the same reason as backfillStrictNoWhere: an operator who later
// turns masking back on for uat must not lose it at the next restart.
func backfillTierMaskExport(db *gorm.DB) error {
	var done int64
	if err := db.Model(&model.Setting{}).Where("k = ?", tierMaskExportGuard).Count(&done).Error; err != nil {
		return err
	}
	if done > 0 {
		return nil
	}
	if err := db.Model(&model.EnvTier{}).Where("code IN ?", []string{model.EnvDev, model.EnvUat}).
		Updates(map[string]any{"mask_sensitive": false, "biz_export_restrict": false}).Error; err != nil {
		return err
	}
	return db.Create(&model.Setting{K: tierMaskExportGuard, V: "1"}).Error
}
