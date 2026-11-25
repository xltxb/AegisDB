package service

// 在线变更的终端入口与审批(.scratch/osc-product 工单 03)。
//
// 终端 Exec / RiskCheck 先过一遍 OSCRoute:
//   - 生产大表上的索引变更 → 不执行原 DDL,改成一张「在线变更」工单(不论能力矩阵原本是放行还是
//     审批,一律要审);生成之前先跑引擎的前置检查,阻塞就直接拒绝并逐条给原因;
//   - 一批语句里混着要走在线变更的 → 整批拒绝,要求单独提交(一张在线变更单只承载一个变更);
//   - 大表上、在线变更做不了的 DDL → 照原路径,但回执与工单上带一句"可能长时间锁表"。
//
// 审批通过后,「执行」= 再跑一次前置检查 + 启动后台迁移(StartOSCJob),工单的执行状态在迁移
// 结束时回写。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"aegisdb/internal/apimsg"
	"aegisdb/internal/dto"
	"aegisdb/internal/gateway"
	"aegisdb/internal/model"
	"aegisdb/internal/osc"
)

// oscPlan 是一条命令(可能是一批语句)里与在线变更有关的判定结果。
type oscPlan struct {
	online *OSCDecision  // 要走在线变更的那条(至多处理一条)
	big    []OSCDecision // 大表上、在线变更做不了的 DDL(照原路径,提示)
}

func (s *Services) oscPlanFor(ctx context.Context, conn *model.Connection, stmts []string) oscPlan {
	var p oscPlan
	for _, st := range stmts {
		d := s.OSCRoute(ctx, conn, st)
		switch d.Kind {
		case OSCOnline:
			if p.online == nil {
				dd := d
				p.online = &dd
			}
		case OSCBigDirect:
			p.big = append(p.big, d)
		}
	}
	return p
}

// oscApprovalRule 是在线变更工单的"为什么要审批"。
func oscApprovalRule(d OSCDecision) (string, *model.RuleRef) {
	size := humanBytes(int(d.Bytes))
	return fmt.Sprintf("生产大表在线变更须审批:表 %s 约 %s 行 / %s", d.Table, thousands(d.Rows), size),
		model.NewRuleRef("oscApproval", "table", d.Table, "rows", thousands(d.Rows), "size", size)
}

func blockerItems(xs []osc.Blocker) []dto.OSCItem {
	out := make([]dto.OSCItem, 0, len(xs))
	for _, x := range xs {
		out = append(out, dto.OSCItem{Code: x.Code, Reason: x.Reason})
	}
	return out
}

func warningItems(xs []osc.Warning) []dto.OSCItem {
	out := make([]dto.OSCItem, 0, len(xs))
	for _, x := range xs {
		out = append(out, dto.OSCItem{Code: x.Code, Reason: x.Reason})
	}
	return out
}

func blockersText(bs []osc.Blocker) string {
	rs := make([]string, len(bs))
	for i, b := range bs {
		rs[i] = b.Reason
	}
	return strings.Join(rs, ";")
}

