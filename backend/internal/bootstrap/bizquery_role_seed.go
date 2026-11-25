package bootstrap

import (
	"gorm.io/gorm"

	"aegisdb/internal/model"
)

// ensureBizQueryRole 建立(或补齐)内置的「业务查询」角色。
//
// 业务方的查询权**只来自查询授权**(QueryGrant),这个角色本身不能开放任何东西。
// 难点在于这套权限模型里有两处"缺省即放行":
//
//   - 能力矩阵:某个角色 × 能力 × 分层查不到行,读作 allow(repository.CapabilityLevel)。
//     一个一行能力都没配的角色,等于在所有分层上什么都能做。
//   - 标签范围:一个角色没有标签,读作不限实例(TagsForRoles)。
//
// 所以这里给它写满:每个能力 × 每个分层都是 deny,挂一个没有实例会带的占位标签,
// 已有菜单里只开数据查询页(query),其余全关。
//
// 只**补缺**,不覆盖:管理员事后有意改过的格子不会在下次重启时被改回去。新建分层时
// 规则从模板分层克隆,deny 行会跟着过去。
//
// 两条 schema 路径(SQL 迁移、AutoMigrate)都经 seedPipelineReference 调到这里。
func ensureBizQueryRole(db *gorm.DB) error {
	// 全新数据库、还没做初始化:什么都不建。Seed 与 `server init` 都以"角色表是空的"
	// 判断这是一次全新安装 —— 在它们之前插进一个角色,演示数据与首个管理员就都建不出来了。
	// 初始化完成后,backfillEnvTiers 末尾会再调到这里。
	var roles int64
	if err := db.Model(&model.Role{}).Count(&roles).Error; err != nil {
		return err
	}
	if roles == 0 {
		return nil
	}
	var role model.Role
	err := db.Where("code = ?", model.RoleCodeBizQuery).First(&role).Error
	if err == gorm.ErrRecordNotFound {
		role = model.Role{
			Code: model.RoleCodeBizQuery, Name: "业务查询", Layer: "L4 · 数据查询", Icon: "eye",
			CanApprove: false, DefaultConnRole: "readonly",
			Description: "业务方数据查询。能查哪些库只由查询授权决定,角色本身不开放任何实例与操作。",
		}
		if err := db.Create(&role).Error; err != nil {
			return err
		}
	} else if err != nil {
		return err
	}

	var tiers []model.EnvTier
	if err := db.Find(&tiers).Error; err != nil {
		return err
	}
	for _, t := range tiers {
		for _, capName := range model.Capabilities {
			var n int64
			if err := db.Model(&model.RoleCapability{}).
				Where("role_id = ? AND capability = ? AND tier_code = ?", role.ID, capName, t.Code).
				Count(&n).Error; err != nil {
				return err
			}
			if n == 0 {
				if err := db.Create(&model.RoleCapability{
					RoleID: role.ID, Capability: capName, TierCode: t.Code, Level: model.LevelDeny,
				}).Error; err != nil {
					return err
				}
			}
		}
	}

	var keys []string
	if err := db.Model(&model.RoleMenu{}).Distinct("menu_key").Pluck("menu_key", &keys).Error; err != nil {
		return err
	}
	for _, k := range keys {
		var n int64
		if err := db.Model(&model.RoleMenu{}).Where("role_id = ? AND menu_key = ?", role.ID, k).Count(&n).Error; err != nil {
			return err
		}
		if n == 0 {
			// 唯一打开的是数据查询页本身。
			if err := db.Create(&model.RoleMenu{RoleID: role.ID, MenuKey: k, Enabled: k == MenuQuery}).Error; err != nil {
				return err
			}
		}
	}

	var tagN int64
	if err := db.Model(&model.RoleTag{}).Where("role_id = ?", role.ID).Count(&tagN).Error; err != nil {
		return err
	}
	if tagN == 0 {
		return db.Create(&model.RoleTag{RoleID: role.ID, Tag: model.BizQueryNoTag}).Error
	}
	return nil
}

// MenuSchemaDiff 是表结构对比页的菜单键。
const MenuSchemaDiff = "schemadiff"

// backfillSchemaDiffMenu 在 schemadiff 菜单键第一次出现时给每个角色补一行:平台管理员与 DBA 负责人
// 打开,其余关闭(管理员在【权限】里按需开)。规则同 backfillQueryMenu:只在这个键完全不存在时写,
// 一次有意的收回不会在下次重启时被撤销。
func backfillSchemaDiffMenu(db *gorm.DB) error {
	var existing int64
	if err := db.Model(&model.RoleMenu{}).Where("menu_key = ?", MenuSchemaDiff).Count(&existing).Error; err != nil {
		return err
	}
	if existing > 0 {
		return nil
	}
	var roles []model.Role
	if err := db.Find(&roles).Error; err != nil {
		return err
	}
	for _, r := range roles {
		on := r.Code == "admin" || r.Code == "owner"
		if err := db.Create(&model.RoleMenu{RoleID: r.ID, MenuKey: MenuSchemaDiff, Enabled: on}).Error; err != nil {
			return err
		}
	}
	return nil
}

// MenuInspection 是「巡检报告」的菜单键(.scratch/inspection-report)。
const MenuInspection = "inspection"

// backfillInspectionMenu 在 inspection 菜单键第一次出现时给每个角色补一行:平台管理员、DBA 负责人、
// DBA L2 打开,其余关闭(2026-10-15 用户确认「DBA 都可以看」)。与其它菜单回填同一规矩:只在这个键
// 完全不存在时写,一次有意的收回不会在下次重启时被撤销。
func backfillInspectionMenu(db *gorm.DB) error {
	var existing int64
	if err := db.Model(&model.RoleMenu{}).Where("menu_key = ?", MenuInspection).Count(&existing).Error; err != nil {
		return err
	}
	if existing > 0 {
		return nil
	}
	var roles []model.Role
	if err := db.Find(&roles).Error; err != nil {
		return err
	}
	for _, r := range roles {
		on := r.Code == "admin" || r.Code == "owner" || r.Code == "l2"
		if err := db.Create(&model.RoleMenu{RoleID: r.ID, MenuKey: MenuInspection, Enabled: on}).Error; err != nil {
			return err
		}
	}
	return nil
}

// MenuQuery 是数据查询页的菜单键。
const MenuQuery = "query"

// backfillQueryMenu 在 query 菜单键第一次出现时,给每个角色补一行:只对平台管理员与业务
// 查询角色打开。没有 RoleMenu 行的菜单键读作对所有人拒绝(管理员也不例外),所以必须补。
//
// 只在这个键完全不存在时写入,一次有意的收回不会在下次重启时被撤销。全新数据库(还没有
// 任何角色)时什么都不做,初始化之后再补。
func backfillQueryMenu(db *gorm.DB) error {
	var existing int64
	if err := db.Model(&model.RoleMenu{}).Where("menu_key = ?", MenuQuery).Count(&existing).Error; err != nil {
		return err
	}
	if existing > 0 {
		return nil
	}
	var roles []model.Role
	if err := db.Find(&roles).Error; err != nil {
		return err
	}
	for _, r := range roles {
		on := r.Code == "admin" || r.Code == model.RoleCodeBizQuery
		if err := db.Create(&model.RoleMenu{RoleID: r.ID, MenuKey: MenuQuery, Enabled: on}).Error; err != nil {
			return err
		}
	}
	return nil
}
