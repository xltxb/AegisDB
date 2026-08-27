package repository

import (
	"errors"
	"time"

	"gorm.io/gorm"

	"velagateway/internal/model"
)

// ----------------------------------------------------- 发布流程模板 (Pipeline)

func (r *Repo) ListPipelines() ([]model.Pipeline, error) {
	var ps []model.Pipeline
	err := r.db.Order("is_default desc, id asc").Find(&ps).Error
	return ps, err
}

func (r *Repo) GetPipeline(id int64) (*model.Pipeline, error) {
	var p model.Pipeline
	if err := r.db.First(&p, id).Error; err != nil {
		return nil, err
	}
	return &p, nil
}

func (r *Repo) StagesOfPipeline(id int64) ([]model.PipelineStage, error) {
	var ss []model.PipelineStage
	err := r.db.Where("pipeline_id = ?", id).Order("step_order asc, id asc").Find(&ss).Error
	return ss, err
}

// SavePipeline creates or updates a template together with its stage list, in
// one transaction.
//
// The stages are REPLACED rather than merged: a stage list is an ordered whole,
// and reconciling it row by row would leave a template half-edited if one write
// failed — a flow that reviews but no longer approves, which nobody asked for.
// Running releases are unaffected because a run snapshots its own stages.
func (r *Repo) SavePipeline(p *model.Pipeline, stages []model.PipelineStage) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		p.UpdatedAt = time.Now()
		if p.ID == 0 {
			p.CreatedAt = p.UpdatedAt
			if err := tx.Create(p).Error; err != nil {
				return err
			}
		} else if err := tx.Save(p).Error; err != nil {
			return err
		}
		// Exactly one template may be the default, so claiming it clears the others
		// in the same transaction — two defaults would make "the default flow"
		// whichever row the database returned first.
		if p.IsDefault {
			if err := tx.Model(&model.Pipeline{}).Where("id <> ?", p.ID).
				Update("is_default", false).Error; err != nil {
				return err
			}
		}
		if err := tx.Where("pipeline_id = ?", p.ID).Delete(&model.PipelineStage{}).Error; err != nil {
			return err
		}
		for i := range stages {
			stages[i].ID = 0
			stages[i].PipelineID = p.ID
			stages[i].StepOrder = i + 1
			if err := tx.Create(&stages[i]).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// DeletePipeline removes a template and its stages. Releases that ran on it keep
// their snapshotted stages and their pipeline NAME, so history stays readable.
func (r *Repo) DeletePipeline(id int64) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("pipeline_id = ?", id).Delete(&model.PipelineStage{}).Error; err != nil {
			return err
		}
		return tx.Delete(&model.Pipeline{}, id).Error
	})
}

// DefaultPipelineFor picks the template a release should use for a tier when the
// submitter did not name one: the tier-specific default first, then the global
// default, then any enabled tier-specific or global template.
func (r *Repo) DefaultPipelineFor(tierCode string) (*model.Pipeline, error) {
	var p model.Pipeline
	q := r.db.Where("enabled = ?", true)
	if err := q.Session(&gorm.Session{}).Where("tier_code = ? AND is_default = ?", tierCode, true).
		First(&p).Error; err == nil {
		return &p, nil
	}
	if err := q.Session(&gorm.Session{}).Where("(tier_code = '' OR tier_code IS NULL) AND is_default = ?", true).
		First(&p).Error; err == nil {
		return &p, nil
	}
	if err := q.Session(&gorm.Session{}).Where("tier_code = ? OR tier_code = '' OR tier_code IS NULL", tierCode).
		Order("id asc").First(&p).Error; err != nil {
		return nil, err
	}
	return &p, nil
}

// ----------------------------------------------------- 发布单 (Release)

// MaxReleaseSeq returns the largest numeric suffix of any REL-<n>, so numbers
// are never reused after rows are deleted (same reasoning as MaxApprovalSeq).
func (r *Repo) MaxReleaseSeq() int64 { return r.maxSeq("tbl_release", "rel_no", "REL-") }

// CreateRelease writes the release together with the stage rows snapshotted from
// its template, so a run never exists without the steps it is supposed to take.
func (r *Repo) CreateRelease(rel *model.Release, stages []model.ReleaseStage) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(rel).Error; err != nil {
			return err
		}
		for i := range stages {
			stages[i].ID = 0
			stages[i].ReleaseID = rel.ID
			stages[i].StepOrder = i + 1
			if stages[i].Status == "" {
				stages[i].Status = model.RunPending
			}
			if err := tx.Create(&stages[i]).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *Repo) GetRelease(id int64) (*model.Release, error) {
	var rel model.Release
	if err := r.db.First(&rel, id).Error; err != nil {
		return nil, err
	}
	return &rel, nil
}

func (r *Repo) GetReleaseByNo(no string) (*model.Release, error) {
	var rel model.Release
	if err := r.db.Where("rel_no = ?", no).First(&rel).Error; err != nil {
		return nil, err
	}
	return &rel, nil
}

