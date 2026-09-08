package service

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"velagateway/internal/dto"
	"velagateway/internal/gateway"
	"velagateway/internal/model"
	"velagateway/internal/review"
	"velagateway/pkg/sqlutil"
)

// 数据库变更发布流水线 (CI/CD).
//
// A release is one change, one target instance, and an ordered list of stages it
// must pass: 规范审查 → 审批 → 备份 → 执行 → 校验, in whatever combination the
// organisation defines. The point of running it here rather than in an external
// CI system is that the execute stage goes through the SAME gate as the terminal:
// the capability matrix, the high-risk dictionary and strict mode all judge it
// again at the moment it runs.
//
// That re-judgement is deliberate and non-negotiable. Every other execution
// channel in this gateway has been through the same correction (ER6: the async
// channel let anyone blocked in the terminal resubmit the identical statement as
// a background job). A pipeline is the most attractive bypass of all — it is
// asynchronous, it runs as a service, and its whole purpose is to apply changes
// to production — so it re-reads the verdict at execute time and refuses a
// release whose SQL now requires an approval it does not hold.

// releaseWorkers is how many releases advance in parallel. Stages within one
// release are strictly sequential (that is what a pipeline means); this bounds
// how many DIFFERENT releases can be mid-flight.
const releaseWorkers = 3

// ---------------------------------------------------------------- 模板 (templates)

// ListPipelines returns the templates with their stage definitions.
func (s *Services) ListPipelines() []dto.PipelineDetail {
	ps, err := s.Repo.ListPipelines()
	if err != nil {
		return []dto.PipelineDetail{}
	}
	out := make([]dto.PipelineDetail, 0, len(ps))
	for _, p := range ps {
		stages, _ := s.Repo.StagesOfPipeline(p.ID)
		if stages == nil {
			stages = []model.PipelineStage{}
		}
		out = append(out, dto.PipelineDetail{Pipeline: p, Stages: stages})
	}
	return out
}

// GetPipeline returns one template with its stages.
func (s *Services) GetPipeline(id int64) (*dto.PipelineDetail, error) {
	p, err := s.Repo.GetPipeline(id)
	if err != nil {
		return nil, ErrNotFound
	}
	stages, _ := s.Repo.StagesOfPipeline(id)
	if stages == nil {
		stages = []model.PipelineStage{}
	}
	return &dto.PipelineDetail{Pipeline: *p, Stages: stages}, nil
}

// SavePipeline creates or replaces a template.
//
// A template with no EXECUTE stage is accepted: a "审查 + 审批" flow that ends in
// a manual hand-off to a DBA is a real workflow. A template with no stages at
// all is not — it would report success without doing anything, which is the one
// outcome a release must never produce.
func (s *Services) SavePipeline(u *model.User, id int64, req dto.PipelineReq) (*dto.PipelineDetail, error) {
	if strings.TrimSpace(req.Name) == "" {
		return nil, fmt.Errorf("流程名称不能为空")
	}
	if len(req.Stages) == 0 {
		return nil, fmt.Errorf("流程至少需要一个阶段")
	}
	if len(req.Stages) > 20 {
		return nil, fmt.Errorf("流程阶段最多 20 个")
	}
	stages := make([]model.PipelineStage, 0, len(req.Stages))
	for i, st := range req.Stages {
		if !validStageType(st.Type) {
			return nil, fmt.Errorf("第 %d 个阶段的类型 %q 不受支持", i+1, st.Type)
		}
		if st.Config != "" && !json.Valid([]byte(st.Config)) {
			return nil, fmt.Errorf("第 %d 个阶段的配置不是合法 JSON", i+1)
		}
		// 指定的审批角色必须存在 —— 拼错的角色名不该等到某次真实发布才暴露。
		// 成员数不在这里查:成员是会变的,保存时有人不代表跑的时候还有人,那一条
		// 由 approvalStepsFor 在阶段运行时把关。
		if st.Type == model.StageApprove {
			if err := s.validateApproverRole(cfgApproverRole(st.Config)); err != nil {
				return nil, fmt.Errorf("第 %d 个阶段: %w", i+1, err)
			}
		}
		if st.Type == model.StageManual || st.Type == model.StageExecute {
			if err := s.validateConfirmRole(cfgConfirmRole(st.Config)); err != nil {
				return nil, fmt.Errorf("第 %d 个阶段: %w", i+1, err)
			}
		}
		name := strings.TrimSpace(st.Name)
		if name == "" {
			name = stageTypeLabel(st.Type)
		}
		onFail := st.OnFailure
		if onFail != model.OnFailureContinue {
			onFail = model.OnFailureAbort
		}
		// 执行阶段失败后继续,意味着后面的校验/通知会宣告一次并未发生的变更成功。
		if st.Type == model.StageExecute {
			onFail = model.OnFailureAbort
		}
		stages = append(stages, model.PipelineStage{
			Name: clip(name, 60), Type: st.Type, Config: st.Config, OnFailure: onFail,
		})
	}
	p := &model.Pipeline{
		ID: id, Name: clip(strings.TrimSpace(req.Name), 100), Description: clip(req.Description, 200),
		TierCode: strings.TrimSpace(req.TierCode), Enabled: req.Enabled, IsDefault: req.IsDefault,
		CreatedBy: u.ID,
	}
	if id > 0 {
		old, err := s.Repo.GetPipeline(id)
		if err != nil {
			return nil, ErrNotFound
		}
		p.CreatedBy, p.CreatedAt = old.CreatedBy, old.CreatedAt
	}
	if err := s.Repo.SavePipeline(p, stages); err != nil {
		return nil, err
	}
	return s.GetPipeline(p.ID)
}

// DeletePipeline removes a template. Releases already running on it are
// unaffected: a run owns a snapshot of its stages.
func (s *Services) DeletePipeline(id int64) error {
	if _, err := s.Repo.GetPipeline(id); err != nil {
		return ErrNotFound
	}
	return s.Repo.DeletePipeline(id)
}

func validStageType(t string) bool {
	switch t {
	case model.StageReview, model.StageApprove, model.StageBackup,
		model.StageExecute, model.StageVerify, model.StageManual, model.StageNotify:
		return true
	}
	return false
}

func stageTypeLabel(t string) string {
	switch t {
	case model.StageReview:
		return "规范审查"
	case model.StageApprove:
		return "人工审批"
	case model.StageBackup:
		return "备份/回滚点"
	case model.StageExecute:
		return "执行变更"
	case model.StageVerify:
		return "执行后校验"
	case model.StageManual:
		return "人工确认"
	case model.StageNotify:
		return "结果通知"
	}
	return t
}

// ---------------------------------------------------------------- 提交 (submit)

// SubmitRelease validates a change, snapshots the chosen flow and queues the run.
//
// The gate runs HERE as well as at execute time, for two different reasons. Here
// it gives the submitter an immediate, actionable refusal when their role may
// not touch the instance at all — better than a release that queues, starts and
// dies three stages later. At execute time it runs again because the rules, the
// instance and the submitter's roles can all change while a release waits for
// approval, and the verdict that matters is the one in force when the statement
// actually runs.
func (s *Services) SubmitRelease(u *model.User, req dto.ReleaseReq) (*model.Release, error) {
	return s.submitRelease(u, req, releaseOrigin{Source: model.ReleaseSourceConsole})
}

