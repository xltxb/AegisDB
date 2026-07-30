package service

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"velagateway/internal/dto"
	"velagateway/internal/gateway"
	"velagateway/internal/model"
	"velagateway/pkg/sqlutil"
)

// asyncWorkers is how many background SQL jobs run in parallel.
const asyncWorkers = 3

// ExecAsync submits a long-running SQL for background execution. It runs the same
// three-layer gate as a synchronous exec (access → capability/dictionary verdict,
// MFA on PROD); deny is rejected, an approve verdict creates an approval ticket,
// and an allow verdict enqueues a background job whose progress (RAISE NOTICE)
// streams into the job log. Returns immediately — poll the job for status/log.
func (s *Services) ExecAsync(u *model.User, connID int64, sql, reason, mfaCode, database string) (*dto.AsyncSubmitResp, error) {
	conn, err := s.Repo.GetConnection(connID)
	if err != nil {
		return nil, ErrNotFound
	}
	if database != "" {
		conn.Database = database
	}
	if !s.canAccessConn(u, conn) {
		return nil, ErrForbidden
	}
	// FR-CONN-04: maintenance freezes activity on an instance. This channel runs
	// against the same instance through the same executor, so skipping the check
	// meant anyone blocked in the terminal could resubmit the identical statement
	// as a background job and have it run (ER6).
	if conn.Status == "maint" {
		s.recordAudit(u, conn, sql, model.RiskLow, model.ResultWarn, "", "")
		return &dto.AsyncSubmitResp{Output: "· 目标实例处于维护态，操作受限"}, nil
	}
	if err := s.checkMFA(u, conn, mfaCode); err != nil {
		return nil, err
	}
	// Judge (a multi-statement batch is governed by its strictest sub-statement).
	v := s.Engine.EvaluateRoles(s.Repo.EffectiveRoleIDs(u), conn.Env, sql)
	if stmts := sqlutil.SplitStatements(sql); len(stmts) > 1 {
		v = s.strictestVerdict(u, conn, stmts)
	}
	switch v.Action {
	case gateway.ActionDeny:
		s.recordAudit(u, conn, sql, model.RiskHigh, model.ResultRejected, "", "intercept")
		return nil, ErrForbidden
	case gateway.ActionApprove:
		ap, _, aerr := s.createApproval(u, conn, sql, v, reason)
		if aerr != nil {
			return nil, aerr
		}
		s.recordAudit(u, conn, sql, v.Risk, model.ResultPending, ap.ApNo, "intercept")
		return &dto.AsyncSubmitResp{Intercepted: true, ApprovalNo: ap.ApNo, Risk: v.Risk, Rule: v.Rule}, nil
	default: // allow → enqueue a background job
		job := &model.AsyncJob{
			UserID: u.ID, ConnectionID: conn.ID, Instance: conn.Env + "-" + conn.Name,
			Database: conn.Database, SQL: sql, Reason: reason, Status: model.AsyncPending,
			Risk: v.Risk,
		}
		if err := s.Repo.CreateAsyncJob(job); err != nil {
			return nil, err
		}
		select {
		case s.asyncQueue <- job.ID:
		default:
			s.failAsync(job.ID, "异步执行队列已满,请稍后重试")
		}
		return &dto.AsyncSubmitResp{JobID: job.ID}, nil
	}
}

// runAsyncJobSafe runs one async job with panic recovery so a single bad job
// never kills the worker.
func (s *Services) runAsyncJobSafe(id int64) {
	defer func() {
		if r := recover(); r != nil {
			s.failAsync(id, fmt.Sprintf("异步执行异常: %v", r))
		}
	}()
	s.runAsyncJob(id)
}

