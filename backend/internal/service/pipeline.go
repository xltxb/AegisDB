package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"aegisdb/internal/apimsg"
	"aegisdb/internal/dto"
	"aegisdb/internal/gateway"
	"aegisdb/internal/model"
	"aegisdb/internal/repository"
	"aegisdb/internal/review"
	"aegisdb/pkg/sqlutil"
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

// pipelineHadNoExecute:这个已有模板此刻存着的阶段里没有执行阶段(读不到按"有"算,不放宽)。
func (s *Services) pipelineHadNoExecute(id int64) bool {
	old, err := s.Repo.StagesOfPipeline(id)
	if err != nil || len(old) == 0 {
		return false
	}
	for _, st := range old {
		if isExecuteStage(st.Type) {
			return false
		}
	}
	return true
}

// isExecuteStage:执行变更与自动执行是同一件事(把发布内容下发到目标库),只差一道人工闸 ——
// 「恰好一个执行阶段」「执行失败必须中止」这些结构规则对两者一视同仁。
func isExecuteStage(t string) bool {
	return t == model.StageExecute || t == model.StageAutoExecute
}

// SavePipeline creates or replaces a template.
//
// 必须**恰好一个**执行阶段(2026-10 黑盒测试,工单 02 第 12 条)。没有执行阶段的流程跑完
// 会报"发布成功",而变更一条也没执行 —— 那正是发布单最不能给出的结论;两个执行阶段则会把
// 同一份 SQL 跑两遍。分层(非空时)必须存在,名称不可重复(不分大小写)。
func (s *Services) SavePipeline(u *model.User, id int64, req dto.PipelineReq) (*dto.PipelineDetail, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return nil, apimsg.Errorf("流程名称不能为空")
	}
	if len(req.Stages) == 0 {
		return nil, apimsg.Errorf("流程至少需要一个阶段")
	}
	if len(req.Stages) > 20 {
		return nil, apimsg.Errorf("流程阶段最多 20 个")
	}
	stages := make([]model.PipelineStage, 0, len(req.Stages))
	for i, st := range req.Stages {
		if !validStageType(st.Type) {
			return nil, apimsg.Errorf("第 %d 个阶段的类型 %q 不受支持", i+1, st.Type)
		}
		if st.Config != "" && !json.Valid([]byte(st.Config)) {
			return nil, apimsg.Errorf("第 %d 个阶段的配置不是合法 JSON", i+1)
		}
		// 指定的审批角色必须存在 —— 拼错的角色名不该等到某次真实发布才暴露。
		// 成员数不在这里查:成员是会变的,保存时有人不代表跑的时候还有人,那一条
		// 由 approvalStepsFor 在阶段运行时把关。
		if st.Type == model.StageApprove {
			if err := s.validateApproverRole(cfgApproverRole(st.Config)); err != nil {
				return nil, apimsg.Errorf("第 %d 个阶段: %w", i+1, err)
			}
		}
		if st.Type == model.StageManual || isExecuteStage(st.Type) {
			if err := s.validateConfirmRole(cfgConfirmRole(st.Config)); err != nil {
				return nil, apimsg.Errorf("第 %d 个阶段: %w", i+1, err)
			}
		}
		// 阶段名由**类型**决定,客户端送什么都不算数。
		//
		// 它原先是一个自由文本框,于是界面上出现了两个说同一件事、却可能对不上的
		// 东西:左边输入框里的名字,和右边的类型下拉框。更麻烦的是这个名字会进记录 ——
		// 它被快照进 ReleaseStage、写进通知正文("进入阶段:X")和人工确认的审计文本 ——
		// 所以它必须语言稳定;而一个可编辑的框意味着谁用哪种语言看过、按过保存,记录
		// 里就留下哪种语言。
		//
		// 派生之后两件事一起解决:界面按读者的语言显示(前端用 plType_* 渲染同一个
		// 类型),记录里始终是这里这份规范中文。与 Rule/RuleRef 是同一条原则。
		name := stageTypeLabel(st.Type)
		onFail := st.OnFailure
		if onFail != model.OnFailureContinue {
			onFail = model.OnFailureAbort
		}
		// 执行阶段失败后继续,意味着后面的校验/通知会宣告一次并未发生的变更成功。
		if isExecuteStage(st.Type) {
			onFail = model.OnFailureAbort
		}
		stages = append(stages, model.PipelineStage{
			Name: clip(name, 60), Type: st.Type, Config: st.Config, OnFailure: onFail,
		})
	}
	// 逐阶段的校验(类型、配置、角色)先报 —— 那些错误说得更具体;整体结构放在后面。
	execs, auto := 0, false
	for _, st := range stages {
		if isExecuteStage(st.Type) {
			execs++
		}
		if st.Type == model.StageAutoExecute {
			auto = true
		}
	}
	// 例外:规则之前存下的、本来就没有执行阶段的模板(从前注释里说"审查 + 审批 + 人工交接"是合法流程)。
	// 一刀切之后这类模板连停用、改名都存不下(2026-10-26 回归排查)。只要它原本就是 0 个、这次仍是 0 个,
	// 照常保存;新建的、或原本有执行阶段的,仍须恰好一个。多于一个永远拒绝(同一份 SQL 会跑两遍)。
	if execs != 1 && !(execs == 0 && id > 0 && s.pipelineHadNoExecute(id)) {
		return nil, apimsg.Errorf("流程必须有且只有一个「执行变更」或「自动执行」阶段(当前 %d 个)", execs)
	}
	tier := strings.TrimSpace(req.TierCode)
	if tier != "" {
		t, err := s.Repo.GetEnvTier(tier)
		if err != nil {
			return nil, apimsg.Errorf("分层标签不存在:%s", tier)
		}
		if auto && !t.AutoExecute {
			return nil, apimsg.Errorf("分层 %s 未开启「允许自动执行」,不能使用「自动执行」阶段;请改用「执行变更」,或先在分层管理里打开该开关", strings.ToUpper(tier))
		}
	} else if auto {
		// 适用全部分层 = 也适用生产。自动执行只能挂在一个明确允许它的分层上。
		return nil, apimsg.Errorf("「自动执行」阶段只能用于指定了分层的流程,请先选择适用分层")
	}
	if all, err := s.Repo.ListPipelines(); err == nil {
		for _, p := range all {
			if p.ID != id && strings.EqualFold(strings.TrimSpace(p.Name), name) {
				return nil, apimsg.Errorf("流程「%s」已存在", name)
			}
		}
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
		model.StageExecute, model.StageAutoExecute, model.StageVerify, model.StageManual, model.StageNotify:
		return true
	}
	return false
}

