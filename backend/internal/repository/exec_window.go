package repository

import (
	"time"

	"velagateway/internal/model"
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
	err := r.db.Where("connection_id = ? AND db_name = ? AND enabled = ? AND status = ?",
		connID, database, true, model.WindowApproved).
		Order("id asc").Find(&out).Error
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ListExecWindows returns every window (console listing), newest first.
func (r *Repo) ListExecWindows() ([]model.ExecWindow, error) {
	var out []model.ExecWindow
	err := r.db.Order("id desc").Find(&out).Error
	return out, err
}

// LinkWindowApproval 把窗口指回它的审批单。
func (r *Repo) LinkWindowApproval(id, approvalID int64, apNo string) error {
	return r.db.Model(&model.ExecWindow{}).Where("id = ?", id).
		Updates(map[string]any{"approval_id": approvalID, "ap_no": apNo}).Error
}

// SetExecWindowDecision 写下审批结论 —— 由**作出它的那张单**写下。
//
// 两个条件都在同一条 UPDATE 的 WHERE 里,缺一不可:
//
//   - `status = pending` —— 一张已经被决定过的窗口不该被第二次决定覆盖(外部回调
//     与站内审批可能同时到达),而"谁先到算谁的"要由数据库来裁。
//   - `approval_id = ?` —— 这张单必须是这个窗口**此刻**那一张。
//
// 第二个条件不是重复劳动。只在代码里先读一遍再比对,中间那道缝正好放得进一次改窗口:
// 审批人认领了旧单 → 比对时窗口还指着旧单,通过 → 另一个人改窗口,窗口回到 pending
// 并链上新单(旧单已 approved,作废不了)→ 第一条线程继续往下写,而它只认
// `id + status=pending`,改后的窗口恰好是 pending —— **改后的定义就被旧单上的那个
// 签字批准了**。读-then-写挡不住这个,只有把 approval_id 放进同一次更新才挡得住。
//
// 返回值说的是"这一次决策生没生效"。调用方必须看它:false 不是错误,是"这张单说了
// 不算",而把它当成功会让审计里出现一条从未发生过的决策。
func (r *Repo) SetExecWindowDecision(id, approvalID int64, status string, at time.Time) (bool, error) {
	res := r.db.Model(&model.ExecWindow{}).
		Where("id = ? AND status = ? AND approval_id = ?", id, model.WindowPending, approvalID).
		Updates(map[string]any{"status": status, "decided_at": at})
	return res.RowsAffected == 1, res.Error
}

func (r *Repo) GetExecWindow(id int64) (*model.ExecWindow, error) {
	var w model.ExecWindow
	if err := r.db.First(&w, id).Error; err != nil {
		return nil, err
	}
	return &w, nil
}

func (r *Repo) CreateExecWindow(w *model.ExecWindow) error {
	now := time.Now()
	w.CreatedAt, w.UpdatedAt = now, now
	// Select("*") 的理由同 EnvTier:Enabled 带 default 标签,建窗口时若显式传 false,
	// GORM 会把这个零值丢掉,数据库默认值再把它打开 —— 一个建出来就是关着的窗口,
	// 存下来却是开着的。
	return r.db.Select("*").Create(w).Error
}

func (r *Repo) UpdateExecWindow(w *model.ExecWindow) error {
	w.UpdatedAt = time.Now()
	return r.db.Save(w).Error
}

func (r *Repo) DeleteExecWindow(id int64) error {
	return r.db.Delete(&model.ExecWindow{}, id).Error
}
