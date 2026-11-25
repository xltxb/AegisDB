package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"aegisdb/internal/apimsg"
	"aegisdb/internal/chat"
	"aegisdb/internal/dto"
	"aegisdb/internal/gateway"
	"aegisdb/internal/model"
	"aegisdb/pkg/crypto"
	"aegisdb/pkg/jwt"
	"aegisdb/pkg/sqlutil"
	"aegisdb/pkg/totp"
)

// maintNotice 是维护态那条提示的规范中文串。
//
// 它出现在两条入口上(单条执行与脚本执行),原先各写一遍 —— 两份字面量迟早分叉,
// 而分叉之后界面上会出现两句意思相同、措辞不同的提示。规范串归它,读者看到的那句
// 由 model.OutMaintenance 渲染。
const maintNotice = "· 目标实例处于维护态，操作受限"

// BuildMe assembles the /auth/me payload (user + menus + capabilities).
func (s *Services) BuildMe(u *model.User) (*dto.MeResp, error) {
	// 主角色可能压根不存在,而这不是错误。
	//
	// 没有角色是一个合法状态(比如角色被删、或是历史上停用时被收回过角色的账户):账户
	// 能登录,但没有任何菜单,能力矩阵里处处被拒(capabilityLevelUnion 对空角色集返回 deny)。
	//
	// 从前这里查不到角色就直接返回错误,于是重新启用的账户一登录就是 500 —— 界面上
	// 看到的是"服务器错误",而不是"这个人还没有角色",管理员无从下手。
	role := &model.Role{}
	if r, rerr := s.Repo.GetRole(u.RoleID); rerr == nil && r != nil {
		role = r
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
		CanApprove: canApprove, CanSeeAllReleases: s.canSeeAllReleases(u),
		MfaEnabled: u.MFAEnabled && u.MFASecret != "", Menus: menus, Capabilities: matrix,
	}, nil
}

// RiskCheck performs the pure three-layer pre-check (no side effects).
func (s *Services) RiskCheck(u *model.User, connID int64, sql, database string) (*dto.RiskCheckResp, error) {
	conn, err := s.Repo.GetConnection(connID)
	if err != nil {
		return nil, ErrNotFound
	}
	// 够不到这台实例的人不该能拿它做预检。预检回的是"这条语句在那台实例上会怎样",
	// 而那本身就是情报:实例存不存在、挂在哪个分层、哪些命令在它上面要审批 —— 一个
	// 看不见这台实例的人,可以靠枚举 connID 把这些问出来(L-8)。与 Exec 同一道闸。
	if !s.canAccessConn(u, conn) {
		return nil, ErrForbidden
	}
	// 与 Exec 同一口径:执行窗口按库开,预检不带库名就判不出窗口,于是会比执行更严 ——
	// 终端先弹审批理由框,提交后才发现根本不用审批。
	applyTargetDatabase(conn, database)
	if _, err := s.tierCodeOf(conn); err != nil {
		return nil, ErrBadRequest // unresolvable tier — see tierOf; never judged as allow
	}
	// Judge the SPLIT statements, exactly as Exec does. Pre-checking the raw
	// string keyed the capability matrix on the leading verb only, so a batch
	// whose risky statement was not first ("SELECT 1; UPDATE …") pre-checked as
	// allow — the terminal skipped the approval-reason prompt and told the
	// operator the batch was safe, while Exec then intercepted it anyway.
	v := gateway.Verdict{Action: gateway.ActionAllow, Risk: model.RiskLow}
	var notice string
	var noticeRef *model.RuleRef
	if stmts := gateway.SplitForJudge(conn.Engine, sql); len(stmts) > 0 {
		v = s.strictestVerdict(u, conn, stmts)
		// 与 Exec 同一口径:要走在线变更的要审批(终端据此先弹审批理由框),执行窗口内除外 ——
		// 窗口内当场开始(见 startOSCInWindow)。混批 / 阻塞由 Exec 说。
		plan := s.oscPlanFor(context.Background(), conn, stmts)
		if plan.online != nil && v.Action != gateway.ActionDeny {
			rule, ref := oscApprovalRule(*plan.online)
			action := gateway.ActionApprove
			if w := s.windowFor(conn, stmts, time.Now()); w != nil {
				action = gateway.ActionAllow
				wref := model.NewRuleRef(model.RuleExecWindow, "window", w.Name)
				wref.Parts = []model.RuleRef{*ref}
				rule, ref = model.RenderRule(wref), wref
			}
			v = gateway.Verdict{Action: action, Risk: v.Risk, Rule: rule, Ref: ref, Command: v.Command}
			if v.Risk == model.RiskLow || v.Risk == "" {
				v.Risk = model.RiskMid
			}
		}
		notice, noticeRef = bigDirectNoticeOf(plan)
	}
	return &dto.RiskCheckResp{
		Notice: notice, NoticeRef: noticeRef,
		Risk:             v.Risk,
		Action:           v.Action,
		RequiresApproval: v.RequiresApproval(),
		MatchedRule:      v.Rule,
		MatchedRuleRef:   v.Ref,
		Command:          v.Command,
	}, nil
}