// execOSC 处理终端命令里要走在线变更的那条。handled=false 表示交回原路径。
func (s *Services) execOSC(ctx context.Context, u *model.User, conn *model.Connection, sql string, stmts []string, v gateway.Verdict, reason string, p oscPlan) (*dto.ExecResp, error, bool) {
	d := *p.online
	if v.Action == gateway.ActionDeny {
		return nil, nil, false // 能力矩阵拒绝:照原路径拒绝(没有生产 DDL 权限的人发起不了)
	}
	if len(stmts) > 1 {
		msg := fmt.Sprintf("· 表 %s 的索引变更要走在线变更,请把这条语句单独提交(不能和别的语句一起审批、执行)", d.Table)
		s.recordAudit(u, conn, sql, v.Risk, model.ResultWarn, "", "intercept")
		return &dto.ExecResp{Risk: v.Risk, Output: msg, OutputRef: model.NewRuleRef("oscSplit", "table", d.Table)}, nil, true
	}
	blockers, warnings, err := s.OSCPreflight(ctx, conn, d.Schema, d.Table, d.Parsed.Change)
	if err != nil {
		msg := "· 在线变更前置检查失败: " + err.Error()
		s.recordAudit(u, conn, sql, v.Risk, model.ResultWarn, "", "intercept")
		return &dto.ExecResp{Risk: v.Risk, Output: msg, OutputRef: model.NewRuleRef("oscPreflightFailed", "err", err.Error())}, nil, true
	}
	if len(blockers) > 0 {
		msg := "· 在线变更前置检查未通过,未生成工单:" + blockersText(blockers)
		s.recordAudit(u, conn, sql, model.RiskHigh, model.ResultRejected, "", "intercept")
		return &dto.ExecResp{Risk: v.Risk, Output: msg, OutputRef: model.NewRuleRef("oscBlocked", "table", d.Table),
			OSCBlockers: blockerItems(blockers)}, nil, true
	}
	job, err := s.NewOSCJob(u, conn, strings.TrimSpace(stmts[0]), d, warnings)
	if err != nil {
		return nil, err, true
	}
	if w := s.windowFor(conn, stmts, time.Now()); w != nil {
		resp, err := s.startOSCInWindow(u, conn, sql, v, reason, d, job, warnings, w)
		return resp, err, true
	}
	rule, ref := oscApprovalRule(d)
	ov := gateway.Verdict{Action: gateway.ActionApprove, Risk: v.Risk, Rule: rule, Ref: ref}
	if ov.Risk == model.RiskLow || ov.Risk == "" {
		ov.Risk = model.RiskMid
	}
	ap, auditID, err := s.createApprovalWith(u, conn, sql, ov, reason, func(a *model.Approval) { a.OSCJobID = job.ID })
	if err != nil {
		_ = s.Repo.UpdateOSCJob(job.ID, map[string]any{"status": model.OSCFailed, "error": apimsg.Text("工单未能生成")})
		return nil, err, true
	}
	_ = s.Repo.LinkOSCApproval(job.ID, ap.ID, ap.ApNo)
	s.recordAudit(u, conn, sql, ov.Risk, model.ResultPending, ap.ApNo, "intercept")
	return &dto.ExecResp{Intercepted: true, ApprovalNo: ap.ApNo, AuditID: auditID, Risk: ov.Risk, Rule: rule, RuleRef: ref,
		OSC: &dto.OSCInfo{JobID: job.ID, Table: d.Table, Rows: d.Rows, Bytes: d.Bytes, Warnings: warningItems(warnings)}}, nil, true
}

