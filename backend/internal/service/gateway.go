package service

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"velagateway/internal/dto"
	"velagateway/internal/gateway"
	"velagateway/internal/model"
	"velagateway/pkg/crypto"
	"velagateway/pkg/sqlutil"
	"velagateway/pkg/totp"
)

// BuildMe assembles the /auth/me payload (user + menus + capabilities).
func (s *Services) BuildMe(u *model.User) (*dto.MeResp, error) {
	role, err := s.Repo.GetRole(u.RoleID)
	if err != nil {
		return nil, err
	}
	// Permissions compose across every role the user holds (union): menus and the
	// capability matrix merge (most permissive), and CanApprove is true if any role
	// can approve. The primary role still drives the displayed role name/layer.
	ids := s.Repo.EffectiveRoleIDs(u)
	menus, _ := s.Repo.MenusForRoles(ids)
	matrix, _ := s.Repo.MatrixForRoles(ids)
	canApprove := false
	roleNames := []string{}
	roleCodes := []string{}
	for _, id := range ids {
		if r, e := s.Repo.GetRole(id); e == nil && r != nil {
			canApprove = canApprove || r.CanApprove
			roleNames = append(roleNames, r.Name)
			roleCodes = append(roleCodes, r.Code)
		}
	}
	return &dto.MeResp{
		ID: u.ID, Name: u.Name, Email: u.Email, Initials: u.Initials,
		RoleID: role.ID, RoleCode: role.Code, RoleName: role.Name, Layer: role.Layer,
		RoleIDs: ids, RoleNames: roleNames, RoleCodes: roleCodes,
		CanApprove: canApprove, MfaEnabled: u.MFAEnabled && u.MFASecret != "", Menus: menus, Capabilities: matrix,
	}, nil
}

// RiskCheck performs the pure three-layer pre-check (no side effects).
func (s *Services) RiskCheck(u *model.User, connID int64, sql string) (*dto.RiskCheckResp, error) {
	conn, err := s.Repo.GetConnection(connID)
	if err != nil {
		return nil, ErrNotFound
	}
	if _, err := s.tierCodeOf(conn); err != nil {
		return nil, ErrBadRequest // unresolvable tier — see tierOf; never judged as allow
	}
	// Judge the SPLIT statements, exactly as Exec does. Pre-checking the raw
	// string keyed the capability matrix on the leading verb only, so a batch
	// whose risky statement was not first ("SELECT 1; UPDATE …") pre-checked as
	// allow — the terminal skipped the approval-reason prompt and told the
	// operator the batch was safe, while Exec then intercepted it anyway.
	v := gateway.Verdict{Action: gateway.ActionAllow, Risk: model.RiskLow}
	if stmts := sqlutil.SplitStatements(sql); len(stmts) > 0 {
		v = s.strictestVerdict(u, conn, stmts)
	}
	return &dto.RiskCheckResp{
		Risk:             v.Risk,
		Action:           v.Action,
		RequiresApproval: v.RequiresApproval(),
		MatchedRule:      v.Rule,
		Command:          v.Command,
	}, nil
}

// Exec runs the full gateway flow: judge → (deny | approve | allow) → audit.
func (s *Services) Exec(u *model.User, connID int64, sql, reason, mfaCode, database string) (*dto.ExecResp, error) {
	// Everything on this path may be persisted verbatim (audit command, approval
	// command — MEDIUMTEXT, migration 0018); refuse past the bound with a message
	// instead of letting the metadata DB throw "Data too long for column".
	if len(sql) > maxStoredSQLBytes {
		return nil, storedSQLTooLong(len(sql))
	}
	conn, err := s.Repo.GetConnection(connID)
	if err != nil {
		return nil, ErrNotFound
	}
	// Target a specific database within the instance when the caller chose one.
	applyTargetDatabase(conn, database)
	// Tag-based access: a restricted role may only operate on its assigned DBs.
	if !s.canAccessConn(u, conn) {
		return nil, ErrForbidden
	}
	// FR-CONN-04: maintenance-state instances restrict operations.
	if conn.Status == "maint" {
		s.recordAudit(u, conn, sql, model.RiskLow, model.ResultWarn, "", "")
		return &dto.ExecResp{Risk: model.RiskLow, Output: "· 目标实例处于维护态，操作受限"}, nil
	}
	// Session & Security · Require MFA: an enrolled user must present a valid
	// TOTP step-up code before any PROD operation (submit or execute).
	if err := s.checkMFA(u, conn, mfaCode); err != nil {
		return nil, err
	}
	// A terminal command may bundle several statements ("SELECT 1; UPDATE ..").
	// The capability matrix keys on the LEADING verb only, so judging the raw
	// string as one unit lets a benign leading SELECT smuggle a mutating tail
	// statement past the matrix on backends that accept stacked queries (e.g.
	// PostgreSQL's simple query protocol). Judge every statement and let the
	// strictest verdict govern the whole command (A1).
	//
	// This runs for a single statement too, so the judged text is always the
	// SPLIT one. Judging the raw string in that case let a stray leading
	// separator (";UPDATE …") defeat verb parsing: no leading keyword was found,
	// the unknown verb fell into the read dimension, and the write ran under a
	// read-only role (ER3). Splitting normalises the separator away.
	stmts := sqlutil.SplitStatements(sql)
	if len(stmts) == 0 { // blank or comment-only input — nothing to normalise
		return s.execJudged(u, conn, sql, reason)
	}
	return s.applyVerdict(u, conn, sql, s.strictestVerdict(u, conn, stmts), reason)
}

