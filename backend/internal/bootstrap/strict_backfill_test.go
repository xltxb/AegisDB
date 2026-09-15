package bootstrap

import (
	"testing"

	"velagateway/internal/model"
)

// The column default (TRUE) reproduces the shipped behaviour on upgrade, but it
// cannot express the deployment that deliberately set gateway.strict_mode to
// false: SQL cannot read a YAML file. That case is folded in by
// backfillStrictNoWhere, and it must run EXACTLY ONCE — re-applying it on every
// boot would wipe an operator's later decision to switch a tier back on, so the
// switch would look editable and silently refuse to stay edited.
func TestStrictBackfill_FoldsLegacyOffAndOnlyOnce(t *testing.T) {
	app := newTestApp(t)
	db := app.repo.DB()

	// The harness boots with StrictMode=false, so the fold has already run and
	// stamped its guard. Undo its effect the way an operator would, then prove a
	// second run does not undo the operator.
	if err := db.Model(&model.EnvTier{}).Where("code = ?", "prod").
		Update("strict_nowhere", true).Error; err != nil {
		t.Fatal(err)
	}
	if err := backfillStrictNoWhere(db, false); err != nil {
		t.Fatalf("second run: %v", err)
	}
	var prod model.EnvTier
	if err := db.First(&prod, "code = ?", "prod").Error; err != nil {
		t.Fatal(err)
	}
	if !prod.StrictNoWhere {
		t.Error("re-running the fold wiped a tier the operator had switched back on")
	}
}

// On a database that never carried the legacy switch as "off", the fold writes
// nothing: every tier keeps whatever the column default gave it.
func TestStrictBackfill_LeavesTiersAloneWhenLegacyWasOn(t *testing.T) {
	app := newTestApp(t)
	db := app.repo.DB()

	// Clear the guard so the fold runs again, this time as a legacy-ON install.
	if err := db.Where("k = ?", strictBackfillGuard).Delete(&model.Setting{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.EnvTier{}).Where("code = ?", "dev").
		Update("strict_nowhere", true).Error; err != nil {
		t.Fatal(err)
	}
	if err := backfillStrictNoWhere(db, true); err != nil {
		t.Fatalf("legacy-on fold: %v", err)
	}
	var dev model.EnvTier
	if err := db.First(&dev, "code = ?", "dev").Error; err != nil {
		t.Fatal(err)
	}
	if !dev.StrictNoWhere {
		t.Error("a legacy-ON install must not have any tier switched off by the fold")
	}
}

// 升级那一半:一台**已经在跑**的网关,运维显式关掉的全局开关必须真的被折进各分层。
//
// 这条和 TestStrictNoWhere_IsPerTier 是一对,合起来把 backfillStrictNoWhere 里
// `!legacyStrict && !fresh` 那个条件从两侧钉住:
//
//	· 新库(那条用例):折叠**不能**发生 —— 分层身上的值是 builtinTiers 定的出厂默认
//	  (PROD 开),不是任何人的历史选择。折了就等于一份 dev 配置悄悄关掉 PROD 的闸门。
//	· 老库(这条用例):折叠**必须**发生 —— 那个 false 是运维在分层化(ADR 0013)之前做的决定,
//	  丢掉它就是在重启时替人改回一个他明确关过的开关。
//
// 少了任何一条,把 `!fresh` 写反、或者整段删掉,整套测试都会全绿。
func TestStrictBackfill_UpgradeFoldsAnExplicitOffOntoTheTiers(t *testing.T) {
	app := newTestApp(t) // 播过种 —— 角色表非空,也就是「这不是一个新库」
	db := app.repo.DB()

	// 夹具建库时 backfillStrictNoWhere 已经盖过戳(那一次库还是空的,所以什么都没改)。
	// 把戳清掉,库就成了一台从没折叠过的、正在升级的老库。
	if err := db.Where("k = ?", strictBackfillGuard).Delete(&model.Setting{}).Error; err != nil {
		t.Fatal(err)
	}

	// 前置:PROD 的闸门此刻开着(出厂默认)。不先确认这一点,下面那个断言可能只是
	// 在重复一个本来就成立的状态。
	var prod model.EnvTier
	if err := db.First(&prod, "code = ?", model.EnvProd).Error; err != nil {
		t.Fatal(err)
	}
	if !prod.StrictNoWhere {
		t.Fatal("前置不成立:PROD 的无 WHERE 闸门本该是开着的")
	}

	// 老配置里那句 gateway.strict_mode: false。
	if err := backfillStrictNoWhere(db, false); err != nil {
		t.Fatalf("fold: %v", err)
	}

	if err := db.First(&prod, "code = ?", model.EnvProd).Error; err != nil {
		t.Fatal(err)
	}
	if prod.StrictNoWhere {
		t.Error("升级时那句显式的 strict_mode: false 没有折进 PROD —— " +
			"运维明确关过的开关在升级后自己回来了")
	}

	// 而且这一次要留下戳,否则下一次重启会把运维之后的修改再折一遍。
	var done int64
	db.Model(&model.Setting{}).Where("k = ?", strictBackfillGuard).Count(&done)
	if done != 1 {
		t.Errorf("折叠之后应当留下一个 %q 的戳,实际 %d 条", strictBackfillGuard, done)
	}
}