// ListReleasesPaged returns releases newest first. scope "mine" narrows to the
// caller's own; anything else lists all (the handler decides who may ask for it).
func (r *Repo) ListReleasesPaged(scope string, userID int64, status string, projectID int64, offset, limit int) ([]model.Release, int64, error) {
	q := r.db.Model(&model.Release{})
	if scope == "mine" {
		q = q.Where("creator_id = ?", userID)
	}
	// 按项目跟进:0 = 不限,与"未归属"不同 —— 后者要显式查 project_id = 0,
	// 这里用不到,列表页的"全部项目"就是不加这个条件。
	if projectID > 0 {
		q = q.Where("project_id = ?", projectID)
	}
	if status != "" && status != "all" {
		q = q.Where("status = ?", status)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rels []model.Release
	err := q.Order("id desc").Offset(offset).Limit(limit).Find(&rels).Error
	return rels, total, err
}

func (r *Repo) StagesOfRelease(id int64) ([]model.ReleaseStage, error) {
	var ss []model.ReleaseStage
	err := r.db.Where("release_id = ?", id).Order("step_order asc, id asc").Find(&ss).Error
	return ss, err
}

func (r *Repo) GetReleaseStage(id int64) (*model.ReleaseStage, error) {
	var s model.ReleaseStage
	if err := r.db.First(&s, id).Error; err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *Repo) UpdateReleaseStage(id int64, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	return r.db.Model(&model.ReleaseStage{}).Where("id = ?", id).Updates(fields).Error
}

// ClaimReleaseStage moves one stage from `from` to `to` and reports whether THIS
// caller won. The manual-continue button and the approval sweeper can both aim
// at the same waiting stage, and a stage that ran twice would run its execute
// successor twice.
func (r *Repo) ClaimReleaseStage(id int64, from, to string) (bool, error) {
	res := r.db.Model(&model.ReleaseStage{}).
		Where("id = ? AND status = ?", id, from).
		Updates(map[string]any{"status": to})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

// ClaimRelease is the same guard for the release row: pending → running is what
// makes a double-enqueued run execute once.
func (r *Repo) ClaimRelease(id int64, from, to string, startedAt *time.Time) (bool, error) {
	fields := map[string]any{"status": to}
	if startedAt != nil {
		fields["started_at"] = *startedAt
	}
	res := r.db.Model(&model.Release{}).Where("id = ? AND status = ?", id, from).Updates(fields)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

func (r *Repo) UpdateRelease(id int64, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	return r.db.Model(&model.Release{}).Where("id = ?", id).Updates(fields).Error
}

// WaitingApprovalStages returns the approve stages currently blocking a release,
// which is what the sweeper polls to notice a decided ticket.
func (r *Repo) WaitingApprovalStages() ([]model.ReleaseStage, error) {
	var ss []model.ReleaseStage
	err := r.db.Where("status = ? AND type = ? AND approval_id > 0", model.RunWaiting, model.StageApprove).
		Find(&ss).Error
	return ss, err
}

// FailStuckReleases marks releases left mid-flight by a previous process.
//
// Only `running` rows are reconciled. A `waiting` release is blocked on a human
// and its state lives in the database, so it resumes cleanly after a restart —
// failing it would throw away an approval someone already gave. A `running` one
// cannot be resumed: its execute stage may have reached the target database, and
// re-running it is the one outcome worse than reporting a failure.
func (r *Repo) FailStuckReleases() (int64, error) {
	now := time.Now()
	res := r.db.Model(&model.Release{}).Where("status = ?", model.RunRunning).
		Updates(map[string]any{
			"status":      model.RunFailed,
			"error":       "网关重启,发布中断(执行阶段结果未知,请人工确认后重新发起)",
			"finished_at": now,
		})
	if res.Error != nil {
		return 0, res.Error
	}
	if res.RowsAffected > 0 {
		if err := r.db.Model(&model.ReleaseStage{}).
			Where("status = ?", model.RunRunning).
			Updates(map[string]any{"status": model.RunFailed, "finished_at": now}).Error; err != nil {
			return res.RowsAffected, err
		}
	}
	return res.RowsAffected, nil
}

// PendingReleases lists runs that were queued but never started (the in-memory
// queue does not survive a restart), so boot can re-enqueue them.
func (r *Repo) PendingReleases() ([]model.Release, error) {
	var rels []model.Release
	err := r.db.Where("status = ?", model.RunPending).Order("id asc").Find(&rels).Error
	return rels, err
}

// CountReleases counts runs in a status (empty = all), for the console header.
func (r *Repo) CountReleases(status string) int64 {
	q := r.db.Model(&model.Release{})
	if status != "" {
		q = q.Where("status = ?", status)
	}
	var n int64
	if err := q.Count(&n).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return 0
	}
	return n
}