// strictestVerdict evaluates every statement and returns the one demanding the
// most gating (deny > approve > allow; within the same action, the higher risk),
// so a mixed command is governed by its most dangerous part rather than its
// leading verb. Ranking by action alone let the FIRST approve-ranked statement
// freeze the verdict: `UPDATE …(mid); DELETE …(high)` produced a ticket graded
// mid with the DELETE's dictionary rule dropped, so the approver reviewed a
// high-risk batch under a mid-risk label. Every triggered rule is kept on the
// verdict, not just the winner's — the ticket must show all of what fired.
func (s *Services) strictestVerdict(u *model.User, conn *model.Connection, stmts []string) gateway.Verdict {
	tier, err := s.tierCodeOf(conn)
	if err != nil {
		return gateway.Unavailable(conn.Engine, strings.Join(stmts, ";"), err)
	}
	strict := gateway.Verdict{Action: gateway.ActionAllow, Risk: model.RiskLow}
	roleIDs := s.Repo.EffectiveRoleIDs(u)
	var rules []string
	for _, st := range stmts {
		v := s.Engine.EvaluateFor(roleIDs, conn.Engine, tier, st)
		if v.Action != gateway.ActionAllow && v.Rule != "" && !slices.Contains(rules, v.Rule) {
			rules = append(rules, v.Rule)
		}
		if actionRank(v.Action) > actionRank(strict.Action) ||
			(actionRank(v.Action) == actionRank(strict.Action) && riskRank(v.Risk) > riskRank(strict.Risk)) {
			strict = v
		}
	}
	if len(rules) > 1 {
		strict.Rule = strings.Join(rules, " + ")
	}
	return strict
}

// riskRank orders risk grades so equal-action verdicts can still escalate.
func riskRank(r string) int {
	switch r {
	case model.RiskHigh:
		return 2
	case model.RiskMid:
		return 1
	default: // low
		return 0
	}
}

// actionRank orders verdict actions by how much gating they impose.
func actionRank(a string) int {
	switch a {
	case gateway.ActionDeny:
		return 2
	case gateway.ActionApprove:
		return 1
	default: // allow
		return 0
	}
}

// execJudged runs the three-layer judgement + audit for one statement, assuming
// access + maintenance + MFA have already been checked by the caller. Splitting
// this out lets a whole-script execution validate MFA ONCE up front instead of
// per statement (which forced an empty code on every line — R18).
func (s *Services) execJudged(u *model.User, conn *model.Connection, sql, reason string) (*dto.ExecResp, error) {
	tier, err := s.tierCodeOf(conn)
	if err != nil {
		return s.applyVerdict(u, conn, sql, gateway.Unavailable(conn.Engine, sql, err), reason)
	}
	return s.applyVerdict(u, conn, sql, s.Engine.EvaluateFor(s.Repo.EffectiveRoleIDs(u), conn.Engine, tier, sql), reason)
}

// applyVerdict routes a judged command to deny / approve / allow and records the
// matching audit entry. The command text (sql) is preserved verbatim even when
// the verdict was derived from an individual sub-statement (A1).
func (s *Services) applyVerdict(u *model.User, conn *model.Connection, sql string, v gateway.Verdict, reason string) (*dto.ExecResp, error) {
	switch v.Action {
	case gateway.ActionDeny:
		s.recordAudit(u, conn, sql, model.RiskHigh, model.ResultRejected, "", "intercept")
		return nil, ErrForbidden

	case gateway.ActionApprove:
		ap, auditID, err := s.createApproval(u, conn, sql, v, reason)
		if err != nil {
			return nil, err
		}
		s.recordAudit(u, conn, sql, v.Risk, model.ResultPending, ap.ApNo, "intercept")
		return &dto.ExecResp{Intercepted: true, ApprovalNo: ap.ApNo, AuditID: auditID, Risk: v.Risk, Rule: v.Rule}, nil

	default: // allow
		res := s.Executor.Run(conn, sql, s.execTimeout())
		s.recordAudit(u, conn, sql, v.Risk, execResultStatus(res), "", "exec")
		return &dto.ExecResp{Risk: v.Risk, Output: res.Output, Rows: res.Rows, Ms: res.Ms,
			Columns: res.Columns, Data: res.Data, Truncated: res.Truncated}, nil
	}
}

// execResultStatus maps a run outcome to the audit result: a target-DB error is
// recorded as "warn", not "executed" (R19).
func execResultStatus(res gateway.ExecResult) string {
	if res.Err != nil {
		return model.ResultWarn
	}
	return model.ResultExecuted
}

// SubmitScriptForApproval submits a whole script that the per-statement scan
// flagged as risky. The `\i file` wrapper isn't itself risk-matched, so the
// approval is created directly from the scan result (role-level hard denies
// still block). This is what makes a risky-script submission appear in the
// approval channel.
func (s *Services) SubmitScriptForApproval(u *model.User, connID int64, filename, content, reason, mfaCode, database string, uploadID int64) (*dto.ExecResp, error) {
	conn, err := s.Repo.GetConnection(connID)
	if err != nil {
		return nil, ErrNotFound
	}
	applyTargetDatabase(conn, database) // target database captured on the approval ticket
	if !s.canAccessConn(u, conn) {
		return nil, ErrForbidden
	}
	if conn.Status == "maint" {
		s.recordAudit(u, conn, "\\i "+filename, model.RiskLow, model.ResultWarn, "", "")
		return &dto.ExecResp{Risk: model.RiskLow, Output: "· 目标实例处于维护态，操作受限"}, nil
	}
	if err := s.checkMFA(u, conn, mfaCode); err != nil {
		return nil, err
	}
	scan, err := s.ScanScript(filename, content)
	if err != nil {
		return nil, err // no scan baseline — see ScanScript; refusing beats "all clear"
	}
	risk := model.RiskMid
	if scan.High > 0 {
		risk = model.RiskHigh
	}
	// pick the riskiest statement to check for a hard capability deny
	worst := ""
	for _, st := range scan.Statements {
		if st.Risk == "high" {
			worst = st.SQL
			break
		}
		if st.Risk == "mid" && worst == "" {
			worst = st.SQL
		}
	}
	if worst != "" {
		tier, terr := s.tierCodeOf(conn)
		if terr != nil {
			return nil, ErrBadRequest // unresolvable tier — see tierOf
		}
		if v := s.Engine.EvaluateFor(s.Repo.EffectiveRoleIDs(u), conn.Engine, tier, worst); v.Action == gateway.ActionDeny {
			s.recordAudit(u, conn, "\\i "+filename, model.RiskHigh, model.ResultRejected, "", "intercept")
			return nil, ErrForbidden
		}
	}
	rule := "脚本含高危语句 · 需审批"
	av := gateway.Verdict{Action: gateway.ActionApprove, Risk: risk, Rule: rule}

	// The ticket carries an excerpt and a digest, not the script. See script_ref.go
	// — the body would not fit in `command` on MySQL, and the file it came from is
	// already on disk. uploadID == 0 (a script that was never saved) keeps the old
	// behaviour of storing the body, which is the only thing available then —
	// ScriptExecute always records the paste as an upload now, so this fallback
	// only serves legacy callers, and it must refuse a body past the column bound
	// rather than let the INSERT die on "Data too long for column 'command'".
	label := "\\i " + filename + "\n" + strings.TrimSpace(content)
	if uploadID > 0 {
		label = scriptExcerpt(filename, content, scan.Total, scan.High, scan.Mid)
	} else if len(label) > maxStoredSQLBytes {
		return nil, storedSQLTooLong(len(label))
	}
	ap, auditID, err := s.createApprovalForScript(u, conn, label, av, reason, uploadID, scriptDigest(content))
	if err != nil {
		return nil, err
	}
	s.recordAudit(u, conn, label, risk, model.ResultPending, ap.ApNo, "intercept")
	return &dto.ExecResp{Intercepted: true, ApprovalNo: ap.ApNo, AuditID: auditID, Risk: risk, Rule: rule}, nil
}

