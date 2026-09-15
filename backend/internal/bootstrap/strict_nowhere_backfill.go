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
	// 全新的库没有「遗留值」可折。
	//
	// 这个折叠要保住的是一台**已经在跑**的网关上那个显式关掉的全局开关。而三条启动路径
	// 现在都是先 Migrate 再播种,所以它每次都会先在一个空库上被调用一次 —— 那时分层刚由
	// builtinTiers 建出来,它们身上的值就是出厂默认(PROD 开、DEV 关),不是任何人的历史
	// 选择。在那上面折,等于让一份 dev 配置里的 strict_mode: false 把 PROD 的无 WHERE
	// 闸门直接关掉,而没有任何人做过这个决定 —— 正是 ED1 那一类「闸门缺席即放行」的失效。
	//
	// 记成「做过了」而不改任何行:新库上这件事本来就没有内容,下一次重启也不该再来一遍。
	fresh, err := databaseIsUnseeded(db)
	if err != nil {
		return err
	}
	// Only the "was explicitly off" case needs writing; TRUE is already the
	// column default, so the common upgrade touches no rows at all.
	if !legacyStrict && !fresh {
		if err := db.Model(&model.EnvTier{}).
			Where("strict_nowhere = ?", true).
			Update("strict_nowhere", false).Error; err != nil {
			return err
		}
	}
	return db.Create(&model.Setting{K: strictBackfillGuard, V: "1"}).Error
}
