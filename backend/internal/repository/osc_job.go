package repository

import (
	"time"

	"aegisdb/internal/apimsg"
	"aegisdb/internal/model"
)

// CreateOSCJob inserts a new in-line index migration job.
func (r *Repo) CreateOSCJob(j *model.OSCJob) error { return r.db.Create(j).Error }

// GetOSCJob loads one job.
func (r *Repo) GetOSCJob(id int64) (*model.OSCJob, error) {
	var j model.OSCJob
	if err := r.db.First(&j, id).Error; err != nil {
		return nil, err
	}
	return &j, nil
}

// LinkOSCApproval records the approval ticket a job hangs off.
func (r *Repo) LinkOSCApproval(jobID, approvalID int64, apNo string) error {
	return r.db.Model(&model.OSCJob{}).Where("id = ?", jobID).
		Updates(map[string]any{"approval_id": approvalID, "ap_no": apNo}).Error
}

// ClaimOSCJobStart 原子地把一个 awaiting 的任务变成 running —— 前提是同一张表上没有别的进行中的
// 任务(同一张表同时只允许一个迁移:两个影子表、两路重放会互相踩)。返回 false 表示没占到:
// 要么它已经开始过了,要么那张表上已经有一个在跑。
func (r *Repo) ClaimOSCJobStart(j *model.OSCJob, now time.Time) (bool, error) {
	var busy int64
	if err := r.db.Model(&model.OSCJob{}).
		Where("connection_id = ? AND db_name = ? AND tbl_name = ? AND status = ? AND id <> ?",
			j.ConnectionID, j.Database, j.Table, model.OSCRunning, j.ID).Count(&busy).Error; err != nil {
		return false, err
	}
	if busy > 0 {
		return false, nil
	}
	res := r.db.Model(&model.OSCJob{}).Where("id = ? AND status = ?", j.ID, model.OSCAwaiting).
		Updates(map[string]any{"status": model.OSCRunning, "started_at": now, "error": ""})
	return res.RowsAffected == 1, res.Error
}

// UpdateOSCJob writes the given fields (progress snapshot or the final outcome).
func (r *Repo) UpdateOSCJob(id int64, fields map[string]any) error {
	return r.db.Model(&model.OSCJob{}).Where("id = ?", id).Updates(fields).Error
}

// OSCInterrupted 是网关重启时进行中的迁移写进任务的那句话。
var OSCInterrupted = apimsg.Text("网关重启,迁移中断,原表未切换")

// ReconcileStuckOSCJobs 收尾上一个进程留下的进行中任务,分两类(工单 07):
//   - 来自终端 / 审批、且已有检查点 → interrupted:影子表与进度保留,等人续跑或放弃;
//   - 其余(流水线发起的、还没拷完第一块的)→ 判失败,调用方去清理残留的影子表。
//
// 流水线来源不续跑:它的执行阶段由流水线自己的重启收尾判失败(FailStuckReleases),任务再续上
// 也没有阶段等着它的结果。
func (r *Repo) ReconcileStuckOSCJobs() (interrupted, failed []model.OSCJob, err error) {
	var jobs []model.OSCJob
	if err := r.db.Where("status = ?", model.OSCRunning).Find(&jobs).Error; err != nil {
		return nil, nil, err
	}
	now := time.Now()
	for _, j := range jobs {
		if j.ReleaseID == 0 && j.Checkpoint != "" {
			err = r.db.Model(&model.OSCJob{}).Where("id = ? AND status = ?", j.ID, model.OSCRunning).
				Updates(map[string]any{"status": model.OSCInterrupted, "error": OSCInterruptedResumable,
					"paused": false, "throttled": false, "throttle_reason": ""}).Error
			interrupted = append(interrupted, j)
		} else {
			err = r.db.Model(&model.OSCJob{}).Where("id = ? AND status = ?", j.ID, model.OSCRunning).
				Updates(map[string]any{"status": model.OSCFailed, "error": OSCInterrupted, "finished_at": now, "paused": false}).Error
			failed = append(failed, j)
		}
		if err != nil {
			return nil, nil, err
		}
	}
	return interrupted, failed, nil
}

// OSCInterruptedResumable 是可续跑的中断任务写进 error 的那句话。
var OSCInterruptedResumable = apimsg.Text("网关重启,迁移中断;影子表与进度已保留,可在工单里「续跑」或「放弃」")

// ClaimOSCJobContinue 把一个中断的任务重新占为 running(同一张表上不能另有在跑的)。
func (r *Repo) ClaimOSCJobContinue(j *model.OSCJob) (bool, error) {
	var busy int64
	if err := r.db.Model(&model.OSCJob{}).
		Where("connection_id = ? AND db_name = ? AND tbl_name = ? AND status = ? AND id <> ?",
			j.ConnectionID, j.Database, j.Table, model.OSCRunning, j.ID).Count(&busy).Error; err != nil {
		return false, err
	}
	if busy > 0 {
		return false, nil
	}
	res := r.db.Model(&model.OSCJob{}).Where("id = ? AND status = ?", j.ID, model.OSCInterrupted).
		Updates(map[string]any{"status": model.OSCRunning, "error": ""})
	return res.RowsAffected == 1, res.Error
}

// RunningOSCJobForRelease 是发布单执行阶段此刻在跑的那次在线变更(没有返回 nil)。
func (r *Repo) RunningOSCJobForRelease(releaseID int64) (*model.OSCJob, error) {
	var j model.OSCJob
	err := r.db.Where("release_id = ? AND status = ?", releaseID, model.OSCRunning).Order("id desc").Limit(1).Find(&j).Error
	if err != nil || j.ID == 0 {
		return nil, err
	}
	return &j, nil
}