func stageTypeLabel(t string) string {
	switch t {
	case model.StageReview:
		return apimsg.Text("规范审查")
	case model.StageApprove:
		return apimsg.Text("人工审批")
	case model.StageBackup:
		return apimsg.Text("备份/回滚点")
	case model.StageExecute:
		return apimsg.Text("执行变更")
	case model.StageAutoExecute:
		return apimsg.Text("自动执行")
	case model.StageVerify:
		return apimsg.Text("执行后校验")
	case model.StageManual:
		return apimsg.Text("人工确认")
	case model.StageNotify:
		return apimsg.Text("结果通知")
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
		return nil, apimsg.Errorf("发布内容不能为空")
	}
	if len(sql) > maxStoredSQLBytes {
		return nil, storedSQLTooLong(len(sql))
	}
	if strings.TrimSpace(req.Title) == "" {
		return nil, apimsg.Errorf("发布标题不能为空")
	}
	conn, err := s.Repo.GetConnection(req.ConnectionID)
	if err != nil {
		return nil, ErrNotFound
	}
	applyTargetDatabase(conn, req.Database)
	if !s.canAccessConn(u, conn) {
		return nil, ErrForbidden
	}
	if conn.Status == model.ConnStatusMaint {
		return nil, apimsg.Errorf("目标实例处于维护态,暂不能发起发布")
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
			return nil, apimsg.Errorf("脚本文件不可读: %w", rerr)
		}
		body = content
		sha = scriptDigest(content)
		sql = "" // the file is the source of truth; the row keeps only the reference
	}
	stmts := sqlutil.SplitStatements(body)
	if len(stmts) == 0 {
		return nil, apimsg.Errorf("发布内容中没有可执行的语句")
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
		s.recordAuditFrom(model.AuditSourceRelease, u, origin.operator(), conn, releaseAuditLabel(req.Title, body), model.RiskHigh, model.ResultRejected, "", "intercept")
		return nil, ErrForbidden
	}

	v := s.strictestVerdict(u, conn, stmts)
	if v.Action == gateway.ActionDeny {
		s.recordAuditFrom(model.AuditSourceRelease, u, origin.operator(), conn, releaseAuditLabel(req.Title, body), model.RiskHigh, model.ResultRejected, "", "intercept")
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
			return nil, apimsg.Errorf("该变更命中「%s」需审批,请选择包含审批阶段的发布流程", v.Rule)
		}
		if relLevel == model.LevelApprove {
			return nil, apimsg.Errorf("当前角色在 %s 分层的发布权限为「需审批」,请选择包含审批阶段的发布流程", strings.ToUpper(tier))
		}
		// 生产大表的在线变更一律要审批(.scratch/osc-product),不论复判原本是不是放行
		for _, one := range stmts {
			if d := s.OSCRoute(context.Background(), conn, one); d.Kind == OSCOnline {
				return nil, apimsg.Errorf("表 %s 的索引变更将以在线变更执行,须审批,请选择包含审批阶段的发布流程", d.Table)
			}
		}
	}

	// 语法与对象检查。放在这里是有讲究的:
	//
	//   在判定**之后** —— 一条会被拒的变更不该先去连一次生产库;
	//   在建单**之前** —— 结果要随这张单一起落库,并随响应返回给提单方。
	//
	// 它不拦发布(见 precheck.go 顶部):语法检查覆盖不到 DDL,对象检查可能基于过期的
	// 缓存,拿一个覆盖不全、可能过期的结论去拦正常变更,比不检查更糟。
	pre := s.precheckRelease(conn, stmts)

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
		Reason: clip(req.Reason, 400), Precheck: precheckJSON(pre),
		CreatorID: u.ID, Creator: u.Name,
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
	s.recordAuditFrom(model.AuditSourceRelease, u, origin.operator(), conn, releaseAuditLabel(rel.Title, body), v.Risk, model.ResultPending, rel.RelNo, "intercept")
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
			return nil, nil, apimsg.Errorf("发布流程不存在")
		}
		if !p.Enabled {
			return nil, nil, apimsg.Errorf("发布流程「%s」已停用", p.Name)
		}
		// A tier-scoped template governs that tier only; running a dev flow against
		// production is precisely the mix-up the scope exists to prevent.
		if p.TierCode != "" && p.TierCode != tier {
			return nil, nil, apimsg.Errorf("发布流程「%s」仅适用于 %s 分层", p.Name, strings.ToUpper(p.TierCode))
		}
	} else if p, err = s.Repo.DefaultPipelineFor(tier); err != nil {
		return nil, nil, apimsg.Errorf("尚未配置可用的发布流程")
	}
	stages, err := s.Repo.StagesOfPipeline(p.ID)
	if err != nil || len(stages) == 0 {
		return nil, nil, apimsg.Errorf("发布流程「%s」没有配置阶段", p.Name)
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
		_ = s.Repo.UpdateRelease(id, releaseErrFields(model.NewRuleRef(model.RelErrQueueFull), map[string]any{
			"status": model.RunFailed, "finished_at": time.Now(),
		}))
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
			_ = s.Repo.UpdateRelease(id, releaseErrFields(model.NewRuleRef(model.RelErrPanic, "err", fmt.Sprint(r)), map[string]any{
				"status": model.RunFailed, "finished_at": time.Now(),
			}))
		}
	}()
	fn()
}

