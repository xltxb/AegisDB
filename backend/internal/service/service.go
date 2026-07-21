package service

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"velagateway/internal/gateway"
	"velagateway/internal/model"
	"velagateway/internal/repository"
	"velagateway/pkg/crypto"
	"velagateway/pkg/jwt"
)

// Services is the application service container wired in bootstrap.
type Services struct {
	Repo       *repository.Repo
	Engine     *gateway.RiskEngine
	Executor   *gateway.Executor
	JWT        *jwt.Manager
	Webhook    *Dispatcher
	StrictMode atomic.Bool

	apCounter    atomic.Int64
	auditCounter atomic.Int64
	auditMu      sync.Mutex // serialize audit-chain writes (prev-read + insert must be atomic)
	exportQueue  chan int64 // async export-job ids, drained by a worker pool
}

const exportWorkers = 3 // how many export jobs run in parallel

func New(repo *repository.Repo, engine *gateway.RiskEngine, jwtMgr *jwt.Manager, strict bool) *Services {
	s := &Services{
		Repo:     repo,
		Engine:   engine,
		Executor: gateway.NewExecutor(),
		JWT:      jwtMgr,
		Webhook:  NewDispatcher(repo),
	}
	s.StrictMode.Store(strict)
	// Seed counters from the MAX existing sequence (never reuse a number, even
	// after rows are deleted); fall back to the demo base on a fresh DB.
	if m := repo.MaxApprovalSeq(); m >= 2294 {
		s.apCounter.Store(m)
	} else {
		s.apCounter.Store(2294)
	}
	if m := repo.MaxAuditSeq(); m >= 77310 {
		s.auditCounter.Store(m)
	} else {
		s.auditCounter.Store(77310)
	}
	// Reconcile export jobs orphaned by a previous process (the in-memory queue
	// doesn't survive a restart) so they don't hang forever in the UI (R24).
	if n, err := repo.FailStuckExportJobs(); err == nil && n > 0 {
		slog.Info("reconciled orphaned export jobs on startup", "failed", n)
	}
	// Async export workers (parallel jobs). Each job is isolated so a panic in
	// one never kills the worker or leaves the job stuck.
	s.exportQueue = make(chan int64, 256)
	for i := 0; i < exportWorkers; i++ {
		go func() {
			for id := range s.exportQueue {
				s.runExportJobSafe(id)
			}
		}()
	}
	return s
}

// runExportJobSafe runs one export job with panic recovery so a single bad job
// never kills the worker; on panic the job is marked failed.
func (s *Services) runExportJobSafe(id int64) {
	defer func() {
		if r := recover(); r != nil {
			s.failExport(id, fmt.Sprintf("导出异常: %v", r))
		}
	}()
	s.runExportJob(id)
}

func (s *Services) nextApNo() string {
	return fmt.Sprintf("AP-%d", s.apCounter.Add(1))
}

func (s *Services) nextAuditID() string {
	return fmt.Sprintf("AUD-%d", s.auditCounter.Add(1))
}

// recordAudit appends a hash-chained audit row and fires the matching webhook event.
// The prev-hash read and the insert are serialized so the chain never forks
// under concurrent writers.
func (s *Services) recordAudit(actor *model.User, conn *model.Connection, command, risk, result, apNo, eventType string) *model.AuditLog {
	a := s.appendAudit(actor, conn, command, risk, result, apNo)
	if eventType != "" {
		s.Webhook.Dispatch(eventType, a) // fire the webhook OUTSIDE the audit lock
	}
	return a
}