// releaseOrigin says WHERE a release came from. The console leaves it at its
// zero value; the open API fills it in so the row, the audit rows and the
// idempotency key all name the external system that asked (see model.Release).
type releaseOrigin struct {
	Source      string
	ClientID    int64
	ClientName  string
	ExternalRef string
	// IdemKey is nil for a console release. See model.Release.IdemKey for why an
	// absent key must be NULL rather than "".
	IdemKey *string
}

// operator renders the origin as an audit operator — who ASKED, when that is not
// the account the action runs as.
func (o releaseOrigin) operator() string {
	if o.Source == model.ReleaseSourceAPI && o.ClientName != "" {
		return "API:" + o.ClientName
	}
	return ""
}

// releaseOperator is origin.operator() reconstructed from a stored release, for
// the stage audits — those run minutes or hours after the submission, from a
// worker that never saw the request.
func releaseOperator(rel *model.Release) string {
	if rel != nil && rel.Source == model.ReleaseSourceAPI && rel.ClientName != "" {
		return "API:" + rel.ClientName
	}
	return ""
}

func (s *Services) submitRelease(u *model.User, req dto.ReleaseReq, origin releaseOrigin) (*model.Release, error) {
	sql := strings.TrimSpace(req.SQL)
	if sql == "" && req.ScriptUploadID == 0 {
		return nil, fmt.Errorf("发布内容不能为空")
	}
	if len(sql) > maxStoredSQLBytes {
		return nil, storedSQLTooLong(len(sql))
	}
	if strings.TrimSpace(req.Title) == "" {
		return nil, fmt.Errorf("发布标题不能为空")
	}
	conn, err := s.Repo.GetConnection(req.ConnectionID)
	if err != nil {
		return nil, ErrNotFound
	}
	applyTargetDatabase(conn, req.Database)
	if !s.canAccessConn(u, conn) {
		return nil, ErrForbidden
	}
	if conn.Status == "maint" {
		return nil, fmt.Errorf("目标实例处于维护态,暂不能发起发布")
	}
	// PROD step-up, same as every other channel that ends in an execution.
	if err := s.checkMFA(u, conn, req.MfaCode); err != nil {
		return nil, err
	}
	tier, err := s.tierCodeOf(conn)
	if err != nil {
		return nil, ErrBadRequest // unresolvable tier — never judged as allow
	}

	// A script-backed release keeps the body in the uploaded file and records the
	// digest that was reviewed, exactly like a script approval.
	body := sql
	sha := ""
	if req.ScriptUploadID > 0 {
		content, _, rerr := s.ReadUploadedScriptFor(u, req.ScriptUploadID)
		if rerr != nil {
			return nil, fmt.Errorf("脚本文件不可读: %w", rerr)
		}
		body = content
		sha = scriptDigest(content)
		sql = "" // the file is the source of truth; the row keeps only the reference
	}
	stmts := sqlutil.SplitStatements(body)
	if len(stmts) == 0 {
		return nil, fmt.Errorf("发布内容中没有可执行的语句")
	}
	// 变更类型:声明必须与内容一致,DML/DDL 不得同单;未声明则推断。内容一经
	// 提交不可变(内联在行里,脚本按 SHA-256 校验),所以只需在这里判一次。
	changeType, err := releaseChangeType(req.ChangeType, stmts)
	if err != nil {
		return nil, err
	}
	// The release capability is judged BEFORE the statement is: "may this role
	// raise a release on this tier" is a separate question from "may it run this
	// SQL", and an operator who has denied production releases to a role means it
	// regardless of how harmless the statement happens to be (see model.CapRelease).
	relLevel, cerr := s.Engine.CapabilityFor(s.Repo.EffectiveRoleIDs(u), model.CapRelease, tier)
	if cerr != nil {
		return nil, ErrBadRequest // an unreadable matrix is never judged as allow (ED3)
	}
	if relLevel == model.LevelDeny {
		s.recordAuditBy(u, origin.operator(), conn, releaseAuditLabel(req.Title, body), model.RiskHigh, model.ResultRejected, "", "intercept")
		return nil, ErrForbidden
	}

	v := s.strictestVerdict(u, conn, stmts)
	if v.Action == gateway.ActionDeny {
		s.recordAuditBy(u, origin.operator(), conn, releaseAuditLabel(req.Title, body), model.RiskHigh, model.ResultRejected, "", "intercept")
		return nil, ErrForbidden
	}

	pipeline, stages, err := s.resolvePipeline(req.PipelineID, tier)
	if err != nil {
		return nil, err
	}
	// An approval-requiring change can only be released through a flow that
	// actually asks someone. Queuing it under a flow with no approve stage would
	// end in an execute stage that refuses — after the operator had been told the
	// release was accepted.
	//
	// Two different things can demand the approval, and the message says which:
	// the STATEMENT (dictionary/matrix verdict) or the ROLE's release capability
	// on this tier, which forces ceremony even for a statement that would have
	// been allowed.
	if !hasStage(stages, model.StageApprove) {
		if v.RequiresApproval() {
			return nil, fmt.Errorf("该变更命中「%s」需审批,请选择包含审批阶段的发布流程", v.Rule)
		}
		if relLevel == model.LevelApprove {
			return nil, fmt.Errorf("当前角色在 %s 分层的发布权限为「需审批」,请选择包含审批阶段的发布流程", strings.ToUpper(tier))
		}
	}

	// 归属项目按提交这一刻的目标库快照下来 —— 库以后改挂别的项目,历史单据不改账。
	projectID, projectName := s.projectOf(conn, req.Database)
	rel := &model.Release{
		RelNo: s.nextRelNo(), Title: clip(strings.TrimSpace(req.Title), 100),
		PipelineID: pipeline.ID, PipelineName: pipeline.Name,
		// effectiveDatabase:Oracle 记 schema,不是服务名 —— 执行阶段要照着它切回去。
		ConnectionID: conn.ID, Instance: conn.Name, Database: effectiveDatabase(conn),
		Env: conn.Env, TierCode: tier, Engine: conn.Engine, ChangeType: changeType,
		ProjectID: projectID, ProjectName: projectName,
		SQL: sql, ScriptUploadID: req.ScriptUploadID, ScriptSHA256: sha,
		Reason: clip(req.Reason, 400), CreatorID: u.ID, Creator: u.Name,
		Status: model.RunPending, Risk: v.Risk,
		Source:   orDefault(origin.Source, model.ReleaseSourceConsole),
		ClientID: origin.ClientID, ClientName: origin.ClientName,
		ExternalRef: clip(origin.ExternalRef, 120), IdemKey: origin.IdemKey,
	}
	runStages := make([]model.ReleaseStage, 0, len(stages))
	for _, st := range stages {
		runStages = append(runStages, model.ReleaseStage{
			StepOrder: st.StepOrder, Name: st.Name, Type: st.Type, Config: st.Config,
			OnFailure: st.OnFailure, Status: model.RunPending,
		})
	}
	if err := s.Repo.CreateRelease(rel, runStages); err != nil {
		// A duplicate idempotency key means an external caller retried and the
		// first attempt already exists. Hand back THAT release rather than an
		// error: the caller asked for one ticket and there is exactly one.
		if origin.IdemKey != nil {
			if prev, perr := s.Repo.GetReleaseByIdemKey(*origin.IdemKey); perr == nil {
				return prev, nil
			}
		}
		return nil, err
	}
	// The submission itself is audited before anything runs: a release that is
	// later aborted still happened, and the chain should say who asked for it.
	s.recordAuditBy(u, origin.operator(), conn, releaseAuditLabel(rel.Title, body), v.Risk, model.ResultPending, rel.RelNo, "intercept")
	s.enqueueRelease(rel.ID)
	return rel, nil
}

