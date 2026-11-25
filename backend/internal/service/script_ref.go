package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"velagateway/internal/apimsg"
	"velagateway/internal/dto"
	"velagateway/internal/gateway"
	"velagateway/internal/model"
	"velagateway/pkg/sqlutil"
)

// Script approvals that reference their uploaded file instead of carrying it.
//
// The script is already on the gateway's disk — the initiator uploaded it — so
// shipping the body back through the request, into tbl_approval.command and
// through every approvals-list page was work nobody needed. `command` is TEXT
// (65,535 bytes on MySQL), so a real migration simply could not be submitted.
//
// What replaces it is a reference plus a digest, and the digest is the part that
// matters. Reviewing a file and executing a file are separated by however long
// approval takes, and in between the file on disk can be replaced. Recording what
// was reviewed is the only way the gateway can tell, at execution time, that it
// is about to run the same bytes.

// scriptExcerptLines is how much of the script the approval carries verbatim.
// Enough for an approver to recognise what they are looking at; small enough that
// the ticket stays a ticket. The full file is one click away and the digest ties
// the two together.
const scriptExcerptLines = 40

// scriptExcerptLineBytes bounds ONE excerpt line. Truncating by line count
// alone left the excerpt unbounded for long LINES — a generated single-line
// statement (an 80KB IN-list) rode into the ticket whole, which is exactly the
// oversized-command failure the excerpt exists to prevent.
const scriptExcerptLineBytes = 300

// maxScriptBytes bounds what the gateway will read off disk and judge in one
// request. Not a storage limit — the reference model removed that — but a limit
// on the work a single submission can demand: every statement is split, scanned
// against the dictionary and judged, and that is linear in the file.
const maxScriptBytes = 32 << 20 // 32MB

// ErrScriptChanged is returned when the uploaded file no longer hashes to what
// was scanned and approved.
var ErrScriptChanged = apimsg.Errorf("脚本文件在审批期间被修改,内容与评审时不一致,已拒绝执行")

// ErrScriptTooLarge is returned when a script exceeds maxScriptBytes.
var ErrScriptTooLarge = apimsg.Errorf("脚本过大,超出单次提交上限")