// createApproval builds an approval ticket + chain steps (default approvers from
// the DBA-owner role) and links a fresh audit id.
func (s *Services) createApproval(u *model.User, conn *model.Connection, sql string, v gateway.Verdict, reason string) (*model.Approval, string, error) {
	return s.createApprovalForScript(u, conn, sql, v, reason, 0, "")
}

// createApprovalForScript is createApproval for a ticket whose body lives in an
// uploaded file. uploadID == 0 means the body is `sql` itself, which is every
// ordinary command.
func (s *Services) createApprovalForScript(u *model.User, conn *model.Connection, sql string, v gateway.Verdict, reason string, uploadID int64, sha string) (*model.Approval, string, error) {
	apNo := s.nextApNo()
	auditID := s.nextAuditID()
	kw := firstWord(sql)
	// Dual snapshot (see model.Approval): the environment it ran in and the tier
	// it was judged under. An unresolvable environment leaves the tier blank
	// rather than failing the ticket — by this point the command has already been
	// judged, and dropping the approval would leave it neither run nor recorded.
	tierCode := ""
	if t, err := s.tierOf(conn); err == nil {
		tierCode = t.Code
	}
	ap := &model.Approval{
		ApNo: apNo, ConnectionID: conn.ID, Env: conn.Env, TierCode: tierCode, Instance: conn.Name,
		Command: sql, Keyword: kw, Database: conn.Database, InitiatorID: u.ID, Initiator: u.Name,
		Reason: reason, RiskLevel: v.Risk, Status: model.StatusPending, AuditID: auditID,
		ScriptUploadID: uploadID, ScriptSHA256: sha,
	}
	steps := s.defaultChainSteps()
	if len(steps) > 0 {
		steps[0].Status = "active"
	}
	if err := s.Repo.CreateApproval(ap, steps); err != nil {
		// Never report a ticket that failed to persist as "intercepted" — that
		// leaves the user waiting on an approval that isn't in any queue (R9).
		slog.Error("create approval failed", "apNo", apNo, "err", err)
		return nil, "", err
	}
	s.Webhook.SendLarkApproval(ap)     // push an interactive Lark card to the approvers
	s.dispatchExternalApproval(u, ap)  // (审批魔方) best-effort external interactive approval
	return ap, auditID, nil
}

// isChainMember reports whether the user is an approver on the given approval's
// chain (i.e. one of its step approvers).
func (s *Services) isChainMember(approvalID int64, u *model.User) bool {
	if u == nil {
		return false
	}
	steps, _ := s.Repo.StepsOf(approvalID)
	for _, st := range steps {
		if st.ApproverID == u.ID {
			return true
		}
	}
	return false
}

// defaultChainSteps returns the fallback approval chain (DBA-owner role members).
// defaultChainSteps builds the approver chain from the members of the "owner"
// (DBA 负责人) role. When that role has no members yet (e.g. a fresh install),
// it falls back to the platform admins so high-risk commands are never created
// with an empty chain that no one could ever approve.
func (s *Services) defaultChainSteps() []model.ApprovalStep {
	members := s.approverPool()
	steps := make([]model.ApprovalStep, 0, len(members))
	for i, m := range members {
		steps = append(steps, model.ApprovalStep{
			StepOrder: i + 1, ApproverID: m.ID, Approver: m.Name, Status: "waiting",
		})
	}
	return steps
}

// approverPool returns the users eligible to approve: the "owner" role members,
// or the "admin" members as a fallback when no owner has been assigned yet.
func (s *Services) approverPool() []model.User {
	// 服务账号一律排除:它登录不了控制台,进了链就是一个永远不会有人点的节点
	// (decidableApprovers)。若某角色只剩服务账号,视同该角色没有审批人,继续
	// 回落到下一档,而不是造一条谁都动不了的链。
	if owner, err := s.Repo.GetRoleByCode("owner"); err == nil {
		if members, _ := s.Repo.MembersOfRole(owner.ID); len(decidableApprovers(members)) > 0 {
			return decidableApprovers(members)
		}
	}
	if admin, err := s.Repo.GetRoleByCode("admin"); err == nil {
		members, _ := s.Repo.MembersOfRole(admin.ID)
		return decidableApprovers(members)
	}
	return nil
}