// resolvePipeline picks the template for a release and returns its stages.
func (s *Services) resolvePipeline(id int64, tier string) (*model.Pipeline, []model.PipelineStage, error) {
	var p *model.Pipeline
	var err error
	if id > 0 {
		p, err = s.Repo.GetPipeline(id)
		if err != nil {
			return nil, nil, fmt.Errorf("发布流程不存在")
		}
		if !p.Enabled {
			return nil, nil, fmt.Errorf("发布流程「%s」已停用", p.Name)
		}
		// A tier-scoped template governs that tier only; running a dev flow against
		// production is precisely the mix-up the scope exists to prevent.
		if p.TierCode != "" && p.TierCode != tier {
			return nil, nil, fmt.Errorf("发布流程「%s」仅适用于 %s 分层", p.Name, strings.ToUpper(p.TierCode))
		}
	} else if p, err = s.Repo.DefaultPipelineFor(tier); err != nil {
		return nil, nil, fmt.Errorf("尚未配置可用的发布流程")
	}
	stages, err := s.Repo.StagesOfPipeline(p.ID)
	if err != nil || len(stages) == 0 {
		return nil, nil, fmt.Errorf("发布流程「%s」没有配置阶段", p.Name)
	}
	return p, stages, nil
}

func hasStage(stages []model.PipelineStage, t string) bool {
	for _, st := range stages {
		if st.Type == t {
			return true
		}
	}
	return false
}

func (s *Services) nextRelNo() string {
	return fmt.Sprintf("REL-%d", s.relCounter.Add(1))
}

// releaseAuditLabel keeps the audit row readable without dragging a migration
// body through the chain.
func releaseAuditLabel(title, body string) string {
	return "RELEASE " + title + " :: " + safeClip(strings.Join(strings.Fields(body), " "), 200)
}

func (s *Services) enqueueRelease(id int64) {
	select {
	case s.releaseQueue <- id:
	default:
		_ = s.Repo.UpdateRelease(id, map[string]any{
			"status": model.RunFailed, "error": "发布队列已满,请稍后重试", "finished_at": time.Now(),
		})
	}
}

// ---------------------------------------------------------------- 运行 (runner)

// dispatchReleaseJob is the worker entry point. A positive id is a fresh run
// that still has to be claimed; a NEGATIVE one is a resume of a run
// continueRelease already claimed, which must not be claimed a second time (the
// row is `running` by then, so a second claim would silently drop it).
func (s *Services) dispatchReleaseJob(id int64) {
	if id < 0 {
		s.guardRelease(-id, func() { s.driveRelease(-id) })
		return
	}
	s.runReleaseSafe(id)
}

// guardRelease runs fn with panic recovery so a single bad run never kills the
// worker or leaves the release stuck mid-flight.
func (s *Services) guardRelease(id int64, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("release run panicked", "release", id, "err", r)
			_ = s.Repo.UpdateRelease(id, map[string]any{
				"status": model.RunFailed, "error": fmt.Sprintf("发布执行异常: %v", r), "finished_at": time.Now(),
			})
		}
	}()
	fn()
}

// runReleaseSafe claims a queued run and drives it.
func (s *Services) runReleaseSafe(id int64) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("release run panicked", "release", id, "err", r)
			_ = s.Repo.UpdateRelease(id, map[string]any{
				"status": model.RunFailed, "error": fmt.Sprintf("发布执行异常: %v", r), "finished_at": time.Now(),
			})
		}
	}()
	now := time.Now()
	claimed, err := s.Repo.ClaimRelease(id, model.RunPending, model.RunRunning, &now)
	if err != nil || !claimed {
		return // already running, or reconciled by another worker
	}
	s.driveRelease(id)
}

// driveRelease advances a run through its stages until one blocks on a human,
// one fails fatally, or the flow completes. It is re-entrant: resuming after an
// approval calls it again, and every already-terminal stage is skipped.
func (s *Services) driveRelease(id int64) {
	rel, err := s.Repo.GetRelease(id)
	if err != nil {
		return
	}
	if rel.Status == model.RunAborted {
		return
	}
	conn, cerr := s.Repo.GetConnection(rel.ConnectionID)
	if cerr != nil {
		s.finishRelease(rel, model.RunFailed, "目标连接已不存在")
		return
	}
	// 统一入口,不裸赋值(理由同 target_database.go)。
	applyTargetDatabase(conn, rel.Database)
	stages, err := s.Repo.StagesOfRelease(id)
	if err != nil {
		return
	}
	for i := range stages {
		st := stages[i]
		switch st.Status {
		case model.RunSuccess, model.RunSkipped, model.RunFailed:
			continue // done, or failed under onFailure=continue
		case model.RunWaiting:
			return // blocked on an approval / a person
		}
		// Claim the stage so a resumed run and a queued one cannot both execute it.
		claimed, cerr := s.Repo.ClaimReleaseStage(st.ID, st.Status, model.RunRunning)
		if cerr != nil || !claimed {
			return
		}
		start := time.Now()
		_ = s.Repo.UpdateReleaseStage(st.ID, map[string]any{"started_at": start})

		out := s.runStage(rel, conn, &st)

		fields := map[string]any{"status": out.status, "log": clip(out.log, 20000), "rows": out.rows}
		if out.findings != "" {
			fields["findings"] = out.findings
		}
		if out.approvalID > 0 {
			fields["approval_id"] = out.approvalID
			fields["approval_no"] = out.approvalNo
		}
		if out.status != model.RunWaiting {
			fields["finished_at"] = time.Now()
		}
		_ = s.Repo.UpdateReleaseStage(st.ID, fields)

		switch out.status {
		case model.RunWaiting:
			_ = s.Repo.UpdateRelease(rel.ID, map[string]any{"status": model.RunWaiting})
			s.notify(rel.CreatorID, model.NotifReleaseWait, "发布等待处理",
				fmt.Sprintf("%s「%s」进入阶段:%s", rel.RelNo, rel.Title, st.Name), rel.RelNo)
			return
		case model.RunFailed:
			if st.OnFailure == model.OnFailureContinue {
				continue // recorded as failed, flow carries on by explicit configuration
			}
			s.finishRelease(rel, model.RunFailed, fmt.Sprintf("阶段「%s」失败: %s", st.Name, clip(out.log, 300)))
			return
		}
	}
	s.finishRelease(rel, model.RunSuccess, "")
}

// stageOutcome is what one stage reports back to the driver.
type stageOutcome struct {
	status     string // success|failed|skipped|waiting
	log        string
	findings   string // review stage only: the JSON result
	rows       int
	approvalID int64
	approvalNo string
}

