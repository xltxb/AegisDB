package bootstrap

// 管理员删掉的内置分层,不该在下次 migrate 时自己回来。
//
// env-tier-model 工单 01 承诺「种子按表为空判定,而不是逐行补 —— 否则管理员删掉的 tier
// 会在重启时复活」。而 correctBuiltinTiers 逐行遍历五个内置分层,发现哪个不在就
// `Create` 一个,连同同名环境和一整套规则行。
//
// 于是:管理员删掉 uat(因为这套系统里根本没有演练环境)→ 运维执行一次
// `server migrate` → uat 分层、uat 环境、一整套克隆自 staging 的规则,全部原样回来。
// 他会以为是自己没删干净,再删一次,下一次升级再回来。
//
// 难处在于这段代码**本来就是为了补上 uat 而写的**(2026-08-06 那次五分层修正)。所以
// 不能简单改成「表为空才建」—— 那样修正本身就失效了。要分清的是两件事:
//
//   · 补上一个从未存在过的内置分层 —— **一次性**的数据修正,做过就不该再做
//   · 纠正我们自己写错的显示名               —— 幂等,每次都可以跑
//
// 前者用一个标记记住做过了;后者照旧。

import (
	"testing"

	"velagateway/internal/model"
)

func TestBuiltinTiers_ADeletedTierStaysDeleted(t *testing.T) {
	app := newTestApp(t)
	db := app.repo.DB()

	// 第一次升级把 uat 补上 —— 这是既有行为,先确认它还在(TestBuiltinTiers_
	// UpgradeRelabelsAndAddsUat 钉的就是它)。
	var uat model.EnvTier
	if err := db.First(&uat, "code = ?", "uat").Error; err != nil {
		t.Fatalf("前置条件不成立:全新库里应当有 uat: %v", err)
	}

	// 管理员删掉它 —— 连同环境与规则行,就像界面上删一个分层那样。
	db.Where("code = ?", "uat").Delete(&model.EnvTier{})
	db.Where("code = ?", "uat").Delete(&model.Environment{})
	db.Where("tier_code = ?", "uat").Delete(&model.RoleCapability{})
	db.Where("tier_code = ?", "uat").Delete(&model.RiskCommand{})

	// 运维执行一次升级。
	if err := Migrate(app.cfg, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	if err := db.First(&uat, "code = ?", "uat").Error; err == nil {
		t.Error("管理员删掉的分层在 migrate 之后又回来了 —— 他会以为是自己没删干净," +
			"再删一次,下一次升级再回来")
	}
	var env model.Environment
	if err := db.First(&env, "code = ?", "uat").Error; err == nil {
		t.Error("连同环境也一起复活了")
	}
	caps, cmds := app.countRuleRows("uat")
	if caps != 0 || cmds != 0 {
		t.Errorf("规则行也回来了:能力 %d 条、字典 %d 条 —— 一个删掉的分层不该还管着东西", caps, cmds)
	}
}

// 复活的分层不能带着错的闸门设置回来。
//
// ADR 0013 要求 strict_nowhere 按分层存,而 dev 出厂是**关**的(那里清空一张草稿表是
// 日常)。GORM 的 `default` 标签会把零值丢掉、让数据库默认值顶上 —— 于是一个用
// `Create` 建出来的 dev 会带着 strict_nowhere=true 出现:开发环境上的每一条无 WHERE
// 的 DELETE 从此都要审批,而没有人改过那个开关。
func TestBuiltinTiers_RecreatedTierKeepsItsIntendedFlags(t *testing.T) {
	app := newTestApp(t)
	db := app.repo.DB()

	// 让 dev 缺席,模拟一个前修正时代的库(那时它可能确实没有)。
	db.Where("code = ?", "dev").Delete(&model.EnvTier{})
	// 把「修正已做过」的标记也清掉,好让这一轮真的去补。
	db.Where("k = ?", builtinTierCorrectionKey).Delete(&model.Setting{})

	if err := Migrate(app.cfg, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var dev model.EnvTier
	if err := db.First(&dev, "code = ?", "dev").Error; err != nil {
		t.Fatalf("dev 应当被补回来: %v", err)
	}
	if dev.StrictNoWhere {
		t.Error("补回来的 dev 带着 strict_nowhere=true —— 开发环境上每一条无 WHERE 的 DELETE " +
			"从此都要审批,而没有人改过那个开关")
	}
}