// Exec runs the full gateway flow: judge → (deny | approve | allow) → audit.
// ctx 来自调用方,为的是**取消**:终端里操作员按 Ctrl+C 时,这条正在跑的语句要能停下来。
// 其余入口(脚本、发布、后台任务)传 context.Background() —— 它们背后没有一个正等着的人。
func (s *Services) Exec(ctx context.Context, u *model.User, connID int64, sql, reason, mfaCode, database string) (*dto.ExecResp, error) {
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
	if conn.Status == model.ConnStatusMaint {
		s.recordAudit(u, conn, sql, model.RiskLow, model.ResultWarn, "", "")
		return &dto.ExecResp{Risk: model.RiskLow, Output: maintNotice,
			OutputRef: model.NewRuleRef(model.OutMaintenance)}, nil
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
	stmts := gateway.SplitForJudge(conn.Engine, sql) // `--` 按目标引擎的读法(MySQL 的 1--1 不是注释)
	if len(stmts) == 0 { // blank or comment-only input — nothing to normalise
		return s.execJudged(ctx, u, conn, sql, reason)
	}
	v, win := s.judge(u, conn, stmts)
	// 生产大表的索引变更改走在线变更;大表上做不了的 DDL 照原路径但带提示(.scratch/osc-product)
	plan := s.oscPlanFor(ctx, conn, stmts)
	if plan.online != nil {
		if resp, err, handled := s.execOSC(ctx, u, conn, sql, stmts, v, reason, plan); handled {
			return resp, err
		}
	}
	if notice, ref := bigDirectNoticeOf(plan); notice != "" {
		return s.applyVerdictNoticed(ctx, u, conn, sql, v, reason, win, notice, ref)
	}
	return s.applyVerdict(ctx, u, conn, sql, v, reason, win)
}

// applyVerdictNoticed 是 applyVerdict 加一句要特别留意的提示:回执里带上,生成工单时写进工单。
func (s *Services) applyVerdictNoticed(ctx context.Context, u *model.User, conn *model.Connection, sql string, v gateway.Verdict, reason string, win *model.ExecWindow, notice string, ref *model.RuleRef) (*dto.ExecResp, error) {
	if v.Action == gateway.ActionApprove {
		ap, auditID, err := s.createApprovalWith(u, conn, sql, v, reason, func(a *model.Approval) { a.Notice, a.NoticeRef = notice, ref })
		if err != nil {
			return nil, err
		}
		s.recordAudit(u, conn, sql, v.Risk, model.ResultPending, ap.ApNo, "intercept")
		return &dto.ExecResp{Intercepted: true, ApprovalNo: ap.ApNo, AuditID: auditID, Risk: v.Risk, Rule: v.Rule, RuleRef: v.Ref,
			Notice: notice, NoticeRef: ref}, nil
	}
	resp, err := s.applyVerdict(ctx, u, conn, sql, v, reason, win)
	if resp != nil {
		resp.Notice, resp.NoticeRef = notice, ref
	}
	return resp, err
}

// strictestVerdict evaluates every statement and returns the one demanding the
// most gating (deny > approve > allow; within the same action, the higher risk),
// so a mixed command is governed by its most dangerous part rather than its
// leading verb. Ranking by action alone let the FIRST approve-ranked statement
// freeze the verdict: `UPDATE …(mid); DELETE …(high)` produced a ticket graded
// mid with the DELETE's dictionary rule dropped, so the approver reviewed a
// high-risk batch under a mid-risk label.
//
// For a batch the rule names EVERY gated statement — position and verb — not
// just the winner's. Collapsing identical rule texts hid the count: three DROPs
// pasted as one command produced the single line "高危命令字典 · PROD 禁止直接执行"
// with Command = "DROP", which read as "only the first statement was caught"
// while the other two were in fact judged and gated too. The judgement was
// never the gap; the report was.
func (s *Services) strictestVerdict(u *model.User, conn *model.Connection, stmts []string) gateway.Verdict {
	v, _ := s.judge(u, conn, stmts)
	return v
}

// judge is strictestVerdict plus the execution window that relaxed it, if any.
//
// 放宽发生在**这里**,而不是各个执行入口:终端、异步执行、发布流水线、Oracle 编译,
// 每一条都经过这个函数。放在入口上就意味着"新加一条路时要记得也放宽" —— 而这类
// 遗漏的表现是"同一条语句在终端免审批、在后台执行却要审批",用户完全看不懂。
//
// 预检(RiskCheck)走的也是这里,所以终端不会先弹一个填审批理由的框、再发现不用审批。
func (s *Services) judge(u *model.User, conn *model.Connection, stmts []string) (gateway.Verdict, *model.ExecWindow) {
	v := s.rawVerdict(u, conn, stmts)
	return s.relaxByWindow(conn, stmts, v, time.Now())
}

// rawVerdict 是三层判定本身的结论,不含任何窗口放宽。
func (s *Services) rawVerdict(u *model.User, conn *model.Connection, stmts []string) gateway.Verdict {
	tier, err := s.tierCodeOf(conn)
	if err != nil {
		return gateway.Unavailable(conn.Engine, strings.Join(stmts, ";"), err)
	}
	strict := gateway.Verdict{Action: gateway.ActionAllow, Risk: model.RiskLow}
	roleIDs := s.Repo.EffectiveRoleIDs(u)
	var hits []model.RuleRef // one entry per gated statement, in script order
	for i, st := range stmts {
		v := s.Engine.EvaluateFor(roleIDs, conn.Engine, tier, st)
		if v.Action != gateway.ActionAllow && v.Ref != nil {
			hits = append(hits, *batchHitRef(i+1, v))
		}
		if actionRank(v.Action) > actionRank(strict.Action) ||
			(actionRank(v.Action) == actionRank(strict.Action) && riskRank(v.Risk) > riskRank(strict.Risk)) {
			strict = v
		}
	}
	if len(stmts) > 1 && len(hits) > 0 {
		strict.Ref = batchRef(hits)
		strict.Rule = model.RenderRule(strict.Ref)
	}
	return strict
}

// maxBatchHits bounds how many per-statement hits the rule text spells out. A
// pasted script can run to hundreds of statements; the ticket and the terminal
// line both need the first few and the total, not the whole list.
const maxBatchHits = 8

// batchHitRef identifies one gated statement of a batch: "第2条 TRUNCATE · 高危命令字典 · …",
// keeping the statement's own rule nested so a client can translate both halves.
func batchHitRef(pos int, v gateway.Verdict) *model.RuleRef {
	code := model.RuleBatchHit
	if v.Command == "" {
		code = model.RuleBatchHitBare
	}
	r := model.NewRuleRef(code, "pos", model.Itoa(pos), "command", v.Command)
	r.Parts = []model.RuleRef{*v.Ref}
	return r
}

// batchRef gathers the per-statement hits, truncating past maxBatchHits with the
// count of what was left out.
func batchRef(hits []model.RuleRef) *model.RuleRef {
	r := model.NewRuleRef(model.RuleBatch)
	if len(hits) <= maxBatchHits {
		r.Parts = hits
		return r
	}
	r.Parts = append(append([]model.RuleRef{}, hits[:maxBatchHits]...),
		*model.NewRuleRef(model.RuleBatchMore, "n", model.Itoa(len(hits)-maxBatchHits)))
	return r
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
func (s *Services) execJudged(ctx context.Context, u *model.User, conn *model.Connection, sql, reason string) (*dto.ExecResp, error) {
	tier, err := s.tierCodeOf(conn)
	if err != nil {
		// 分层解析不出来是 deny,窗口放宽不作用于 deny,所以这里没有窗口可言。
		return s.applyVerdict(ctx, u, conn, sql, gateway.Unavailable(conn.Engine, sql, err), reason, nil)
	}
	// 这条路不经过 strictestVerdict(它按单条语句判),所以放宽要在这里显式接上 ——
	// 否则同一条语句在终端里免审批、走脚本执行却要审批。
	v, win := s.relaxByWindow(conn, []string{sql}, s.Engine.EvaluateFor(s.Repo.EffectiveRoleIDs(u), conn.Engine, tier, sql), time.Now())
	return s.applyVerdict(ctx, u, conn, sql, v, reason, win)
}

// applyVerdict routes a judged command to deny / approve / allow and records the
// matching audit entry. The command text (sql) is preserved verbatim even when
// the verdict was derived from an individual sub-statement (A1).
// win 非空表示这条命令是被执行窗口放行的(本来要审批)。它会写进审计的 Operator ——
// 那个字段的语义正是"当授权者不是操作者本人时,是谁授权的",而这里授权的不是人,是
// 当时开着的那扇门。Operator 在审计链的哈希里,事后改不了。
func (s *Services) applyVerdict(ctx context.Context, u *model.User, conn *model.Connection, sql string, v gateway.Verdict, reason string, win *model.ExecWindow) (*dto.ExecResp, error) {
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
		return &dto.ExecResp{Intercepted: true, ApprovalNo: ap.ApNo, AuditID: auditID, Risk: v.Risk, Rule: v.Rule, RuleRef: v.Ref}, nil

	default: // allow
		if s.readOnlyExec(u, conn, sql) {
			ctx = gateway.WithReadOnlyGuard(ctx)
		}
		res := s.execCommand(ctx, conn, sql, s.execTimeout())
		s.recordAuditBy(u, windowOperator(win), conn, sql, v.Risk, execResultStatus(res), "", "exec")
		return &dto.ExecResp{Risk: v.Risk, Output: res.Output, OutputRef: res.OutputRef, Rows: res.Rows, Ms: res.Ms,
			ConnMs: res.ConnMs, QueryMs: res.QueryMs, FetchMs: res.FetchMs, ProcessMs: res.ProcessMs,
			Notice: res.Notice, NoticeRef: res.NoticeRef,
			Columns: res.Columns, Data: res.Data, Truncated: res.Truncated,
			ResultID: res.ResultID, TotalRows: res.TotalRows, BeyondCap: res.BeyondCap,
			ServerOutput: res.ServerOutput, ServerOutputTruncated: res.ServerOutputTruncated, Failed: res.Err != nil,
			Binds: res.Binds}, nil
	}
}

// readOnlyExec:这条直接执行的命令要不要在数据库层面只读跑(gateway.WithReadOnlyGuard)。
//
// 条件是两条同时成立:这个人在这台实例上**不能直接改数据**(write / ddl / grant 都不是 allow ——
// 只读用户,或写要审批的人),且命令拆开后每一条都只是读。前者让有写权限的人照旧(他们在查询里
// 调一个写日志的函数,本来就在权限之内);后者让执行窗口放行的写不受影响 —— 那时命令里有写语句。
// 判不出分层就按只读:能走到这里的是 allow,只读跑不会挡住任何读。
func (s *Services) readOnlyExec(u *model.User, conn *model.Connection, sql string) bool {
	if u == nil || !gateway.ReadOnlyStatements(conn.Engine, sql) {
		return false
	}
	tier, err := s.tierCodeOf(conn)
	if err != nil {
		return true
	}
	roleIDs := s.Repo.EffectiveRoleIDs(u)
	for _, c := range []string{"write", "ddl", "grant"} {
		if lvl, err := s.Engine.CapabilityFor(roleIDs, c, tier); err == nil && lvl == model.LevelAllow {
			return false
		}
	}
	return true
}

// rowWritingVerb 是"影响行数有意义"的语句:它们影响 0 行多半意味着条件没匹配上。建表、
// 查询、COMMIT 本来就不影响行,不在其列。
var rowWritingVerb = map[string]bool{"UPDATE": true, "DELETE": true, "INSERT": true, "MERGE": true, "REPLACE": true, "UPSERT": true}

// execCommand 执行一条终端命令 —— 多条语句**逐条下发**。
//
// 为什么不能把整段直接交给驱动:MySQL 的 DSN 上 AllowMultiStatements 是**故意**关掉的
// (见 gateway.engineDriver 里的注释),关掉它正是为了让"一次判定管住的就是这些语句"
// 没法靠 DSN 绕过。于是同一段批量在 SQLite / PostgreSQL 上能跑、在 MySQL 上直接语法错
// —— 同一个网关、同一段 SQL,结果取决于对面是什么引擎。那是个陷阱,不是特性。
//
// 拆在这里的好处是两件事各归各位:**判定看到的是整批**(最严裁决、一张审批单、
// RuleBatch 逐条列命中),**下发时一条一条给驱动**。这与脚本工单 runApprovedScript
// 是同一条路子,连"停在第几条"的说法都刻意保持一致 —— 一个跑到一半的批量,报成功
// 比报"已执行 3/7 条后失败"糟得多。
//
// 单条语句原样走老路:它要带回结果集(列和行),而批量不带 —— 一次回执里塞不下 N 个
// 结果集,脚本通道当初也是这么定的。
// execSession 让 ctx 下的执行都落在这台实例的一条专用连接上(见 gateway/exec_session.go):
// 整个执行同一个会话,执行中不被连接池换掉,也不占终端共用的池。返回的 done 在执行结束后调用 ——
// 连接当场断开、资源释放。
//
// 原样返回 ctx 的三种情形:模拟连接(没有库可连)、ctx 上已经有专用连接(嵌套的执行共用外层那条)、
// 专用连接开不出来(多半是连不上 —— 那就让执行照常走,连不上的错误由它自己报出来,不在这里另起
// 一种说法)。
func (s *Services) execSession(ctx context.Context, conn *model.Connection) (context.Context, func()) {
	// 终端标签页自己有一条独占连接(gateway/term_session.go),一批语句就在它上面跑,不另开。
	if !gateway.RealExecSupported(conn) || gateway.HasExecSession(ctx) || gateway.HasTermSession(ctx) {
		return ctx, func() {}
	}
	sess, err := gateway.OpenExecSession(conn)
	if err != nil {
		return ctx, func() {}
	}
	return gateway.WithExecSession(ctx, sess), func() { _ = sess.Close() }
}

func (s *Services) execCommand(ctx context.Context, conn *model.Connection, sql string, timeout time.Duration) gateway.ExecResult {
	ctx = s.withTierMasking(ctx, conn) // 分层关掉了脱敏(默认 dev / uat):结果与报错不打码
	stmts := sqlutil.SplitStatements(sql)
	if len(stmts) <= 1 {
		return s.Executor.Run(ctx, conn, sql, timeout)
	}
	// 下发用保留了可执行注释标记的那一份(与 stmts 一一对应):剥掉标记会改语义 ——
	// TiDB 的 `/*T![auto_rand] AUTO_RANDOM(5) */` 整段丢了,建出的表没有 AUTO_RANDOM(2026-10)。
	execs := sqlutil.SplitStatementsExec(sql)
	// 一批语句在同一条连接上逐条跑:SET、BEGIN … COMMIT、临时表这类依赖会话的写法才连得上。
	// 从前逐条从池里取,相邻两条可能落在两条不同的物理连接上。
	ctx, done := s.execSession(ctx, conn)
	defer done()
	total, rows, ms := len(stmts), 0, 0
	simulated := true // 空批次不会走到这里;逐条都模拟才算整批模拟
	// DBMS_OUTPUT:批里的 SET SERVEROUTPUT ON / OFF 对**同一批后面的语句**生效(与 SQL*Plus
	// 跑脚本一样),各条读回的输出按顺序合在一起。
	var srvOut []string
	srvTrunc := false
	// 影响了 0 行的写语句是第几条。批量只回一个总数,"应为 8 行、实际 7 行"时人看不出是哪一条
	// 的 where 没匹配上 —— 而那正是要去查的那一条。
	var zero []string
	var notice gateway.ExecResult // 批里任一条带了提示(如 EXPLAIN 改成只取计划),整批也要说
	// WHENEVER SQLERROR CONTINUE / EXIT 与 EXIT(SQL*Plus 脚本):见 gateway.ScriptFlow
	flow := gateway.NewScriptFlowCtx(ctx, conn.Engine)
	var failed []string
	var firstErr error
	exitPos := 0
	for i, one := range stmts {
		if on, ok := gateway.SQLPlusServerOutput(conn.Engine, one); ok {
			ctx = gateway.WithServerOutput(ctx, on)
		}
		flow.Observe(one)
		res := s.Executor.Run(flow.Context(ctx), conn, execs[i], timeout)
		flow.Update(res.Binds) // PL/SQL 改了绑定变量:后面的语句看到新值
		ms += res.Ms
		simulated = simulated && res.Simulated
		if res.Notice != "" && notice.Notice == "" {
			notice.Notice, notice.NoticeRef = res.Notice, res.NoticeRef
		}
		srvOut = append(srvOut, res.ServerOutput...)
		srvTrunc = srvTrunc || res.ServerOutputTruncated
		if res.Err != nil && flow.ContinueOnError() {
			failed = append(failed, model.Itoa(i+1))
			if firstErr == nil {
				firstErr = res.Err
			}
			continue
		}
		if res.Err != nil {
			ref := model.NewRuleRef(model.OutBatchFailed,
				"done", model.Itoa(i), "total", model.Itoa(total),
				"pos", model.Itoa(i+1), "err", res.Output)
			// 失败那一条自己的结局(execFailed / 超时 / 取消…)作为子项带上,界面按读者语言
			// 渲染;err 仍是规范中文串,给认不出子项的旧前端回落用。
			if res.OutputRef != nil {
				ref.Parts = []model.RuleRef{*res.OutputRef}
			}
			return gateway.ExecResult{
				Output:    fmt.Sprintf("· 已执行 %d/%d 条后失败 · 第 %d 条: %s", i, total, i+1, res.Output),
				OutputRef: ref,
				Rows: rows, Ms: ms, Err: res.Err,
				ServerOutput: srvOut, ServerOutputTruncated: srvTrunc,
			}
		}
		// 查询返回的行不是"受影响"的行:批里夹一条 SELECT,总数不该因此变大。
		if len(res.Columns) == 0 {
			rows += res.Rows
		}
		if res.Rows == 0 && len(res.Columns) == 0 && !res.Simulated && rowWritingVerb[gateway.ParseVerb(one)] {
			zero = append(zero, model.Itoa(i+1))
		}
		if flow.IsExit(one) {
			exitPos = i + 1
			break
		}
	}
	if exitPos > 0 { // EXIT 之后的语句没有执行,不算进"共几条"
		total = exitPos
	}
	output := fmt.Sprintf("批量执行完成 · 共 %d 条语句 · %d 行受影响", total, rows)
	ref := model.NewRuleRef(model.OutBatchDone, "n", model.Itoa(total), "rows", model.Itoa(rows))
	if text, note := flowNote(exitPos, failed); note != nil {
		// 流程被 WHENEVER / EXIT 改过:这句话比"几条影响 0 行"要紧,替换掉它
		output += " · " + text
		ref = model.NewRuleRef(model.OutBatchDoneFlow, "n", model.Itoa(total), "rows", model.Itoa(rows))
		ref.Parts = []model.RuleRef{*note}
	} else if len(zero) > 0 {
		shown := zero
		if len(shown) > 10 {
			shown = append(append([]string{}, shown[:10]...), "…")
		}
		list := strings.Join(shown, ", ")
		output += fmt.Sprintf(" · ⚠ 其中 %d 条影响 0 行:第 %s 条", len(zero), list)
		ref = model.NewRuleRef(model.OutBatchDoneZero, "n", model.Itoa(total), "rows", model.Itoa(rows),
			"z", model.Itoa(len(zero)), "zero", list)
	}
	return gateway.ExecResult{
		Output:    output,
		OutputRef: ref,
		Rows:      rows, Ms: ms,
		// 整批的性质跟着单条走:逐条都是模拟的,整批当然也没碰过任何数据库。
		Simulated:    simulated,
		ServerOutput: srvOut, ServerOutputTruncated: srvTrunc,
		Notice:       notice.Notice, NoticeRef: notice.NoticeRef,
		// WHENEVER SQLERROR CONTINUE 下有语句失败:整批跑完了,但审计要记 warn
		Err:   firstErr,
		Binds: flow.Binds(),
	}
}

// flowNote 是脚本流程被 WHENEVER / EXIT 改过时,结尾那句话的补充(没改过返回 nil)。
func flowNote(exitPos int, failed []string) (string, *model.RuleRef) {
	shown := failed
	if len(shown) > 10 {
		shown = append(append([]string{}, shown[:10]...), "…")
	}
	list := strings.Join(shown, ", ")
	switch {
	case exitPos > 0 && len(failed) > 0:
		return fmt.Sprintf("在第 %d 条 EXIT 处结束 · ⚠ WHENEVER SQLERROR CONTINUE 下 %d 条失败:第 %s 条", exitPos, len(failed), list),
			model.NewRuleRef(model.OutFlowExitedContinued, "pos", model.Itoa(exitPos), "f", model.Itoa(len(failed)), "failed", list)
	case exitPos > 0:
		return fmt.Sprintf("在第 %d 条 EXIT 处结束", exitPos), model.NewRuleRef(model.OutFlowExited, "pos", model.Itoa(exitPos))
	case len(failed) > 0:
		return fmt.Sprintf("⚠ WHENEVER SQLERROR CONTINUE 下 %d 条失败:第 %s 条", len(failed), list),
			model.NewRuleRef(model.OutFlowContinued, "f", model.Itoa(len(failed)), "failed", list)
	}
	return "", nil
}

// execResultStatus maps a run outcome to the audit result: a target-DB error is
// recorded as "warn", not "executed" (R19).
func execResultStatus(res gateway.ExecResult) string {
	if res.Err != nil {
		return model.ResultWarn
	}
	// 模拟结果没有接触任何数据库 —— 记成 executed 就是在审计链上写下一句假话。
	// 它也不是 warn:没有任何东西出错。见 model.ResultSimulated。
	if res.Simulated {
		return model.ResultSimulated
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
	if conn.Status == model.ConnStatusMaint {
		s.recordAudit(u, conn, "\\i "+filename, model.RiskLow, model.ResultWarn, "", "")
		return &dto.ExecResp{Risk: model.RiskLow, Output: maintNotice,
			OutputRef: model.NewRuleRef(model.OutMaintenance)}, nil
	}
	if err := s.checkMFA(u, conn, mfaCode); err != nil {
		return nil, err
	}
	scan, err := s.scanScriptFor(conn, filename, content)
	if err != nil {
		return nil, err // 分层解析不出来 —— 见 ScanScript,拒绝好过"一切正常"
	}
	// 每一条都按这个人在这台实例上的完整判定过一遍(judgeScan):原先只拿字典里最危险的
	// 那一条去问能力矩阵,被能力矩阵禁止、字典又没登记的语句就混在脚本里送审了。
	s.judgeScan(u, conn, scan)
	if scan.Blocked {
		s.recordAudit(u, conn, "\\i "+filename, model.RiskHigh, model.ResultRejected, "", "intercept")
		return nil, ErrForbidden
	}
	risk := model.RiskMid
	if scan.High > 0 {
		risk = model.RiskHigh
	}
	ref := model.NewRuleRef(model.RuleScriptHigh)
	rule := model.RenderRule(ref)
	av := gateway.Verdict{Action: gateway.ActionApprove, Risk: risk, Rule: rule, Ref: ref}

	// The ticket carries an excerpt and a digest, not the script. See script_ref.go
	// — the body would not fit in `command` on MySQL, and the file it came from is
	// already on disk. uploadID == 0 (a script that was never saved) keeps the old
	// behaviour of storing the body, which is the only thing available then —
	// ScriptExecute always records the paste as an upload now, so this fallback
	// only serves legacy callers, and it must refuse a body past the column bound
	// rather than let the INSERT die on "Data too long for column 'command'".
	label := "\\i " + filename + "\n" + strings.TrimSpace(content)
	if uploadID > 0 {
		label = scriptExcerpt(filename, content, scan.Statements, scan.High, scan.Mid)
	} else if len(label) > maxStoredSQLBytes {
		return nil, storedSQLTooLong(len(label))
	}
	ap, auditID, err := s.createApprovalForScript(u, conn, label, av, reason, uploadID, scriptDigest(content))
	if err != nil {
		return nil, err
	}
	s.recordAudit(u, conn, label, risk, model.ResultPending, ap.ApNo, "intercept")
	return &dto.ExecResp{Intercepted: true, ApprovalNo: ap.ApNo, AuditID: auditID, Risk: risk, Rule: rule, RuleRef: ref}, nil
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
	return s.createApprovalWith(u, conn, sql, v, reason, func(a *model.Approval) { a.ScriptUploadID, a.ScriptSHA256 = uploadID, sha })
}

// createApprovalWith 建一张审批单;mutate 在落库前补上这类工单自己的字段(脚本引用、在线变更任务、提示)。
func (s *Services) createApprovalWith(u *model.User, conn *model.Connection, sql string, v gateway.Verdict, reason string, mutate func(*model.Approval)) (*model.Approval, string, error) {
	apNo := s.nextApNo()
	auditID := s.nextAuditID()
	kw := approvalKeyword(sql)
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
		// effectiveDatabase 而不是 conn.Database:Oracle 的 Database 装的是 SERVICE
		// NAME,选中的 schema 在 TargetSchema 上。记成服务名的后果不是显示错了 ——
		// 批准之后执行会把它当 schema 去 ALTER SESSION SET CURRENT_SCHEMA,于是那条
		// CREATE TABLE 建到了一个不是你选的地方。
		Command: sql, Keyword: kw, Database: effectiveDatabase(conn), InitiatorID: u.ID, Initiator: u.Name,
		Reason: reason, RiskLevel: v.Risk, Status: model.StatusPending, AuditID: auditID,
	}
	if mutate != nil {
		mutate(ap)
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
	s.Webhook.SendLarkApproval(ap) // push an interactive Lark card to the approvers
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
	return s.DecideApprovalVia(actor, id, approve, viaConsole, "")
}

// DecideApprovalVia 是带渠道的 DecideApproval:站内与聊天卡片走的是同一套规则、同一个决定核心,
// 区别只在审计里看得出是从哪里点的。reason 只对拒绝有意义,通过时忽略。
func (s *Services) DecideApprovalVia(actor *model.User, id int64, approve bool, via, reason string) (*dto.ExecResp, error) {
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
	return s.finalizeApproval(ap, decision{Approve: approve, ActorID: actor.ID, ActorName: actor.Name, Via: via, Reason: reason})
}

// 决定的渠道(tbl_approval.decided_via)。聊天渠道就是 chat.Channel 的值(lark / telegram)。
const (
	viaConsole = "console"
	viaAPI     = "api"
)

// rejectReasonWidth 是 tbl_approval.reject_reason 的列宽。
const rejectReasonWidth = 512

// rejectReasonNotifMax 是拒绝原因在通知里最多占的字数。通知正文那一列也只有 512,前面还有姓名
// (≤64)、命令节选(≤81)和固定文字;放不下时 notify 写库失败、发起人什么都收不到。完整原因在审批页上。
const rejectReasonNotifMax = 300

// decision 是一个人对一张审批单的决定。
type decision struct {
	Approve   bool
	ActorID   int64  // 实际做决定的人:审批步骤记在他那一步上(Repo.DecideStepBy,工单 01 第 6 条)
	ActorName string // 给发起人的通知只写这个
	Via       string // console | api | lark | telegram
	Reason    string // 拒绝原因,可空
}

// auditOperator 是审计里的操作人。聊天渠道写「张伟 (Lark)」;站内与开放接口是既有口径,只写姓名。
func (d decision) auditOperator() string {
	switch ch := chat.Channel(d.Via); ch {
	case chat.Lark, chat.Telegram:
		return d.ActorName + " (" + ch.Name() + ")"
	}
	return d.ActorName
}

// rejectReason 是要落库的拒绝原因:去掉首尾空白、截到列宽。坏 UTF-8 与 NUL 一并去掉 —— 文字来自
// 聊天平台,MySQL 拒收前者、PG 拒收后者,而它和状态在同一条 UPDATE 里,写不进去就是这次决定失败。
func (d decision) rejectReason() string {
	s := strings.ReplaceAll(strings.ToValidUTF8(d.Reason, ""), "\x00", "")
	return clipColumn(strings.TrimSpace(s), rejectReasonWidth)
}

// finalizeApproval is the shared decision core: atomically claim pending →
// approved/rejected, then (approve) execute the fixed ap.Command on behalf of the
// initiator + audit + notify, or (reject) record the rejection. It is reused by
// the in-app decision path (DecideApproval) and the open-API release approval
// (DecideReleaseForClient, open_release_approve.go); authorization is the caller's responsibility — this
// function assumes the decision is already authorized. d.ActorName is who acted
// (shown in the initiator's notification); the audit is attributed to the
// initiator since the command runs on their behalf.
func (s *Services) finalizeApproval(ap *model.Approval, d decision) (*dto.ExecResp, error) {
	// 窗口单只认窗口此刻挂着的那一张:窗口改过(另起了新单)或删了,旧单就作废,
	// 批准它不能让改过之后的定义生效(2026-10 黑盒,工单 01 第 2、3 条)。顺手把它撤掉,
	// 别让它继续躺在待办里。
	if ap.WindowID > 0 && !s.windowTicketCurrent(ap) {
		s.cancelStaleWindowTicket(nil, ap, apimsg.Text("执行窗口已修改或删除,此单作废"))
		return nil, ErrAlreadyDecided
	}
	conn, _ := s.Repo.GetConnection(ap.ConnectionID)
	// 走 applyTargetDatabase,不要裸赋值:Oracle 上 conn.Database 是服务名,直接写
	// 进去等于把连接指向一个不存在的服务(TNS-12514),而工单里那个值本来是 schema。
	// 这条规则收敛成一个函数,正是因为分散的写法只会以"又有人新写一处"的方式复发。
	applyTargetDatabase(conn, ap.Database)
	initiator, _ := s.Repo.GetUserByID(ap.InitiatorID)
	if initiator == nil {
		// The initiator user was removed — keep audit attribution via the stored name.
		initiator = &model.User{ID: ap.InitiatorID, Name: ap.Initiator}
	}
	now := time.Now()
	if d.Approve {
		// Atomically claim the pending → approved transition. If we lose the race
		// (already decided/rejected/expired by a concurrent caller or the timeout
		// sweep), stop here so the command is never executed twice.
		claimed, cerr := s.Repo.ClaimApprovalDecision(ap.ID, model.StatusApproved, d.Via, "")
		if cerr != nil {
			return nil, cerr
		}
		if !claimed {
			return nil, ErrAlreadyDecided // someone else already decided/expired it
		}
		_ = s.Repo.DecideStepBy(ap.ID, d.ActorID, model.StatusApproved, now)
		// 通过不再执行任何命令,两条分支的区别只是**接下来由谁执行**:
		// 发布单归流水线,普通工单归发起人。
		var res gateway.ExecResult
		result, title := model.ResultPending, apimsg.T("审批已通过,请前往执行")
		if ap.WindowID > 0 {
			// 窗口单授权的是"让那扇门在它自己的时间表内生效",不是"执行一条命令"。
			// 批准即生效,没有什么可等发起人去做的。
			res.Output = "· 已批准,执行窗口已生效"
			res.OutputRef = model.NewRuleRef(model.OutApWindowLive)
			title = apimsg.T("执行窗口审批已通过")
			s.applyWindowDecision(ap, true)
		} else if ap.ExportJobID > 0 {
			// 导出单授权的是"让导出 worker 去跑这次导出",不是"执行一条命令" ——
			// 所以它不停在等发起人,批准即入队。
			res.Output = "· 已批准,导出任务已进入队列"
			res.OutputRef = model.NewRuleRef(model.OutApExportQueued)
			title = apimsg.T("导出审批已通过,任务已开始")
			s.releaseApprovedExport(ap)
		} else if ap.ReleaseID > 0 {
			// A release ticket authorises the pipeline; it does not run anything.
			// The execute stage owns execution (and re-judges the statement before
			// applying it), so running the command here as well would apply the same
			// change twice — the second time to a pipeline that still believes it
			// has not run. The audit row therefore stays `pending`: approved, not yet
			// executed, with the execution audited by the stage that performs it.
			res.Output = "· 已批准,由发布流水线继续执行"
			res.OutputRef = model.NewRuleRef(model.OutApPipeline)
			title = apimsg.T("审批已通过,发布流水线继续")
		} else {
			// 通过**不再顺带执行**。命令在审批人点下去的那一刻跑,等于让审批人替
			// 发起人选择了执行时机 —— 而业务低峰、应用是否已停、备份是否就绪,
			// 只有发起人知道。审批人按下的是"我同意",不是"现在就跑"。
			//
			// 工单就停在这里等发起人来执行(ExecuteApproved)。审计行同样记 pending:
			// 已批准、尚未执行,真正的执行由执行它的那一刻自己写审计。
			res.Output = model.AwaitingExecution
			res.OutputRef = model.NewRuleRef(model.OutApAwaitingExec)
		}
		_ = s.Repo.SetApprovalResult(ap.ID, res.Output, res.OutputRef, approvalRows(res), now)
		// 动作记 approve 而不是 exec:这一刻发生的事情是一次审批决定,命令一行都
		// 没有跑。记成 exec 会让审计里出现一条查不到对应库变更的"执行",也会让只
		// 订阅 exec 的 webhook 在什么都没执行时收到通知。真正的执行由
		// ExecuteApproved 自己写一条 exec —— 那条才对得上库里的变化。
		s.recordAuditFrom(approvalAuditSource(ap), initiator, d.auditOperator(), conn, ap.Command, ap.RiskLevel, result, ap.ApNo, "approve")
		// 话要说清"还需要你去执行" —— 一句"已通过"会让人以为事情办完了,然后那条
		// 命令就一直挂在那里,直到有人发现变更根本没生效。
		body := apimsg.Sprintf("%s 通过了你的命令：%s\n请到「审批」页找到这张工单并执行。",
			d.ActorName, safeClip(ap.Command, 60))
		if ap.WindowID > 0 {
			body = apimsg.Sprintf("%s 通过了你的执行窗口申请：%s\n窗口已生效,到点自动开启、到点自动关闭。",
				d.ActorName, safeClip(ap.Command, 60))
		} else if ap.ExportJobID > 0 {
			body = apimsg.Sprintf("%s 通过了你的导出申请：%s\n任务已进入队列,完成后会再通知你。",
				d.ActorName, safeClip(ap.Command, 60))
		} else if ap.ReleaseID > 0 {
			body = apimsg.Sprintf("%s 通过了发布单里的这一步：%s\n流水线将继续执行,无需手动操作。",
				d.ActorName, safeClip(ap.Command, 60))
		}
		s.notify(ap.InitiatorID, model.NotifApprovalApproved, title, body, ap.ApNo)
		return &dto.ExecResp{Risk: ap.RiskLevel, Output: res.Output, OutputRef: res.OutputRef, Rows: res.Rows, Ms: res.Ms,
			Columns: res.Columns, Data: res.Data, Truncated: res.Truncated}, nil
	}
	reason := d.rejectReason()
	// Same atomic guard on the reject path.
	claimed, cerr := s.Repo.ClaimApprovalDecision(ap.ID, model.StatusRejected, d.Via, reason)
	if cerr != nil {
		return nil, cerr
	}
	if !claimed {
		return nil, ErrAlreadyDecided // someone else already decided/expired it
	}
	_ = s.Repo.DecideStepBy(ap.ID, d.ActorID, model.StatusRejected, now)
	_ = s.Repo.SetApprovalResult(ap.ID, "", nil, 0, now)
	if ap.WindowID > 0 {
		// 驳回的窗口永久留在 rejected:判定层不认它,但列表里仍然看得见它被驳回过。
		s.applyWindowDecision(ap, false)
	}
	s.abortAwaitingOSC(ap.OSCJobID, "工单被驳回,未执行")
	s.recordAuditFrom(approvalAuditSource(ap), initiator, d.auditOperator(), conn, ap.Command, ap.RiskLevel, model.ResultRejected, ap.ApNo, "approve")
	body := apimsg.Sprintf("%s 驳回了你的命令：%s", d.ActorName, safeClip(ap.Command, 80))
	if reason != "" {
		body = apimsg.Sprintf("%s 驳回了你的命令：%s\n原因：%s",
			d.ActorName, safeClip(ap.Command, 80), clip(reason, rejectReasonNotifMax))
	}
	s.notify(ap.InitiatorID, model.NotifApprovalRejected, apimsg.T("审批被拒绝"), body, ap.ApNo)
	return nil, nil
}

// notify records an in-app message for a user (no-op for an unknown user).
//
// 标题是固定句(读出时按原句查表翻译);正文常带参数,ref 随行存进 body_ref(迁移 0060)。
func (s *Services) notify(userID int64, typ string, title, body apimsg.M, refNo string) {
	if userID == 0 {
		return
	}
	_ = s.Repo.CreateNotification(&model.Notification{
		UserID: userID, Type: typ, Title: title.S, Body: body.S, BodyRef: apimsg.RefJSON(body.Ref), RefNo: refNo,
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

// clipColumn is clip for a value going into a column of width n: the ellipsis
// counts toward the width. clip(s, n) can return n+1 runes, and one rune over is
// Error 1406 on a strict-mode MySQL — for the metadata sync, that aborts the whole
// run on every sweep because of one long ENUM.
func clipColumn(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return clip(s, n-1)
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

// InspectionSavePath 是 AI 巡检报告的保存目录(系统设置 inspection.savePath)。没配置时返回 "",
// 上传接口据此拒绝 —— 报告要永久保留,不能悄悄落到程序运行目录下(.scratch/inspection-report)。
func (s *Services) InspectionSavePath() string {
	return strings.TrimSpace(s.settingString("inspection.savePath", ""))
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
	// sanitizeFilename 走的是 filepath.Base,带分隔符的穿越已经没了;但字面的 "." 和
	// ".." 过得去,而 Join(savePath, "..") 会把整个上传目录挪到上一级(L-16)。用户名
	// 由管理员设定,所以这是一条"误配置"而非"攻击"路径 —— 代价仍然是文件落在预期之外。
	if name == "" || name == "." || name == ".." {
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
	// 被审批单 / 发布单引用的不许删:它们只存摘录与摘要,原件是"当时到底执行了什么"的证据
	// (2026-10 安全审计 M5)。
	if s.Repo.ScriptUploadReferenced(id) {
		return ErrScriptReferenced
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

// ScanScript scans a .sql script statement-by-statement (dictionary + strict).
//
// 判定用的是**目标实例所属分层**的字典,而不是某个固定的基准分层。
//
// 从前是后者(哪个分层挂着 ScanBaseline 就用哪个,发行默认是 prod),理由是"扫描发生
// 在上传时,目标未必已经选定;按 dev 宽松地判,同一个文件转头就能带着'已扫干净'去
// prod"。但实际的接口里,扫描和执行拿的是同一个 connectionId —— 目标一直是知道的,
// 而执行时每条语句本来就按目标分层重判一次(见 ExecuteSafeScript → execJudged)。
// 于是那道固定镜头没有拦住任何东西,只是让报告说的和将要发生的事对不上:一个只准备
// 在 UAT 跑的菜单初始化脚本,被按 prod 的尺子判成高危 P1。
//
// 扫描报告的职责是描述"这个脚本在这台实例上会怎样",所以它必须和执行用同一把尺子。
//
// 解析不出目标分层就报错,调用方必须把错误透出去:拿空分层去扫,字典一行都匹配不到,
// 每条语句(包括 DROP TABLE)都会报安全,而且没有任何地方会失败(ED3,与
// unavailableVerdict 同一立场)。
func (s *Services) ScanScript(connID int64, filename, content string) (*dto.ScriptScanResp, error) {
	conn, err := s.Repo.GetConnection(connID)
	if err != nil {
		return nil, ErrNotFound
	}
	return s.scanScriptFor(conn, filename, content)
}

// scanScriptFor is ScanScript for callers that already hold the connection.
func (s *Services) scanScriptFor(conn *model.Connection, filename, content string, prog ...*scriptProgress) (*dto.ScriptScanResp, error) {
	var p *scriptProgress
	if len(prog) > 0 {
		p = prog[0]
	}
	tier, err := s.tierCodeOf(conn)
	if err != nil {
		return nil, fmt.Errorf("unresolvable tier for %s: %w", conn.Name, err)
	}
	stmts := splitStatements(content)
	out := &dto.ScriptScanResp{Filename: filename, Statements: []dto.ScannedStmt{}}
	p.phase("scan", len(stmts))
	for i, sql := range stmts {
		p.at(i+1, sql)
		cmd, risk, noWhere := s.Engine.ScanStatementFor(conn.Engine, tier, sql)
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
	p.done(len(stmts))
	out.Total = len(out.Statements)
	out.HasRisky = out.High+out.Mid > 0
	return out, nil
}

// ScanScriptFor is ScanScript judged for THIS user on THIS instance: every
// statement also goes through the full judgement the terminal applies (capability
// matrix + dictionary + strict mode + execution window). See judgeScan.
func (s *Services) ScanScriptFor(u *model.User, connID int64, filename, content, database string) (*dto.ScriptScanResp, error) {
	return s.ScanScriptForCtx(context.Background(), u, connID, filename, content, database)
}

// ScanScriptForCtx 同 ScanScriptFor;ctx 上挂着进度记录(WithScriptProgress)时逐条记进度。
func (s *Services) ScanScriptForCtx(ctx context.Context, u *model.User, connID int64, filename, content, database string) (*dto.ScriptScanResp, error) {
	conn, err := s.Repo.GetConnection(connID)
	if err != nil {
		return nil, ErrNotFound
	}
	// 与执行、终端同口径校验实例访问范围:扫描报告按这台实例的分层与这个人的权限逐条判定,
	// 范围外的实例连"会怎样"也不该告诉他(2026-10 黑盒测试)。
	if !s.canAccessConn(u, conn) {
		return nil, ErrForbidden
	}
	applyTargetDatabase(conn, database) // 执行窗口按库开:库不同,判定可能不同
	prog := scriptProgressFrom(ctx)
	scan, err := s.scanScriptFor(conn, filename, content, prog)
	if err != nil {
		return nil, err
	}
	s.judgeScan(u, conn, scan, prog)
	return scan, nil
}

// judgeScan overlays the full per-user judgement on a dictionary scan.
//
// The dictionary scan alone decided whether a script went to approval, and it is
// role-independent: a statement only the capability matrix gates — CREATE TABLE on
// production, typically — scanned "safe". The script then ran as a safe script,
// statement by statement; the first gated statement raised a ticket FOR ITSELF and
// stopped the run, so the approver signed one CREATE TABLE while the rest of the
// DDL was neither run nor submitted (2026-10). One statement needing approval now
// sends the whole script; one denied statement refuses the whole script.
func (s *Services) judgeScan(u *model.User, conn *model.Connection, scan *dto.ScriptScanResp, prog ...*scriptProgress) {
	var p *scriptProgress
	if len(prog) > 0 {
		p = prog[0]
	}
	tier, err := s.tierCodeOf(conn)
	if err != nil {
		m := apimsg.T("实例的分层标签解析失败")
		scan.Blocked, scan.BlockedRule, scan.BlockedRuleRef = true, m.S, m.Ref
		return
	}
	roleIDs := s.Repo.EffectiveRoleIDs(u)
	now := time.Now()
	needsApproval := false
	p.phase("judge", len(scan.Statements))
	for i := range scan.Statements {
		st := &scan.Statements[i]
		p.at(i+1, st.SQL)
		v, win := s.relaxByWindow(conn, []string{st.SQL}, s.Engine.EvaluateFor(roleIDs, conn.Engine, tier, st.SQL), now)
		st.Action = v.Action
		if win != nil { // 本该审批,窗口放行:报告里写明是哪个窗口
			st.Rule = v.Rule
			scan.Window = win.Name
		}
		if v.Action == gateway.ActionApprove {
			needsApproval = true
		}
		switch v.Action {
		case gateway.ActionDeny:
			st.Rule = v.Rule
			if !scan.Blocked {
				scan.Blocked = true
				m := apimsg.Sprintf("第 %d 条:%s", st.Index, apimsg.M{S: v.Rule, Ref: v.Ref})
				scan.BlockedRule, scan.BlockedRuleRef = m.S, m.Ref
			}
		case gateway.ActionApprove:
			st.Rule = v.Rule
			if st.Risk == "safe" || st.Risk == "" { // 字典没拦、能力矩阵要审批:报告里也得显示"需审批"
				st.Risk = "mid"
				scan.Safe--
				scan.Mid++
			}
		}
	}
	// 送不送审看**最终裁决**,不看风险等级。窗口放宽不降风险等级(高危还是高危),从前这里按
	// High+Mid 算,班车时段里含字典高危语句的脚本照样整份送审(2026-10 生产 AP-2554:
	// "高危 4 · 需审批 0",窗口 AP-2552 生效中)。
	p.done(len(scan.Statements))
	scan.HasRisky = needsApproval
}

// ExecuteSafeScript runs each statement of an already-scanned all-safe script
// through the full gateway flow (judge → execute → audit), so direct script
// execution leaves the same per-statement audit trail as terminal commands
// (FR-AUDIT / US#32-33). Returns the count of executed statements.
func (s *Services) ExecuteSafeScript(u *model.User, connID int64, content, mfaCode, database string) (int, error) {
	res, err := s.ExecuteSafeScriptCtx(context.Background(), u, connID, content, mfaCode, database)
	return countExecuted(res), err
}

// countExecuted:结果里真正执行成功的条数(失败的那一条也在结果里,不算)。
func countExecuted(res []dto.ScriptStmtResult) int {
	n := 0
	for _, r := range res {
		if !r.Failed {
			n++
		}
	}
	return n
}

// scriptSQLPreview:结果里认出是哪一条用的语句开头 —— 第一个不是纯注释的非空行,截到 120 个字。
// 脚本里语句前面常有一行说明注释(-- 查一下),拿它当开头就认不出是哪条语句了。
func scriptSQLPreview(sql string) string {
	for _, l := range strings.Split(sql, "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "--") || strings.HasPrefix(l, "#") || (strings.HasPrefix(l, "/*") && strings.HasSuffix(l, "*/")) {
			continue
		}
		if l != "" {
			if r := []rune(l); len(r) > 120 {
				return string(r[:120]) + "…"
			}
			return l
		}
	}
	return ""
}

// ExecuteSafeScriptCtx 同 ExecuteSafeScript;ctx 带着终端标签时,脚本在这个标签独占的连接上跑。
// 返回每一条的执行结果(终端逐条打印);停下来时,已执行的与失败的那一条都在结果里。
func (s *Services) ExecuteSafeScriptCtx(ctx context.Context, u *model.User, connID int64, content, mfaCode, database string) ([]dto.ScriptStmtResult, error) {
	conn, err := s.Repo.GetConnection(connID)
	if err != nil {
		return nil, ErrNotFound
	}
	applyTargetDatabase(conn, database)
	if !s.canAccessConn(u, conn) {
		return nil, ErrForbidden
	}
	if conn.Status == model.ConnStatusMaint {
		return nil, nil // maintenance: nothing executed (mirrors Exec's soft no-op)
	}
	// Validate the PROD step-up ONCE for the whole script — a single TOTP code
	// covers the batch; per-statement checks would demand (and consume) a code on
	// every line and always fail for MFA users (R18).
	if err := s.checkMFA(u, conn, mfaCode); err != nil {
		return nil, err
	}
	// 脚本里的查询不分批:每条只带回一屏(gateway 的 maxResultRows),不往「加载更多」的暂存里放
	ctx = gateway.WithoutResultPaging(ctx)
	var results []dto.ScriptStmtResult
	executed := 0
	// 每条交下去的是保留了可执行注释标记的文本(与终端里直接敲这一条完全一样):剥掉标记,
	// TiDB 建库脚本里的 `/*T![auto_rand] AUTO_RANDOM(5) */` 就丢了(2026-10)。判定入口自己会
	// 再分句、剥注释并保留注释体,所以判的仍是服务器会执行的内容。
	stmts := sqlutil.SplitStatementsExec(content)
	prog := scriptProgressFrom(ctx) // 上传脚本弹窗的执行进度(script_progress.go)
	prog.phase("run", len(stmts))
	flow := gateway.NewScriptFlowCtx(ctx, conn.Engine) // WHENEVER / EXIT / VARIABLE(SQL*Plus 脚本)
	for i, sql := range stmts {
		prog.at(i+1, sql)
		flow.Observe(sql)
		resp, err := s.execJudged(flow.Context(ctx), u, conn, sql, "脚本安全语句直接执行")
		if err != nil {
			return results, err
		}
		if resp != nil {
			flow.Update(resp.Binds)
		}
		if resp != nil && !resp.Intercepted {
			results = append(results, dto.ScriptStmtResult{Pos: i + 1, SQL: scriptSQLPreview(sql), ExecResp: *resp})
		}
		// 脚本写了 WHENEVER SQLERROR CONTINUE:这一条的失败已经在结果里(标红),接着跑下一条
		if resp != nil && resp.Failed && flow.ContinueOnError() {
			continue
		}
		// 目标库拒绝了这一条(含 Oracle 建包编译失败):停在这里并说清是第几条。从前不看结果,
		// 失败的语句照样计数,脚本报"执行成功 · N 条语句",库里的对象却是坏的。
		if resp != nil && resp.Failed {
			return results, &ScriptStmtError{Done: executed, Total: len(stmts), Pos: i + 1, Msg: strings.TrimSpace(strings.TrimPrefix(resp.Output, "·"))}
		}
		if flow.IsExit(sql) { // EXIT / QUIT:脚本在这里正常结束
			executed++
			prog.done(executed)
			break
		}
		// A statement the capability matrix routes to approval was NOT executed —
		// don't count it, and stop so the caller learns the batch is incomplete.
		// (The whole-script scan uses a PROD lens; per-connection capability can
		// still intercept, so this can legitimately happen.)
		if resp != nil && resp.Intercepted {
			return results, ErrBadRequest
		}
		executed++
		prog.done(executed)
	}
	return results, nil
}


// ScriptStmtError:脚本执行到某一条时目标库报错,后面的没有执行。
type ScriptStmtError struct {
	Done, Total, Pos int
	Msg              string
}

func (e *ScriptStmtError) Error() string { return e.err().Error() }

// MsgRef 让界面翻译这句话;目标库的报错原文(Msg)原样嵌在里面。
func (e *ScriptStmtError) MsgRef() *model.RuleRef { return apimsg.RefOf(e.err()) }

func (e *ScriptStmtError) err() error {
	return apimsg.Errorf("已执行 %d/%d 条后失败 · 第 %d 条: %s", e.Done, e.Total, e.Pos, e.Msg)
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
	var escalated []model.Approval
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
			s.abortAwaitingOSC(a.OSCJobID, "工单超时作废,未执行")
			s.recordAuditSrc(approvalAuditSource(&a), initiator, conn, a.Command, a.RiskLevel, model.ResultRejected, a.ApNo, "approve")
			s.notify(a.InitiatorID, model.NotifApprovalExpired, apimsg.T("审批已超时作废"),
				apimsg.Sprintf("超过时限未审批，已自动作废：%s", safeClip(a.Command, 80)), a.ApNo)
		case "auto-escalate":
			// Keep pending but call the default approvers in, ONCE — claim the
			// escalation atomically so repeated sweeps don't re-audit/re-notify the
			// same ticket every 60s (R13).
			if claimed, _ := s.Repo.ClaimEscalation(a.ID); !claimed {
				continue
			}
			s.recordAuditSrc(approvalAuditSource(&a), initiator, conn, a.Command, a.RiskLevel, model.ResultWarn, a.ApNo, "approve")
			s.notifyEscalation(&a)
			escalated = append(escalated, a)
		}
	}
	s.Webhook.SendLarkEscalations(escalated)
}

// notifyEscalation 告诉默认审批人有一张单超时没人处理。
//
// 正文不带语句:发布单可以用别的审批链,默认审批人不一定在那张单的链上,也就不一定看得到它。
func (s *Services) notifyEscalation(a *model.Approval) {
	for _, u := range s.approverPool() {
		s.notify(u.ID, model.NotifApprovalEscalated, apimsg.T("审批超时未处理"),
			apimsg.Sprintf("%s 在 %s 上发起的审批超过时限仍未处理，请跟进", a.Initiator, a.Instance), a.ApNo)
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
	// 失败次数限制(mfa_guard.go):锁定期间不再校验
	if s.mfaFails.locked(u.ID, time.Now()) {
		return ErrMFALocked
	}
	// 没带码不算输错:前端每条生产命令第一次都不带码发,收到 42800 才弹输码框。从前(d4a7e36 起)
	// 这一下也记一次失败,每弹一次框先吃掉一次额度,5 次就锁 15 分钟、封住所有生产实例(2026-10-26)。
	if strings.TrimSpace(code) == "" {
		return ErrMFARequired
	}
	if !s.validateTOTP(u, code) {
		return s.mfaFailed(u, "生产操作步进验证", ErrMFARequired)
	}
	s.mfaFails.succeed(u.ID)
	s.noteMFAVerified(u, conn)
	return nil
}

// validateTOTP verifies a TOTP code for an enrolled user and consumes its counter.
// Anti-replay: a given code (time-step) may be consumed only once, so a captured /
// logged code replayed within its ~90s window is rejected (M3). Shared by the PROD
// step-up (checkMFA); login consumes the same counter in Login.
func (s *Services) validateTOTP(u *model.User, code string) bool {
	ok, counter := totp.ValidateWithCounter(u.MFASecret, code, time.Now())
	return ok && s.Repo.ConsumeMFACounter(u.ID, int64(counter))
}

// ssoFreshForMFA:SSO 登录后多长时间内,绑 MFA 可以不出示本地口令(2026-10-26 回归 03)。
const ssoFreshForMFA = 15 * time.Minute

// ErrSSOReauthRequired:SSO 会话已超过 ssoFreshForMFA,又没带本地口令 —— 请重新经 SSO 登录。
var ErrSSOReauthRequired = apimsg.Errorf("%w: SSO 登录已超过 15 分钟,请重新通过 SSO 登录后再绑定二次验证(或输入当前登录密码)", ErrBadRequest)

// ssoSessionFresh:这一个会话是经 SSO 登录签发的,且登录不超过 ssoFreshForMFA。登录时刻取
// token 的 iat;读不到登录方式(老 token、本地口令登录、改密后重签)一律 false。
func ssoSessionFresh(u *model.User, now time.Time) bool {
	if u == nil || u.SessionAuth != jwt.AuthMethodSSO || u.SessionLoginAt.IsZero() {
		return false
	}
	age := now.Sub(u.SessionLoginAt)
	return age >= -time.Minute && age <= ssoFreshForMFA
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
// password:当前登录密码。有本地口令的账号必须出示(2026-10 安全审计 L-AUTH4):拿到别人会话
// 的人给还没绑定的账号绑上自己的验证器,此后连口令带码都在他手里,本人反而被挡在外面。没有本地
// 口令的账号(只经 SSO 登录)无从出示,照旧。
func (s *Services) MFASetup(u *model.User, password string) (*dto.MFASetupResp, error) {
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
	// 2026-10-26 回归 03:只经 SSO 登录的人往往不知道管理员开号时设的本地口令,于是绑不上
	// MFA。这一次会话是 SSO 登录、且登录不超过 ssoFreshForMFA 时,刚经 SSO 认证过就算出示过
	// 身份,不再要本地口令;超时的 SSO 会话不带口令时回 ErrSSOReauthRequired(请重新 SSO 登录)。
	// 带了口令仍按口令校验。老 token 没有登录方式,按非 SSO 会话处理。
	ssoFresh := ssoSessionFresh(u, time.Now())
	if u.PasswordHash != "" && !ssoFresh {
		if password == "" {
			if u.SessionAuth == jwt.AuthMethodSSO {
				return nil, ErrSSOReauthRequired
			}
			return nil, ErrCurrentPasswordRequired
		}
		if s.mfaFails.locked(u.ID, time.Now()) {
			return nil, ErrMFALocked
		}
		if !crypto.CheckPassword(u.PasswordHash, password) {
			s.auditAdminAction(u, "mfa.setup.rejected 领取 MFA 密钥被拒:当前密码错误")
			return nil, s.mfaFailed(u, "领取 MFA 密钥", ErrCurrentPasswordWrong)
		}
	}
	secret := u.MFASecret
	if secret == "" {
		secret = totp.GenerateSecret()
	}
	if err := s.Repo.UpdateUserMFA(u.ID, false, secret); err != nil {
		return nil, err
	}
	// 安全因子的每一次变更都要留痕。管理员侧的 mfa.reset / mfa.bind 一直有审计,自助
	// 这三个一条都没有(M-8)——而"谁把自己的第二因子换掉了、什么时候换的",事后正是
	// 要从审计链里读出来的。密钥本身当然不进去:审计行是给人看的,也是外发的。
	if u.PasswordHash != "" && ssoFresh {
		s.auditAdminAction(u, "mfa.setup 自助领取新的 MFA 密钥(尚未启用;本次会话刚经 SSO 登录,免出示本地口令)")
	} else {
		s.auditAdminAction(u, "mfa.setup 自助领取新的 MFA 密钥(尚未启用)")
	}
	// 这里**不**提升会话代次。
	//
	// 审计建议里有这一条,但试过之后发现它是错的:自助改自己的第二因子,操作者就是
	// 这个会话本人,而他刚刚用一个有效的码证明了持有它 —— 把他自己踢下线,只是在
	// 流程中间插一次重新登录,换不来任何防护。
	//
	// 真正能挡住"盗 token 者给未启用 MFA 的账号装上自己的因子"的,是**要求携带当前
	// 口令**(审计建议里的第一条)。那要前端一起改(多传一个字段),属接口变更,留作
	// 后续项 —— 见白盒审计报告第八节 M-8。
	return &dto.MFASetupResp{Secret: secret, OtpauthURI: totp.URI(secret, u.Email, s.Brand().Text(BrandDefaultUI, true))}, nil
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
	s.auditAdminAction(u, "mfa.enable 自助启用 MFA")
	return nil
}

// MFADisable turns MFA off after verifying a current, not-yet-used code, clearing
// the secret. Consuming the counter blocks replay of a code already spent on a
// PROD step-up (R15).
func (s *Services) MFADisable(u *model.User, code string) error {
	if !u.MFAEnabled {
		return nil
	}
	if err := s.spendMFACode(u, code, "关闭 MFA"); err != nil {
		return err
	}
	if err := s.Repo.UpdateUserMFA(u.ID, false, ""); err != nil {
		return err
	}
	// 关掉第二因子是**降低**这个账号的保护等级,比开启它更该留痕(M-8)。
	s.auditAdminAction(u, "mfa.disable 自助停用 MFA(该账号此后仅凭口令登录)")
	return nil
}

// spendMFACode 核对并消费一个当前的 TOTP 码:同一个码只能用一次(R15)。what 是这一步的名字,
// 触发锁定时写进审计。调用方自己先判这个账号启没启用 MFA。
func (s *Services) spendMFACode(u *model.User, code, what string) error {
	// 失败次数限制(mfa_guard.go):锁定期间任何码都不认
	if s.mfaFails.locked(u.ID, time.Now()) {
		return ErrMFALocked
	}
	if strings.TrimSpace(code) == "" {
		return ErrMFARequired // 没带码不算输错(同 checkMFA)
	}
	ok, counter := totp.ValidateWithCounter(u.MFASecret, code, time.Now())
	if !ok {
		return s.mfaFailed(u, what, ErrMFARequired)
	}
	if !s.Repo.ConsumeMFACounter(u.ID, int64(counter)) {
		return ErrMFARequired // code already used (e.g. for a step-up)
	}
	s.mfaFails.succeed(u.ID)
	return nil
}

// ---- admin user management (password + OTP binding on behalf of a user) ----

// AdminSetPassword resets a user's password (bcrypt), no old password needed.
func (s *Services) AdminSetPassword(actor *model.User, id int64, newPassword string) error {
	if err := passwordPolicyInput(newPassword); err != nil { // keep in sync with the frontend savePw check (C4)
		return err
	}
	u, err := s.Repo.GetUserByID(id)
	if err != nil {
		return ErrNotFound
	}
	// A service account has no console door, so a password on it is a key to a
	// door that must stay locked. Login refuses kind=service regardless, but a
	// hash that exists is a hash that can leak — refuse to mint one at all.
	if u.Kind == model.UserKindService {
		return invalidf("服务账号不能设置登录口令,它只通过 API 凭据访问")
	}
	if s.bizQueryOnly(u) {
		return ErrBizNoLocalPassword
	}
	hash, err := crypto.HashPassword(newPassword)
	if err != nil {
		return err
	}
	if err := s.Repo.UpdateUserPassword(u.ID, hash); err != nil {
		return err
	}
	// A password reset must invalidate the user's existing sessions (M1).
	err = s.revokeSessionsAndChat(actor, u.ID, apimsg.Text("管理员重置了登录口令"))
	s.auditAdminAction(actor, "admin.password.reset user="+u.Email)
	return err
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
	// 解绑多半是因为手机丢了:丢的那台设备上开着的会话不能继续活着,与改口令同理(M1)。
	err = s.revokeSessionsAndChat(actor, u.ID, apimsg.Text("管理员重置了 MFA"))
	s.auditAdminAction(actor, "admin.mfa.reset user="+u.Email)
	return err
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
	// 这里不作废会话(一直如此):旧会话此后要绑聊天账号,得先过这个新因子,它拿不到。
	s.autoUnbindChat(actor, u.ID, apimsg.Text("管理员重新绑定了 MFA"))
	s.auditAdminAction(actor, "admin.mfa.bind user="+u.Email)
	return &dto.MFASetupResp{Secret: secret, OtpauthURI: totp.URI(secret, u.Email, s.Brand().Text(BrandDefaultUI, true))}, nil
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

// 审批执行时限的默认值与上限(秒)。
const (
	defaultApprovalExecTimeout = 3600
	maxApprovalExecTimeout     = 86400
)

// approvalExecTimeout 是**审批单执行**每条语句的时限,设置项 gateway.approvalExecTimeout(秒),
// 默认 1 小时,范围 [1s, 24h]。
//
// 与终端的 execTimeout(默认 30 秒)分开:终端是交互式的,一条查询卡 30 秒就该停;审批单里是
// 批过的变更,常是在大表上跑几分钟到几小时的 DDL —— 用终端那 30 秒,ALTER 跑到一半就被取消,
// 而取消本身(MySQL 是 KILL QUERY)也要时间回滚。老库里没有这一项时按默认值,不必迁移。
func (s *Services) approvalExecTimeout() time.Duration {
	sec := s.settingInt("gateway.approvalExecTimeout", defaultApprovalExecTimeout)
	if sec < 1 {
		sec = 1
	}
	if sec > maxApprovalExecTimeout {
		sec = maxApprovalExecTimeout
	}
	return time.Duration(sec) * time.Second
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

// splitStatements splits a script into statements (quote/comment-aware so a
// literal like 'a;b' isn't cut in two). Each statement is exactly the text that
// will be judged, audited and executed.
//
// 这里从前还把每条语句的空白压成一个空格。对普通语句无害(切分器已经剥掉了注释),
// 对整块取出的 PL/SQL 单元是致命的:包体是**带着注释**原样取出的,换行一没,第一个
// `--` 就把后面整个包体注释掉。Oracle 对 CREATE OR REPLACE 的编译错误只是留下一个
// INVALID 对象,于是脚本报"执行成功",库里的包却全是坏的(PLS-00103 end-of-file,
// 位置全在第 1 行)。换行是语句的一部分,不许动。
func splitStatements(text string) []string {
	return sqlutil.SplitStatements(text)
}

func firstWord(s string) string {
	f := strings.Fields(strings.TrimSpace(s))
	if len(f) == 0 {
		return ""
	}
	return strings.ToUpper(f[0])
}

// approvalKeywordWidth 是 tbl_approval.keyword 的列宽(model.Approval.Keyword size:32)。
const approvalKeywordWidth = 32

// approvalKeyword 取审批单的「关键字」:去掉注释后的第一个词，截到列宽。
//
// 从前直接 firstWord(sql):脚本以一行 `--==========…` 分隔线或一句不带空格的中文
// 注释开头时，第一个"词"就是那整行注释，超过 32 字符，严格模式的 MySQL 报 Error 1406,
// 审批单建不出来 —— 发布单停在「人工审批」失败(2026-11)。关键字只用于展示，不参与
// 任何判定，所以超长时截断即可，不值得让整张单失败。
//
// 只看开头 64KB:发布单的脚本可以有几 MB,关键字总在最前面。
func approvalKeyword(sql string) string {
	const head = 64 << 10
	if len(sql) > head {
		sql = strings.ToValidUTF8(sql[:head], "")
	}
	body := strings.TrimSpace(gateway.StripComments(sql))
	// MySQL 的 # 行注释 StripComments 不管(它在 PG 里是运算符);只在开头跳过
	for strings.HasPrefix(body, "#") {
		i := strings.IndexByte(body, '\n')
		if i < 0 {
			body = ""
			break
		}
		body = strings.TrimSpace(body[i+1:])
	}
	kw := firstWord(body)
	if kw == "" { // 只有注释:退回原来的取法，总比空着强
		kw = firstWord(sql)
	}
	return clipColumn(strings.ToValidUTF8(kw, ""), approvalKeywordWidth)
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