func scriptDigest(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// readUploadedScript reads an uploaded script by id, WITHOUT checking who is
// asking.
//
// Callers must establish the right to it themselves. There are two, and neither
// takes the id from the caller: submission resolves it through
// ScriptUploadFile (which does check ownership), and execution takes it from the
// approval row the gateway wrote. A path never comes from a request — an
// endpoint that read a client-supplied path would be an arbitrary file read
// dressed up as a feature.
func (s *Services) readUploadedScript(uploadID int64) (content, filename string, err error) {
	up, err := s.Repo.GetScriptUpload(uploadID)
	if err != nil {
		return "", "", ErrNotFound
	}
	fi, err := os.Stat(up.Path)
	if err != nil || fi.IsDir() {
		return "", "", ErrNotFound
	}
	if fi.Size() > maxScriptBytes {
		return "", "", ErrScriptTooLarge
	}
	b, err := os.ReadFile(up.Path)
	if err != nil {
		return "", "", ErrNotFound
	}
	return string(b), up.Filename, nil
}

// scriptExcerpt renders what the approver reads: the head of the script, then a
// line stating everything the excerpt leaves out.
//
// The summary line is not garnish. Without it the excerpt is indistinguishable
// from a short script, and an approver would be agreeing to 40 lines when 84,000
// statements are queued behind them.
//
// Every statement is also listed by its first line, ahead of the head. The head
// alone fails exactly the common case: a CREATE TABLE runs to dozens of lines, so
// 40 lines hold the first one and the rest of the DDL is just "N statements" —
// approved without anyone having seen it.
func scriptExcerpt(filename, content string, stmts []dto.ScannedStmt, high, mid int) string {
	stmtCount := len(stmts)
	lines := strings.Split(content, "\n")
	head := lines
	truncated := false
	if len(lines) > scriptExcerptLines {
		head = lines[:scriptExcerptLines]
		truncated = true
	}
	// Bound each line too — see scriptExcerptLineBytes. Cut on a rune boundary
	// so a multi-byte character isn't split into mojibake.
	clipped := make([]string, len(head))
	for i, l := range head {
		if len(l) > scriptExcerptLineBytes {
			cut := scriptExcerptLineBytes
			for cut > 0 && !utf8.RuneStart(l[cut]) {
				cut--
			}
			l = l[:cut] + " …(本行截断,全文见所引用的脚本文件)"
		}
		clipped[i] = l
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\\i %s\n", filename)
	fmt.Fprintf(&b, "-- 共 %d 条语句 · 高危 %d · 需审批 %d · %s · sha256:%s\n",
		stmtCount, high, mid, humanBytes(len(content)), scriptDigest(content))
	writeStatementIndex(&b, stmts)
	b.WriteString(strings.Join(clipped, "\n"))
	if truncated {
		fmt.Fprintf(&b, "\n-- …… 以下省略 %d 行,全文见所引用的脚本文件 ……", len(lines)-scriptExcerptLines)
	}
	return b.String()
}

// scriptIndexMax / scriptIndexBytes bound the statement list by count AND by size:
// a ticket's command is stored (MySQL TEXT), listed and pushed to Lark cards, and
// the whole excerpt is kept under 8KB. A script of a few DDLs is listed in full; a
// migration of thousands of statements lists what fits and says how many remain.
// The full script stays one click away (ApprovalScript).
const (
	scriptIndexMax   = 200
	scriptIndexBytes = 3 * 1024
)

// scriptIndexLineRunes bounds one entry: enough for `CREATE TABLE name (` or
// `ALTER TABLE name ADD …`, which is what the approver scans the list for.
const scriptIndexLineRunes = 100

// writeStatementIndex lists each statement by its first line, as `-- ` comment
// lines so the excerpt still reads as SQL.
func writeStatementIndex(b *strings.Builder, stmts []dto.ScannedStmt) {
	if len(stmts) == 0 {
		return
	}
	fmt.Fprintf(b, "-- 语句清单(共 %d 条):\n", len(stmts))
	used := 0
	for i, st := range stmts {
		first := strings.TrimSpace(st.SQL)
		if j := strings.IndexByte(first, '\n'); j >= 0 {
			first = strings.TrimSpace(first[:j])
		}
		if r := []rune(first); len(r) > scriptIndexLineRunes {
			first = string(r[:scriptIndexLineRunes]) + " …"
		}
		tag := ""
		switch st.Risk {
		case "high":
			tag = "[高危] "
		case "mid":
			tag = "[需审批] "
		}
		entry := fmt.Sprintf("--  %d. %s%s\n", i+1, tag, first)
		if i == scriptIndexMax || used+len(entry) > scriptIndexBytes {
			fmt.Fprintf(b, "-- …… 另有 %d 条,全文见所引用的脚本文件\n", len(stmts)-i)
			break
		}
		b.WriteString(entry)
		used += len(entry)
	}
	b.WriteString("--\n")
}

func humanBytes(n int) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1fGB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fKB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}

// approvedScriptDenied judges every statement of a script ticket for the actor
// BEFORE the ticket is claimed. The Command of a script ticket is only an excerpt
// (`\i file` …) that parses to no verb, so judging it says nothing; and refusing
// only after the claim would let any read-only peer burn an approved ticket with
// one click. runApprovedScript judges again right before running — this is the
// early, non-destructive copy of that check.
//
// A missing or modified file is NOT refused here: such a ticket can never run, and
// runApprovedScript claims it and records why on the ticket itself.
func (s *Services) approvedScriptDenied(actor *model.User, ap *model.Approval, conn *model.Connection, tier string) error {
	content, _, err := s.readUploadedScript(ap.ScriptUploadID)
	if err != nil || scriptDigest(content) != ap.ScriptSHA256 {
		return nil
	}
	roleIDs := s.Repo.EffectiveRoleIDs(actor)
	for i, sql := range splitStatements(content) {
		if v := s.Engine.EvaluateFor(roleIDs, conn.Engine, tier, sql); v.Action == gateway.ActionDeny {
			return apimsg.Errorf("第 %d 条语句现在被禁止执行(%s),请确认你有权执行这份脚本", i+1, v.Rule)
		}
	}
	return nil
}

// runApprovedScript executes an approved script referenced by upload id.
//
// Three things happen before anything runs, in this order, and each one can stop
// it:
//
//  1. The file is re-read and re-hashed. A file that changed since review is
//     refused outright — approval was given to those bytes, not to that path.
//  2. Every statement is judged again. Approval says a human accepted the risk;
//     it does not say the rules still allow it. Tiers, the capability matrix and
//     the dictionary can all have changed while the ticket sat in the queue, and
//     the ticket must not outrank them.
//  3. Statements are then executed ONE AT A TIME. The executor sends whatever it
//     is given as a single statement, so the old path handed it `\i file` plus
//     the whole body as one string — which no database would accept.
//
// Execution stops at the first failure and reports how far it got: a half-applied
// script that reports success is worse than one that reports where it stopped.
//
// Statements are judged with the ACTOR's roles — the person pressing the button,
// not the initiator — exactly like the gates at the top of ExecuteApproved. Judged
// with the initiator's, a read-only peer could run an approved DDL script.
func (s *Services) runApprovedScript(ctx context.Context, actor *model.User, ap *model.Approval, conn *model.Connection) gateway.ExecResult {
	ctx = s.withTierMasking(ctx, conn)
	content, _, err := s.readUploadedScript(ap.ScriptUploadID)
	if err != nil {
		m := apimsg.Sprintf("· 脚本文件不可读,未执行: %s", err)
		return gateway.ExecResult{Output: m.S, OutputRef: m.Ref, Err: err}
	}
	if got := scriptDigest(content); got != ap.ScriptSHA256 {
		m := apimsg.Sprintf("· %s", ErrScriptChanged)
		return gateway.ExecResult{Output: m.S, OutputRef: m.Ref, Err: ErrScriptChanged}
	}

	tier, terr := s.tierCodeOf(conn)
	if terr != nil {
		m := apimsg.T("· 实例的分层标签解析失败,未执行")
		return gateway.ExecResult{Output: m.S, OutputRef: m.Ref, Err: terr}
	}
	roleIDs := s.Repo.EffectiveRoleIDs(actor)

	stmts := splitStatements(content)
	execs := sqlutil.SplitStatementsExec(content) // 下发用:保留可执行注释标记(与 stmts 一一对应)
	flow := gateway.NewScriptFlow(conn.Engine)    // WHENEVER / EXIT(SQL*Plus 脚本)
	var failed []string
	var firstErr error
	exitPos := 0
	for i, sql := range stmts {
		flow.Observe(sql)
		if v := s.Engine.EvaluateFor(roleIDs, conn.Engine, tier, sql); v.Action == gateway.ActionDeny {
			err := apimsg.Errorf("第 %d 条语句已被规则禁止(%s),脚本在此中止", i+1, v.Rule)
			m := apimsg.Sprintf("· 已执行 %d/%d 条后中止 · %s", i, len(stmts), err)
			return gateway.ExecResult{Output: m.S, OutputRef: m.Ref, Rows: i, Err: err}
		}
		// ctx 带着调用方开的专用连接:整份脚本同一个会话
		res := s.Executor.Run(flow.Context(ctx), conn, execs[i], s.approvalExecTimeout())
		flow.Update(res.Binds)
		if res.Err != nil && flow.ContinueOnError() {
			failed = append(failed, model.Itoa(i+1))
			if firstErr == nil {
				firstErr = res.Err
			}
			continue
		}
		if res.Err != nil {
			m := apimsg.Sprintf("· 已执行 %d/%d 条后失败 · 第 %d 条: %s", i, len(stmts), i+1, apimsg.M{S: res.Output, Ref: res.OutputRef})
			return gateway.ExecResult{Output: m.S, OutputRef: m.Ref, Rows: i, Err: res.Err}
		}
		if flow.IsExit(sql) {
			exitPos = i + 1
			break
		}
	}
	n := len(stmts)
	if exitPos > 0 {
		n = exitPos
	}
	m := apimsg.Sprintf("脚本执行完成 · 共 %d 条语句", n)
	if text, ref := flowNote(exitPos, failed); text != "" {
		m = apimsg.Sprintf("脚本执行完成 · 共 %d 条语句 · %s", n, apimsg.M{S: text, Ref: ref})
	}
	// WHENEVER SQLERROR CONTINUE 下有语句失败:整份跑完了,但不是全部成功 —— 审计记 warn
	return gateway.ExecResult{Output: m.S, OutputRef: m.Ref, Rows: n, Err: firstErr}
}

// ApprovalScript returns the whole script a script ticket refers to, for anyone
// who can see that ticket.
//
// The ticket itself carries only an excerpt, and the uploads endpoint serves the
// uploader alone — so an approver had no way to read past the excerpt, yet was
// asked to approve every statement in the file. Visibility follows the approvals
// list exactly (repository.approvalScope): the initiator, a member of the ticket's
// chain, or — once approved and not yet run — anyone who can reach the instance,
// since such a peer may execute it.
//
// The bytes are checked against the digest recorded at review: what is shown must
// be what was reviewed and what will run. A file changed since is refused rather
// than shown as if it were the approved script.
func (s *Services) ApprovalScript(u *model.User, id int64) (content, filename string, err error) {
	ap, err := s.Repo.GetApproval(id)
	if err != nil || ap.ScriptUploadID == 0 {
		return "", "", ErrNotFound
	}
	if !s.canSeeApprovalScript(u, ap) {
		return "", "", ErrForbidden
	}
	content, filename, err = s.readUploadedScript(ap.ScriptUploadID)
	if err != nil {
		return "", "", err
	}
	if scriptDigest(content) != ap.ScriptSHA256 {
		return "", "", ErrScriptChanged
	}
	return content, filename, nil
}

func (s *Services) canSeeApprovalScript(u *model.User, ap *model.Approval) bool {
	if u == nil {
		return false
	}
	if ap.InitiatorID == u.ID || s.isChainMember(ap.ID, u) {
		return true
	}
	if ap.Status == model.StatusApproved && ap.ExecutedAt == nil {
		if conn, err := s.Repo.GetConnection(ap.ConnectionID); err == nil && s.canAccessConn(u, conn) {
			return true
		}
	}
	return false
}

// ReadUploadedScriptFor reads an uploaded script on behalf of a caller, checking
// that the caller owns it. This is the only entry point a request may reach:
// readUploadedScript itself asks no questions, so everything that comes from a
// client goes through the ownership check here first.
func (s *Services) ReadUploadedScriptFor(u *model.User, uploadID int64) (content, filename string, err error) {
	if _, _, err := s.ScriptUploadFile(u, uploadID); err != nil {
		return "", "", err
	}
	return s.readUploadedScript(uploadID)
}
