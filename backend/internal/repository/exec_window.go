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

// SetExecWindowDecision 写下审批结论。
//
// 带上 status = pending 这个条件:一张已经被决定过的窗口不该被第二次决定覆盖
// (外部回调与站内审批可能同时到达),而"谁先到算谁的"要由数据库来裁,不是由
// 两段各自读一遍再写回去的代码来裁。
func (r *Repo) SetExecWindowDecision(id int64, status string, at time.Time) error {
	return r.db.Model(&model.ExecWindow{}).
		Where("id = ? AND status = ?", id, model.WindowPending).
		Updates(map[string]any{"status": status, "decided_at": at}).Error
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