func failStage(format string, a ...any) stageOutcome {
	return stageOutcome{status: model.RunFailed, log: fmt.Sprintf(format, a...)}
}

func okStage(format string, a ...any) stageOutcome {
	return stageOutcome{status: model.RunSuccess, log: fmt.Sprintf(format, a...)}
}

func (s *Services) runStage(rel *model.Release, conn *model.Connection, st *model.ReleaseStage) stageOutcome {
	cfg := parseStageConfig(st.Config)
	switch st.Type {
	case model.StageReview:
		return s.stageReview(rel, conn, cfg)
	case model.StageApprove:
		return s.stageApprove(rel, conn, st)
	case model.StageBackup:
		return s.stageBackup(rel, conn, cfg)
	case model.StageExecute:
		return s.stageExecute(rel, conn, st)
	case model.StageVerify:
		return s.stageVerify(rel, conn, cfg)
	case model.StageManual:
		if role := cfgConfirmRole(st.Config); role != "" {
			people, err := s.gateStaffing(role)
			if err != nil {
				return failStage("· %v", err)
			}
			return stageOutcome{status: model.RunWaiting,
				log: "· 等待「" + gateWhoLabel(people) + "」人工确认" + cfgNote(cfg)}
		}
		return stageOutcome{status: model.RunWaiting, log: "· 等待人工确认" + cfgNote(cfg)}
	case model.StageNotify:
		return s.stageNotify(rel, conn)
	}
	// An unknown type cannot be silently skipped: the flow would report success
	// having omitted a step somebody put there on purpose.
	return failStage("· 未知的阶段类型 %s", st.Type)
}

// stageReview runs the 规范审查 rule library against the release body.
func (s *Services) stageReview(rel *model.Release, conn *model.Connection, cfg stageCfg) stageOutcome {
	body, err := s.releaseBody(rel)
	if err != nil {
		return failStage("· 无法读取发布内容: %v", err)
	}
	// The engine SNAPSHOT decides the dialect, not the connection's current label:
	// the release was written for the database it was aimed at, and re-reading the
	// row after someone edits the instance would review an Oracle change under
	// MySQL rules.
	engine := rel.Engine
	if engine == "" {
		engine = conn.Engine
	}
	dialect := review.DialectFor(engine)
	res := review.Check(dialect, body, s.Repo.ReviewRules())
	payload, _ := json.Marshal(res)
	var b strings.Builder
	fmt.Fprintf(&b, "· 方言 %s · 共 %d 条语句\n", dialect, res.Statements)
	fmt.Fprintf(&b, "· 命中 %d 错误 / %d 警告 / %d 提示\n", res.Errors, res.Warnings, res.Infos)
	shown := res.Findings
	if len(shown) > 50 {
		shown = shown[:50]
	}
	for _, f := range shown {
		fmt.Fprintf(&b, "  [%s] 第%d条 %s: %s\n", strings.ToUpper(f.Level), f.Stmt, f.Name, f.Message)
	}
	if len(res.Findings) > len(shown) {
		fmt.Fprintf(&b, "  …另有 %d 条,详见审查结果\n", len(res.Findings)-len(shown))
	}
	// failOn decides what a finding COSTS here, and "error" is the default so a
	// library full of style advice cannot block every release on day one.
	switch cfg.str("failOn", "error") {
	case "none":
		return stageOutcome{status: model.RunSuccess, log: b.String(), findings: string(payload)}
	case "warn":
		if res.Errors+res.Warnings > 0 {
			return stageOutcome{status: model.RunFailed, log: b.String(), findings: string(payload)}
		}
	default:
		if res.Errors > 0 {
			return stageOutcome{status: model.RunFailed, log: b.String(), findings: string(payload)}
		}
	}
	return stageOutcome{status: model.RunSuccess, log: b.String(), findings: string(payload)}
}

// stageApprove raises an approval ticket and parks the run on it.
//
// The ticket carries ReleaseID, which is what keeps it out of the manual
// execute path (ExecuteApproved): the pipeline owns execution, and a change
// applied twice — once by hand from the approvals page, once by the execute
// stage — is the failure this link exists to prevent.
func (s *Services) stageApprove(rel *model.Release, conn *model.Connection, st *model.ReleaseStage) stageOutcome {
	if st.ApprovalID > 0 {
		return stageOutcome{status: model.RunWaiting, log: "· 已存在审批单 " + st.ApprovalNo}
	}
	creator, _ := s.Repo.GetUserByID(rel.CreatorID)
	if creator == nil {
		creator = &model.User{ID: rel.CreatorID, Name: rel.Creator}
	}
	body, err := s.releaseBody(rel)
	if err != nil {
		return failStage("· 无法读取发布内容: %v", err)
	}
	label := releaseApprovalLabel(rel, body)
	risk := rel.Risk
	if risk == "" {
		risk = model.RiskMid
	}
	// 审批人:阶段配置指定的角色,没配则走全局默认链。空链是致命的 ——
	// isChainMember 对谁都返回 false,单子建出来没有任何人能批,发布永远停在
	// waiting。宁可在这里失败并说清楚,也不要造一张死单。
	steps, serr := s.approvalStepsFor(cfgApproverRole(st.Config))
	if serr != nil {
		return failStage("· %v", serr)
	}
	ap := &model.Approval{
		ApNo: s.nextApNo(), ConnectionID: conn.ID, Env: rel.Env, TierCode: rel.TierCode,
		Instance: conn.Name, Command: label, Keyword: firstWord(body), Database: rel.Database,
		InitiatorID: creator.ID, Initiator: creator.Name,
		Reason:    strings.TrimSpace(rel.Reason + " · 发布单 " + rel.RelNo),
		RiskLevel: risk, Status: model.StatusPending, AuditID: s.nextAuditID(),
		ScriptUploadID: rel.ScriptUploadID, ScriptSHA256: rel.ScriptSHA256,
		ReleaseID: rel.ID,
	}
	steps[0].Status = "active" // approvalStepsFor guarantees a non-empty chain
	if err := s.Repo.CreateApproval(ap, steps); err != nil {
		return failStage("· 审批单创建失败: %v", err)
	}
	s.Webhook.SendLarkApproval(ap)
	s.dispatchExternalApproval(creator, ap)
	s.recordAuditBy(creator, releaseOperator(rel), conn, label, risk, model.ResultPending, ap.ApNo, "intercept")
	return stageOutcome{
		status: model.RunWaiting, approvalID: ap.ID, approvalNo: ap.ApNo,
		log: "· 已创建审批单 " + ap.ApNo + ",等待审批",
	}
}

// backupVerbs is what a backup statement may BE. A backup copies data — CREATE
// … AS SELECT, INSERT … SELECT, SELECT INTO — so only copying verbs pass.
// (ParseVerb resolves a WITH to its main clause's verb, so a CTE-fronted copy
// still lands here and a CTE-smuggled mutation lands on its mutation.)
//
// The check exists because template config is the one execution channel that
// never meets the risk engine: the SQL is written by an admin against no
// particular target, then runs later against whichever instance a release
// picks. An allowlist of copying verbs keeps the slot being what its name says,
// no matter who edits the template or where the release points it.
var backupVerbs = map[string]bool{"CREATE": true, "INSERT": true, "SELECT": true}

