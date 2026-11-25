package bootstrap

import (
	"log/slog"

	"gorm.io/gorm"

	"velagateway/internal/model"
)

// backfillApprovalExecuted stamps executed_at on tickets that ran under the OLD
// behaviour, where approving a command also executed it.
//
// 为什么必须回填 —— 迁移 0028 的注释说"存量数据不需要回填",那句话是错的:
//
// 历史工单在旧行为下批准即执行,但它们的 executed_at 是空的。而"已批准 + executed_at
// 为空 + 非发布单"正是 ExecuteApproved 判定"这张单可以执行"的条件 —— 于是一条七月份
// 就已经跑过的 DROP TABLE,今天还能被发起人一键再跑一遍。这不是显示问题,是一条通往
// 重复执行的路。
//
// 难点在于历史工单和一张**真的在等执行**的新工单,行级别一模一样:都是
// (approved, executed_at IS NULL)。按时间切分要猜"什么时候升的级",猜错的两个方向
// 都很糟:
//
//   - 少标:历史单仍可重跑 —— 不可逆
//   - 多标:真正待执行的工单被标成已执行,那条命令再也不会跑,而人以为它跑了
//
// 所以不猜。判据是新代码自己写下的那句话:真正在等执行的工单,Result 恰好是
// model.AwaitingExecution。其余 executed_at 为空的 approved 单都是旧行为的产物。
//
// 这个条件不依赖任何时间点,因此**天然幂等** —— 每次启动都跑也不会误伤:已回填的行
// executed_at 不再为空,等待执行的行带着那句话,而它一旦被执行,ExecuteApproved 会同时
// 写上 executed_at 并覆盖 Result。
//
// 执行失败的历史单同样标记。旧行为下失败就是"这张单用掉了",要重来得重新提单;新行为
// 也是同一条规矩(占位在执行之前、失败不退回,因为命令到底跑没跑并不确定)。放它回到
// 可执行状态,等于给了它一次旧行为从来没给过的重试。
func backfillApprovalExecuted(db *gorm.DB) error {
	res := db.Model(&model.Approval{}).
		Where("status = ? AND executed_at IS NULL AND release_id = 0 AND COALESCE(result, '') <> ?",
			model.StatusApproved, model.AwaitingExecution).
		Update("executed_at", gorm.Expr("COALESCE(decided_at, created_at)"))
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected > 0 {
		// 这条日志值得留:它说明升级那一刻有多少张历史单曾经短暂地处于"可以再跑一次"
		// 的状态。数字不为零时,运维应该去审计里对一眼这段时间有没有人点过。
		slog.Info("回填历史审批单的执行时刻(旧行为下批准即执行)", "rows", res.RowsAffected)
	}
	return nil
}
