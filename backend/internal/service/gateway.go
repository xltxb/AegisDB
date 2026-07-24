package service

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
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
	v := s.Engine.EvaluateRoles(s.Repo.EffectiveRoleIDs(u), conn.Env, sql)
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
	conn, err := s.Repo.GetConnection(connID)
	if err != nil {
		return nil, ErrNotFound
	}
	// Target a specific database within the instance when the caller chose one.
	if database != "" {
		conn.Database = database
	}
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
	if stmts := sqlutil.SplitStatements(sql); len(stmts) > 1 {
		return s.applyVerdict(u, conn, sql, s.strictestVerdict(u, conn, stmts), reason)
	}
	return s.execJudged(u, conn, sql, reason)
}

// strictestVerdict evaluates every statement and returns the one demanding the
// most gating (deny > approve > allow), so a mixed command is governed by its
// most dangerous part rather than its leading verb.
func (s *Services) strictestVerdict(u *model.User, conn *model.Connection, stmts []string) gateway.Verdict {
	strict := gateway.Verdict{Action: gateway.ActionAllow, Risk: model.RiskLow}
	roleIDs := s.Repo.EffectiveRoleIDs(u)
	for _, st := range stmts {
		v := s.Engine.EvaluateRoles(roleIDs, conn.Env, st)
		if actionRank(v.Action) > actionRank(strict.Action) {
			strict = v
		}
	}
	return strict
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
	return s.applyVerdict(u, conn, sql, s.Engine.EvaluateRoles(s.Repo.EffectiveRoleIDs(u), conn.Env, sql), reason)
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
func (s *Services) SubmitScriptForApproval(u *model.User, connID int64, filename, content, reason, mfaCode, database string) (*dto.ExecResp, error) {
	conn, err := s.Repo.GetConnection(connID)
	if err != nil {
		return nil, ErrNotFound
	}
	if database != "" {
		conn.Database = database // target database captured on the approval ticket
	}
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
	scan := s.ScanScript(filename, content)
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
		if v := s.Engine.EvaluateRoles(s.Repo.EffectiveRoleIDs(u), conn.Env, worst); v.Action == gateway.ActionDeny {
			s.recordAudit(u, conn, "\\i "+filename, model.RiskHigh, model.ResultRejected, "", "intercept")
			return nil, ErrForbidden
		}
	}
	rule := "脚本含高危语句 · 需审批"
	av := gateway.Verdict{Action: gateway.ActionApprove, Risk: risk, Rule: rule}
	label := "\\i " + filename + "\n" + strings.TrimSpace(content)
	ap, auditID, err := s.createApproval(u, conn, label, av, reason)
	if err != nil {
		return nil, err
	}
	s.recordAudit(u, conn, label, risk, model.ResultPending, ap.ApNo, "intercept")
	return &dto.ExecResp{Intercepted: true, ApprovalNo: ap.ApNo, AuditID: auditID, Risk: risk, Rule: rule}, nil
}

