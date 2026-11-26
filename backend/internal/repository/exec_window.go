package repository

import (
	"time"

	"gorm.io/gorm"

	"aegisdb/internal/model"
)

// 执行窗口(「班车」)的存取。
//
// 判定路径上只用 ExecWindowsFor,它按 (connection_id, db_name) 走索引,命中行数是
// 个位数 —— 每条命令都会调它一次,不能变成一次全表扫描。

// ExecWindowsFor returns the ENABLED windows filed against one database.
//
// 时间是否落在窗口内由 service 层判断,不在 SQL 里算:周期班车要按窗口自己的时区
// 换算、还要处理跨午夜,这些用 SQL 表达既难读也难测,而错一处就是错误放行。
// 这里只做便宜的等值过滤。
func (r *Repo) ExecWindowsFor(connID int64, database string) ([]model.ExecWindow, error) {
	var out []model.ExecWindow
	// status = approved 是**这个功能的闸门**:一张还在等审批(或被驳回)的窗口一行
	// 都不该放行。它和 enabled 是两件事 —— enabled 是"运维要不要用它",status 是
	// "有没有人签过字"。少一个条件,窗口在提交申请的那一刻就已经开着了。
	//
	// 多库窗口(2026-10-22):库在子表 tbl_exec_window_db 里;主表的 db_name 是第一个库,
	// 也一并认 —— 子表还没回填的老窗口照样生效。
	err := r.db.Where("connection_id = ? AND enabled = ? AND status = ?", connID, true, model.WindowApproved).
		Where("db_name = ? OR id IN (?)", database,
			r.db.Model(&model.ExecWindowDB{}).Select("window_id").Where("db_name = ?", database)).
		Order("id asc").Find(&out).Error
	if err != nil {
		return nil, err
	}
	return out, r.fillWindowDBs(out)
}

// fillWindowDBs 给一批窗口填上 Databases(按勾选顺序;子表里没有行的退回主表的 db_name)。
func (r *Repo) fillWindowDBs(ws []model.ExecWindow) error {
	if len(ws) == 0 {
		return nil
	}
	ids := make([]int64, len(ws))
	for i := range ws {
		ids[i] = ws[i].ID
	}
	var rows []struct {
		WindowID int64
		DBName   string
	}
	// 顺序:主表那个库排第一,其余按库名 —— 子表没有自增列,插入顺序不可靠
	if err := r.db.Model(&model.ExecWindowDB{}).Select("window_id, db_name").Where("window_id IN ?", ids).
		Order("window_id, db_name").Scan(&rows).Error; err != nil {
		return err
	}
	by := map[int64][]string{}
	for _, x := range rows {
		by[x.WindowID] = append(by[x.WindowID], x.DBName)
	}
	for i := range ws {
		ws[i].Databases = orderWindowDBs(ws[i].Database, by[ws[i].ID])
	}
	return nil
}

func orderWindowDBs(primary string, dbs []string) []string {
	out := make([]string, 0, len(dbs)+1)
	if primary != "" {
		out = append(out, primary)
	}
	for _, d := range dbs {
		if d != primary {
			out = append(out, d)
		}
	}
	return out
}

// saveWindowDBs 把窗口覆盖的库整组换成 w.DBs()(同一个事务里先删后插)。
func saveWindowDBs(tx *gorm.DB, w *model.ExecWindow) error {
	if err := tx.Where("window_id = ?", w.ID).Delete(&model.ExecWindowDB{}).Error; err != nil {
		return err
	}
	for _, d := range w.DBs() {
		if err := tx.Create(&model.ExecWindowDB{WindowID: w.ID, Database: d}).Error; err != nil {
			return err
		}
	}
	return nil
}

// ListExecWindows returns every window (console listing), newest first.
func (r *Repo) ListExecWindows() ([]model.ExecWindow, error) {
	var out []model.ExecWindow
	if err := r.db.Order("id desc").Find(&out).Error; err != nil {
		return nil, err
	}
	return out, r.fillWindowDBs(out)
}

// LinkWindowApproval 把窗口指回它的审批单。
func (r *Repo) LinkWindowApproval(id, approvalID int64, apNo string) error {
	return r.db.Model(&model.ExecWindow{}).Where("id = ?", id).
		Updates(map[string]any{"approval_id": approvalID, "ap_no": apNo}).Error
}

func (r *Repo) GetExecWindow(id int64) (*model.ExecWindow, error) {
	var w model.ExecWindow
	if err := r.db.First(&w, id).Error; err != nil {
		return nil, err
	}
	one := []model.ExecWindow{w}
	if err := r.fillWindowDBs(one); err != nil {
		return nil, err
	}
	return &one[0], nil
}