// stageBackup runs the configured backup statement(s). With none configured the
// stage is SKIPPED and says so — reporting a green "备份完成" for a step that did
// nothing would be the most dangerous line in the whole run.
func (s *Services) stageBackup(rel *model.Release, conn *model.Connection, cfg stageCfg) stageOutcome {
	sql := strings.TrimSpace(cfg.str("sql", ""))
	if sql == "" {
		return stageOutcome{status: model.RunSkipped,
			log: "· 未配置备份语句,本阶段跳过(物理备份需由 DBA 侧完成)"}
	}
	stmts := sqlutil.SplitStatements(sql)
	if len(stmts) == 0 {
		return stageOutcome{status: model.RunSkipped, log: "· 备份配置中没有可执行的语句,本阶段跳过"}
	}
	// Judge EVERY statement before running ANY: a mutation hidden behind a
	// legitimate first statement must not get the first one executed.
	for _, one := range stmts {
		if verb := gateway.ParseVerb(one); !backupVerbs[verb] {
			return failStage("· 备份语句只允许复制数据(CREATE/INSERT/SELECT),拒绝执行 %s;如需其它操作请放入发布内容走完整判定", verb)
		}
	}
	creator, _ := s.Repo.GetUserByID(rel.CreatorID)
	var b strings.Builder
	total := 0
	for i, one := range stmts {
		res := s.Executor.Run(conn, one, s.asyncExecTimeout())
		if res.Err != nil {
			return failStage("· 备份第 %d/%d 条失败: %s", i+1, len(stmts), res.Output)
		}
		total += res.Rows
		fmt.Fprintf(&b, "· [%d/%d] %s (%dms)\n", i+1, len(stmts), clip(res.Output, 200), res.Ms)
	}
	if creator != nil {
		s.recordAuditBy(creator, releaseOperator(rel), conn, "RELEASE-BACKUP "+rel.RelNo+" :: "+safeClip(sql, 200),
			model.RiskLow, model.ResultExecuted, rel.RelNo, "exec")
	}
	return stageOutcome{status: model.RunSuccess, rows: total,
		log: fmt.Sprintf("· 备份完成 · %d 条语句\n%s", len(stmts), b.String())}
}

// stageExecute applies the change — after a human clicks and after judging it
// again. The gate is unconditional: 审批回答"可不可以做",这里回答"现在做",
// 变更窗口和上下游就绪只有到点的人知道,自动落库等于把时机交给调度器。
func (s *Services) stageExecute(rel *model.Release, conn *model.Connection, st *model.ReleaseStage) stageOutcome {
	if st.ConfirmedBy == "" {
		if role := cfgConfirmRole(st.Config); role != "" {
			people, err := s.gateStaffing(role)
			if err != nil {
				return failStage("· %v", err)
			}
			return stageOutcome{status: model.RunWaiting,
				log: "· 等待「" + gateWhoLabel(people) + "」确认执行后才会落库"}
		}
		return stageOutcome{status: model.RunWaiting,
			log: "· 等待人工确认执行(创建者或审批角色点击「确认执行」后才会落库)"}
	}
	body, err := s.releaseBody(rel)
	if err != nil {
		return failStage("· 无法读取发布内容: %v", err)
	}
	creator, _ := s.Repo.GetUserByID(rel.CreatorID)
	if creator == nil {
		return failStage("· 发布人不存在,未执行")
	}
	if conn.Status == "maint" {
		return failStage("· 目标实例处于维护态,未执行")
	}
	// An instance whose tier no longer resolves is never judged as allowed —
	// strictestVerdict returns Unavailable for it, but failing here says why.
	if _, terr := s.tierCodeOf(conn); terr != nil {
		return failStage("· 实例的分层标签解析失败,未执行")
	}
	stmts := sqlutil.SplitStatements(body)
	if len(stmts) == 0 {
		return failStage("· 发布内容中没有可执行的语句")
	}
	v := s.strictestVerdict(creator, conn, stmts)
	switch {
	case v.Action == gateway.ActionDeny:
		s.recordAuditBy(creator, releaseOperator(rel), conn, releaseAuditLabel(rel.Title, body), model.RiskHigh, model.ResultRejected, rel.RelNo, "intercept")
		return failStage("· 执行前复判被拒绝(%s),未执行", v.Rule)
	case v.RequiresApproval() && !s.releaseApproved(rel.ID):
		// The verdict changed while the release waited, or the flow never had an
		// approve stage. Either way nothing runs without the approval it now needs.
		return failStage("· 执行前复判需要审批(%s),但本次发布没有已通过的审批单,未执行", v.Rule)
	}

	timeout := s.asyncExecTimeout()
	var b strings.Builder
	total := 0
	for i, one := range stmts {
		res := s.Executor.Run(conn, one, timeout)
		if res.Err != nil {
			fmt.Fprintf(&b, "· 第 %d/%d 条失败: %s\n", i+1, len(stmts), clip(res.Output, 300))
			s.recordAuditBy(creator, releaseOperator(rel), conn, one, v.Risk, model.ResultWarn, rel.RelNo, "exec")
			return stageOutcome{status: model.RunFailed, rows: total,
				log: fmt.Sprintf("· 已执行 %d/%d 条后中止\n%s", i, len(stmts), b.String())}
		}
		total += res.Rows
		fmt.Fprintf(&b, "· [%d/%d] %s (%dms)\n", i+1, len(stmts), clip(res.Output, 200), res.Ms)
		// A statement that returned a result SET gets it echoed into the log:
		// releases carry SELECTs to verify their own work, and the operator
		// reading the run wants to see what came back, not re-run the query.
		b.WriteString(resultPreview(res))
		// Every statement is audited individually: the chain must show what ran,
		// not that "a release ran".
		s.recordAuditBy(creator, releaseOperator(rel), conn, one, v.Risk, model.ResultExecuted, rel.RelNo, "exec")
	}
	return stageOutcome{status: model.RunSuccess, rows: total,
		log: fmt.Sprintf("· 执行完成 · %d 条语句 · 影响 %d 行\n%s", len(stmts), total, b.String())}
}

// execLogPreviewRows bounds how many result rows an execute log echoes. The
// log is an execution record, not a data export — someone who needs the full
// set has the 数据导出 channel, which encrypts and audits it as an export.
const execLogPreviewRows = 20

// execLogCellWidth clips one cell: a TEXT column holding a document must not
// turn the log into that document.
const execLogCellWidth = 60