// runReleaseSafe claims a queued run and drives it.
func (s *Services) runReleaseSafe(id int64) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("release run panicked", "release", id, "err", r)
			_ = s.Repo.UpdateRelease(id, releaseErrFields(model.NewRuleRef(model.RelErrPanic, "err", fmt.Sprint(r)), map[string]any{
				"status": model.RunFailed, "finished_at": time.Now(),
			}))
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
		s.finishRelease(rel, model.RunFailed, model.NewRuleRef(model.RelErrConnGone))
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

		fields := out.log.fields()
		fields["status"], fields["rows"] = out.status, out.rows
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
		if err := s.Repo.UpdateReleaseStage(st.ID, fields); err != nil {
			// 从前这里 `_ =` 吞掉错误:审查结果超过 MySQL MEDIUMTEXT(16MB)时整条更新失败,
			// 状态也没写进去,阶段在库里永远"运行中",流水线却在内存里接着往下走(2026-11-10)。
			// 大字段写不进去,至少把状态和结束时间落库,别让阶段卡死。
			slog.Error("发布阶段结果写库失败,改为只写状态", "release", rel.RelNo, "stage", st.ID, "err", err)
			slim := map[string]any{"status": fields["status"], "rows": fields["rows"]}
			for _, k := range []string{"finished_at", "approval_id", "approval_no"} {
				if v, ok := fields[k]; ok {
					slim[k] = v
				}
			}
			if err2 := s.Repo.UpdateReleaseStage(st.ID, slim); err2 != nil {
				slog.Error("发布阶段状态写库仍失败", "release", rel.RelNo, "stage", st.ID, "err", err2)
			}
		}

		switch out.status {
		case model.RunWaiting:
			_ = s.Repo.UpdateRelease(rel.ID, map[string]any{"status": model.RunWaiting})
			s.notify(rel.CreatorID, model.NotifReleaseWait, apimsg.T("发布等待处理"),
				apimsg.Sprintf("%s「%s」进入阶段:%s", rel.RelNo, rel.Title, apimsg.Of(st.Name)), rel.RelNo)
			return
		case model.RunFailed:
			if st.OnFailure == model.OnFailureContinue {
				continue // recorded as failed, flow carries on by explicit configuration
			}
			s.finishRelease(rel, model.RunFailed, stageFailedRef(&st, out.log))
			return
		}
	}
	s.finishRelease(rel, model.RunSuccess, nil)
}

// stageOutcome is what one stage reports back to the driver.
type stageOutcome struct {
	status     string // success|failed|skipped|waiting
	log        relLog // 逐行 RuleRef;规范中文由它渲染(见 release_log.go)
	findings   string // review stage only: the JSON result
	rows       int
	approvalID int64
	approvalNo string
}

func failStage(code string, kv ...string) stageOutcome {
	return stageOutcome{status: model.RunFailed, log: relLogOf(code, kv...)}
}

func okStage(code string, kv ...string) stageOutcome {
	return stageOutcome{status: model.RunSuccess, log: relLogOf(code, kv...)}
}

func outcome(status string, code string, kv ...string) stageOutcome {
	return stageOutcome{status: status, log: relLogOf(code, kv...)}
}

// stageFailedRef 是"阶段失败"的错误条:规范串照旧带日志开头 300 字,refs 带日志的前几行。
func stageFailedRef(st *model.ReleaseStage, l relLog) *model.RuleRef {
	r := model.NewRuleRef(model.RelErrStageFailed, "type", st.Type, "stage", st.Name, "log", clip(l.text(), 300))
	n := len(l.refs)
	if n > 3 {
		n = 3
	}
	r.Parts = append([]model.RuleRef(nil), l.refs[:n]...)
	return r
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
	case model.StageExecute, model.StageAutoExecute:
		return s.stageExecute(rel, conn, st)
	case model.StageVerify:
		return s.stageVerify(rel, conn, cfg)
	case model.StageManual:
		if role := cfgConfirmRole(st.Config); role != "" {
			people, err := s.gateStaffing(role)
			if err != nil {
				return failStage(model.RelLogStageErr, "err", err.Error())
			}
			out := outcome(model.RunWaiting, model.RelLogWaitManBy, "who", gateWhoLabel(people), "names", gateWhoNames(people))
			cfgNote(&out.log, cfg)
			return out
		}
		out := outcome(model.RunWaiting, model.RelLogWaitMan)
		cfgNote(&out.log, cfg)
		return out
	case model.StageNotify:
		return s.stageNotify(rel, conn)
	}
	// An unknown type cannot be silently skipped: the flow would report success
	// having omitted a step somebody put there on purpose.
	return failStage(model.RelLogUnknown, "type", st.Type)
}

