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
	"velagateway/pkg/sqlutil"
)

// Services is the application service container wired in bootstrap.
type Services struct {
	Repo       *repository.Repo
	Engine     *gateway.RiskEngine
	Executor   *gateway.Executor
	JWT        *jwt.Manager
	Webhook    *Dispatcher

	apCounter    atomic.Int64
	auditCounter atomic.Int64
	relCounter   atomic.Int64 // REL-<n> release numbers
	auditMu      sync.Mutex // serialize audit-chain writes (prev-read + insert must be atomic)
	// 敏感字段规则的短缓存。挂在实例上而不是做成包级变量:包级的那一份会在
	// 多个 Services 之间串味(测试里一个用例的规则漏进另一个用例),而一个
	// "有时候用的是别人的规则"的脱敏开关,比没有还危险。
	sensitiveMu    sync.RWMutex
	sensitiveRules []gateway.SensitiveRule
	sensitiveAt    time.Time
	// mfaGrace remembers a successful PROD step-up per user/session/instance so a
	// code vouches for a working session rather than a single command.
	mfaGraceMu sync.Mutex
	mfaGrace   map[string]time.Time
	exportQueue  chan int64 // async export-job ids, drained by a worker pool
	asyncQueue   chan int64 // async SQL-exec-job ids, drained by a worker pool
	// releaseQueue carries release ids for the CI/CD runner. A negative id means
	// "resume an already-claimed run" — see dispatchReleaseJob.
	releaseQueue chan int64
}

const exportWorkers = 3 // how many export jobs run in parallel

