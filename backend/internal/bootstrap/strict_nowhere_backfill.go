package bootstrap

import (
	"gorm.io/gorm"

	"velagateway/internal/model"
)

// strictBackfillGuard marks that the legacy global strict switch has already
// been folded into the per-tier flags.
const strictBackfillGuard = "envtier.strictNoWhere.backfilled"

// backfillStrictNoWhere carries the retired global strict switch onto the tiers,
// exactly once.
//
// Before migration 0030, "block DELETE / UPDATE with no WHERE" was one
// process-wide boolean read from gateway.strict_mode. The column added by that
// migration defaults to TRUE, which reproduces the shipped default (strict_mode
// is true in both config files) on every tier. The one case the column default
// cannot express is a deployment that deliberately set strict_mode to false:
// SQL cannot read a YAML file, so it is folded in here.
//
// Running exactly once is the whole design. Re-applying it on every boot would
// mean an operator who turns the flag back on for PROD loses it at the next
// restart — the flag would look editable and silently refuse to stay edited. The
// guard row is what makes the fold a one-time migration step rather than a
// policy that keeps reasserting itself.
func backfillStrictNoWhere(db *gorm.DB, legacyStrict bool) error {
	var done int64
	if err := db.Model(&model.Setting{}).Where("k = ?", strictBackfillGuard).Count(&done).Error; err != nil {
		return err
	}
	if done > 0 {
		return nil
	}
	// Only the "was explicitly off" case needs writing; TRUE is already the
	// column default, so the common upgrade touches no rows at all.
	if !legacyStrict {
		if err := db.Model(&model.EnvTier{}).
			Where("strict_nowhere = ?", true).
			Update("strict_nowhere", false).Error; err != nil {
			return err
		}
	}
	return db.Create(&model.Setting{K: strictBackfillGuard, V: "1"}).Error
}