// Approve / Reject act on an approval; on approval the gateway executes the
// command and returns the execution result (output + rows).
func (s *Services) DecideApproval(actor *model.User, id int64, approve bool) (*dto.ExecResp, error) {
	ap, err := s.Repo.GetApproval(id)
	if err != nil {
		return nil, ErrNotFound
	}
	// 能不能决定这张单,只有一处判断(DecideBlockFor)—— 它同时喂给待办列表,
	// 所以按钮亮不亮和点下去放不放行,永远说的是同一件事。
	switch block := s.DecideBlockFor(actor, ap); block {
	case BlockNone:
	case BlockNotPending:
		return nil, ErrAlreadyDecided
	default:
		// 理由随错误一起带出去:笼统一句"无权处理"会让人去修错的东西 ——
		// 不在链上要去找管理员,自己发起的要去找同事,两件事完全不同。
		return nil, &DecideRefusal{Block: block}
	}
	return s.finalizeApproval(ap, approve, actor.Name)
}

// finalizeApproval is the shared decision core: atomically claim pending →
// approved/rejected, then (approve) execute the fixed ap.Command on behalf of the
// initiator + audit + notify, or (reject) record the rejection. It is reused by
// the in-app decision path (DecideApproval) and the external审批魔方 callback
// (DecideApprovalExternal); authorization is the caller's responsibility — this
// function assumes the decision is already authorized. operatorName is who acted
// (shown in the initiator's notification); the audit is attributed to the
// initiator since the command runs on their behalf.
func (s *Services) finalizeApproval(ap *model.Approval, approve bool, operatorName string) (*dto.ExecResp, error) {
	conn, _ := s.Repo.GetConnection(ap.ConnectionID)
	if conn != nil && ap.Database != "" {
		conn.Database = ap.Database // execute against the selected target database
	}
	initiator, _ := s.Repo.GetUserByID(ap.InitiatorID)
	if initiator == nil {
		// The initiator user was removed — keep audit attribution via the stored name.
		initiator = &model.User{ID: ap.InitiatorID, Name: ap.Initiator}
	}
	now := time.Now()
	if approve {
		// Atomically claim the pending → approved transition. If we lose the race
		// (already decided/rejected/expired by a concurrent caller or the timeout
		// sweep), stop here so the command is never executed twice.
		claimed, cerr := s.Repo.ClaimApproval(ap.ID, model.StatusPending, model.StatusApproved)
		if cerr != nil {
			return nil, cerr
		}
		if !claimed {
			return nil, ErrAlreadyDecided // someone else already decided/expired it
		}
		_ = s.Repo.DecideActiveStep(ap.ID, model.StatusApproved, now)
		// 通过不再执行任何命令,两条分支的区别只是**接下来由谁执行**:
		// 发布单归流水线,普通工单归发起人。
		var res gateway.ExecResult
		result, title := model.ResultPending, "审批已通过,请前往执行"
		if ap.ReleaseID > 0 {
			// A release ticket authorises the pipeline; it does not run anything.
			// The execute stage owns execution (and re-judges the statement before
			// applying it), so running the command here as well would apply the same
			// change twice — the second time to a pipeline that still believes it
			// has not run. The audit row therefore stays `pending`: approved, not yet
			// executed, with the execution audited by the stage that performs it.
			res.Output = "· 已批准,由发布流水线继续执行"
			title = "审批已通过,发布流水线继续"
		} else {
			// 通过**不再顺带执行**。命令在审批人点下去的那一刻跑,等于让审批人替
			// 发起人选择了执行时机 —— 而业务低峰、应用是否已停、备份是否就绪,
			// 只有发起人知道。审批人按下的是"我同意",不是"现在就跑"。
			//
			// 工单就停在这里等发起人来执行(ExecuteApproved)。审计行同样记 pending:
			// 已批准、尚未执行,真正的执行由执行它的那一刻自己写审计。
			res.Output = "· 已批准,等待发起人执行"
		}
		_ = s.Repo.SetApprovalResult(ap.ID, res.Output, res.Rows, now)
		// 动作记 approve 而不是 exec:这一刻发生的事情是一次审批决定,命令一行都
		// 没有跑。记成 exec 会让审计里出现一条查不到对应库变更的"执行",也会让只
		// 订阅 exec 的 webhook 在什么都没执行时收到通知。真正的执行由
		// ExecuteApproved 自己写一条 exec —— 那条才对得上库里的变化。
		s.recordAuditBy(initiator, operatorName, conn, ap.Command, ap.RiskLevel, result, ap.ApNo, "approve")
		// 话要说清"还需要你去执行" —— 一句"已通过"会让人以为事情办完了,然后那条
		// 命令就一直挂在那里,直到有人发现变更根本没生效。
		body := fmt.Sprintf("%s 通过了你的命令：%s\n请到「审批」页找到这张工单并执行。",
			operatorName, safeClip(ap.Command, 60))
		if ap.ReleaseID > 0 {
			body = fmt.Sprintf("%s 通过了发布单里的这一步：%s\n流水线将继续执行,无需手动操作。",
				operatorName, safeClip(ap.Command, 60))
		}
		s.notify(ap.InitiatorID, model.NotifApprovalApproved, title, body, ap.ApNo)
		return &dto.ExecResp{Risk: ap.RiskLevel, Output: res.Output, Rows: res.Rows, Ms: res.Ms,
			Columns: res.Columns, Data: res.Data, Truncated: res.Truncated}, nil
	}
	// Same atomic guard on the reject path.
	claimed, cerr := s.Repo.ClaimApproval(ap.ID, model.StatusPending, model.StatusRejected)
	if cerr != nil {
		return nil, cerr
	}
	if !claimed {
		return nil, ErrAlreadyDecided // someone else already decided/expired it
	}
	_ = s.Repo.DecideActiveStep(ap.ID, model.StatusRejected, now)
	_ = s.Repo.SetApprovalResult(ap.ID, "", 0, now)
	s.recordAuditBy(initiator, operatorName, conn, ap.Command, ap.RiskLevel, model.ResultRejected, ap.ApNo, "approve")
	s.notify(ap.InitiatorID, model.NotifApprovalRejected, "审批被拒绝",
		fmt.Sprintf("%s 驳回了你的命令：%s", operatorName, safeClip(ap.Command, 80)), ap.ApNo)
	return nil, nil
}