// startOSCInWindow:执行窗口内的在线变更免审批,当场开始。
//
// 窗口本身就是"这段时间里这个库上的变更已经批过了"(它的开启要另一个人签字,见 ADR 0015),
// 在线变更不例外 —— 这是用户的决定。仍然生成那张在线变更工单:进度、暂停 / 终止、执行结果
// 都挂在工单详情上,没有它就没地方看。工单直接是已批准,审批链只有一步,审批人记为那扇
// 窗口;不推飞书卡片、不进任何人的待审批队列。
//
// 放宽的只有"要不要人来批":能力矩阵判拒绝的、前置检查有阻塞项的,在走到这里之前已经拦下。
func (s *Services) startOSCInWindow(u *model.User, conn *model.Connection, sql string, v gateway.Verdict, reason string,
	d OSCDecision, job *model.OSCJob, warnings []osc.Warning, w *model.ExecWindow) (*dto.ExecResp, error) {
	_, inner := oscApprovalRule(d)
	ref := model.NewRuleRef(model.RuleExecWindow, "window", w.Name)
	ref.Parts = []model.RuleRef{*inner}
	rule := model.RenderRule(ref)
	risk := v.Risk
	if risk == model.RiskLow || risk == "" {
		risk = model.RiskMid
	}
	ap, err := s.createWindowApprovedOSC(u, conn, sql, risk, reason, job.ID, w)
	if err != nil {
		_ = s.Repo.UpdateOSCJob(job.ID, map[string]any{"status": model.OSCFailed, "error": apimsg.Text("工单未能生成")})
		return nil, err
	}
	_ = s.Repo.LinkOSCApproval(job.ID, ap.ID, ap.ApNo)
	info := &dto.OSCInfo{JobID: job.ID, Table: d.Table, Rows: d.Rows, Bytes: d.Bytes, Warnings: warningItems(warnings)}
	resp, err := s.startApprovedOSC(u, windowOperator(w), ap, conn)
	if err != nil {
		// 前置检查刚刚通过,这里失败多半是瞬时的(连接、并发启动)。工单已是批准状态,
		// 稍后在审批页点执行即可,不必再申请一次。
		m := apimsg.Sprintf("· 在线变更未能开始:%s;工单 %s 已由执行窗口批准,可稍后在审批页执行", err, ap.ApNo)
		return &dto.ExecResp{ApprovalNo: ap.ApNo, AuditID: ap.AuditID, Risk: risk, Rule: rule, RuleRef: ref, OSC: info,
			Output: m.S, OutputRef: m.Ref}, nil
	}
	resp.ApprovalNo, resp.AuditID, resp.Risk, resp.Rule, resp.RuleRef, resp.OSC = ap.ApNo, ap.AuditID, risk, rule, ref, info
	// 没有审批弹窗告诉他工单号,回执里得说:进度、暂停 / 终止都在那张单上。
	resp.Output = fmt.Sprintf("· 执行窗口「%s」内免审批,在线变更已开始,进度见工单 %s", w.Name, ap.ApNo)
	resp.OutputRef = model.NewRuleRef("oscWindowStarted", "window", w.Name, "no", ap.ApNo)
	return resp, nil
}

// createWindowApprovedOSC 建一张由执行窗口直接批准的在线变更工单(见 startOSCInWindow)。
func (s *Services) createWindowApprovedOSC(u *model.User, conn *model.Connection, sql, risk, reason string, jobID int64, w *model.ExecWindow) (*model.Approval, error) {
	tierCode := ""
	if t, err := s.tierOf(conn); err == nil {
		tierCode = t.Code
	}
	now := time.Now()
	ap := &model.Approval{
		ApNo: s.nextApNo(), ConnectionID: conn.ID, Env: conn.Env, TierCode: tierCode, Instance: conn.Name,
		Command: sql, Keyword: approvalKeyword(sql), Database: effectiveDatabase(conn), InitiatorID: u.ID, Initiator: u.Name,
		Reason: reason, RiskLevel: risk, Status: model.StatusApproved, DecidedAt: &now, AuditID: s.nextAuditID(),
		OSCJobID: jobID,
	}
	// 审批人一栏 64 字符;窗口名本身就能有 64 个,前缀加上去要截断,否则 MySQL 上整张单写不进去。
	approver := []rune(windowOperator(w))
	if len(approver) > 64 {
		approver = approver[:64]
	}
	steps := []model.ApprovalStep{{StepOrder: 1, Approver: string(approver), Status: "approved", ActedAt: &now}}
	if err := s.Repo.CreateApproval(ap, steps); err != nil {
		slog.Error("create window-approved osc ticket failed", "apNo", ap.ApNo, "err", err)
		return nil, err
	}
	return ap, nil
}

// bigDirectNoticeOf 合成一批语句里"大表直接 DDL"的提示(取第一条;多条时都会写进审计的那一行命令)。
func bigDirectNoticeOf(p oscPlan) (string, *model.RuleRef) {
	if len(p.big) == 0 {
		return "", nil
	}
	return bigDirectNotice(p.big[0])
}

// abortAwaitingOSC 收掉一张没执行就结束了的在线变更单所挂的任务(驳回 / 撤回 / 超时作废)。
func (s *Services) abortAwaitingOSC(jobID int64, why string) {
	if jobID <= 0 {
		return
	}
	j, err := s.Repo.GetOSCJob(jobID)
	if err != nil || j.Status != model.OSCAwaiting {
		return
	}
	now := time.Now()
	_ = s.Repo.UpdateOSCJob(jobID, map[string]any{"status": model.OSCAborted, "error": why, "finished_at": now})
}

