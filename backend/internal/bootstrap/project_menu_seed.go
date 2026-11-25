package bootstrap

import (
	"gorm.io/gorm"

	"velagateway/internal/model"
)

// backfillProjectMenu grants the new "project" menu to whoever already holds
// "pipeline".
//
// 必须回填,否则功能上线即死:一个没有任何 RoleMenu 行的菜单键,MenuGuard 读作
// **对所有人拒绝,管理员也不例外**。这是本仓库踩过的坑,pipeline 菜单当初就是这么
// 补的(见 backfillPipelineMenu)。
//
// 选 pipeline 作为祖先,不是 db:项目这个维度存在的理由是"方便跟进升级单状态",
// 该看到它的正是能看到发布单的那批人。挂在 db(数据源配置)上会把它变成一个只有
// 配置实例的人才进得去的页面,而跟进升级单的往往不是同一批人。
//
// 只在这个键**完全不存在**时写入,所以一次有意的收回不会被下次重启撤销。
func backfillProjectMenu(db *gorm.DB) error {
	var existing int64
	if err := db.Model(&model.RoleMenu{}).Where("menu_key = ?", "project").Count(&existing).Error; err != nil {
		return err
	}
	if existing > 0 {
		return nil
	}
	var src []model.RoleMenu
	if err := db.Where("menu_key = ?", "pipeline").Find(&src).Error; err != nil {
		return err
	}
	for _, r := range src {
		if err := db.Create(&model.RoleMenu{
			RoleID: r.RoleID, MenuKey: "project", Enabled: r.Enabled,
		}).Error; err != nil {
			return err
		}
	}
	return nil
}