// notify records an in-app message for a user (no-op for an unknown user).
func (s *Services) notify(userID int64, typ, title, body, refNo string) {
	if userID == 0 {
		return
	}
	_ = s.Repo.CreateNotification(&model.Notification{
		UserID: userID, Type: typ, Title: title, Body: body, RefNo: refNo,
	})
}

// ListNotifications returns a user's inbox (newest first) plus the unread count.
func (s *Services) ListNotifications(userID int64, limit int) ([]model.Notification, int64) {
	ns, _ := s.Repo.ListNotifications(userID, limit)
	unread, _ := s.Repo.CountUnreadNotifications(userID)
	return ns, unread
}

// MarkNotificationsRead marks the user's notifications read (empty ids = all).
func (s *Services) MarkNotificationsRead(userID int64, ids []int64) error {
	return s.Repo.MarkNotificationsRead(userID, ids)
}

// clip trims s to at most n runes, appending an ellipsis when truncated.
func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// safeClip masks credential literals and THEN shortens — never the other way
// round, which is the only reason this exists as a function.
//
// Redaction matches a complete quoted literal, so it needs the closing quote.
// Clipping first can cut inside the password, leaving `IDENTIFIED BY 'X8wr^J+iu`
// — which matches nothing, passes through untouched, and puts most of the secret
// in whatever the excerpt was for. Use this for every command excerpt shown to a
// human or sent anywhere.
func safeClip(command string, n int) string {
	return clip(sqlutil.RedactSecrets(command), n)
}

// DefaultUploadDir is the built-in upload directory (relative to the backend's
// working directory) used when no script.savePath is configured.
const DefaultUploadDir = "uploads"

// ScriptSavePath returns the upload-script save directory: the configured
// script.savePath, or the default "uploads" dir under the backend run dir.
func (s *Services) ScriptSavePath() string {
	if p := strings.TrimSpace(s.settingString("script.savePath", "")); p != "" {
		return p
	}
	return DefaultUploadDir
}

// SaveUploadedScript archives a script pasted into the terminal's script panel
// under the configured directory and returns the OWNED upload record — not just
// the path. The record's id is what lets the approval ticket reference the file
// (excerpt + digest) instead of embedding the whole body in `command`: throwing
// the id away here was why a pasted 100KB script still died with "Data too long
// for column 'command'" while the upload-picker path had long moved to
// references. Errors with ErrScriptPathUnset when no path is configured.
func (s *Services) SaveUploadedScript(u *model.User, _ int64, filename, content string) (*model.ScriptUpload, error) {
	return s.storeScript(u, filename, content, "terminal")
}

// UploadScript stores a script file from the upload page and records it under
// the uploader (per-user isolation).
func (s *Services) UploadScript(u *model.User, filename, content string) (*model.ScriptUpload, error) {
	return s.storeScript(u, filename, content, "upload")
}

// userDirName returns a filesystem-safe per-user directory name (the username,
// spaces to underscores), falling back to user-<id>.
func userDirName(u *model.User) string {
	name := sanitizeFilename(strings.ReplaceAll(strings.TrimSpace(u.Name), " ", "_"))
	if name == "" {
		name = fmt.Sprintf("user-%d", u.ID)
	}
	return name
}

// UserUploadDir returns a user's upload directory: <savePath>/<username>.
func (s *Services) UserUploadDir(u *model.User) string {
	return filepath.Join(s.ScriptSavePath(), userDirName(u))
}

// storeScript writes an uploaded script under <savePath>/<username>/ and records
// an owned ScriptUpload row.
func (s *Services) storeScript(u *model.User, filename, content, source string) (*model.ScriptUpload, error) {
	dir := s.UserUploadDir(u)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	name := sanitizeFilename(filename)
	if name == "" {
		name = "script.sql"
	}
	saved := filepath.Join(dir, time.Now().Format("20060102-150405.000")+"_"+name)
	if err := os.WriteFile(saved, []byte(content), 0o644); err != nil {
		return nil, err
	}
	up := &model.ScriptUpload{UserID: u.ID, Filename: name, Path: saved, Size: int64(len(content)), Source: source}
	if err := s.Repo.CreateScriptUpload(up); err != nil {
		return nil, err
	}
	return up, nil
}

// ListScriptUploads returns the user's own uploaded scripts (newest first).
func (s *Services) ListScriptUploads(u *model.User) []model.ScriptUpload {
	if u == nil {
		return []model.ScriptUpload{}
	}
	us, _ := s.Repo.ListScriptUploads(u.ID)
	if us == nil {
		us = []model.ScriptUpload{}
	}
	return us
}

// DeleteScriptUpload removes a user's own uploaded script (row + file).
func (s *Services) DeleteScriptUpload(u *model.User, id int64) error {
	up, err := s.Repo.GetScriptUpload(id)
	if err != nil || up.UserID != u.ID {
		return ErrForbidden
	}
	_ = os.Remove(up.Path)
	return s.Repo.DeleteScriptUpload(id)
}

// ScriptUploadFile resolves a user's own uploaded-script file path for download.
func (s *Services) ScriptUploadFile(u *model.User, id int64) (string, string, error) {
	up, err := s.Repo.GetScriptUpload(id)
	if err != nil || up.UserID != u.ID {
		return "", "", ErrForbidden
	}
	if fi, err := os.Stat(up.Path); err != nil || fi.IsDir() {
		return "", "", ErrNotFound
	}
	return up.Path, up.Filename, nil
}

// ScriptUploadContent loads a user's own uploaded-script content so it can be
// dispatched for execution from the terminal.
func (s *Services) ScriptUploadContent(u *model.User, id int64) (string, string, error) {
	up, err := s.Repo.GetScriptUpload(id)
	if err != nil || up.UserID != u.ID {
		return "", "", ErrForbidden
	}
	data, err := os.ReadFile(up.Path)
	if err != nil {
		return "", "", ErrNotFound
	}
	return string(data), up.Filename, nil
}

// sanitizeFilename reduces an uploaded name to a safe base filename.
func sanitizeFilename(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|':
			return '_'
		}
		return r
	}, name)
	return strings.TrimSpace(name)
}