// stageReview runs the 规范审查 rule library against the release body.
func (s *Services) stageReview(rel *model.Release, conn *model.Connection, cfg stageCfg) stageOutcome {
	body, err := s.releaseBody(rel)
	if err != nil {
		return failStage(model.RelLogReadBody, "err", err.Error())
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
	var b relLog
	b.add(model.RelLogRevHead, "dialect", string(dialect), "n", model.Itoa(res.Statements))
	b.add(model.RelLogRevCounts, "e", model.Itoa(res.Errors), "w", model.Itoa(res.Warnings), "i", model.Itoa(res.Infos))
	shown := res.Findings
	if len(shown) > 50 {
		shown = shown[:50]
	}
	for _, f := range shown {
		r := b.add(model.RelLogRevFinding, "level", strings.ToUpper(f.Level), "stmt", model.Itoa(f.Stmt),
			"rule", f.Code, "name", f.Name, "msg", f.Message)
		if f.MessageRef != nil {
			// msg 是审查引擎的中文;它的 apiMsg 身份嵌成 part,界面按读者语言显示
			r.Args["ns"] = "apiMsg"
			r.Parts = []model.RuleRef{*f.MessageRef}
		}
	}
	// Findings 只是每条规则的前几条示例,"另有 N 条"要按全部命中算。
	if res.Total > len(shown) {
		b.add(model.RelLogRevMore, "n", model.Itoa(res.Total-len(shown)))
	}
	// failOn decides what a finding COSTS here, and "error" is the default so a
	// library full of style advice cannot block every release on day one.
	switch cfg.str("failOn", "error") {
	case "none":
		return stageOutcome{status: model.RunSuccess, log: b, findings: string(payload)}
	case "warn":
		if res.Errors+res.Warnings > 0 {
			return stageOutcome{status: model.RunFailed, log: b, findings: string(payload)}
		}
	default:
		if res.Errors > 0 {
			return stageOutcome{status: model.RunFailed, log: b, findings: string(payload)}
		}
	}
	return stageOutcome{status: model.RunSuccess, log: b, findings: string(payload)}
}

// stageApprove raises an approval ticket and parks the run on it.
//
// The ticket carries ReleaseID, which is what keeps it out of the manual
// execute path (ExecuteApproved): the pipeline owns execution, and a change
// applied twice — once by hand from the approvals page, once by the execute
// stage — is the failure this link exists to prevent.
func (s *Services) stageApprove(rel *model.Release, conn *model.Connection, st *model.ReleaseStage) stageOutcome {
	if st.ApprovalID > 0 {
		return outcome(model.RunWaiting, model.RelLogApExists, "no", st.ApprovalNo)
	}
	creator, _ := s.Repo.GetUserByID(rel.CreatorID)
	if creator == nil {
		creator = &model.User{ID: rel.CreatorID, Name: rel.Creator}
	}
	body, err := s.releaseBody(rel)
	if err != nil {
		return failStage(model.RelLogReadBody, "err", err.Error())
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
		return failStage(model.RelLogStageErr, "err", serr.Error())
	}
	ap := &model.Approval{
		ApNo: s.nextApNo(), ConnectionID: conn.ID, Env: rel.Env, TierCode: rel.TierCode,
		Instance: conn.Name, Command: label, Keyword: approvalKeyword(body), Database: rel.Database,
		InitiatorID: creator.ID, Initiator: creator.Name,
		Reason:    clipColumn(strings.TrimSpace(rel.Reason+" · 发布单 "+rel.RelNo), 512),
		RiskLevel: risk, Status: model.StatusPending, AuditID: s.nextAuditID(),
		ScriptUploadID: rel.ScriptUploadID, ScriptSHA256: rel.ScriptSHA256,
		ReleaseID: rel.ID,
	}
	steps[0].Status = "active" // approvalStepsFor guarantees a non-empty chain
	if err := s.Repo.CreateApproval(ap, steps); err != nil {
		return failStage(model.RelLogApCreateFail, "err", err.Error())
	}
	s.Webhook.SendLarkApproval(ap)
	// 发布审批推不推审批魔方单独一个开关(默认关):发布单在控制台 / 飞书卡片里批即可,
	// 不跟着高危命令一起进审批魔方。没外发的单回调一律被拒(见 external_approval.go)。
	if s.settingBool("approval.external.releaseEnabled", false) {
		s.dispatchExternalApproval(creator, ap)
	}
	s.recordAuditFrom(model.AuditSourceRelease, creator, releaseOperator(rel), conn, label, risk, model.ResultPending, ap.ApNo, "intercept")
	return stageOutcome{
		status: model.RunWaiting, approvalID: ap.ID, approvalNo: ap.ApNo,
		log: relLogOf(model.RelLogApCreated, "no", ap.ApNo),
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
		return outcome(model.RunSkipped, model.RelLogBkNoSQL)
	}
	stmts := sqlutil.SplitStatements(sql)
	if len(stmts) == 0 {
		return outcome(model.RunSkipped, model.RelLogBkEmpty)
	}
	// Judge EVERY statement before running ANY: a mutation hidden behind a
	// legitimate first statement must not get the first one executed.
	for _, one := range stmts {
		if verb := gateway.ParseVerb(one); !backupVerbs[verb] {
			return failStage(model.RelLogBkVerb, "verb", verb)
		}
		// 动词白名单挡不住 `SELECT … INTO OUTFILE '/var/lib/mysql-files/x'`:它的动词
		// 就是 SELECT,却在**数据库服务器上落一个文件**,而这条通道不经风险引擎(M-3)。
		// 导出路径早就有这道判断,这里复用它 —— 同一个危险,不该只有一处认得。
		if gateway.WritesServerFile(one) {
			return failStage(model.RelLogBkFile)
		}
		// 同一条理由:这条通道不经能力矩阵,`SELECT pg_terminate_backend(…)` 这类调用即
		// 生效的函数只能在这里认(见 gateway.EffectfulCall)。
		if fn := gateway.EffectfulCall(one); fn != "" {
			return failStage(model.RelLogBkFunc, "fn", fn)
		}
	}
	creator, _ := s.Repo.GetUserByID(rel.CreatorID)
	var b relLog
	total := 0
	// 整段备份在一条专用连接上跑,跑完断开(见 execSession)
	ctx, done := s.execSession(s.withTierMasking(context.Background(), conn), conn)
	defer done()
	execs := sqlutil.SplitStatementsExec(sql) // 下发用:保留可执行注释标记(与 stmts 一一对应)
	for i := range stmts {
		res := s.Executor.Run(ctx, conn, execs[i], s.asyncExecTimeout())
		if res.Err != nil {
			// 失败那一行在最前:发布的 error 只截日志开头 300 字,报错原文不能被前面成功的几条挤掉
			var f relLog
			withOut(f.add(model.RelLogBkFail, "pos", model.Itoa(i+1), "total", model.Itoa(len(stmts)), "ms", msText(res.Ms)), res, 0)
			f.raw(execLogText(stmts[i]))
			f.extend(b)
			return stageOutcome{status: model.RunFailed, log: f}
		}
		total += res.Rows
		withOut(b.add(model.RelLogStmtOk, "pos", model.Itoa(i+1), "total", model.Itoa(len(stmts)), "ms", msText(res.Ms)), res, 200)
		b.raw(execLogText(stmts[i]))
	}
	if creator != nil {
		s.recordAuditFrom(model.AuditSourceRelease, creator, releaseOperator(rel), conn, "RELEASE-BACKUP "+rel.RelNo+" :: "+safeClip(sql, 200),
			model.RiskLow, model.ResultExecuted, rel.RelNo, "exec")
	}
	fin := relLogOf(model.RelLogBkDone, "n", model.Itoa(len(stmts)))
	fin.extend(b)
	return stageOutcome{status: model.RunSuccess, rows: total, log: fin}
}

// stageExecute applies the change — after a human clicks and after judging it
// again. The gate is unconditional: 审批回答"可不可以做",这里回答"现在做",
// 变更窗口和上下游就绪只有到点的人知道,自动落库等于把时机交给调度器。
//
// 唯一的例外是「自动执行」阶段落在一个打开了「允许自动执行」的分层上(dev / uat 这类):时机不
// 需要人来定,前序阶段通过就下发。分层按**此刻**实例所在的那一层查 —— 流程保存时校验过一次,但
// 开关可能已被关掉、实例可能已改挂生产,那就退回与执行变更相同的等待确认。复判与逐条审计不变。
func (s *Services) stageExecute(rel *model.Release, conn *model.Connection, st *model.ReleaseStage) stageOutcome {
	auto := false
	var autoNote relLog // 自动执行阶段开头的一行:直接下发了,还是退回等人工确认
	if st.ConfirmedBy == "" && st.Type == model.StageAutoExecute {
		if auto = s.tierAllowsAutoExecute(conn); auto {
			autoNote.add(model.RelLogAutoOn)
		} else {
			autoNote.add(model.RelLogAutoOff)
		}
	}
	if st.ConfirmedBy == "" && !auto {
		wait := autoNote.clone()
		if role := cfgConfirmRole(st.Config); role != "" {
			people, err := s.gateStaffing(role)
			if err != nil {
				return failStage(model.RelLogStageErr, "err", err.Error())
			}
			wait.add(model.RelLogWaitExecBy, "who", gateWhoLabel(people), "names", gateWhoNames(people))
			return stageOutcome{status: model.RunWaiting, log: wait}
		}
		wait.add(model.RelLogWaitExec)
		return stageOutcome{status: model.RunWaiting, log: wait}
	}
	body, err := s.releaseBody(rel)
	if err != nil {
		return failStage(model.RelLogReadBody, "err", err.Error())
	}
	creator, _ := s.Repo.GetUserByID(rel.CreatorID)
	if creator == nil {
		return failStage(model.RelLogNoCreator)
	}
	if conn.Status == model.ConnStatusMaint {
		return failStage(model.RelLogMaint)
	}
	// An instance whose tier no longer resolves is never judged as allowed —
	// strictestVerdict returns Unavailable for it, but failing here says why.
	if _, terr := s.tierCodeOf(conn); terr != nil {
		return failStage(model.RelLogTierFail)
	}
	stmts := sqlutil.SplitStatements(body)
	if len(stmts) == 0 {
		return failStage(model.RelLogNoStmts)
	}
	v := s.strictestVerdict(creator, conn, stmts)
	switch {
	case v.Action == gateway.ActionDeny:
		s.recordAuditFrom(model.AuditSourceRelease, creator, releaseOperator(rel), conn, releaseAuditLabel(rel.Title, body), model.RiskHigh, model.ResultRejected, rel.RelNo, "intercept")
		return recheckFail(model.RelLogRecheckDeny, v)
	case v.RequiresApproval() && !s.releaseApproved(rel.ID):
		// The verdict changed while the release waited, or the flow never had an
		// approve stage. Either way nothing runs without the approval it now needs.
		return recheckFail(model.RelLogRecheckAppr, v)
	}

	// 生产大表的索引变更改走在线变更(.scratch/osc-product):不论复判是放行还是审批,都要有已通过的审批
	online := make([]*OSCDecision, len(stmts))
	anyOnline := false
	for i, one := range stmts {
		if d := s.OSCRoute(context.Background(), conn, one); d.Kind == OSCOnline {
			dd := d
			online[i], anyOnline = &dd, true
		}
	}
	if anyOnline && !s.releaseApproved(rel.ID) {
		return failStage(model.RelLogOSCNeedAppr)
	}

	timeout := s.asyncExecTimeout()
	var b relLog
	total := 0
	// 发布内容在一条专用连接上逐条跑,跑完断开(见 execSession):同一个会话,也不占终端的池
	ctx, done := s.execSession(s.withTierMasking(context.Background(), conn), conn)
	defer done()
	execs := sqlutil.SplitStatementsExec(body) // 下发用:保留可执行注释标记(与 stmts 一一对应)
	flow := gateway.NewScriptFlow(conn.Engine)  // WHENEVER / EXIT(SQL*Plus 脚本)
	var failed []string
	exitPos := 0
	for i, one := range stmts {
		flow.Observe(one)
		if online[i] != nil {
			rows, lines, ok := s.runReleaseOSC(rel, creator, conn, st, one, *online[i], i+1, len(stmts), &b)
			b.extend(lines)
			if !ok {
				s.recordAuditFrom(model.AuditSourceRelease, creator, releaseOperator(rel), conn, one, v.Risk, model.ResultWarn, rel.RelNo, "exec")
				halt := relLogOf(model.RelLogHalted, "done", model.Itoa(i), "total", model.Itoa(len(stmts)))
				halt.extend(b)
				return stageOutcome{status: model.RunFailed, rows: total, log: halt}
			}
			total += rows
			s.recordAuditFrom(model.AuditSourceRelease, creator, releaseOperator(rel), conn, one, v.Risk, model.ResultExecuted, rel.RelNo, "exec")
			continue
		}
		res := s.Executor.Run(flow.Context(ctx), conn, execs[i], timeout)
		flow.Update(res.Binds)
		if res.Err != nil && flow.ContinueOnError() {
			withOut(b.add(model.RelLogStmtFailCont, "pos", model.Itoa(i+1), "total", model.Itoa(len(stmts)), "ms", msText(res.Ms)), res, 300)
			b.raw(execLogText(one))
			s.recordAuditFrom(model.AuditSourceRelease, creator, releaseOperator(rel), conn, one, v.Risk, model.ResultWarn, rel.RelNo, "exec")
			failed = append(failed, model.Itoa(i+1))
			continue
		}
		if res.Err != nil {
			withOut(b.add(model.RelLogStmtFail, "pos", model.Itoa(i+1), "total", model.Itoa(len(stmts)), "ms", msText(res.Ms)), res, 300)
			b.raw(execLogText(one))
			s.recordAuditFrom(model.AuditSourceRelease, creator, releaseOperator(rel), conn, one, v.Risk, model.ResultWarn, rel.RelNo, "exec")
			// 报错原文也放进首行:发布的 error 只截日志开头 300 字,前面几条成功语句的回显不能把它挤掉
			halt := relLog{}
			withOut(halt.add(model.RelLogHaltedFail, "done", model.Itoa(i), "total", model.Itoa(len(stmts)), "pos", model.Itoa(i+1)), res, 200)
			halt.extend(b)
			return stageOutcome{status: model.RunFailed, rows: total, log: halt}
		}
		total += res.Rows
		withOut(b.add(model.RelLogStmtOk, "pos", model.Itoa(i+1), "total", model.Itoa(len(stmts)), "ms", msText(res.Ms)), res, 200)
		b.raw(execLogText(stmts[i]))
		// A statement that returned a result SET gets it echoed into the log:
		// releases carry SELECTs to verify their own work, and the operator
		// reading the run wants to see what came back, not re-run the query.
		resultPreview(&b, res)
		// Every statement is audited individually: the chain must show what ran,
		// not that "a release ran".
		s.recordAuditFrom(model.AuditSourceRelease, creator, releaseOperator(rel), conn, one, v.Risk, model.ResultExecuted, rel.RelNo, "exec")
		if flow.IsExit(one) {
			exitPos = i + 1
			break
		}
	}
	n := len(stmts)
	if exitPos > 0 {
		n = exitPos
	}
	head := autoNote.clone()
	head.add(model.RelLogExecDone, "n", model.Itoa(n), "rows", model.Itoa(total))
	if text, ref := flowNote(exitPos, failed); text != "" {
		r := head.cat(model.RelLogExecNote, "note", text)
		if ref != nil {
			r.Args["ns"] = "outText"
			r.Parts = []model.RuleRef{*ref}
		}
	}
	head.extend(b)
	status := model.RunSuccess
	// WHENEVER SQLERROR CONTINUE 下有语句失败:内容跑完了,但这一阶段不能算成功 —— 流水线停下等人看
	if len(failed) > 0 {
		status = model.RunFailed
	}
	return stageOutcome{status: status, rows: total, log: head}
}

// execLogStmt echoes the statement a log line is about. "[1/1] 1 行受影响" says
// THAT something ran, not WHAT — and the stage log is where an operator checks
// that the change which landed is the one they meant. Clipped: a package body
// belongs in 变更内容, the log only needs to identify it. safeClip, not clip:
// cutting first could split an IDENTIFIED BY '...' literal so the read-side
// redaction no longer recognises it.
func execLogStmt(one string) string {
	return "    " + strings.ReplaceAll(safeClip(strings.TrimSpace(one), 500), "\n", "\n    ") + "\n"
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
func resultPreview(l *relLog, res gateway.ExecResult) {
	if len(res.Columns) == 0 || len(res.Data) == 0 {
		return
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
	l.raw(strings.TrimSuffix(b.String(), "\n"))
	// Elision is SAID, never silent: a log that looks complete but isn't is
	// worse than either a complete one or an honest preview.
	switch {
	case res.Truncated:
		l.add(model.RelLogPrevTrunc, "n", model.Itoa(len(rows)))
	case elided > 0:
		l.add(model.RelLogPrevElided, "n", model.Itoa(elided), "max", model.Itoa(execLogPreviewRows))
	}
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
		return outcome(model.RunSkipped, model.RelLogVfNoSQL)
	}
	if !gateway.IsRead(sql) {
		// A verification that mutates is a second, unreviewed change.
		return failStage(model.RelLogVfNotRead)
	}
	// IsRead 看的是动词,而 `SELECT … INTO OUTFILE` 的动词正是 SELECT —— 一条"只读"
	// 的校验语句照样能在库服务器上写文件(M-3)。与备份阶段同一道判断。
	if gateway.WritesServerFile(sql) {
		return failStage(model.RelLogVfFile)
	}
	if fn := gateway.EffectfulCall(sql); fn != "" {
		return failStage(model.RelLogVfFunc, "fn", fn)
	}
	res := s.Executor.Run(s.withTierMasking(context.Background(), conn), conn, sql, s.execTimeout())
	if res.Err != nil {
		var f relLog
		withOut(f.add(model.RelLogVfFail), res, 0)
		return stageOutcome{status: model.RunFailed, log: f}
	}
	creator, _ := s.Repo.GetUserByID(rel.CreatorID)
	if creator != nil {
		s.recordAuditFrom(model.AuditSourceRelease, creator, releaseOperator(rel), conn, "RELEASE-VERIFY "+rel.RelNo+" :: "+safeClip(sql, 200),
			model.RiskLow, model.ResultExecuted, rel.RelNo, "exec")
	}
	var log relLog
	withOut(log.add(model.RelLogVfDone, "ms", msText(res.Ms)), res, 0)
	switch cfg.str("expect", "") {
	case "empty":
		if res.Rows != 0 {
			log.add(model.RelLogVfWantEmpty, "n", model.Itoa(res.Rows))
			return stageOutcome{status: model.RunFailed, rows: res.Rows, log: log}
		}
	case "nonempty":
		if res.Rows == 0 {
			log.add(model.RelLogVfWantRows)
			return stageOutcome{status: model.RunFailed, rows: res.Rows, log: log}
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
	s.recordAuditFrom(model.AuditSourceRelease, creator, releaseOperator(rel), conn, fmt.Sprintf("RELEASE-NOTIFY %s :: %s", rel.RelNo, rel.Title),
		model.RiskLow, model.ResultExecuted, rel.RelNo, "exec")
	s.notify(rel.CreatorID, model.NotifReleaseDone, apimsg.T("发布进度通知"),
		apimsg.Sprintf("%s「%s」已执行完成,等待后续阶段", rel.RelNo, rel.Title), rel.RelNo)
	return okStage(model.RelLogNotified)
}

// finishRelease writes the terminal state once and notifies the creator.
//
// A failed terminal also SKIPS every stage the run never reached: a dead
// release whose later stages read 待执行 forever looks like a run that is
// still coming — the same debris AbortRelease already tidies, so every
// terminal path tidies it the same way.
func (s *Services) finishRelease(rel *model.Release, status string, errRef *model.RuleRef) {
	now := time.Now()
	errMsg := model.RenderRelLog(errRef)
	_ = s.Repo.UpdateRelease(rel.ID, releaseErrFields(errRef, map[string]any{"status": status, "finished_at": now}))
	if status != model.RunSuccess {
		s.skipUnrunStages(rel.ID, now)
	}
	if status == model.RunSuccess {
		s.notify(rel.CreatorID, model.NotifReleaseDone, apimsg.T("发布完成"),
			apimsg.Sprintf("%s「%s」已全部阶段通过", rel.RelNo, rel.Title), rel.RelNo)
		return
	}
	s.notify(rel.CreatorID, model.NotifReleaseFailed, apimsg.T("发布失败"),
		apimsg.Sprintf("%s「%s」%s", rel.RelNo, rel.Title, clip(errMsg, 200)), rel.RelNo)
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
			f := relLogOf(model.RelLogApApproved, "no", ap.ApNo).fields()
			f["finished_at"] = time.Now()
			_ = s.Repo.UpdateReleaseStage(st.ID, f)
			s.continueRelease(st.ReleaseID)
		case model.StatusRejected, model.StatusExpired:
			claimed, cerr := s.Repo.ClaimReleaseStage(st.ID, model.RunWaiting, model.RunFailed)
			if cerr != nil || !claimed {
				continue
			}
			logCode, errCode := model.RelLogApRejected, model.RelErrApRejected
			if ap.Status == model.StatusExpired {
				logCode, errCode = model.RelLogApExpired, model.RelErrApExpired
			}
			f := relLogOf(logCode, "no", ap.ApNo).fields()
			f["finished_at"] = time.Now()
			_ = s.Repo.UpdateReleaseStage(st.ID, f)
			if rel, err := s.Repo.GetRelease(st.ReleaseID); err == nil {
				s.finishRelease(rel, model.RunFailed, model.NewRuleRef(errCode, "no", ap.ApNo))
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
		return apimsg.Errorf("该阶段当前不需要人工确认")
	}
	// 谁能放行这道闸,全系统只有一处判断(两人控制、确认角色、审批能力都在里面)——
	// 右上角待办据同一条规则决定要不要把这张单摆到他面前,见 release_confirm.go。
	if !s.mayConfirmGate(u, rel, st) {
		return ErrForbidden
	}
	claimed, err := s.Repo.ClaimReleaseStage(stageID, model.RunWaiting, model.RunSuccess)
	if err != nil || !claimed {
		return ErrAlreadyDecided
	}
	f := relLogOf(model.RelLogConfirmedCont, "who", u.Name).fields()
	f["finished_at"] = time.Now()
	_ = s.Repo.UpdateReleaseStage(stageID, f)
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
	s.recordAuditFrom(model.AuditSourceRelease, creator, u.Name, conn,
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
	if !isExecuteStage(st.Type) || st.Status != model.RunWaiting {
		return apimsg.Errorf("该阶段当前不在等待执行确认")
	}
	// 与人工确认闸共用同一处判断(execute 这一路默认允许发起人,理由见
	// release_confirm.go)。
	if !s.mayConfirmGate(u, rel, st) {
		return ErrForbidden
	}
	// waiting → pending(不是 success:执行还没发生),driveRelease 会重新拿起它,
	// 这次 ConfirmedBy 非空,闸放行。claim 保证两个人同时点只放行一次。
	claimed, err := s.Repo.ClaimReleaseStage(stageID, model.RunWaiting, model.RunPending)
	if err != nil || !claimed {
		return ErrAlreadyDecided
	}
	f := relLogOf(model.RelLogConfirmedExec, "who", u.Name).fields()
	f["confirmed_by"] = u.Name
	_ = s.Repo.UpdateReleaseStage(stageID, f)
	// 决策入链:actor = 变更归属人,operator = 点击的人,pending = 放行非执行
	// (真正的执行由 execute 阶段逐条记账)。
	creator, _ := s.Repo.GetUserByID(rel.CreatorID)
	if creator == nil {
		creator = &model.User{ID: rel.CreatorID, Name: rel.Creator}
	}
	conn, _ := s.Repo.GetConnection(rel.ConnectionID)
	s.recordAuditFrom(model.AuditSourceRelease, creator, u.Name, conn,
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
	// 终止一张还没开始执行的发布单,语义上同审批单的「驳回」,不要求实例标签(2026-10-26
	// 回归 04,与人工确认闸同口径)。正在跑的在线变更被中止会动目标库(删影子表),那是
	// 执行侧的动作,仍要求够得到这台实例(L-AUTH2)。
	// 执行阶段正在跑在线变更:中止发布 = 中止那次迁移(删影子表,原表不动);执行阶段随之失败结束。
	// 其余正在执行的发布仍不可中止 —— 一条普通 DDL 跑到一半停不下来。
	if rel.Status == model.RunRunning {
		if j, _ := s.Repo.RunningOSCJobForRelease(rel.ID); j != nil {
			if !s.releaseConnReachable(u, rel) {
				return ErrForbidden
			}
			_ = s.Repo.UpdateRelease(rel.ID, releaseErrFields(model.NewRuleRef(model.RelErrAbortedOSC, "who", u.Name), map[string]any{}))
			return s.AbortOSCJob(j.ID)
		}
	}
	if rel.Status != model.RunPending && rel.Status != model.RunWaiting {
		return apimsg.Errorf("发布单当前状态(%s)不可终止", rel.Status)
	}
	claimed, err := s.Repo.ClaimRelease(rel.ID, rel.Status, model.RunAborted, nil)
	if err != nil || !claimed {
		return ErrAlreadyDecided
	}
	now := time.Now()
	_ = s.Repo.UpdateRelease(rel.ID, releaseErrFields(model.NewRuleRef(model.RelErrAborted, "who", u.Name), map[string]any{"finished_at": now}))
	// The pending approval a parked run raised is voided WITH the run: leaving
	// it in the approvers' queue invites a decision on a change that no longer
	// exists — and the sweeper would then try to resume a corpse.
	s.voidReleaseApprovals(rel)
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
func (s *Services) voidReleaseApprovals(rel *model.Release) {
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
		_ = s.Repo.UpdateReleaseStage(st.ID, relLogOf(model.RelLogApVoided, "no", ap.ApNo).fields())
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

// canSeeAllReleases 报告 u 能不能看全部发布单:平台管理员、DBA 负责人、DBA L2、审计员,
// 多角色取并集。
//
// 它比 canSeeAllActivity 多一个 l2 —— 发布单是团队协作的工作单,L2 及以上都要看得到
// 别人提的单子(2026-10-30 用户确认)。终端活动、审计、后台任务、执行窗口回看仍走
// canSeeAllActivity,口径不动。自定义角色一律不算(同日确认),哪怕勾了「可审批」。
func (s *Services) canSeeAllReleases(u *model.User) bool {
	if u == nil {
		return false
	}
	for _, code := range s.Repo.RoleCodesForIDs(s.Repo.EffectiveRoleIDs(u)) {
		switch code {
		case "admin", "owner", "l2", "audit":
			return true
		}
	}
	return false
}

// ListReleases returns a page of runs. A user sees their own; canSeeAllReleases
// roles may ask for everything.
func (s *Services) ListReleases(u *model.User, scope, status string, page, pageSize int, projectID int64, env, keyword string) dto.ReleasePage {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	if scope != "all" || !s.canSeeAllReleases(u) {
		scope = "mine"
	}
	// 范围在上面定死之后才进筛选:单号、环境只在看得到的单子里找。
	filter := repository.ReleaseFilter{
		Scope: scope, UserID: u.ID, Status: status, ProjectID: projectID,
		Env: strings.TrimSpace(env), Keyword: clipColumn(keyword, 128),
	}
	rels, total, err := s.Repo.ListReleasesPaged(filter, (page-1)*pageSize, pageSize)
	if err != nil {
		return dto.ReleasePage{Items: []dto.ReleaseView{}, Envs: []string{}}
	}
	envs, err := s.Repo.ReleaseEnvs(filter)
	if err != nil {
		envs = []string{}
	}
	// The listing carries the stage SHAPE (how many, which types, where the run
	// is) but not the stage OUTPUT. A page of 50 runs times six stages of
	// execution log is megabytes of text nothing on the list screen renders;
	// the detail endpoint is what a reader opens to see a log. So the stages come
	// in one IN query without the log / logRefs / findings / config columns —
	// nothing to redact, nothing to blank — instead of one query per run.
	ids := make([]int64, 0, len(rels))
	for _, rel := range rels {
		ids = append(ids, rel.ID)
	}
	shapes, err := s.Repo.StageShapesOfReleases(ids)
	if err != nil {
		shapes = map[int64][]model.ReleaseStage{}
	}
	items := make([]dto.ReleaseView, 0, len(rels))
	for _, rel := range rels {
		stages := shapes[rel.ID]
		if stages == nil {
			stages = []model.ReleaseStage{}
		}
		rel.SQL = listSQLPreview(rel.SQL)
		rel = redactRelease(rel) // SQL 已是脱敏后的预览,再过一遍是幂等的,只剩几百字的开销
		items = append(items, dto.ReleaseView{Release: rel, Stages: stages})
	}
	return dto.ReleasePage{Items: items, Total: total, Page: page, PageSize: pageSize, Envs: envs}
}

// 发布单列表里脚本只显示开头一段(listSQLPreviewRunes 字)。
//
// 原先是对**整段脚本**脱敏后再截:脱敏是几条正则扫全文,实测约 0.46 秒/MB,一页 50 张、
// 页面执行中每几秒轮询一次,列表被大批量 INSERT 的单子拖到几百毫秒以上(2026-11-20
// 生产 403ms)。现在先截到 listSQLRedactWindow 字再脱敏、再截到预览长度:显示出来的
// 内容与原先一致。窗口比预览长,是为了预览里的口令不被截成半截而认不出 —— 只要单个口令
// 字面量不超过两者之差(600 字),就一定落在窗口内完整被认出。
const (
	listSQLPreviewRunes = 400
	listSQLRedactWindow = 1000
)

func listSQLPreview(sql string) string {
	return clip(sqlutil.RedactSecrets(headRunes(sql, listSQLRedactWindow)), listSQLPreviewRunes)
}

// headRunes 取前 n 个字符,不像 clip 那样把整段先转成 []rune(几 MB 的脚本会白分配一遍)。
// 不加省略号:后面的 clip 看得出它比预览长,省略号由 clip 补。
func headRunes(s string, n int) string {
	i := 0
	for idx := range s {
		if i == n {
			return s[:idx]
		}
		i++
	}
	return s
}

// GetReleaseDetail returns one run with its stages.
func (s *Services) GetReleaseDetail(u *model.User, id int64) (*dto.ReleaseView, error) {
	rel, err := s.Repo.GetRelease(id)
	if err != nil {
		return nil, ErrNotFound
	}
	// 被系统点名去确认的人也要看得见:待办里给了他一条「等你确认」,点进去却是
	// 「无权查看」—— 那是这条链路上最容易出现、也最说不通的一种失败。
	if rel.CreatorID != u.ID && !s.canSeeAllReleases(u) && !s.staffsConfirmGate(u, rel) {
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
		stages[i].LogRefs = redactRefs(stages[i].LogRefs, true)
	}
	return dto.ReleaseView{Release: redactRelease(rel), Stages: stages}
}

// redactRelease masks the run's own text fields (body, error) — the part of
// releaseView the listing shares with the detail.
func redactRelease(rel model.Release) model.Release {
	rel.SQL = sqlutil.RedactSecrets(rel.SQL)
	rel.Error = sqlutil.RedactSecrets(rel.Error)
	rel.ErrorRef = redactRefs(rel.ErrorRef, false)
	return rel
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

// cfgNote 把人工确认点配置的备注接在等待那一行后面(用户数据,原样显示)。
func cfgNote(l *relLog, c stageCfg) {
	if n := c.str("note", ""); n != "" {
		l.cat(model.RelLogNote, "note", n)
	}
}