// createApproval builds an approval ticket + chain steps (default approvers from
// the DBA-owner role) and links a fresh audit id.
func (s *Services) createApproval(u *model.User, conn *model.Connection, sql string, v gateway.Verdict, reason string) (*model.Approval, string, error) {
	apNo := s.nextApNo()
	auditID := s.nextAuditID()
	kw := firstWord(sql)
	ap := &model.Approval{
		ApNo: apNo, ConnectionID: conn.ID, Env: conn.Env, Instance: conn.Name,
		Command: sql, Keyword: kw, Database: conn.Database, InitiatorID: u.ID, Initiator: u.Name,
		Reason: reason, RiskLevel: v.Risk, Status: model.StatusPending, AuditID: auditID,
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
	if owner, err := s.Repo.GetRoleByCode("owner"); err == nil {
		if members, _ := s.Repo.MembersOfRole(owner.ID); len(members) > 0 {
			return members
		}
	}
	if admin, err := s.Repo.GetRoleByCode("admin"); err == nil {
		members, _ := s.Repo.MembersOfRole(admin.ID)
		return members
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
	if ap.Status != model.StatusPending {
		return nil, ErrAlreadyDecided
	}
	// The initiator may not decide their own ticket (two-person control, R16) —
	// unless an admin has explicitly enabled self-approval (small teams / single
	// operator). Default off preserves the segregation-of-duties guarantee.
	if actor != nil && actor.ID == ap.InitiatorID && !s.settingBool("approval.allowSelfApprove", false) {
		return nil, ErrForbidden
	}
	// Only a member on this approval's chain may act on it — holding the approve
	// menu is not enough.
	if !s.isChainMember(id, actor) {
		return nil, ErrForbidden
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
		var res gateway.ExecResult
		result, title := model.ResultExecuted, "审批已通过并执行"
		if conn != nil {
			res = s.Executor.Run(conn, ap.Command, s.execTimeout()) // gateway runs it on behalf of the initiator
			result = execResultStatus(res)
		} else {
			// The target connection was deleted/unavailable — nothing ran. Report
			// this honestly instead of claiming "executed" (R-verify).
			res.Output = "· 目标连接已不存在,命令未执行"
			result, title = model.ResultWarn, "审批已通过但目标连接不存在,未执行"
		}
		_ = s.Repo.SetApprovalResult(ap.ID, res.Output, res.Rows, now)
		s.recordAudit(initiator, conn, ap.Command, ap.RiskLevel, result, ap.ApNo, "exec")
		s.notify(ap.InitiatorID, model.NotifApprovalApproved, title,
			fmt.Sprintf("%s 处理了你的命令：%s\n结果：%s", operatorName, clip(ap.Command, 60), clip(res.Output, 120)), ap.ApNo)
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
	s.recordAudit(initiator, conn, ap.Command, ap.RiskLevel, model.ResultRejected, ap.ApNo, "approve")
	s.notify(ap.InitiatorID, model.NotifApprovalRejected, "审批被拒绝",
		fmt.Sprintf("%s 驳回了你的命令：%s", operatorName, clip(ap.Command, 80)), ap.ApNo)
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

// SaveUploadedScript archives an uploaded script under the configured directory,
// organized into <savePath>/<env-connection>/<YYYY-MM-DD>/<HHMMSS>_<file> so the
// history is browsable by instance and day. Returns the saved path. Errors with
// ErrScriptPathUnset when no path is configured — upload is unusable until then.
func (s *Services) SaveUploadedScript(u *model.User, _ int64, filename, content string) (string, error) {
	up, err := s.storeScript(u, filename, content, "terminal")
	if err != nil {
		return "", err
	}
	return up.Path, nil
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
func (s *Services) ScanScript(filename, content string) *dto.ScriptScanResp {
	stmts := splitStatements(content)
	out := &dto.ScriptScanResp{Filename: filename, Statements: []dto.ScannedStmt{}}
	for i, sql := range stmts {
		cmd, risk, noWhere := s.Engine.ScanStatement(model.EnvProd, sql)
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
	return out
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
	if database != "" {
		conn.Database = database
	}
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
				fmt.Sprintf("超过时限未审批，已自动作废：%s", clip(a.Command, 80)), a.ApNo)
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

// checkMFA enforces the TOTP step-up policy for a PROD operation. It returns
// ErrMFARequired when the caller must present a valid code (or, under the
// mandatory policy, must first enroll). nil means the op may proceed.
//
// By default MFA is opt-in: an un-enrolled user passes (preserves existing
// flows). Turning on security.mfaMandatory closes that gap (M4) by blocking any
// PROD op from a user who has not enrolled MFA.
func (s *Services) checkMFA(u *model.User, conn *model.Connection, code string) error {
	if u == nil || conn == nil || conn.Env != model.EnvProd || !s.settingBool("security.requireMFA", true) {
		return nil // policy inactive for this operation
	}
	enrolled := u.MFAEnabled && u.MFASecret != "" // seed flags enabled w/o secret; that isn't enrolled
	if !enrolled {
		if s.settingBool("security.mfaMandatory", false) {
			return ErrMFARequired // must enroll before any PROD op
		}
		return nil // opt-in default
	}
	if !s.validateTOTP(u, code) {
		return ErrMFARequired
	}
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
	secret := u.MFASecret
	if secret == "" || u.MFAEnabled {
		// fresh secret for a new enrollment (or re-enrollment)
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
func (s *Services) AdminSetPassword(id int64, newPassword string) error {
	if len(newPassword) < 8 { // keep in sync with the frontend savePw check (C4)
		return ErrBadRequest
	}
	u, err := s.Repo.GetUserByID(id)
	if err != nil {
		return ErrNotFound
	}
	hash, err := crypto.HashPassword(newPassword)
	if err != nil {
		return err
	}
	if err := s.Repo.UpdateUserPassword(u.ID, hash); err != nil {
		return err
	}
	// A password reset must invalidate the user's existing sessions (M1).
	return s.Repo.BumpTokenVersion(u.ID)
}

// AdminResetMFA unbinds a user's OTP (disable + clear secret) so they can
// re-enroll.
func (s *Services) AdminResetMFA(id int64) error {
	if _, err := s.Repo.GetUserByID(id); err != nil {
		return ErrNotFound
	}
	return s.Repo.UpdateUserMFA(id, false, "")
}

// AdminBindMFA generates a fresh OTP secret, binds it (enabled) to the user, and
// returns the secret + otpauth URI so the admin can hand the QR to the user.
func (s *Services) AdminBindMFA(id int64) (*dto.MFASetupResp, error) {
	u, err := s.Repo.GetUserByID(id)
	if err != nil {
		return nil, ErrNotFound
	}
	secret := totp.GenerateSecret()
	if err := s.Repo.UpdateUserMFA(u.ID, true, secret); err != nil {
		return nil, err
	}
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