// ScanScript scans a .sql script statement-by-statement (dictionary + strict),
// mirroring the prototype scanner.
//
// A script is scanned against the dictionary of the tier holding ScanBaseline —
// previously always PROD, now whichever tier the operator designated. It is
// deliberately one fixed lens rather than the target connection's own tier: the
// scan happens on upload, before a target is necessarily chosen, and grading a
// script leniently because it is bound for dev would let the same file move to
// prod already marked clean.
//
// Returns an error when no baseline tier can be read, and the caller MUST
// surface it. Scanning against an empty tier code matches no dictionary rows and
// reports every statement — DROP TABLE included — as safe, with no failure
// anywhere to notice (ED3, same stance as unavailableVerdict).
func (s *Services) ScanScript(filename, content string) (*dto.ScriptScanResp, error) {
	base, err := s.Repo.ScanBaselineTier()
	if err != nil {
		return nil, fmt.Errorf("no scan baseline tier: %w", err)
	}
	stmts := splitStatements(content)
	out := &dto.ScriptScanResp{Filename: filename, Statements: []dto.ScannedStmt{}}
	for i, sql := range stmts {
		cmd, risk, noWhere := s.Engine.ScanStatement(base.Code, sql)
		out.Statements = append(out.Statements, dto.ScannedStmt{
			Index: i + 1, SQL: sql, Command: cmd, Risk: risk, NoWhere: noWhere,
		})
		switch risk {
		case "high":
			out.High++
		case "mid":
			out.Mid++
		default:
			out.Safe++
		}
	}
	out.Total = len(out.Statements)
	out.HasRisky = out.High+out.Mid > 0
	return out, nil
}

// ExecuteSafeScript runs each statement of an already-scanned all-safe script
// through the full gateway flow (judge → execute → audit), so direct script
// execution leaves the same per-statement audit trail as terminal commands
// (FR-AUDIT / US#32-33). Returns the count of executed statements.
func (s *Services) ExecuteSafeScript(u *model.User, connID int64, content, mfaCode, database string) (int, error) {
	conn, err := s.Repo.GetConnection(connID)
	if err != nil {
		return 0, ErrNotFound
	}
	applyTargetDatabase(conn, database)
	if !s.canAccessConn(u, conn) {
		return 0, ErrForbidden
	}
	if conn.Status == "maint" {
		return 0, nil // maintenance: nothing executed (mirrors Exec's soft no-op)
	}
	// Validate the PROD step-up ONCE for the whole script — a single TOTP code
	// covers the batch; per-statement checks would demand (and consume) a code on
	// every line and always fail for MFA users (R18).
	if err := s.checkMFA(u, conn, mfaCode); err != nil {
		return 0, err
	}
	executed := 0
	for _, sql := range splitStatements(content) {
		resp, err := s.execJudged(u, conn, sql, "脚本安全语句直接执行")
		if err != nil {
			return executed, err
		}
		// A statement the capability matrix routes to approval was NOT executed —
		// don't count it, and stop so the caller learns the batch is incomplete.
		// (The whole-script scan uses a PROD lens; per-connection capability can
		// still intercept, so this can legitimately happen.)
		if resp != nil && resp.Intercepted {
			return executed, ErrBadRequest
		}
		executed++
	}
	return executed, nil
}

// SetStrict toggles strict mode on the live engine.
func (s *Services) SetStrict(v bool) {
	s.StrictMode.Store(v)
	s.Engine.SetStrict(v)
}

// SweepApprovalTimeouts applies the configured timeout policy to overdue pending
// approvals (FR-APPR-05 / backend doc §7). Called periodically from a goroutine.
func (s *Services) SweepApprovalTimeouts() {
	action := s.settingString("approval.onTimeout", "keep-waiting")
	if action == "" || action == "keep-waiting" {
		return
	}
	minutes := s.settingInt("approval.timeoutMinutes", 720)
	cutoff := time.Now().Add(-time.Duration(minutes) * time.Minute)
	aps, err := s.Repo.ListPendingApprovalsOlderThan(cutoff)
	if err != nil {
		return
	}
	for _, a := range aps {
		conn, _ := s.Repo.GetConnection(a.ConnectionID)
		initiator, _ := s.Repo.GetUserByID(a.InitiatorID)
		if initiator == nil {
			continue
		}
		switch action {
		case "auto-reject":
			// Atomically claim pending → expired; if a human decision landed first,
			// skip so we don't overwrite an approved/rejected outcome.
			if claimed, _ := s.Repo.ClaimApproval(a.ID, model.StatusPending, model.StatusExpired); !claimed {
				continue
			}
			s.recordAudit(initiator, conn, a.Command, a.RiskLevel, model.ResultRejected, a.ApNo, "approve")
			s.notify(a.InitiatorID, model.NotifApprovalExpired, "审批已超时作废",
				fmt.Sprintf("超过时限未审批，已自动作废：%s", safeClip(a.Command, 80)), a.ApNo)
			s.cancelExternalApproval(a) // (审批魔方) collapse the still-open Lark card, best-effort
		case "auto-escalate":
			// Keep pending for the final approver (owner) but raise an escalation
			// alert ONCE — claim the escalation atomically so repeated sweeps don't
			// re-audit/re-notify the same ticket every 60s (R13).
			if claimed, _ := s.Repo.ClaimEscalation(a.ID); !claimed {
				continue
			}
			s.recordAudit(initiator, conn, a.Command, a.RiskLevel, model.ResultWarn, a.ApNo, "approve")
		}
	}
}

// settingString reads a string setting (JSON-encoded), falling back to def.
func (s *Services) settingString(key, def string) string {
	v, err := s.Repo.GetSetting(key)
	if err != nil || v == "" {
		return def
	}
	var str string
	if json.Unmarshal([]byte(v), &str) == nil {
		return str
	}
	return strings.Trim(v, "\"")
}