// resultPreview renders a statement's result set as an indented text table for
// the stage log. Empty for DML (no columns) — "N 行受影响" already says
// everything a write returns.
func resultPreview(res gateway.ExecResult) string {
	if len(res.Columns) == 0 || len(res.Data) == 0 {
		return ""
	}
	rows := res.Data
	elided := 0
	if len(rows) > execLogPreviewRows {
		elided = len(rows) - execLogPreviewRows
		rows = rows[:execLogPreviewRows]
	}
	cell := func(s string) string {
		s = strings.ReplaceAll(s, "\n", "␤")
		if len(s) > execLogCellWidth {
			return s[:execLogCellWidth-1] + "…"
		}
		return s
	}
	// Column widths from header + shown rows (byte width — good enough for a
	// log; CJK cells align imperfectly and that is fine).
	w := make([]int, len(res.Columns))
	for j, c := range res.Columns {
		w[j] = len(cell(c))
	}
	for _, r := range rows {
		for j, v := range r {
			if j < len(w) && len(cell(v)) > w[j] {
				w[j] = len(cell(v))
			}
		}
	}
	var b strings.Builder
	line := func(cells []string) {
		b.WriteString("    ")
		for j := 0; j < len(res.Columns); j++ {
			v := ""
			if j < len(cells) {
				v = cell(cells[j])
			}
			fmt.Fprintf(&b, "%-*s", w[j], v)
			if j < len(res.Columns)-1 {
				b.WriteString(" | ")
			}
		}
		b.WriteString("\n")
	}
	line(res.Columns)
	b.WriteString("    ")
	for j := range res.Columns {
		b.WriteString(strings.Repeat("-", w[j]))
		if j < len(res.Columns)-1 {
			b.WriteString("-+-")
		}
	}
	b.WriteString("\n")
	for _, r := range rows {
		line(r)
	}
	// Elision is SAID, never silent: a log that looks complete but isn't is
	// worse than either a complete one or an honest preview.
	switch {
	case res.Truncated:
		fmt.Fprintf(&b, "    … 结果集超出返回上限已被截断,日志预览前 %d 行\n", len(rows))
	case elided > 0:
		fmt.Fprintf(&b, "    … 已省略 %d 行(日志仅预览前 %d 行,需完整数据请走数据导出)\n", elided, execLogPreviewRows)
	}
	return b.String()
}

// releaseApproved reports whether this release holds an APPROVED ticket. It
// reads the approval row rather than the stage status, because the stage records
// what the pipeline believed and the ticket records what a person decided.
func (s *Services) releaseApproved(releaseID int64) bool {
	stages, err := s.Repo.StagesOfRelease(releaseID)
	if err != nil {
		return false
	}
	for _, st := range stages {
		if st.Type != model.StageApprove || st.ApprovalID == 0 {
			continue
		}
		ap, err := s.Repo.GetApproval(st.ApprovalID)
		if err == nil && ap.Status == model.StatusApproved {
			return true
		}
	}
	return false
}

// stageVerify runs a read-only check after the change and can assert its shape.
func (s *Services) stageVerify(rel *model.Release, conn *model.Connection, cfg stageCfg) stageOutcome {
	sql := strings.TrimSpace(cfg.str("sql", ""))
	if sql == "" {
		return stageOutcome{status: model.RunSkipped, log: "· 未配置校验语句,本阶段跳过"}
	}
	if !gateway.IsRead(sql) {
		// A verification that mutates is a second, unreviewed change.
		return failStage("· 校验语句必须是只读查询")
	}
	res := s.Executor.Run(conn, sql, s.execTimeout())
	if res.Err != nil {
		return failStage("· 校验执行失败: %s", res.Output)
	}
	creator, _ := s.Repo.GetUserByID(rel.CreatorID)
	if creator != nil {
		s.recordAuditBy(creator, releaseOperator(rel), conn, "RELEASE-VERIFY "+rel.RelNo+" :: "+safeClip(sql, 200),
			model.RiskLow, model.ResultExecuted, rel.RelNo, "exec")
	}
	log := fmt.Sprintf("· 校验完成 · %s (%dms)", res.Output, res.Ms)
	switch cfg.str("expect", "") {
	case "empty":
		if res.Rows != 0 {
			return stageOutcome{status: model.RunFailed, rows: res.Rows,
				log: log + fmt.Sprintf("\n· 期望结果为空,实际返回 %d 行", res.Rows)}
		}
	case "nonempty":
		if res.Rows == 0 {
			return stageOutcome{status: model.RunFailed, rows: res.Rows, log: log + "\n· 期望有结果,实际返回 0 行"}
		}
	}
	return stageOutcome{status: model.RunSuccess, rows: res.Rows, log: log}
}

// stageNotify records the release outcome in the audit chain, which is also what
// feeds the outbound webhook (the dispatcher is driven by audit events — see
// recordAudit), and drops an in-app message to the release creator.
func (s *Services) stageNotify(rel *model.Release, conn *model.Connection) stageOutcome {
	creator, _ := s.Repo.GetUserByID(rel.CreatorID)
	if creator == nil {
		creator = &model.User{ID: rel.CreatorID, Name: rel.Creator}
	}
	s.recordAuditBy(creator, releaseOperator(rel), conn, fmt.Sprintf("RELEASE-NOTIFY %s :: %s", rel.RelNo, rel.Title),
		model.RiskLow, model.ResultExecuted, rel.RelNo, "exec")
	s.notify(rel.CreatorID, model.NotifReleaseDone, "发布进度通知",
		fmt.Sprintf("%s「%s」已执行完成,等待后续阶段", rel.RelNo, rel.Title), rel.RelNo)
	return okStage("· 已推送发布事件(审计 + Webhook)")
}

// finishRelease writes the terminal state once and notifies the creator.
//
// A failed terminal also SKIPS every stage the run never reached: a dead
// release whose later stages read 待执行 forever looks like a run that is
// still coming — the same debris AbortRelease already tidies, so every
// terminal path tidies it the same way.
func (s *Services) finishRelease(rel *model.Release, status, errMsg string) {
	now := time.Now()
	_ = s.Repo.UpdateRelease(rel.ID, map[string]any{
		"status": status, "error": clip(errMsg, 400), "finished_at": now,
	})
	if status != model.RunSuccess {
		s.skipUnrunStages(rel.ID, now)
	}
	if status == model.RunSuccess {
		s.notify(rel.CreatorID, model.NotifReleaseDone, "发布完成",
			fmt.Sprintf("%s「%s」已全部阶段通过", rel.RelNo, rel.Title), rel.RelNo)
		return
	}
	s.notify(rel.CreatorID, model.NotifReleaseFailed, "发布失败",
		fmt.Sprintf("%s「%s」%s", rel.RelNo, rel.Title, clip(errMsg, 200)), rel.RelNo)
}

// releaseBody returns the SQL a release applies: the inline body, or the
// uploaded script re-read from disk and verified against the digest recorded
// when it was submitted. The file can change between submission and execution,
// and nothing else would notice.
func (s *Services) releaseBody(rel *model.Release) (string, error) {
	if rel.ScriptUploadID == 0 {
		return rel.SQL, nil
	}
	content, _, err := s.readUploadedScript(rel.ScriptUploadID)
	if err != nil {
		return "", err
	}
	if got := scriptDigest(content); got != rel.ScriptSHA256 {
		return "", ErrScriptChanged
	}
	return content, nil
}

func releaseApprovalLabel(rel *model.Release, body string) string {
	head := fmt.Sprintf("[发布 %s] %s @ %s/%s\n", rel.RelNo, rel.Title, rel.Instance, rel.Database)
	if rel.ScriptUploadID > 0 {
		return head + safeClip(strings.Join(strings.Fields(body), " "), 800)
	}
	if len(body) > 4000 {
		return head + safeClip(body, 4000)
	}
	return head + body
}