func (r *Repo) CreateExecWindow(w *model.ExecWindow) error {
	now := time.Now()
	w.CreatedAt, w.UpdatedAt = now, now
	// 关着的窗口要存成关着的。Enabled 曾带 GORM 标签 default:true,建行时 false 被换成
	// true,而这里的 Select("*") 并没有挡住(2026-11 三种存储库实测)。现在靠模型不带
	// 布尔默认值来保证,见 bootstrap.TestModels_NoNonZeroBoolDefaults。
	// Select("*") 留着无害。
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Select("*").Create(w).Error; err != nil {
			return err
		}
		return saveWindowDBs(tx, w)
	})
}

func (r *Repo) UpdateExecWindow(w *model.ExecWindow) error {
	w.UpdatedAt = time.Now()
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Save(w).Error; err != nil {
			return err
		}
		return saveWindowDBs(tx, w)
	})
}

// DisableExecWindow 只把 enabled 置为 false,别的列一个不碰;返回这次是否真的从启用变成了停用。
//
// 从前停用是读整行、改一列、Save 整行写回:读与写之间申请人恰好改了窗口(名称、时间、库),
// 这一下会把它改回旧值(2026-10 审查)。用 map 而不是结构体:GORM 的结构体 Updates 会跳过零值,
// false 根本写不进去。
func (r *Repo) DisableExecWindow(id int64) (bool, error) {
	res := r.db.Model(&model.ExecWindow{}).Where("id = ? AND enabled = ?", id, true).
		Updates(map[string]any{"enabled": false, "updated_at": time.Now()})
	return res.RowsAffected > 0, res.Error
}

func (r *Repo) DeleteExecWindow(id int64) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("window_id = ?", id).Delete(&model.ExecWindowDB{}).Error; err != nil {
			return err
		}
		return tx.Delete(&model.ExecWindow{}, id).Error
	})
}

// AuditForWindow 捞窗口期回看要用的审计行:同一台实例、同一个库、真的下发过的语句。
//
// 只做**便宜的等值与区间过滤**,时间表落不落得进窗口由 service 判(周期班车要按窗口
// 自己的时区换算、还要处理跨午夜 —— 与判定层同一套代码,不在 SQL 里重写一遍)。
//
// limit 是**扫描上限**,不是结果条数:service 还要按时间表再筛一遍。给 0 表示不限。
func (r *Repo) AuditForWindow(actorID, connID int64, databases []string, since, until time.Time, limit int) ([]model.AuditLog, error) {
	q := r.auditWindowQuery(actorID, connID, databases)
	if !since.IsZero() {
		q = q.Where("occurred_at >= ?", since)
	}
	if !until.IsZero() {
		q = q.Where("occurred_at < ?", until) // 左闭右开,与 windowCovers 的边界一致
	}
	return findAuditWindowRows(q, limit)
}

// AuditRelaxedBy 捞这扇门**亲自放行过**的行:按 Operator 精确匹配,不看时间。
//
// 为什么还要这一趟:窗口改一次就重新走审批,改的可以是时间段,于是按**现在**的时间表
// 去筛历史,会把一条确实被这扇门放行过的语句筛掉 —— 而那正是这一页最不能丢的一条。
// Operator 是执行当时写进哈希链的,它说了算。
func (r *Repo) AuditRelaxedBy(actorID, connID int64, databases []string, operator string, limit int) ([]model.AuditLog, error) {
	if operator == "" {
		return nil, nil
	}
	return findAuditWindowRows(r.auditWindowQuery(actorID, connID, databases).Where("operator = ?", operator), limit)
}

// auditWindowQuery 是上面两趟共用的 WHERE。
//
// 两条值得说:
//
//	result NOT IN (pending, rejected) —— 这一页要回答"那段时间里到底跑了什么"。被拦下来
//	    开了单的、被拒绝的,都没有落到库上;而执行失败(warn)与仿真(simulated)**落过**
//	    或**本该落**,它们要留在列表里,由界面标出来。
//	event_type IN ('exec', '') —— 'exec' 是执行路径写下的行。空是迁移 0039 之前的老行,
//	    当时还没有这一列:把它们排除掉,一扇 0039 之前开过的窗口会显示成"什么都没跑过",
//	    那是比偶尔混进一条配置变更行更糟的一种错。
func (r *Repo) auditWindowQuery(actorID, connID int64, databases []string) *gorm.DB {
	q := r.db.Model(&model.AuditLog{}).
		Where("connection_id = ? AND db_name IN ?", connID, databases).
		Where("result NOT IN (?)", []string{model.ResultPending, model.ResultRejected}).
		Where("event_type IN (?)", []string{model.AuditEventExec, ""})
	// actorID > 0 才按人收窄。窗口回看现在传 0(见 WindowStatements 的口径:这扇门
	// 放行过什么是共同可见的),这个参数留着是因为审计页那条规则仍然要用同一段 WHERE。
	if actorID > 0 {
		q = q.Where("actor_id = ?", actorID)
	}
	return q
}

func findAuditWindowRows(q *gorm.DB, limit int) ([]model.AuditLog, error) {
	var rows []model.AuditLog
	q = q.Order("occurred_at desc, id desc")
	if limit > 0 {
		q = q.Limit(limit)
	}
	err := q.Find(&rows).Error
	return rows, err
}