// settingBool reads a bool setting (JSON-encoded), falling back to def.
func (s *Services) settingBool(key string, def bool) bool {
	v, err := s.Repo.GetSetting(key)
	if err != nil || v == "" {
		return def
	}
	var b bool
	if json.Unmarshal([]byte(v), &b) == nil {
		return b
	}
	return def
}

// checkMFA enforces the TOTP step-up policy for an operation on a tier that
// demands it. It returns ErrMFARequired when the caller must present a valid
// code (or, under the mandatory policy, must first enroll). nil means the op may
// proceed.
//
// The trigger is the tier's RequireMFA flag, not the name "prod": a second
// production tier, or any tier an operator marks, steps up identically.
//
// By default MFA is opt-in: an un-enrolled user passes (preserves existing
// flows). Turning on security.mfaMandatory closes that gap (M4) by blocking any
// step-up op from a user who has not enrolled MFA.
func (s *Services) checkMFA(u *model.User, conn *model.Connection, code string) error {
	if u == nil || conn == nil || !s.settingBool("security.requireMFA", true) {
		return nil // policy inactive for this operation
	}
	// An unresolvable tier is treated as demanding the step-up. The op is about
	// to be refused by the judgement layer anyway (tierOf), and guessing the
	// laxer answer here is the one direction that could let it through.
	t, err := s.tierOf(conn)
	if err == nil && !t.RequireMFA {
		return nil // this tier does not step up
	}
	enrolled := u.MFAEnabled && u.MFASecret != "" // seed flags enabled w/o secret; that isn't enrolled
	if !enrolled {
		if s.settingBool("security.mfaMandatory", false) {
			// The mandate binds PEOPLE. A service account cannot enroll TOTP, and
			// forcing it would end with a shared TOTP secret in a CI vault — worse
			// than the exemption. Its second factor is the API credential's bcrypt
			// secret plus that credential's own IP allowlist.
			if u.Kind == model.UserKindService {
				return nil
			}
			return ErrMFARequired // must enroll before any PROD op
		}
		return nil // opt-in default
	}
	// One step-up vouches for this session on THIS instance for a while. Demanding
	// a fresh code per command meant retyping one every 30s during an incident —
	// friction whose realistic outcome is the policy being switched off. The grace
	// is bound to the session generation and the connection, so a logout, password
	// reset or role change voids it, and it never carries to another instance.
	if s.mfaVerifiedRecently(u, conn) {
		return nil
	}
	if !s.validateTOTP(u, code) {
		return ErrMFARequired
	}
	s.noteMFAVerified(u, conn)
	return nil
}

// loginTOTPValid checks a TOTP code WITHOUT consuming its counter. Login only
// verifies the second factor; the PROD step-up (validateTOTP) is what enforces
// one-time use. Not consuming here avoids a same-window double-consume where a
// user who just logged in couldn't immediately use the same code for a step-up.
func loginTOTPValid(secret, code string) bool {
	return totp.Validate(secret, code, time.Now())
}

// validateTOTP verifies a TOTP code for an enrolled user and consumes its counter.
// Anti-replay: a given code (time-step) may be consumed only once, so a captured /
// logged code replayed within its ~90s window is rejected (M3). Shared by the PROD
// step-up (checkMFA) and login-time MFA.
func (s *Services) validateTOTP(u *model.User, code string) bool {
	ok, counter := totp.ValidateWithCounter(u.MFASecret, code, time.Now())
	return ok && s.Repo.ConsumeMFACounter(u.ID, int64(counter))
}

// SessionTTL maps the security.sessionTTL setting ("4h"/"8h"/"24h") to a duration.
func (s *Services) SessionTTL() time.Duration {
	switch s.settingString("security.sessionTTL", "8h") {
	case "4h":
		return 4 * time.Hour
	case "24h":
		return 24 * time.Hour
	default:
		return 8 * time.Hour
	}
}

// MFASetup begins enrollment: (re)issues a pending secret and returns the
// otpauth URI. The secret is stored but MFA stays disabled until MFAEnable.
func (s *Services) MFASetup(u *model.User) (*dto.MFASetupResp, error) {
	// Enrolling stores a pending secret and clears mfa_enabled, which means this
	// endpoint DISARMS the second factor. That is fine for a first enrolment, but
	// on an account already protected it let anyone holding a session turn the
	// factor off and collect a fresh secret — password-only login worked again and
	// the PROD step-up saw an unenrolled user (EU1). MFADisable requires a valid
	// code to reach the same state, so allowing it here just offered a cheaper
	// door. A user who lost their authenticator is recovered by an administrator
	// via POST /users/:id/mfa/reset.
	// Keyed on an ARMED factor (flag AND secret), not the flag alone: an account
	// flagged enabled with no stored secret has nothing to disarm and nothing to
	// prove possession of, and checkMFA already treats it as unenrolled. Blocking
	// that state would lock such users out of enrolling at all.
	if u.MFAEnabled && u.MFASecret != "" {
		return nil, ErrForbidden
	}
	secret := u.MFASecret
	if secret == "" {
		secret = totp.GenerateSecret()
	}
	if err := s.Repo.UpdateUserMFA(u.ID, false, secret); err != nil {
		return nil, err
	}
	return &dto.MFASetupResp{Secret: secret, OtpauthURI: totp.URI(secret, u.Email, "DP DB GATEWAY")}, nil
}

// MFAEnable verifies the first code against the pending secret and turns MFA on.
// The enrollment code is then consumed so it cannot double as a PROD step-up
// within its ~90s window (B8) — enrollment resets the counter to 0, so we spend
// it right after enabling.
func (s *Services) MFAEnable(u *model.User, code string) error {
	if u.MFASecret == "" {
		return ErrBadRequest
	}
	ok, counter := totp.ValidateWithCounter(u.MFASecret, code, time.Now())
	if !ok {
		return ErrMFARequired
	}
	if err := s.Repo.UpdateUserMFA(u.ID, true, u.MFASecret); err != nil {
		return err
	}
	s.Repo.ConsumeMFACounter(u.ID, int64(counter)) // spend the enrollment code
	return nil
}

