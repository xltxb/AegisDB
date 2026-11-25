package bootstrap

// 给历史审计行补上两列分类中**算得出来的那部分**（迁移 0039 / 0040 之前写下的行）。
//
// 加完这两列之后，界面上绝大多数行的「类型」和「增删改查」都是一个短横 —— 一张只对最近
// 几条成立的列，几乎回答不了任何事后问题（"上个月谁删过东西"仍然查不到）。
//
// 但回填有一条线不能过：**不猜**。这条线是仓库里已有的做法
// （见 approval_executed_backfill.go 的注释），在一张哈希链审计表上尤其不能破。所以两列
// 的结论不一样：
//
//	增删改查   **算得出来** —— 它是 command 的纯函数，而 command 本来就存着、还进了哈希。
//	           今天算出来的，和当时写行那一刻算出来的是同一个答案。回填。
//
//	事件类型   **算不出来** —— (risk, result) 区分不开 intercept 与 approve：同一条命令
//	           留下的两行可以都是 high/pending。唯一的例外是登录：它写的命令文本就是
//	           "login" / "login failed"，且 connection_id 为 0，没有第二条路会写出这个组合。
//	           只回填这一类，其余留空。
//
// ## 回填的值**不在哈希里**
//
// 老行的哈希是按当时的键集算的，里面没有这两个键。所以补进去的值不受链保护 —— 但它**本来
// 也不受保护**（那时这一列是空的，同样可以被随便改成任何值）。所以这次回填不削弱任何东西；
// 它只是把一个本来就能从 command 现算出来的结论，先算好放在那儿。真要较真某一行，
// 依据仍然是 command 本身，而那一栏是进了哈希的。
//
// ## 幂等
//
// 只碰那一列为空的行。跑过一遍之后集合就空了，之后每次启动都是一次带索引的空扫描。

import (
	"log/slog"

	"gorm.io/gorm"

	"aegisdb/internal/model"
	"aegisdb/internal/service"
)

// auditBackfillBatch 是一次取回多少行。分批是因为审计表可能很大，一次性读进内存不合适;
// 而每一批只发五条 UPDATE（按算出来的类别分组），不是每行一条 —— 一百万行的表那样要发
// 一百万条语句。
const auditBackfillBatch = 1000

// backfillAuditClassification 补历史行的 data_action，以及登录行的 event_type。
func backfillAuditClassification(db *gorm.DB) error {
	if err := backfillAuditDataAction(db); err != nil {
		return err
	}
	return backfillAuditLoginEvent(db)
}

// backfillAuditDataAction 按 command 算出增删改查，写给还没有这一列的行。
//
// 走的是 service.AuditDataAction —— 和写行时**同一个函数**。回填另写一份的话，历史行和
// 新行会按两套规则归类，而它们摆在同一张表的同一列里，那种不一致没有任何地方看得出来。
func backfillAuditDataAction(db *gorm.DB) error {
	type row struct {
		ID      int64
		Command string
	}
	total := 0
	for {
		var rows []row
		err := db.Model(&model.AuditLog{}).
			Select("id", "command").
			Where("COALESCE(data_action, '') = ''").
			Order("id").Limit(auditBackfillBatch).Find(&rows).Error
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			break
		}
		// 按算出来的类别分组，每一类发一条 UPDATE。
		byAction := map[string][]int64{}
		for _, r := range rows {
			a := service.AuditDataAction(r.Command)
			byAction[a] = append(byAction[a], r.ID)
		}
		for action, ids := range byAction {
			if err := db.Model(&model.AuditLog{}).
				Where("id IN ?", ids).Update("data_action", action).Error; err != nil {
				return err
			}
		}
		total += len(rows)
		// 这一批里一行都没被改到就停 —— 否则 WHERE 选得出、UPDATE 写不进的情况（比如
		// 某个类别算出了空串）会让这个循环永远转下去。
		if len(byAction) == 0 {
			break
		}
		if len(rows) < auditBackfillBatch {
			break
		}
	}
	if total > 0 {
		slog.Info("回填历史审计行的增删改查(按命令文本现算,与写入时同一个分类器)", "rows", total)
	}
	return nil
}

// backfillAuditLoginEvent 只给登录行补事件类型。
//
// 判据不是猜:登录审计写下的命令文本就是 "login" / "login failed"(见 service 里那两处
// recordAudit),而且 conn 为 nil 所以 connection_id 是 0。没有第二条路会写出这个组合 ——
// 终端里跑的是 SQL,发布/窗口/导出写的是各自的标签。
//
// 其余历史行(intercept / approve / exec)**不补**:(risk, result) 区分不开它们,同一条命令
// 留下的两行可以都是 high/pending。界面上它们继续显示一个短横,那是实话。
func backfillAuditLoginEvent(db *gorm.DB) error {
	res := db.Model(&model.AuditLog{}).
		Where("COALESCE(event_type, '') = '' AND connection_id = 0 AND command IN ?",
			[]string{"login", "login failed"}).
		Update("event_type", model.AuditEventLogin)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected > 0 {
		slog.Info("回填历史审计行的事件类型(仅登录 —— 其余几类算不出来,留空)", "rows", res.RowsAffected)
	}
	return nil
}
