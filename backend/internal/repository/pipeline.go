package repository

import (
	"errors"
	"regexp"
	"strings"
	"time"

	"gorm.io/gorm"

	"aegisdb/internal/model"
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

// ReleaseFilter is what the release list narrows by. Scope "mine" means the
// caller's own; anything else lists all (the service decides who may ask for it).
type ReleaseFilter struct {
	Scope     string
	UserID    int64
	Status    string
	ProjectID int64
	Env       string // environment 编码(fat 等),不是分层 tier;空 = 不限
	Keyword   string // 单号,口径见 releaseKeywordWhere;空 = 不限
}

// visibleReleases 是范围 + 项目:发布单列表和它的环境选项共用,选项不会比列表看得更多。
func (r *Repo) visibleReleases(f ReleaseFilter) *gorm.DB {
	q := r.db.Model(&model.Release{})
	if f.Scope == "mine" {
		q = q.Where("creator_id = ?", f.UserID)
	}
	// 按项目跟进:0 = 不限,与"未归属"不同 —— 后者要显式查 project_id = 0,
	// 这里用不到,列表页的"全部项目"就是不加这个条件。
	if f.ProjectID > 0 {
		q = q.Where("project_id = ?", f.ProjectID)
	}
	return q
}

// ListReleasesPaged returns releases newest first.
func (r *Repo) ListReleasesPaged(f ReleaseFilter, offset, limit int) ([]model.Release, int64, error) {
	q := r.visibleReleases(f)
	if f.Status != "" && f.Status != "all" {
		q = q.Where("status = ?", f.Status)
	}
	if f.Env != "" {
		q = q.Where("env = ?", f.Env)
	}
	if where, args := releaseKeywordWhere(f.Keyword); where != "" {
		q = q.Where("("+where+")", args...)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rels []model.Release
	err := q.Order("id desc").Offset(offset).Limit(limit).Find(&rels).Error
	return rels, total, err
}

// ReleaseEnvs 是筛选下拉的选项:当前范围和项目下实际出现过的环境。不跟着环境 / 单号 /
// 状态收窄 —— 选了 fat 之后下拉里只剩 fat,就换不回别的了。
func (r *Repo) ReleaseEnvs(f ReleaseFilter) ([]string, error) {
	envs := []string{}
	err := r.visibleReleases(f).Where("env <> ''").Distinct("env").Order("env asc").Pluck("env", &envs).Error
	return envs, err
}

var (
	relNoSearchRe = regexp.MustCompile(`^rel-?(\d+)$`)
	allDigitsRe   = regexp.MustCompile(`^\d+$`)
)

// releaseKeywordWhere 把"单号"翻成查询条件。用户手里的单号有两种:REL 编号,和外部
// 发布平台的工单号 —— 后者在外部引用(autopub:16436:FAT:292820:1)和标题
// (…-orderID=16436-exec_order=1-FAT-DDL)里。
//
//   - REL-11 / rel11:就这一张,不带出 REL-110。
//   - 纯数字:REL 编号、外部引用里冒号分隔的一段、标题里的 orderID=,都按整段比。不能
//     用包含:标题里有 20261105 这样的日期,11 几乎会命中所有单子。外部引用的**最后
//     一段**不比,它是执行顺序,搜 1 会把每张单都带出来。
//   - 其它(autopub:16436、orderID=16436、标题片段):REL 编号 / 标题 / 外部引用的片段。
//
// 只碰这三列,不碰 SQL 正文:正文可能带口令,界面上看到的都是脱敏后的,按原文搜等于
// 让人一个字符一个字符地猜。
func releaseKeywordWhere(keyword string) (string, []any) {
	kw := strings.ToLower(strings.TrimSpace(keyword))
	if kw == "" {
		return "", nil
	}
	if m := relNoSearchRe.FindStringSubmatch(kw); m != nil {
		return "LOWER(rel_no) = ?", []any{"rel-" + m[1]}
	}
	const refCol, titleCol = "LOWER(external_ref)", "LOWER(title)"
	if allDigitsRe.MatchString(kw) {
		return "LOWER(rel_no) = ? OR " + refCol + " = ? OR " +
				refCol + likeClause + " OR " + refCol + likeClause + " OR " +
				titleCol + likeClause + " OR " + titleCol + likeClause,
			[]any{"rel-" + kw, kw, kw + ":%", "%:" + kw + ":%", "%orderid=" + kw, "%orderid=" + kw + "-%"}
	}
	pat := "%" + escapeLike(kw) + "%"
	return "LOWER(rel_no)" + likeClause + " OR " + titleCol + likeClause + " OR " + refCol + likeClause,
		[]any{pat, pat, pat}
}

func (r *Repo) StagesOfRelease(id int64) ([]model.ReleaseStage, error) {
	var ss []model.ReleaseStage
	err := r.db.Where("release_id = ?", id).Order("step_order asc, id asc").Find(&ss).Error
	return ss, err
}

// StageShapesOfReleases 一条 IN 查询取一批发布单的阶段,按单分组,单内顺序同 StagesOfRelease。
//
// 给发布单列表用:列表只要阶段的"形状"(几个、什么类型、走到哪),不要输出 —— 所以
// log / log_refs / findings / config 四个大列不读,返回的这几个字段为空。要日志走详情。
// 发布页每几秒轮询一次列表,逐单查阶段(N+1)加读整段日志曾把本地单连接 SQLite 拖到排队。
func (r *Repo) StageShapesOfReleases(ids []int64) (map[int64][]model.ReleaseStage, error) {
	out := make(map[int64][]model.ReleaseStage, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	var ss []model.ReleaseStage
	err := r.db.Omit("log", "log_refs", "findings", "config").
		Where("release_id IN ?", ids).
		Order("release_id asc, step_order asc, id asc").Find(&ss).Error
	if err != nil {
		return nil, err
	}
	for _, s := range ss {
		out[s.ReleaseID] = append(out[s.ReleaseID], s)
	}
	return out, nil
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

// FinishRelease writes a release's terminal state and, when settle is set, settles
// its stages and voids its open tickets (see settleRelease) — in ONE transaction.
//
// Written one after the other (as they used to be) a reader in between saw a
// failed release whose later stages were still pending, and a process dying there
// left it that way for good (issue #107). Skipping first is no better: a resumed
// run passes over skipped stages and would report the failed release as a success.
//
// from != "" makes it a claim: nothing is written unless the release is still in
// that state. The results are the tickets voided and whether this caller's write
// took effect.
func (r *Repo) FinishRelease(id int64, from string, fields map[string]any, settle bool, now time.Time) ([]model.Approval, bool, error) {
	var voided []model.Approval
	done := false
	err := r.db.Transaction(func(tx *gorm.DB) error {
		q := tx.Model(&model.Release{}).Where("id = ?", id)
		if from != "" {
			q = q.Where("status = ?", from)
		}
		res := q.Updates(fields)
		if res.Error != nil {
			return res.Error
		}
		if from != "" && res.RowsAffected != 1 {
			return nil
		}
		if settle {
			var err error
			if voided, err = settleRelease(tx, id, now); err != nil {
				return err
			}
		}
		done = true
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return voided, done, nil
}

// settleRelease closes what a release that ended without completing leaves open.
//
// Stages: one cut off mid-run failed, one never reached is skipped. Pending and
// waiting rows on a finished release are debris, not work.
//
// Tickets: every one still pending is voided, using the same word as the timeout
// sweep (expired — no human decision recorded, because none was made). They are
// found by the release they carry, NOT by the ticket a stage recorded: a run cut
// off right after raising its ticket never got to record it, and that ticket
// would sit in the approvers' queue for good (issue #111). One a person already
// decided keeps its outcome.
func settleRelease(tx *gorm.DB, releaseID int64, now time.Time) ([]model.Approval, error) {
	var open []model.Approval
	if err := tx.Where("release_id = ? AND status = ?", releaseID, model.StatusPending).
		Order("id asc").Find(&open).Error; err != nil {
		return nil, err
	}
	var voided []model.Approval
	for _, ap := range open {
		res := tx.Model(&model.Approval{}).Where("id = ? AND status = ?", ap.ID, model.StatusPending).
			Update("status", model.StatusExpired)
		if res.Error != nil {
			return nil, res.Error
		}
		if res.RowsAffected != 1 {
			continue // decided in the meantime: that outcome stands
		}
		voided = append(voided, ap)
		// The stage that raised it may not have recorded it yet. Its run is over,
		// so the one approve stage cut off mid-run is the one.
		if err := tx.Model(&model.ReleaseStage{}).
			Where("release_id = ? AND type = ? AND status = ? AND approval_id = 0", releaseID, model.StageApprove, model.RunRunning).
			Updates(map[string]any{"approval_id": ap.ID, "approval_no": ap.ApNo}).Error; err != nil {
			return nil, err
		}
	}
	if err := tx.Model(&model.ReleaseStage{}).
		Where("release_id = ? AND status = ?", releaseID, model.RunRunning).
		Updates(map[string]any{"status": model.RunFailed, "finished_at": now}).Error; err != nil {
		return nil, err
	}
	err := tx.Model(&model.ReleaseStage{}).
		Where("release_id = ? AND status IN ?", releaseID, []string{model.RunPending, model.RunWaiting}).
		Updates(map[string]any{"status": model.RunSkipped, "finished_at": now}).Error
	return voided, err
}

// WaitingApprovalStages returns the approve stages currently blocking a release,
// which is what the sweeper polls to notice a decided ticket.
func (r *Repo) WaitingApprovalStages() ([]model.ReleaseStage, error) {
	var ss []model.ReleaseStage
	err := r.db.Where("status = ? AND type = ? AND approval_id > 0", model.RunWaiting, model.StageApprove).
		Find(&ss).Error
	return ss, err
}

// WaitingConfirmStages 拉出所有停在**人工闸**上等人点「继续」的阶段。
//
// 只取 manual 与 execute：approve 阶段也会 waiting，但它等的是一张审批工单,那条路由
// 待办的第一类覆盖(见 service/release_confirm.go)。
//
// 不带 owner 条件 —— 谁能放行哪道闸由 service.mayConfirmGate 逐张判,而那条规则读的是
// 阶段配置里的角色,SQL 表达不了。停在闸上的阶段天然是小集合(每张发布单最多停一处)。
func (r *Repo) WaitingConfirmStages() ([]model.ReleaseStage, error) {
	var ss []model.ReleaseStage
	err := r.db.Where("status = ? AND type IN ?", model.RunWaiting,
		[]string{model.StageManual, model.StageExecute, model.StageAutoExecute}).
		Order("id desc").Find(&ss).Error
	return ss, err
}

// FailStuckReleases marks releases left mid-flight by a previous process.
//
// Only `running` rows are reconciled. A `waiting` release is blocked on a human
// and its state lives in the database, so it resumes cleanly after a restart —
// failing it would throw away an approval someone already gave. A `running` one
// cannot be resumed: its execute stage may have reached the target database, and
// re-running it is the one outcome worse than reporting a failure.
//
// Returns how many were failed, and the tickets voided along with them.
func (r *Repo) FailStuckReleases() (int64, []model.Approval, error) {
	now := time.Now()
	var n int64
	var voided []model.Approval
	err := r.db.Transaction(func(tx *gorm.DB) error {
		var ids []int64
		if err := tx.Model(&model.Release{}).Where("status = ?", model.RunRunning).Order("id asc").Pluck("id", &ids).Error; err != nil {
			return err
		}
		for _, id := range ids {
			res := tx.Model(&model.Release{}).Where("id = ? AND status = ?", id, model.RunRunning).
				Updates(map[string]any{
					"status":      model.RunFailed,
					"error":       model.RenderRelLog(model.NewRuleRef(model.RelErrRestart)),
					"error_ref":   `{"code":"` + model.RelErrRestart + `"}`,
					"finished_at": now,
				})
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected != 1 {
				continue
			}
			n++
			aps, err := settleRelease(tx, id, now)
			if err != nil {
				return err
			}
			voided = append(voided, aps...)
		}
		return nil
	})
	if err != nil {
		return 0, nil, err
	}
	return n, voided, nil
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