// appendAudit builds and inserts one hash-chained audit row. The prev-hash read
// and the insert are serialized under auditMu with a deferred unlock, so a panic
// anywhere in the critical section can never leave the lock held (which would
// wedge every subsequent audit — login/exec/approval/export).
//
// auditMu only orders writers within THIS process. The unique index on prev_hash
// (A4) is the cross-connection/cross-process backstop: if another writer chained
// onto the same predecessor first, InsertAudit fails with a duplicate-key error;
// we re-read the chain tip and retry so the chain stays linear instead of forking.
func (s *Services) appendAudit(actor *model.User, conn *model.Connection, command, risk, result, apNo string) *model.AuditLog {
	s.auditMu.Lock()
	defer s.auditMu.Unlock()
	now := time.Now()
	var a *model.AuditLog
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		prev := s.Repo.LastAuditHash()
		a = &model.AuditLog{
			OccurredAt: now,
			ActorID:    actor.ID,
			ActorName:  actor.Name,
			Command:    command,
			Risk:       risk,
			Result:     result,
			ApprovalNo: apNo,
			PrevHash:   prev,
		}
		if conn != nil {
			a.ConnectionID = conn.ID
			a.Instance = conn.Name
			a.Database = conn.Database
		}
		payload, _ := json.Marshal(map[string]any{
			"time": now.Format(time.RFC3339), "actor": actor.Name, "instance": a.Instance,
			"database": a.Database, "command": command, "risk": risk, "result": result, "ap": apNo,
		})
		a.Hash = crypto.ChainHash(prev, payload)
		if err = s.Repo.InsertAudit(a); err == nil {
			return a
		}
		// A prev_hash uniqueness conflict means the tip moved under us; re-read and
		// rebuild onto the new tip. Any other error won't resolve by retrying.
		slog.Warn("audit insert conflict, re-chaining", "attempt", attempt, "err", err)
	}
	slog.Error("audit insert failed after retries", "err", err, "actor", actor.Name, "result", result)
	return a
}

// ---------------------------------------------------------------- Auth

// dummyPasswordHash absorbs a bcrypt comparison on the unknown-user / inactive
// login paths so response time doesn't reveal whether an active account exists
// (account-enumeration timing oracle, C2). It's a valid bcrypt hash so the
// compare does the full work rather than returning early on a malformed hash.
var dummyPasswordHash, _ = crypto.HashPassword("vela-login-timing-equalizer")

// Login verifies credentials and issues a JWT.
func (s *Services) Login(email, password string) (string, time.Time, *model.User, error) {
	u, err := s.Repo.GetUserByEmail(email)
	if err != nil {
		crypto.CheckPassword(dummyPasswordHash, password) // equalize timing vs. a real bcrypt compare
		s.auditLoginFail(email)                           // failed logins are audited for visibility (R29)
		return "", time.Time{}, nil, ErrInvalidCredentials
	}
	// Only fully-active accounts may log in (invited/disabled users cannot).
	if u.Status != "active" {
		crypto.CheckPassword(dummyPasswordHash, password) // equalize timing (C2)
		s.auditLoginFail(email)
		return "", time.Time{}, nil, ErrUserDisabled
	}
	// A set password is required; an empty/unusable hash always fails (this
	// closes the "empty PasswordHash accepts any password" bypass).
	if !crypto.CheckPassword(u.PasswordHash, password) {
		s.auditLoginFail(email)
		return "", time.Time{}, nil, ErrInvalidCredentials
	}
	role, _ := s.Repo.GetRole(u.RoleID)
	roleCode := ""
	if role != nil {
		roleCode = role.Code
	}
	// Session & Security · Session TTL: issue the token with the configured lifetime.
	token, exp, err := s.JWT.IssueTTL(u.ID, u.RoleID, roleCode, u.Name, u.TokenVersion, s.SessionTTL())
	if err != nil {
		return "", time.Time{}, nil, err
	}
	s.recordAudit(u, nil, "login", model.RiskLow, model.ResultExecuted, "", "login")
	return token, exp, u, nil
}

// auditLoginFail records a rejected login attempt (R29). The email is captured as
// the actor name; no user id is available for a bad/unknown account.
func (s *Services) auditLoginFail(email string) {
	actor := &model.User{Name: email}
	s.recordAudit(actor, nil, "login failed", model.RiskLow, model.ResultRejected, "", "login")
}

// Service errors.
var (
	ErrInvalidCredentials = fmt.Errorf("invalid credentials")
	ErrUserDisabled       = fmt.Errorf("user disabled")
	ErrNotFound           = fmt.Errorf("not found")
	ErrForbidden          = fmt.Errorf("forbidden")
	ErrMFARequired        = fmt.Errorf("mfa required")
	ErrBadRequest         = fmt.Errorf("bad request")
	ErrScriptPathUnset    = fmt.Errorf("script save path not configured")
	ErrExportPathUnset    = fmt.Errorf("export save path not configured")
	ErrAlreadyDecided     = fmt.Errorf("approval already decided") // lost the decide race / not pending
)