// executeApprovedOSC 是在线变更单的「执行」:再跑一次前置检查,启动后台迁移,工单进入执行中;
// 迁移结束时回写工单的执行结果。actor 的资格已由 ExecuteApproved 的前置检查确认过。
func (s *Services) executeApprovedOSC(actor *model.User, ap *model.Approval, conn *model.Connection) (*dto.ExecResp, error) {
	return s.startApprovedOSC(actor, actor.Name, ap, conn)
}

// startApprovedOSC 是 executeApprovedOSC 的本体;operator 写进审计,说明是谁授权的这次执行
// (审批页执行时是执行人本人,执行窗口内免审批时是那扇窗口,见 windowOperator)。
func (s *Services) startApprovedOSC(actor *model.User, operator string, ap *model.Approval, conn *model.Connection) (*dto.ExecResp, error) {
	job, err := s.Repo.GetOSCJob(ap.OSCJobID)
	if err != nil {
		return nil, apimsg.Errorf("这张工单的在线变更任务不存在")
	}
	if job.Status != model.OSCAwaiting {
		return nil, apimsg.Errorf("这个在线变更已经执行过了(%s)", job.Status)
	}
	var change osc.Change
	if err := json.Unmarshal([]byte(job.ChangeJSON), &change); err != nil {
		return nil, apimsg.Errorf("在线变更内容损坏: %w", err)
	}
	// 工单可能放了很久:库的配置、表结构、磁盘都可能变了。阻塞就不开始,工单仍可稍后再执行。
	blockers, _, err := s.OSCPreflight(context.Background(), conn, job.Database, job.Table, change)
	if err != nil {
		return nil, apimsg.Errorf("在线变更前置检查失败: %w", err)
	}
	if len(blockers) > 0 {
		return nil, apimsg.Errorf("在线变更前置检查未通过,暂不能执行:%s", blockersText(blockers))
	}

	// 先认领工单、再启动迁移(2026-10 安全审计 L-OSC1)。从前反过来:迁移先在后台开跑(建影子表、
	// 装触发器),认领失败(别人抢先执行、单子已撤回)才去中止它 —— 一次不该发生的迁移已经动过库,
	// 而它结束时还会把结果回写到一张不归它的单上。
	claimed, cerr := s.Repo.ClaimApprovalExecution(ap.ID)
	if cerr != nil {
		return nil, cerr
	}
	if !claimed {
		return nil, apimsg.Errorf("该工单正在执行或已经执行过了")
	}
	onFinish := func(f *model.OSCJob, runErr error) { s.finishApprovedOSC(actor, ap, conn, f) }
	if err := s.StartOSCJob(job.ID, onFinish); err != nil {
		_ = s.Repo.UnclaimApprovalExecution(ap.ID) // 没启动成:单子放回可执行
		return nil, err
	}
	s.recordAuditBy(actor, operator, conn, ap.Command, ap.RiskLevel, model.ResultPending, ap.ApNo, "exec")
	return &dto.ExecResp{Risk: ap.RiskLevel, Output: "· 在线变更已开始,进度见工单详情",
		OutputRef: model.NewRuleRef("oscStarted")}, nil
}

