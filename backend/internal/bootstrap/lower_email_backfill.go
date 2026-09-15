package bootstrap

import (
	"fmt"
	"log/slog"

	"gorm.io/gorm"

	"velagateway/internal/model"
)

// backfillLowerEmail 把历史行里非规范大小写的邮箱折成小写。
//
// 三条建号路径今天都折叠了(CreateUser、Invite、UpsertAdmin),所以新库不会再长出
// 这样的行。但升级上来的库可能带着它们:Invite 从前既不折叠也不查重,而 UpsertAdmin
// 命中已有账号时走 Save,把读到的 email 原样写回 —— 一行 `Ops@Vela.io` 会一直保持
// 那个形态。
//
// 功能上它不影响任何事:查询两侧都折叠(repository.GetUserByEmail、init.UpsertAdmin),
// 唯一索引也建在 lower(email) 上,所以两个只有大小写不同的账号从来建不出来。留着它
// 的代价是一颗哑雷 —— 哪天有人写了一段不折叠的新查询,它会查不到这些行,而那正是
// UpsertAdmin 自己踩过的那个坑(见 email_case_test.go:管理员口令重置当场失效,
// 报错指向唯一索引,完全不指向真因)。
//
// 幂等:WHERE 子句只挑出真正需要改的行,跑第二次一行都不动。
func backfillLowerEmail(db *gorm.DB) error {
	res := db.Model(&model.User{}).
		Where("email <> lower(email)").
		Update("email", gorm.Expr("lower(email)"))
	if res.Error != nil {
		return fmt.Errorf("backfill lower(email): %w", res.Error)
	}
	if res.RowsAffected > 0 {
		slog.Info("normalised historical email casing", "rows", res.RowsAffected)
	}
	return nil
}
