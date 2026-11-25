package service

// 发布流水线的执行阶段里就地跑在线变更(.scratch/osc-product 工单 04)。

import (
	"context"
	"fmt"
	"strings"

	"aegisdb/internal/model"
)

// runReleaseOSC 跑发布内容里要走在线变更的那一条:前置检查 → 建任务(来源 release_id)→ 就地跑完。
// 阶段一直是运行中;每次进度落库时把一行进度接在阶段日志后面,发布单详情里看得到。
// 返回拷贝的行数、要接进日志的几行结论,以及成没成。
func (s *Services) runReleaseOSC(rel *model.Release, creator *model.User, conn *model.Connection, st *model.ReleaseStage,
	stmt string, d OSCDecision, n, total int, log *relLog) (int, relLog, bool) {
	head := relLogOf(model.RelLogOSCHead, "pos", model.Itoa(n), "total", model.Itoa(total), "table", d.Table,
		"rows", thousands(d.Rows), "size", humanBytes(int(d.Bytes)))
	fail := func(code string, kv ...string) (int, relLog, bool) {
		head.add(code, kv...)
		return 0, head, false
	}
	ctx := context.Background()
	blockers, warnings, err := s.OSCPreflight(ctx, conn, d.Schema, d.Table, d.Parsed.Change)
	if err != nil {
		return fail(model.RelLogOSCPreFail, "err", err.Error())
	}
	if len(blockers) > 0 {
		r := head.add(model.RelLogOSCBlocked, "reason", blockersText(blockers))
		for _, b := range blockers {
			r.Parts = append(r.Parts, *model.NewRuleRef(model.RelLogOSCItem, "code", b.Code, "reason", b.Reason))
		}
		return 0, head, false
	}
	for _, w := range warnings {
		head.add(model.RelLogOSCWarn, "code", w.Code, "reason", w.Reason)
	}
	job, err := s.NewOSCJob(creator, conn, strings.TrimSpace(stmt), d, warnings)
	if err != nil {
		return fail(model.RelLogOSCCreateFail, "err", err.Error())
	}
	_ = s.Repo.UpdateOSCJob(job.ID, map[string]any{"release_id": rel.ID})
	prefix := log.clone()
	prefix.extend(head)
	onProgress := func(j *model.OSCJob) {
		p := prefix.clone()
		oscProgressLine(&p, j)
		_ = s.Repo.UpdateReleaseStage(st.ID, p.fields())
	}
	runErr := s.RunOSCJobSync(ctx, job.ID, onProgress)
	f, _ := s.Repo.GetOSCJob(job.ID)
	if f == nil {
		return fail(model.RelLogOSCLost)
	}
	switch {
	case runErr == nil && f.Status == model.OSCDone:
		head.add(model.RelLogOSCDone, "copied", thousands(f.RowsCopied), "replayed", thousands(f.RowsReplayed), "old", f.OldTable)
		return int(f.RowsCopied), head, true
	case f.Status == model.OSCAborted:
		return fail(model.RelLogOSCAborted, "err", f.Error)
	default:
		msg := f.Error
		if msg == "" && runErr != nil {
			msg = runErr.Error()
		}
		return fail(model.RelLogOSCFailed, "err", msg)
	}
}

// oscProgressLine 把一行给人看的进度接进日志。
func oscProgressLine(l *relLog, j *model.OSCJob) {
	phase := model.RuleRef{Code: "oscPh_" + j.Phase}
	if model.RenderRelLog(&phase) == "" {
		phase = *model.NewRuleRef(model.RelLogRaw, "text", j.Phase)
	}
	kv := []string{"copied", thousands(j.RowsCopied), "est", thousands(j.RowsEstimated), "replayed", thousands(j.RowsReplayed)}
	code := model.RelLogOSCProgress
	if j.RowsEstimated > 0 {
		p := j.RowsCopied * 100 / j.RowsEstimated
		if p > 100 {
			p = 100
		}
		code, kv = model.RelLogOSCProgPct, append(kv, "pct", fmt.Sprint(p))
	}
	l.add(code, kv...).Parts = []model.RuleRef{phase}
	if j.CutOverAttempts > 0 {
		l.cat(model.RelLogOSCCut, "n", fmt.Sprint(j.CutOverAttempts))
	}
	if j.Throttled {
		l.cat(model.RelLogOSCThrottled)
	}
	if j.Paused {
		l.cat(model.RelLogOSCPaused)
	}
}