// finishApprovedOSC 把迁移的结局回写到工单:执行状态、结果说明、审计、通知发起人。
func (s *Services) finishApprovedOSC(actor *model.User, ap *model.Approval, conn *model.Connection, f *model.OSCJob) {
	status, result := model.ExecStatusFailed, model.ResultWarn
	var m apimsg.M
	fErr := apimsg.M{S: f.Error, Ref: apimsg.StoredRef(f.Error, f.ErrorRef)}
	switch f.Status {
	case model.OSCDone:
		status, result = model.ExecStatusSuccess, model.ResultExecuted
		m = apimsg.Sprintf("在线变更完成 · 拷贝 %s 行 · 重放 %s 行 · 原表已保留为 %s,请 DBA 择时手动删除",
			thousands(f.RowsCopied), thousands(f.RowsReplayed), f.OldTable)
	case model.OSCAborted:
		m = apimsg.Sprintf("· 在线变更已中止:%s", fErr)
	default:
		m = apimsg.Sprintf("· 在线变更失败:%s", fErr)
	}
	out := m.S
	_ = s.Repo.SetApprovalExecResult(ap.ID, out, m.Ref, int(f.RowsCopied), status)
	s.recordAuditBy(actor, actor.Name, conn, ap.Command, ap.RiskLevel, result, ap.ApNo, "exec")
	title, typ := apimsg.T("在线变更已完成"), model.NotifApprovalApproved
	if status != model.ExecStatusSuccess {
		title, typ = apimsg.T("在线变更未完成"), model.NotifExecFailed
	}
	tail := apimsg.M{S: safeClip(out, 120)}
	if tail.S == out {
		tail.Ref = m.Ref
	}
	s.notify(ap.InitiatorID, typ, title, apimsg.Sprintf("%s:%s", ap.ApNo, tail), ap.ApNo)
}

// ErrOSCNotRunning:控制一个不在运行中的迁移。
var ErrOSCNotRunning = errors.New("这个在线变更没有在运行")

// OSCJobFor 取一个任务给调用人看:够得到那台实例才行。
func (s *Services) OSCJobFor(u *model.User, id int64) (*model.OSCJob, error) {
	j, err := s.Repo.GetOSCJob(id)
	if err != nil {
		return nil, ErrNotFound
	}
	conn, err := s.Repo.GetConnection(j.ConnectionID)
	if err != nil || !s.canAccessConn(u, conn) {
		return nil, ErrForbidden
	}
	return j, nil
}

// CanControlOSC:能暂停 / 继续 / 中止这次迁移的人 —— 够得到实例,并且是下面三种之一:
//   - 发起人本人;
//   - 有审批资格的角色(含平台管理员):批这类变更本来就是他们的事;
//   - 这条 DDL 对他是**直接放行**的。
//
// 从前第三条写的是"不拒绝",于是判定为"需审批"、自己并不能执行这条 DDL 的人,也能去暂停、
// 中止别人正在跑的迁移(2026-10 安全审计 L-OSC2)。
func (s *Services) CanControlOSC(u *model.User, j *model.OSCJob) bool {
	conn, err := s.Repo.GetConnection(j.ConnectionID)
	if err != nil || !s.canAccessConn(u, conn) {
		return false
	}
	if u.ID == j.InitiatorID || s.canApproveReleases(u) {
		return true
	}
	tier, terr := s.tierCodeOf(conn)
	if terr != nil {
		return false
	}
	return s.Engine.EvaluateFor(s.Repo.EffectiveRoleIDs(u), conn.Engine, tier, j.Statement).Action == gateway.ActionAllow
}

// ControlOSC 暂停 / 继续 / 中止;中断的任务则是续跑(continue)/ 放弃(abort)。写审计。
func (s *Services) ControlOSC(u *model.User, id int64, action string) error {
	j, err := s.OSCJobFor(u, id)
	if err != nil {
		return err
	}
	if !s.CanControlOSC(u, j) {
		return ErrForbidden
	}
	switch {
	case j.Status == model.OSCInterrupted: // 网关重启打断的:只能续跑或放弃(工单 07)
		switch action {
		case "continue":
			err = s.ContinueOSCJob(u, id)
		case "abort":
			err = s.AbandonOSCJob(id)
		default:
			return ErrOSCNotRunning
		}
	case j.Status != model.OSCRunning:
		return ErrOSCNotRunning
	default:
		switch action {
		case "pause":
			err = s.PauseOSCJob(id)
		case "resume":
			err = s.ResumeOSCJob(id)
		case "abort":
			err = s.AbortOSCJob(id)
		default:
			return ErrBadRequest
		}
	}
	if err != nil {
		return err
	}
	conn, _ := s.Repo.GetConnection(j.ConnectionID)
	s.recordAuditBy(u, u.Name, conn, "OSC "+strings.ToUpper(action)+" · "+j.Statement, model.RiskMid, model.ResultExecuted, j.ApNo, "exec")
	return nil
}
