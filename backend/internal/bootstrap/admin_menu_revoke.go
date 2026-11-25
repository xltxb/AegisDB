package bootstrap

import (
	"log/slog"
	"strings"

	"gorm.io/gorm"

	"aegisdb/internal/model"
)

// revokeAdminOnlyMenus 收回非平台管理员角色上的管理类菜单(model.AdminOnlyMenus)。
//
// 管理后台拆分之后(.scratch/admin-user-split 工单 03),实例、规则、分层、权限、系统设置只有平台
// 管理员能用:后端接口已强制校验(工单 01),界面上的入口也只给管理员(工单 02)。这里把数据也对齐 ——
// 存量的自定义角色上若还勾着这些菜单,菜单本身已经没有用处,留着只会在权限页里显示成一个能勾、
// 勾了却进不去的开关。
//
// 每次启动都跑(两条建表路径都经过 seedPipelineReference):权限页与接口都已不允许再勾,
// 所以正常情况下第二次起什么都不改;改了就打一条日志,写明是哪些角色。
func revokeAdminOnlyMenus(db *gorm.DB) error {
	var adminIDs []int64
	if err := db.Model(&model.Role{}).Where("code = ?", "admin").Pluck("id", &adminIDs).Error; err != nil {
		return err
	}
	// 找不到平台管理员角色(code 被改过、库是半初始化的):认不出谁是管理员,就一个都不收。
	// 从前这时不加 NOT IN,所有角色的管理菜单都被收走 —— 包括实际在用的那个管理员角色,
	// 结果谁都进不了权限页把它改回来(2026-10 审查)。
	if len(adminIDs) == 0 {
		slog.Warn("管理后台拆分:找不到平台管理员角色(code=admin),跳过收回管理类菜单")
		return nil
	}
	q := db.Model(&model.RoleMenu{}).Where("enabled = ? AND menu_key IN ? AND role_id NOT IN ?", true, model.AdminOnlyMenus, adminIDs)
	var hits []model.RoleMenu
	if err := q.Find(&hits).Error; err != nil {
		return err
	}
	if len(hits) == 0 {
		return nil
	}
	byRole := map[int64][]string{}
	for _, h := range hits {
		byRole[h.RoleID] = append(byRole[h.RoleID], h.MenuKey)
		if err := db.Model(&model.RoleMenu{}).Where("role_id = ? AND menu_key = ?", h.RoleID, h.MenuKey).
			Update("enabled", false).Error; err != nil {
			return err
		}
	}
	for id, keys := range byRole {
		var r model.Role
		_ = db.First(&r, id).Error
		slog.Warn("管理后台拆分:收回非管理员角色上的管理类菜单", "roleId", id, "role", r.Name, "code", r.Code, "menus", strings.Join(keys, ","))
	}
	return nil
}