// runAsyncJob executes one background job: claim → run with a long timeout while
// streaming NOTICE progress into the log → record terminal status + audit.
func (s *Services) runAsyncJob(id int64) {
	start := time.Now()
	// Atomic claim so a restart-reconciled or double-enqueued job runs exactly once.
	if claimed, err := s.Repo.ClaimAsyncJob(id, start); err != nil || !claimed {
		return
	}
	job, err := s.Repo.GetAsyncJob(id)
	if err != nil {
		return
	}
	conn, err := s.Repo.GetConnection(job.ConnectionID)
	if err != nil {
		s.failAsync(id, "连接不存在")
		return
	}
	if job.Database != "" {
		conn.Database = job.Database
	}
	u, _ := s.Repo.GetUserByID(job.UserID)

	// Log sink: keep the full text in memory (capped), flush to the row at most
	// ~once a second so a chatty procedure doesn't hammer the DB.
	var mu sync.Mutex
	var buf strings.Builder
	lastFlush := time.Now()
	writeLine := func(line string) {
		mu.Lock()
		buf.WriteString(line)
		buf.WriteByte('\n')
		if buf.Len() > 256<<10 { // cap ~256KB, keep the tail
			t := buf.String()
			buf.Reset()
			buf.WriteString("· …(日志过长,仅保留末尾)\n")
			buf.WriteString(t[len(t)-(200<<10):])
		}
		var snapshot string
		if time.Since(lastFlush) > time.Second {
			snapshot = buf.String()
			lastFlush = time.Now()
		}
		mu.Unlock()
		if snapshot != "" {
			_ = s.Repo.UpdateAsyncJobLog(id, snapshot)
		}
	}
	writeLine("· 开始执行 @ " + start.Format("2006-01-02 15:04:05"))

	timeout := s.asyncExecTimeout()
	var rows int64
	var runErr error
	if gateway.RealExecSupported(conn) {
		rows, runErr = gateway.RealRunAsync(conn, job.SQL, timeout, func(msg string) {
			writeLine("NOTICE: " + msg)
		})
	} else {
		// Simulated connection (no credentials): no real DB, so no live NOTICEs.
		res := s.Executor.Run(conn, job.SQL, timeout)
		rows = int64(res.Rows)
		runErr = res.Err
		writeLine("· (模拟连接,无真实 NOTICE 输出)")
	}

	fin := time.Now()
	mu.Lock()
	final := buf.String()
	mu.Unlock()
	if runErr != nil {
		final += "· 执行失败: " + runErr.Error() + "\n"
		_ = s.Repo.FinishAsyncJob(id, model.AsyncFailed, final, clip(runErr.Error(), 500), int(rows), fin)
		s.recordAudit(u, conn, "ASYNC "+job.SQL, asyncAuditRisk(job), model.ResultWarn, "", "exec")
		return
	}
	final += fmt.Sprintf("· 执行完成 @ %s · 影响 %d 行\n", fin.Format("2006-01-02 15:04:05"), rows)
	_ = s.Repo.FinishAsyncJob(id, model.AsyncDone, final, "", int(rows), fin)
	s.recordAudit(u, conn, "ASYNC "+job.SQL, asyncAuditRisk(job), model.ResultExecuted, "", "exec")
}

func (s *Services) failAsync(id int64, msg string) {
	_ = s.Repo.FinishAsyncJob(id, model.AsyncFailed, "· "+msg+"\n", msg, 0, time.Now())
}

// asyncExecTimeout is the per-job execution ceiling (default 90 min — covers the
// 30–60min procedures this feature targets, with margin).
func (s *Services) asyncExecTimeout() time.Duration {
	sec := s.settingInt("gateway.asyncExecTimeout", 5400)
	if sec < 1 {
		sec = 5400
	}
	return time.Duration(sec) * time.Second
}

// ListAsyncJobs returns a user's async jobs (newest first).
func (s *Services) ListAsyncJobs(u *model.User, limit int) []model.AsyncJob {
	if u == nil {
		return []model.AsyncJob{}
	}
	js, _ := s.Repo.ListAsyncJobs(u.ID, limit)
	if js == nil {
		js = []model.AsyncJob{}
	}
	return js
}

// GetAsyncJob returns one job (with its streamed log). A user sees only their own
// jobs unless they hold an oversight role.
func (s *Services) GetAsyncJob(u *model.User, id int64) (*model.AsyncJob, error) {
	j, err := s.Repo.GetAsyncJob(id)
	if err != nil {
		return nil, ErrNotFound
	}
	if u == nil || (j.UserID != u.ID && !s.canSeeAllActivity(u)) {
		return nil, ErrForbidden
	}
	return j, nil
}

// asyncAuditRisk returns the verdict recorded when the job was authorised,
// falling back to mid for jobs created before the column existed (ER7).
func asyncAuditRisk(job *model.AsyncJob) string {
	if job.Risk == "" {
		return model.RiskMid
	}
	return job.Risk
}