func New(repo *repository.Repo, engine *gateway.RiskEngine, jwtMgr *jwt.Manager) *Services {
	s := &Services{
		Repo:     repo,
		Engine:   engine,
		Executor: gateway.NewExecutor(),
		JWT:      jwtMgr,
		Webhook:  NewDispatcher(repo),
	}
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
	// Async SQL-exec workers (long-running background jobs, same isolation).
	if n, err := repo.FailStuckAsyncJobs(); err == nil && n > 0 {
		slog.Info("reconciled orphaned async jobs on startup", "failed", n)
	}
	s.asyncQueue = make(chan int64, 256)
	for i := 0; i < asyncWorkers; i++ {
		go func() {
			for id := range s.asyncQueue {
				s.runAsyncJobSafe(id)
			}
		}()
	}
	// Release (CI/CD) runner. Numbers continue from the highest REL- already
	// issued, never restarting at 1 over a truncated table.
	s.relCounter.Store(repo.MaxReleaseSeq())
	// A release left `running` by a previous process cannot be resumed (its
	// execute stage may have reached the database), while one still `pending`
	// never started and is simply re-queued. `waiting` runs are untouched: they
	// are blocked on a human and their state is fully in the database.
	if n, err := repo.FailStuckReleases(); err == nil && n > 0 {
		slog.Warn("reconciled interrupted releases on startup", "failed", n)
	}
	s.releaseQueue = make(chan int64, 256)
	for i := 0; i < releaseWorkers; i++ {
		go func() {
			for id := range s.releaseQueue {
				s.dispatchReleaseJob(id)
			}
		}()
	}
	if pending, err := repo.PendingReleases(); err == nil {
		for _, rel := range pending {
			s.enqueueRelease(rel.ID)
		}
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
	return s.recordAuditBy(actor, "", conn, command, risk, result, apNo, eventType)
}

// recordAuditBy is recordAudit for an action authorised by someone other than
// the actor — an external approver, or an administrator acting on another
// user's account. See AuditLog.Operator.
func (s *Services) recordAuditBy(actor *model.User, operator string, conn *model.Connection, command, risk, result, apNo, eventType string) *model.AuditLog {
	a := s.appendAudit(actor, conn, command, risk, result, apNo, operator)
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
// appendAudit writes one hash-chained audit row. operator names who actually
// authorised the action when that differs from actor (see AuditLog.Operator);
// pass "" when they are the same.
func (s *Services) appendAudit(actor *model.User, conn *model.Connection, command, risk, result, apNo, operator string) *model.AuditLog {
	// Never persist credentials in the clear: mask password literals before the
	// command enters the (immutable, hash-chained) audit log. The audit row is
	// never re-executed, so redaction here is safe; it also flows into the webhook
	// payload, which carries this same audit object.
	command = sqlutil.RedactSecrets(command)
	// Resolve the control tier BEFORE taking the lock — it is a database read, and
	// auditMu serialises every audit writer in the process. An instance whose
	// environment no longer resolves leaves the snapshot empty rather than
	// blocking the write: losing the audit row would be the worse outcome.
	tierCode := ""
	if conn != nil {
		if t, err := s.tierOf(conn); err == nil {
			tierCode = t.Code
		}
	}
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
			Operator:   operator,
			TierCode:   tierCode,
			PrevHash:   prev,
		}
		if conn != nil {
			a.ConnectionID = conn.ID
			a.Instance = conn.Name
			a.Database = conn.Database
			a.Env = conn.Env
		}
		payload, _ := json.Marshal(map[string]any{
			"time": now.Format(time.RFC3339), "actor": actor.Name, "instance": a.Instance,
			"database": a.Database, "command": command, "risk": risk, "result": result, "ap": apNo, "operator": operator,
			// Part of the hash from here on: a snapshot that could be edited without
			// breaking the chain would not be evidence of anything. Rows written
			// before this change hashed a payload without these keys and keep their
			// original hashes — nothing recomputes historical rows.
			"env": a.Env, "tier": tierCode,
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

// Login verifies credentials (and, for an MFA-enrolled user, a TOTP code) and
// issues a JWT.
func (s *Services) Login(email, password, mfaCode string) (string, time.Time, *model.User, error) {
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
	// A service account's only door is its API credential; the console is not
	// one of its doors. Its empty PasswordHash already fails below, but that is
	// an accident of creation — this check holds even if a hash ever appears on
	// the row. Same uniform refusal as a wrong password: the login boundary
	// must not confirm what kind of account an email is.
	if u.Kind == model.UserKindService {
		crypto.CheckPassword(dummyPasswordHash, password) // equalize timing
		s.auditLoginFail(email)
		return "", time.Time{}, nil, ErrInvalidCredentials
	}
	// A set password is required; an empty/unusable hash always fails (this
	// closes the "empty PasswordHash accepts any password" bypass).
	if !crypto.CheckPassword(u.PasswordHash, password) {
		s.auditLoginFail(email)
		return "", time.Time{}, nil, ErrInvalidCredentials
	}
	// Two-step login: an MFA-enrolled user must present a valid TOTP code. Users
	// who never enrolled (no secret) log in with the password alone (opt-in MFA).
	if u.MFAEnabled && u.MFASecret != "" {
		if mfaCode == "" {
			return "", time.Time{}, nil, ErrMFARequired // password OK, prompt for the code
		}
		if !loginTOTPValid(u.MFASecret, mfaCode) {
			s.auditLoginFail(email)
			return "", time.Time{}, nil, ErrMFAInvalid
		}
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
	ErrMFAInvalid         = fmt.Errorf("mfa code invalid")
	ErrBadRequest         = fmt.Errorf("bad request")
	ErrScriptPathUnset    = fmt.Errorf("script save path not configured")
	ErrExportPathUnset    = fmt.Errorf("export save path not configured")
	ErrAlreadyDecided     = fmt.Errorf("approval already decided") // lost the decide race / not pending
	ErrNoDatabase         = fmt.Errorf("no target database selected")
	ErrExportNotReadOnly  = fmt.Errorf("export query must be a single read-only statement")
)

// ---------------------------------------------------------------- MFA step-up grace

// noteMFAVerified records a successful PROD step-up for one user/session/instance.
func (s *Services) noteMFAVerified(u *model.User, conn *model.Connection) {
	s.mfaGraceMu.Lock()
	defer s.mfaGraceMu.Unlock()
	if s.mfaGrace == nil {
		s.mfaGrace = map[string]time.Time{}
	}
	s.mfaGrace[mfaGraceKey(u, conn)] = time.Now()
}

// mfaVerifiedRecently reports whether this session already stepped up on this
// instance within the grace window.
func (s *Services) mfaVerifiedRecently(u *model.User, conn *model.Connection) bool {
	window := s.mfaGraceWindow()
	if window <= 0 {
		return false // grace disabled: every command steps up
	}
	s.mfaGraceMu.Lock()
	defer s.mfaGraceMu.Unlock()
	at, ok := s.mfaGrace[mfaGraceKey(u, conn)]
	return ok && time.Since(at) < window
}

// mfaGraceKey binds a grace entry to the user, their SESSION GENERATION and the
// instance. Including the token version is what makes a logout, password reset or
// role change void the grace: those bump it, so previous entries can never match
// again. Including the connection keeps the decision per instance — being trusted
// on one production database says nothing about another.
func mfaGraceKey(u *model.User, conn *model.Connection) string {
	return fmt.Sprintf("%d:%d:%d", u.ID, u.TokenVersion, conn.ID)
}

// mfaGraceWindow is how long one step-up vouches for a session on an instance.
// 0 disables the grace (step up on every command). Held in memory only, so a
// gateway restart also forces a fresh step-up — the safe direction.
func (s *Services) mfaGraceWindow() time.Duration {
	m := s.settingInt("security.mfaGraceMinutes", 30)
	if m <= 0 {
		return 0
	}
	if m > 720 { // never vouch for longer than half a day
		m = 720
	}
	return time.Duration(m) * time.Minute
}