// MFADisable turns MFA off after verifying a current, not-yet-used code, clearing
// the secret. Consuming the counter blocks replay of a code already spent on a
// PROD step-up (R15).
func (s *Services) MFADisable(u *model.User, code string) error {
	if !u.MFAEnabled {
		return nil
	}
	ok, counter := totp.ValidateWithCounter(u.MFASecret, code, time.Now())
	if !ok {
		return ErrMFARequired
	}
	if !s.Repo.ConsumeMFACounter(u.ID, int64(counter)) {
		return ErrMFARequired // code already used (e.g. for a step-up)
	}
	return s.Repo.UpdateUserMFA(u.ID, false, "")
}

// ---- admin user management (password + OTP binding on behalf of a user) ----

// AdminSetPassword resets a user's password (bcrypt), no old password needed.
func (s *Services) AdminSetPassword(actor *model.User, id int64, newPassword string) error {
	if len(newPassword) < 8 { // keep in sync with the frontend savePw check (C4)
		return ErrBadRequest
	}
	u, err := s.Repo.GetUserByID(id)
	if err != nil {
		return ErrNotFound
	}
	// A service account has no console door, so a password on it is a key to a
	// door that must stay locked. Login refuses kind=service regardless, but a
	// hash that exists is a hash that can leak — refuse to mint one at all.
	if u.Kind == model.UserKindService {
		return fmt.Errorf("服务账号不能设置登录口令,它只通过 API 凭据访问")
	}
	hash, err := crypto.HashPassword(newPassword)
	if err != nil {
		return err
	}
	if err := s.Repo.UpdateUserPassword(u.ID, hash); err != nil {
		return err
	}
	s.auditAdminAction(actor, "admin.password.reset user="+u.Email)
	// A password reset must invalidate the user's existing sessions (M1).
	return s.Repo.BumpTokenVersion(u.ID)
}

// AdminResetMFA unbinds a user's OTP (disable + clear secret) so they can
// re-enroll.
func (s *Services) AdminResetMFA(actor *model.User, id int64) error {
	u, err := s.Repo.GetUserByID(id)
	if err != nil {
		return ErrNotFound
	}
	if err := s.Repo.UpdateUserMFA(id, false, ""); err != nil {
		return err
	}
	s.auditAdminAction(actor, "admin.mfa.reset user="+u.Email)
	return nil
}

// AdminBindMFA generates a fresh OTP secret, binds it (enabled) to the user, and
// returns the secret + otpauth URI so the admin can hand the QR to the user.
func (s *Services) AdminBindMFA(actor *model.User, id int64) (*dto.MFASetupResp, error) {
	u, err := s.Repo.GetUserByID(id)
	if err != nil {
		return nil, ErrNotFound
	}
	secret := totp.GenerateSecret()
	if err := s.Repo.UpdateUserMFA(u.ID, true, secret); err != nil {
		return nil, err
	}
	s.auditAdminAction(actor, "admin.mfa.bind user="+u.Email)
	return &dto.MFASetupResp{Secret: secret, OtpauthURI: totp.URI(secret, u.Email, "DP DB GATEWAY")}, nil
}

// settingInt reads an int setting (JSON-encoded), falling back to def.
func (s *Services) settingInt(key string, def int) int {
	v, err := s.Repo.GetSetting(key)
	if err != nil || v == "" {
		return def
	}
	var n int
	if json.Unmarshal([]byte(v), &n) == nil {
		return n
	}
	return def
}

// execTimeout is the per-command execution timeout, configurable at runtime via
// the gateway.execTimeout setting (seconds). Bounded to [1s, 3600s]; default 30s.
func (s *Services) execTimeout() time.Duration {
	sec := s.settingInt("gateway.execTimeout", 30)
	if sec < 1 {
		sec = 1
	}
	if sec > 3600 {
		sec = 3600
	}
	return time.Duration(sec) * time.Second
}

var wsRe = regexp.MustCompile(`\s+`)

// splitStatements splits a script into statements (quote/comment-aware so a
// literal like 'a;b' isn't cut in two) and normalizes each one's whitespace.
func splitStatements(text string) []string {
	stmts := sqlutil.SplitStatements(text)
	out := make([]string, 0, len(stmts))
	for _, s := range stmts {
		if s = strings.TrimSpace(wsRe.ReplaceAllString(s, " ")); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func firstWord(s string) string {
	f := strings.Fields(strings.TrimSpace(s))
	if len(f) == 0 {
		return ""
	}
	return strings.ToUpper(f[0])
}

// RecordTranscriptExport audits a terminal session log being saved to a file.
//
// The file itself is assembled in the browser out of lines that were already
// displayed to this user, so no new data is read here and there is nothing to
// gate. What there is, is a copy of production query output now sitting outside
// the gateway — and /export already records exactly that. Leaving the terminal
// path silent would mean the audit trail could account for one route out of the
// console and not the other, while both carry the same rows.
//
// Access is still checked: the caller must be able to reach the instance they
// claim to have exported, or the audit trail could be seeded with rows about
// instances they cannot see.
func (s *Services) RecordTranscriptExport(u *model.User, req dto.TranscriptExportReq) error {
	conn, err := s.Repo.GetConnection(req.ConnectionID)
	if err != nil {
		return ErrNotFound
	}
	if !s.canAccessConn(u, conn) {
		return ErrForbidden
	}
	applyTargetDatabase(conn, req.Database)
	name := filepath.Base(strings.ReplaceAll(strings.TrimSpace(req.Filename), "\\", "/"))
	if name == "" || name == "." || name == "/" {
		name = "session.log"
	}
	// The audited "command" describes the export. It is not SQL, and the leading
	// marker keeps it from being mistaken for one in the audit list (same shape as
	// the `\i file` marker script execution uses).
	label := fmt.Sprintf(`\log %s · %d 行`, name, req.Lines)
	if req.Dropped > 0 {
		// Say it here too: the row must not imply the file is the whole session.
		label += fmt.Sprintf(" · 已丢弃最早 %d 行", req.Dropped)
	}
	s.recordAudit(u, conn, label, model.RiskLow, model.ResultExported, "", "")
	return nil
}