// ---------------------------------------------------------------- 恢复 / 人工推进

// ResumeReleaseApprovals is the sweeper: it looks for runs parked on an approval
// that has since been decided and moves them on.
//
// A sweeper rather than a callback from finalizeApproval, because a decision can
// arrive through four different doors — the console, the 飞书 card, the 审批魔方
// callback, and the timeout expiry — and a hook on one of them would leave
// releases hanging when a decision came through another. Polling a row that
// already records the outcome works no matter who wrote it.
func (s *Services) ResumeReleaseApprovals() {
	stages, err := s.Repo.WaitingApprovalStages()
	if err != nil {
		return
	}
	for _, st := range stages {
		ap, err := s.Repo.GetApproval(st.ApprovalID)
		if err != nil {
			continue
		}
		switch ap.Status {
		case model.StatusApproved:
			claimed, cerr := s.Repo.ClaimReleaseStage(st.ID, model.RunWaiting, model.RunSuccess)
			if cerr != nil || !claimed {
				continue
			}
			_ = s.Repo.UpdateReleaseStage(st.ID, map[string]any{
				"log": "· 审批单 " + ap.ApNo + " 已通过", "finished_at": time.Now(),
			})
			s.continueRelease(st.ReleaseID)
		case model.StatusRejected, model.StatusExpired:
			claimed, cerr := s.Repo.ClaimReleaseStage(st.ID, model.RunWaiting, model.RunFailed)
			if cerr != nil || !claimed {
				continue
			}
			reason := "被驳回"
			if ap.Status == model.StatusExpired {
				reason = "已超时失效"
			}
			_ = s.Repo.UpdateReleaseStage(st.ID, map[string]any{
				"log": "· 审批单 " + ap.ApNo + " " + reason, "finished_at": time.Now(),
			})
			if rel, err := s.Repo.GetRelease(st.ReleaseID); err == nil {
				s.finishRelease(rel, model.RunFailed, "审批单 "+ap.ApNo+" "+reason)
			}
		}
	}
}

// continueRelease puts a parked run back on the queue. The run resumes from its
// first non-terminal stage, so nothing that already executed runs again.
func (s *Services) continueRelease(id int64) {
	claimed, err := s.Repo.ClaimRelease(id, model.RunWaiting, model.RunRunning, nil)
	if err != nil || !claimed {
		return
	}
	select {
	case s.releaseQueue <- -id: // negative id = resume, not a fresh claim
	default:
		s.driveRelease(id) // queue full: drive it inline rather than strand it
	}
}

// ContinueManualStage is the human "继续" on a manual gate.
func (s *Services) ContinueManualStage(u *model.User, releaseID, stageID int64) error {
	rel, err := s.Repo.GetRelease(releaseID)
	if err != nil {
		return ErrNotFound
	}
	st, err := s.Repo.GetReleaseStage(stageID)
	if err != nil || st.ReleaseID != releaseID {
		return ErrNotFound
	}
	if st.Type != model.StageManual || st.Status != model.RunWaiting {
		return fmt.Errorf("该阶段当前不需要人工确认")
	}
	// Two-person control: whoever raised the release may not also wave it through
	// its manual gate, unless self-approval is deliberately enabled (same setting
	// the approval chain honours). This holds whichever way the gate is staffed.
	if u.ID == rel.CreatorID && !s.settingBool("approval.allowSelfApprove", false) {
		return ErrForbidden
	}
	// 配了角色就以角色为准 —— 有审批能力但不在这个角色里的人也不能推动本流程。
	if role := cfgConfirmRole(st.Config); role != "" {
		if !s.mayPassGate(u, role) {
			return ErrForbidden
		}
	} else if !s.canApproveReleases(u) {
		return ErrForbidden
	}
	claimed, err := s.Repo.ClaimReleaseStage(stageID, model.RunWaiting, model.RunSuccess)
	if err != nil || !claimed {
		return ErrAlreadyDecided
	}
	_ = s.Repo.UpdateReleaseStage(stageID, map[string]any{
		"log": "· 已由 " + u.Name + " 人工确认继续", "finished_at": time.Now(),
	})
	// The confirmation is a DECISION, so it goes on the chain in the decision
	// shape finalizeApproval writes: actor = whose change it is, operator = who
	// waved it through, result = pending — a green light, not an execution (the
	// execute stage audits what actually runs). The stage log above is neither
	// hash-chained nor what an auditor reads.
	creator, _ := s.Repo.GetUserByID(rel.CreatorID)
	if creator == nil {
		creator = &model.User{ID: rel.CreatorID, Name: rel.Creator}
	}
	conn, _ := s.Repo.GetConnection(rel.ConnectionID)
	s.recordAuditBy(creator, u.Name, conn,
		fmt.Sprintf("RELEASE-CONTINUE %s :: 人工确认「%s」放行", rel.RelNo, st.Name),
		orDefault(rel.Risk, model.RiskMid), model.ResultPending, rel.RelNo, "approve")
	s.continueRelease(releaseID)
	return nil
}

// ConfirmExecuteStage is the human click that lets an execute stage reach the
// database. Authorised for the CREATOR (approval already passed someone else's
// hands; the timing belongs to whoever raised it) and for approver roles.
func (s *Services) ConfirmExecuteStage(u *model.User, releaseID, stageID int64) error {
	rel, err := s.Repo.GetRelease(releaseID)
	if err != nil {
		return ErrNotFound
	}
	st, err := s.Repo.GetReleaseStage(stageID)
	if err != nil || st.ReleaseID != releaseID {
		return ErrNotFound
	}
	if st.Type != model.StageExecute || st.Status != model.RunWaiting {
		return fmt.Errorf("该阶段当前不在等待执行确认")
	}
	// 默认允许发起人,是因为"何时执行"归发起人;一旦显式指定了角色,那正是要把
	// 这个决定收走(变更窗口统一把关),所以配置优先于发起人身份。
	if role := cfgConfirmRole(st.Config); role != "" {
		if !s.mayPassGate(u, role) {
			return ErrForbidden
		}
	} else if u.ID != rel.CreatorID && !s.canApproveReleases(u) {
		return ErrForbidden
	}
	// waiting → pending(不是 success:执行还没发生),driveRelease 会重新拿起它,
	// 这次 ConfirmedBy 非空,闸放行。claim 保证两个人同时点只放行一次。
	claimed, err := s.Repo.ClaimReleaseStage(stageID, model.RunWaiting, model.RunPending)
	if err != nil || !claimed {
		return ErrAlreadyDecided
	}
	_ = s.Repo.UpdateReleaseStage(stageID, map[string]any{
		"confirmed_by": u.Name,
		"log":          "· 已由 " + u.Name + " 确认执行",
	})
	// 决策入链:actor = 变更归属人,operator = 点击的人,pending = 放行非执行
	// (真正的执行由 execute 阶段逐条记账)。
	creator, _ := s.Repo.GetUserByID(rel.CreatorID)
	if creator == nil {
		creator = &model.User{ID: rel.CreatorID, Name: rel.Creator}
	}
	conn, _ := s.Repo.GetConnection(rel.ConnectionID)
	s.recordAuditBy(creator, u.Name, conn,
		fmt.Sprintf("RELEASE-EXECUTE-CONFIRM %s :: 确认执行「%s」", rel.RelNo, rel.Title),
		orDefault(rel.Risk, model.RiskMid), model.ResultPending, rel.RelNo, "approve")
	s.continueRelease(releaseID)
	return nil
}

