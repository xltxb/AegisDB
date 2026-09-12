package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"velagateway/internal/gateway"
	"velagateway/internal/model"
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
var ErrScriptChanged = fmt.Errorf("脚本文件在审批期间被修改,内容与评审时不一致,已拒绝执行")

// ErrScriptTooLarge is returned when a script exceeds maxScriptBytes.
var ErrScriptTooLarge = fmt.Errorf("脚本过大,超出单次提交上限")

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
	fi, err := os.Stat(localScriptPath(up.Path))
	if err != nil || fi.IsDir() {
		return "", "", ErrNotFound
	}
	if fi.Size() > maxScriptBytes {
		return "", "", ErrScriptTooLarge
	}
	b, err := os.ReadFile(localScriptPath(up.Path))
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
func scriptExcerpt(filename, content string, stmtCount, high, mid int) string {
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
	b.WriteString(strings.Join(clipped, "\n"))
	if truncated {
		fmt.Fprintf(&b, "\n-- …… 以下省略 %d 行,全文见所引用的脚本文件 ……", len(lines)-scriptExcerptLines)
	}
	return b.String()
}

func humanBytes(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fKB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
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
func (s *Services) runApprovedScript(ap *model.Approval, conn *model.Connection) gateway.ExecResult {
	content, _, err := s.readUploadedScript(ap.ScriptUploadID)
	if err != nil {
		return gateway.ExecResult{Output: "· 脚本文件不可读,未执行: " + err.Error(), Err: err}
	}
	if got := scriptDigest(content); got != ap.ScriptSHA256 {
		return gateway.ExecResult{Output: "· " + ErrScriptChanged.Error(), Err: ErrScriptChanged}
	}

	tier, terr := s.tierCodeOf(conn)
	if terr != nil {
		return gateway.ExecResult{Output: "· 实例的分层标签解析失败,未执行", Err: terr}
	}
	initiator, uerr := s.Repo.GetUserByID(ap.InitiatorID)
	if uerr != nil {
		return gateway.ExecResult{Output: "· 发起人不存在,未执行", Err: uerr}
	}
	roleIDs := s.Repo.EffectiveRoleIDs(initiator)

	stmts := splitStatements(content)
	for i, sql := range stmts {
		if v := s.Engine.EvaluateFor(roleIDs, conn.Engine, tier, sql); v.Action == gateway.ActionDeny {
			err := fmt.Errorf("第 %d 条语句已被规则禁止(%s),脚本在此中止", i+1, v.Rule)
			return gateway.ExecResult{
				Output: fmt.Sprintf("· 已执行 %d/%d 条后中止 · %s", i, len(stmts), err.Error()),
				Rows:   i, Err: err,
			}
		}
		res := s.Executor.Run(context.Background(), conn, sql, s.execTimeout())
		if res.Err != nil {
			return gateway.ExecResult{
				Output: fmt.Sprintf("· 已执行 %d/%d 条后失败 · 第 %d 条: %s", i, len(stmts), i+1, res.Output),
				Rows:   i, Err: res.Err,
			}
		}
	}
	return gateway.ExecResult{
		Output: fmt.Sprintf("脚本执行完成 · 共 %d 条语句", len(stmts)),
		Rows:   len(stmts),
	}
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