// canApproveReleases reports whether a user may push a release past a human
// gate: an approver role, or a platform admin.
func (s *Services) canApproveReleases(u *model.User) bool {
	if u == nil {
		return false
	}
	for _, id := range s.Repo.EffectiveRoleIDs(u) {
		if r, err := s.Repo.GetRole(id); err == nil && (r.CanApprove || r.Code == "admin") {
			return true
		}
	}
	return false
}

// AbortRelease stops a run that has not started executing.
//
// A RUNNING release is not abortable: its execute stage may already be inside a
// statement on the target database, and a button that claims to have stopped
// something still in flight is worse than no button.
func (s *Services) AbortRelease(u *model.User, id int64) error {
	rel, err := s.Repo.GetRelease(id)
	if err != nil {
		return ErrNotFound
	}
	if u.ID != rel.CreatorID && !s.canApproveReleases(u) {
		return ErrForbidden
	}
	if rel.Status != model.RunPending && rel.Status != model.RunWaiting {
		return fmt.Errorf("发布单当前状态(%s)不可终止", rel.Status)
	}
	claimed, err := s.Repo.ClaimRelease(rel.ID, rel.Status, model.RunAborted, nil)
	if err != nil || !claimed {
		return ErrAlreadyDecided
	}
	now := time.Now()
	_ = s.Repo.UpdateRelease(rel.ID, map[string]any{
		"error": "已由 " + u.Name + " 终止", "finished_at": now,
	})
	// The pending approval a parked run raised is voided WITH the run: leaving
	// it in the approvers' queue invites a decision on a change that no longer
	// exists — and the sweeper would then try to resume a corpse.
	s.voidReleaseApprovals(rel, "发布单已终止")
	s.skipUnrunStages(rel.ID, now)
	return nil
}

// skipUnrunStages marks every stage a terminal run never reached as skipped —
// pending and waiting rows on a finished release are debris, not work.
func (s *Services) skipUnrunStages(releaseID int64, now time.Time) {
	for _, st := range s.stagesOf(releaseID) {
		if st.Status == model.RunPending || st.Status == model.RunWaiting {
			_ = s.Repo.UpdateReleaseStage(st.ID, map[string]any{"status": model.RunSkipped, "finished_at": now})
		}
	}
}

// voidReleaseApprovals expires every still-pending ticket this release raised,
// using the same vocabulary as the timeout sweep (作废 = expired, no human
// decision recorded — because none was made). The claim keeps a racing human
// decision authoritative: if an approver decided first, their outcome stands.
func (s *Services) voidReleaseApprovals(rel *model.Release, reason string) {
	for _, st := range s.stagesOf(rel.ID) {
		if st.Type != model.StageApprove || st.ApprovalID == 0 {
			continue
		}
		ap, err := s.Repo.GetApproval(st.ApprovalID)
		if err != nil || ap.Status != model.StatusPending {
			continue
		}
		if claimed, _ := s.Repo.ClaimApproval(ap.ID, model.StatusPending, model.StatusExpired); !claimed {
			continue
		}
		_ = s.Repo.UpdateReleaseStage(st.ID, map[string]any{
			"log": "· 审批单 " + ap.ApNo + " 已作废(" + reason + ")",
		})
		s.cancelExternalApproval(*ap) // collapse the still-open 审批魔方/飞书 card, best-effort
	}
}

// stagesOf reads a run's stages, treating a read error as "no stages" — every
// caller here is doing best-effort cleanup, not deciding anything.
func (s *Services) stagesOf(id int64) []model.ReleaseStage {
	st, _ := s.Repo.StagesOfRelease(id)
	return st
}

// ---------------------------------------------------------------- 查询 (reads)

// ListReleases returns a page of runs. A user sees their own; oversight roles
// (the same ones that may see all terminal activity) see everything.
func (s *Services) ListReleases(u *model.User, scope, status string, page, pageSize int, projectID int64) dto.ReleasePage {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	if scope != "all" || !s.canSeeAllActivity(u) {
		scope = "mine"
	}
	rels, total, err := s.Repo.ListReleasesPaged(scope, u.ID, status, projectID, (page-1)*pageSize, pageSize)
	if err != nil {
		return dto.ReleasePage{Items: []dto.ReleaseView{}}
	}
	items := make([]dto.ReleaseView, 0, len(rels))
	for _, rel := range rels {
		v := s.releaseView(rel)
		// The listing carries the stage SHAPE (how many, which types, where the run
		// is) but not the stage OUTPUT. A page of 50 runs times six stages of
		// execution log is megabytes of text nothing on the list screen renders;
		// the detail endpoint is what a reader opens to see a log.
		for i := range v.Stages {
			v.Stages[i].Log = ""
			v.Stages[i].Findings = ""
			v.Stages[i].Config = ""
		}
		v.SQL = clip(v.SQL, 400)
		items = append(items, v)
	}
	return dto.ReleasePage{Items: items, Total: total, Page: page, PageSize: pageSize}
}

// GetReleaseDetail returns one run with its stages.
func (s *Services) GetReleaseDetail(u *model.User, id int64) (*dto.ReleaseView, error) {
	rel, err := s.Repo.GetRelease(id)
	if err != nil {
		return nil, ErrNotFound
	}
	if rel.CreatorID != u.ID && !s.canSeeAllActivity(u) {
		return nil, ErrForbidden
	}
	v := s.releaseView(*rel)
	return &v, nil
}

// releaseView masks credentials on the way out, for the same reason the async
// job listing does: a release is visible to oversight roles as well as its
// author, so a password written into a migration would otherwise be shown to
// people who never ran it. The STORED body stays verbatim — the runner executes
// it — so the masking happens here, at the boundary.
func (s *Services) releaseView(rel model.Release) dto.ReleaseView {
	stages, _ := s.Repo.StagesOfRelease(rel.ID)
	if stages == nil {
		stages = []model.ReleaseStage{}
	}
	for i := range stages {
		stages[i].Log = sqlutil.RedactSecrets(stages[i].Log)
	}
	rel.SQL = sqlutil.RedactSecrets(rel.SQL)
	rel.Error = sqlutil.RedactSecrets(rel.Error)
	return dto.ReleaseView{Release: rel, Stages: stages}
}

// ---------------------------------------------------------------- stage config

// stageCfg is one stage's JSON configuration, read defensively: a stage whose
// config failed to parse must not take a different action, it must take the
// default one.
type stageCfg map[string]any

func parseStageConfig(s string) stageCfg {
	c := stageCfg{}
	if strings.TrimSpace(s) == "" {
		return c
	}
	_ = json.Unmarshal([]byte(s), &c)
	return c
}

func (c stageCfg) str(key, def string) string {
	if v, ok := c[key]; ok {
		if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
			return s
		}
	}
	return def
}

func cfgNote(c stageCfg) string {
	if n := c.str("note", ""); n != "" {
		return " · " + n
	}
	return ""
}
