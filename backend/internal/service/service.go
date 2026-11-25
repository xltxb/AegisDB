package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"aegisdb/internal/apimsg"
	"aegisdb/internal/gateway"
	"aegisdb/internal/model"
	"aegisdb/internal/repository"
	"aegisdb/pkg/crypto"
	"aegisdb/pkg/jwt"
	"aegisdb/pkg/sqlutil"
	"aegisdb/pkg/totp"
)

// Services is the application service container wired in bootstrap.
type Services struct {
	Repo       *repository.Repo
	// mfaFails:MFA 验证码失败计数(按用户);callbackFails:审批回调鉴权失败计数(按来源 IP)。
	// 见 mfa_guard.go。挂在实例上而不是包级变量:每个 Services 各算各的。
	mfaFails      *failBook[int64]
	callbackFails *failBook[string]
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
	// auditFailures 计的是"动作发生了、审计行没写成"的次数,见 appendAudit 末尾(M-11)。
	auditFailures atomic.Int64

	mfaGraceMu sync.Mutex
	mfaGrace   map[string]time.Time
	// exportRuns:正在由本进程 worker 执行的导出任务 → 取消它的函数(见 CancelExport)。
	exportRunsMu sync.Mutex
	exportRuns   map[int64]context.CancelFunc
	// metaRuns:各实例的元数据同步在本进程里跑没跑、上次怎么结束(见 metadata_async.go)。
	metaRunsMu sync.Mutex
	metaRuns   map[int64]*metaRun
	// bizSlots 是业务查询的并发名额(每人 / 每实例),见 bizquery_exec.go。
	bizSlots bizSlots
	// replicas 缓存从库的探测结果,见 bizquery_replica.go。
	replicas replicaHealth
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

		mfaFails:      newFailBook[int64](mfaMaxFails, mfaWindow, mfaLockout),
		callbackFails: newFailBook[string](callbackMaxFails, callbackFailWindow, callbackLockout),
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
	// 审批单的执行同理:上一个进程在库上跑到一半的,结果无从得知,判失败并写明,不重跑。
	// 在线变更同理:进行中的判失败(原表没切换),残留的影子表在后台尽力删掉。
	s.ReconcileOSCJobs()
	if n, err := repo.FailStuckApprovalExecutions(); err == nil && n > 0 {
		slog.Warn("reconciled interrupted approval executions on startup", "failed", n)
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
			s.failExport(id, apimsg.Sprintf("导出异常: %v", r))
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

// auditHashPayload 是进哈希的那份快照 —— 审计链**覆盖哪些字段**,只在这一处说。
//
// 拆出来有两个具体理由:
//
//  1. 它可以被直接测:"把某个字段改掉,哈希必须跟着变"是一句能当场验证的话,而写在
//     appendAudit 里面就只能靠黑盒去推。
//  2. 键集是**分代**的。任何离线重算这条链的工具,都得按行的年代选对键集,否则算出来
//     的哈希对不上,看起来像链被篡改过。三代分别是:
//
//        第一代  time / actor / instance / database / command / risk / result / ap / operator
//        第二代  ＋ env / tier                （加 env-tier 模型那次）
//        第三代  ＋ event                     （迁移 0039,审计行记下自己是哪一类事件）
//        第四代  ＋ action                    （迁移 0040,再记它对数据做了什么）
//        第五代  ＋ ip                        （迁移 0041,这一下从哪台机器发出来的）
//        第六代  ＋ source                    （迁移 0063,这一下从网关哪个入口进来的）
//
//     老行的哈希**不重算**,它们仍然按写下那天的键集成立。
//
// 放进来的判据是:这个字段会不会被人引用来下结论。会的话,它就必须是可改即破链的 ——
// 一个改了还能对上的字段不是证据,是一句话。
// 载荷的**代**。校验一行时得按它写下那天的键集重算,否则算出来对不上,看起来像链被
// 篡改过(见上面的分代表)。auditGenOldest..auditGenCurrent 是 VerifyAuditChain 逐代
// 试的范围。
const (
	auditGen1       = 1 // 最初那一套
	auditGen2       = 2 // ＋ env / tier
	auditGen3       = 3 // ＋ event   (迁移 0039)
	auditGen4       = 4 // ＋ action  (迁移 0040)
	auditGen5       = 5 // ＋ ip      (迁移 0041)
	auditGen6       = 6 // ＋ source  (迁移 0063)
	auditGenOldest  = auditGen1
	auditGenCurrent = auditGen6
)

// auditHashPayload 按**当前代**的键集算这一行的载荷。写入走这一个。
func auditHashPayload(a *model.AuditLog) []byte { return auditPayloadGen(a, auditGenCurrent) }

// auditPayloadGen 按指定代的键集算载荷。
//
// 全部字段都从行上取,不再另收一份参数:写入时那些参数本来就已经写进了同一个行对象
// (appendAudit 先把它们填进 a,再来算哈希),多传一份的唯一作用是让**写入和校验有机会
// 算出两份不同的载荷** —— 而那会让整条链在校验面前集体"失效",还查不出为什么。
func auditPayloadGen(a *model.AuditLog, gen int) []byte {
	m := map[string]any{
		// 秒精度、带时区偏移。校验时这一项要能原样重现,所以**服务器时区不能变** ——
		// 换了时区之后重算,每一行的 time 都会差一个偏移量,整条链看着像被改过。
		"time": a.OccurredAt.Format(time.RFC3339), "actor": a.ActorName, "instance": a.Instance,
		"database": a.Database, "command": a.Command, "risk": a.Risk, "result": a.Result,
		"ap": a.ApprovalNo, "operator": a.Operator,
	}
	if gen >= auditGen2 {
		m["env"] = a.Env
		m["tier"] = a.TierCode
	}
	if gen >= auditGen3 {
		m["event"] = a.EventType
	}
	// action 是从 command 推出来的,而 command 本来就在载荷里 —— 那为什么还要单独放进来?
	// 因为**存下来的那一列是可以被单独改掉的**:不放进载荷的话,把一行的 data_action 从
	// delete 改成 read,链照样验得过。行里写着什么,链就得担保什么。
	//
	// 另一面:分类规则以后会变,重新推算会和存着的值不一致 —— 而这正是要的。这一行说的
	// 是它写下**当时**判成了什么,不是今天的规则会怎么判。
	if gen >= auditGen4 {
		m["action"] = a.DataAction
	}
	// 同上:来源地址是拿来下结论的("这个账号昨夜从两个网段各删过一次"),所以它
	// 必须改了就破链。空值也一样进载荷 —— 把一行的 client_ip 从空改成一个地址,
	// 和把它改成另一个地址是同一种伪造。
	if gen >= auditGen5 {
		m["ip"] = a.ClientIP
	}
	// 来源同理:「这条 DELETE 是从开放接口进来的,不是有人在终端里敲的」是拿来下结论
	// 的话,所以改了它就得破链。
	if gen >= auditGen6 {
		m["source"] = a.Source
	}
	payload, _ := json.Marshal(m)
	return payload
}

// AuditDataAction 是 auditDataAction 的导出形态,给启动期的历史行回填用
// (bootstrap.backfillAuditClassification)。
//
// 必须是同一个函数:回填另写一份的话,历史行和新行会按两套规则归类,而它们摆在同一张
// 表的同一列里 —— 那种不一致没有任何地方看得出来。
func AuditDataAction(command string) string { return auditDataAction(command) }

// auditDataAction 判一条**审计命令**对数据做了什么。
//
// 它比 gateway.DataAction 多做一件事:剥掉审计自己加的前缀。这些前缀是服务层为了让人看懂
// 而拼上去的,不是 SQL —— 留着它们会让两类真实的数据操作被判成 other:
//
//   ASYNC <sql>                     后台执行,里面是一条真语句
//   EXPORT [含敏感字段(原值)] <sql>  导出,里面是一条 SELECT(它确实是在读数据)
//
// 其余前缀(\i 脚本、COMPILE、RELEASE-*、开启执行窗口「…」、admin.*)**不剥**:它们后面
// 跟的不是 SQL,而是文件名或一句描述,判成 other 正是对的。
func auditDataAction(command string) string {
	s := strings.TrimSpace(command)
	for _, p := range []string{
		"ASYNC ",
		"EXPORT [含敏感字段(原值)] ",
		"EXPORT [含敏感字段] ",
		"EXPORT ",
	} {
		if rest, ok := strings.CutPrefix(s, p); ok {
			s = strings.TrimSpace(rest)
			break
		}
	}
	return gateway.DataAction(s)
}

// auditClock 是审计行取"当前时间"的地方;设成变量只为测试能钉住一个刁钻的时刻。
var auditClock = time.Now

// auditStamp 把审计时间截到毫秒,**必须在算哈希之前做**。
//
// 存储库的 occurred_at 是 DATETIME(3),MySQL 入库时对多出来的小数位是四舍五入,不是截断。
// 哈希载荷里的时间是秒级的(RFC3339),于是小数秒落在 [.9995, 1) 的那一行:算哈希时是第 N 秒,
// 读回来已经进到第 N+1 秒 —— 校验报"内容不符",而这一行谁也没碰过。约每 2000 行撞上一次,
// VerifyAuditChain 遇到第一处坏行就停,整条链便显示断了(2026-11-16 换 PG 的实验里发现)。
//
// 先截到毫秒,写进去的就是库里能原样存下的值,算哈希用的和读回来的是同一个时刻。
// PG 的 timestamptz(6) 等精度更高的列同样原样存得下毫秒值。
func auditStamp(t time.Time) time.Time { return t.Truncate(time.Millisecond) }

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
	return s.recordAuditFrom("", actor, operator, conn, command, risk, result, apNo, eventType)
}

// recordAuditSrc 是 recordAudit 加显式来源(没有另一位操作人时用),见 recordAuditFrom。
func (s *Services) recordAuditSrc(source string, actor *model.User, conn *model.Connection, command, risk, result, apNo, eventType string) *model.AuditLog {
	return s.recordAuditFrom(source, actor, "", conn, command, risk, result, apNo, eventType)
}

// recordAuditFrom 是 recordAuditBy 再加上**显式的来源**(model.AuditSource*)。
//
// 绝大多数调用点不必用它:请求进来时 middleware.BindRequestUser 已经按路由给 actor 盖了
// 来源(model.AuditSourceForRoute)。要用它的是**入口与路由无关**的那几类 —— 后台跑的
// 导出任务与发布阶段(actor 是从库里新取的,身上没有请求)、审批决定(飞书回调也会走到)、
// 登录(发生在鉴权中间件之前)。source 为空 = 沿用 actor 身上的。
func (s *Services) recordAuditFrom(source string, actor *model.User, operator string, conn *model.Connection, command, risk, result, apNo, eventType string) *model.AuditLog {
	// 同一个 eventType 既写进行里,也交给 webhook —— 一次赋值,两处读同一个变量,
	// 不会漂。推出去的载荷与存下来的行从此说的是同一件事。
	if source == "" {
		source = actor.AuditSource
	}
	a := s.appendAudit(actor, conn, command, risk, result, apNo, operator, eventType, source)
	if eventType != "" {
		// 订阅按增删改查匹配(见 Dispatch);eventType 仍然带着走,它进请求头与 raw_payload。
		s.Webhook.Dispatch(eventType, a.DataAction, a.Env, a) // fire the webhook OUTSIDE the audit lock
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
//
// 时间先过 auditStamp 截到毫秒再算哈希,见那里的说明。
// appendAudit writes one hash-chained audit row. operator names who actually
// authorised the action when that differs from actor (see AuditLog.Operator);
// pass "" when they are the same.
func (s *Services) appendAudit(actor *model.User, conn *model.Connection, command, risk, result, apNo, operator, eventType, source string) *model.AuditLog {
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
	now := auditStamp(auditClock())
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
			EventType:  eventType,
			DataAction: auditDataAction(command),
			// 请求带进来的来源地址(model.User.ClientIP)。后台动作 —— 定时清扫、启动期
			// 回填 —— 没有客户端,这里就是空,而空是如实的:那一下确实不是谁从某台机器
			// 发起的。
			ClientIP: actor.ClientIP,
			// 入口(迁移 0063)。由 recordAuditFrom 定:显式给的优先,否则取请求上盖的。
			Source:   source,
			PrevHash: prev,
		}
		if conn != nil {
			a.ConnectionID = conn.ID
			a.Instance = conn.Name
			// 同一个口径:Oracle 记 schema,不是服务名。审计事后要回答"这条语句落在
			// 哪个库",而服务名回答不了这个问题。
			a.Database = effectiveDatabase(conn)
			a.Env = conn.Env
		}
		// 载荷只认行对象:上面刚把每一个字段都填进 a 了,校验那一路拿到的也只有行对象。
		a.Hash = crypto.ChainHash(prev, auditHashPayload(a))
		if err = s.Repo.InsertAudit(a); err == nil {
			return a
		}
		// A prev_hash uniqueness conflict means the tip moved under us; re-read and
		// rebuild onto the new tip. Any other error won't resolve by retrying.
		slog.Warn("audit insert conflict, re-chaining", "attempt", attempt, "err", err)
	}
	// 走到这里意味着**一次已经发生的动作没有留下审计行**。
	//
	// 这里仍然不回滚、不阻断,而那是一个需要说清楚的取舍:动作此刻已经落在目标库上
	// 了,回滚不了;把调用方变成失败只会让人以为"没执行"而重试一遍,在写操作上那是更
	// 坏的结果。所以方向是:动作照常返回,但这件事**不允许悄无声息**(M-11)。
	//
	// 原先只有一行 slog.Error —— 那在日志量大的机器上等于没有。现在额外记一个计数器,
	// 由 /healthz 暴露出来接监控:审计链缺了行,巡检要在当天就知道,而不是等到下一次
	// verify-audit 发现链断。
	s.auditFailures.Add(1)
	slog.Error("AUDIT WRITE FAILED — 一次已发生的动作没有留下审计行,审计链将出现缺口",
		"err", err, "actor", actor.Name, "result", result, "command", clip(command, 120),
		"total_failures", s.auditFailures.Load())
	return a
}

// AuditWriteFailures 是本进程启动以来审计写入彻底失败的次数。
// **非零就该告警**:它意味着审计链上缺了行,而缺行是 verify-audit 之外看不出来的。
func (s *Services) AuditWriteFailures() int64 { return s.auditFailures.Load() }

// ---------------------------------------------------------------- Auth

// dummyPasswordHash absorbs a bcrypt comparison on the unknown-user / inactive
// login paths so response time doesn't reveal whether an active account exists
// (account-enumeration timing oracle, C2). It's a valid bcrypt hash so the
// compare does the full work rather than returning early on a malformed hash.
var dummyPasswordHash, _ = crypto.HashPassword("aegisdb-login-timing-equalizer")

// Login verifies credentials (and, for an MFA-enrolled user, a TOTP code) and
// issues a JWT.
// clientIP 是这一下从哪儿来。登录发生在鉴权中间件**之前**,那里给每个请求挂 IP 的
// 那条路(model.User.ClientIP)还没经过,所以这一个必须自己带进来 —— 而被拒的登录
// 恰恰是最需要知道来源的一类行(R29 的暴力破解就是按 IP 限流的)。
func (s *Services) Login(email, password, mfaCode, clientIP string) (string, time.Time, *model.User, error) {
	u, err := s.Repo.GetUserByEmail(email)
	// 库里的比较可能不分重音(sameEmail):变体不算这个账号,否则每种写法各算一个频控桶,
	// 按账号的退避就成了摆设(2026-11 审计 V-11)。
	if err != nil || !sameEmail(u.Email, email) {
		crypto.CheckPassword(dummyPasswordHash, password) // equalize timing vs. a real bcrypt compare
		s.auditLoginFail(email, clientIP)                 // failed logins are audited for visibility (R29)
		return "", time.Time{}, nil, ErrInvalidCredentials
	}
	u.ClientIP = clientIP
	u.AuditSource = model.AuditSourceAuth // 登录在鉴权中间件之前,路由那一层没盖上
	// Only fully-active accounts may log in (invited/disabled users cannot).
	if u.Status != "active" {
		crypto.CheckPassword(dummyPasswordHash, password) // equalize timing (C2)
		s.auditLoginFail(email, clientIP)
		return "", time.Time{}, nil, ErrUserDisabled
	}
	// A service account's only door is its API credential; the console is not
	// one of its doors. Its empty PasswordHash already fails below, but that is
	// an accident of creation — this check holds even if a hash ever appears on
	// the row. Same uniform refusal as a wrong password: the login boundary
	// must not confirm what kind of account an email is.
	if u.Kind == model.UserKindService {
		crypto.CheckPassword(dummyPasswordHash, password) // equalize timing
		s.auditLoginFail(email, clientIP)
		return "", time.Time{}, nil, ErrInvalidCredentials
	}
	// 只挂业务查询角色的账号(业务方)不走本地口令:它们等 SRD 授权登录(PRD 决策 10)。拦在
	// 登录这一步而不只是"设不了口令":修复之前设过的口令、将来某条漏掉的设口令路径,都进不来。
	// 与口令错误同样的回答,不透露这是什么账号。
	if s.bizQueryOnly(u) {
		crypto.CheckPassword(dummyPasswordHash, password) // equalize timing
		s.auditLoginFail(email, clientIP)
		return "", time.Time{}, nil, ErrInvalidCredentials
	}
	// A set password is required; an empty/unusable hash always fails (this
	// closes the "empty PasswordHash accepts any password" bypass).
	if !crypto.CheckPassword(u.PasswordHash, password) {
		s.auditLoginFail(email, clientIP)
		return "", time.Time{}, nil, ErrInvalidCredentials
	}
	// 已绑定 SSO 的账号只能经 SSO 登录 —— 逐人收紧,见 sso_bind.go。管理员例外:SSO
	// 宕机时总得有人进得来。没绑定的人照常用口令:邮箱对不上的人不会被锁在门外。
	// 放在口令校验**之后**:口令不对的人拿到的是同一句"凭据错误",分辨不出这个账号
	// 绑没绑定。
	if s.SSOConfig().Enabled && u.SSOBound && !s.IsAdmin(u) {
		s.auditLoginFail(email, clientIP)
		return "", time.Time{}, nil, ErrLocalLoginSSOOnly
	}
	// Two-step login: an MFA-enrolled user must present a valid TOTP code. Users
	// who never enrolled (no secret) log in with the password alone (opt-in MFA).
	if u.MFAEnabled && u.MFASecret != "" {
		// 登录这一步和其它 TOTP 校验共用失败计数(mfa_guard.go,2026-11 审计 V-16):从前只受登录频控,
		// 换 IP 就能接着猜。走到这里的人口令是对的,所以这句"已锁定"不泄露什么。
		if s.mfaFails.locked(u.ID, time.Now()) {
			return "", time.Time{}, nil, ErrMFALocked
		}
		if mfaCode == "" {
			return "", time.Time{}, nil, ErrMFARequired // password OK, prompt for the code
		}
		ok, counter := totp.ValidateWithCounter(u.MFASecret, mfaCode, time.Now())
		if !ok {
			s.auditLoginFail(email, clientIP)
			return "", time.Time{}, nil, s.mfaFailed(u, "登录", ErrMFAInvalid)
		}
		s.mfaFails.succeed(u.ID)
		// 登录同样**消费**这个时间步(M3)。只校验不消费的话,一个码在 ~90 秒里能反复
		// 登录:截到"口令 + 一次验证码"的钓鱼代理就能另开会话。代价是刚登录完、马上
		// 要做生产步进验证的人得等下一个码 —— 至多 30 秒。
		if !s.Repo.ConsumeMFACounter(u.ID, int64(counter)) {
			s.auditLoginFail(email, clientIP)
			return "", time.Time{}, nil, ErrMFACodeUsed
		}
	}
	// Session & Security · Session TTL: issue the token with the configured lifetime.
	return s.issueSession(u, "login", "")
}

// auditLoginFail records a rejected login attempt (R29). The email is captured as
// the actor name; no user id is available for a bad/unknown account.
func (s *Services) auditLoginFail(email, clientIP string) {
	actor := &model.User{Name: email, ClientIP: clientIP, AuditSource: model.AuditSourceAuth}
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
	ErrMFACodeUsed        = fmt.Errorf("mfa code already used") // 码是对的,但这个时间步已经用过(防重放)
	ErrBadRequest         = fmt.Errorf("bad request")
	ErrScriptPathUnset    = fmt.Errorf("script save path not configured")
	// ErrTooManyJobs:这个人排队 / 执行中的导出或后台执行已达上限(maxActiveJobsPerUser,L-API2)。
	ErrTooManyJobs = fmt.Errorf("too many active jobs")
	ErrScriptReferenced   = fmt.Errorf("script upload referenced by an approval or release") // 被单据引用,不许删
	ErrExportPathUnset    = fmt.Errorf("export save path not configured")
	ErrAlreadyDecided     = fmt.Errorf("approval already decided") // lost the decide race / not pending
	ErrNoDatabase         = fmt.Errorf("no target database selected")
	ErrExportNotReadOnly  = fmt.Errorf("export query must be a single read-only statement")
)

// ExportMultiStmtError:导出 SQL 里不止一条语句(N 条)。导出一次只出一份结果。
type ExportMultiStmtError struct{ N int }

func (e *ExportMultiStmtError) Error() string {
	return fmt.Sprintf("export query must be a single statement, got %d", e.N)
}

// ---------------------------------------------------------------- MFA step-up grace

// noteMFAVerified records a successful PROD step-up for one user/session/instance.
func (s *Services) noteMFAVerified(u *model.User, conn *model.Connection) {
	key := mfaGraceKey(u, conn)
	if key == "" {
		return // 无会话标识:不担保,也不记录(见 mfaGraceKey)
	}
	s.mfaGraceMu.Lock()
	defer s.mfaGraceMu.Unlock()
	if s.mfaGrace == nil {
		s.mfaGrace = map[string]time.Time{}
	}
	s.mfaGrace[key] = time.Now()
}

// mfaVerifiedRecently reports whether this session already stepped up on this
// instance within the grace window.
func (s *Services) mfaVerifiedRecently(u *model.User, conn *model.Connection) bool {
	window := s.mfaGraceWindow()
	if window <= 0 {
		return false // grace disabled: every command steps up
	}
	key := mfaGraceKey(u, conn)
	if key == "" {
		return false // 无会话标识:每次都步进验证,这是这里唯一安全的失败方向
	}
	s.mfaGraceMu.Lock()
	defer s.mfaGraceMu.Unlock()
	at, ok := s.mfaGrace[key]
	return ok && time.Since(at) < window
}

// mfaGraceKey binds a grace entry to the user, THIS ONE SESSION and the instance.
//
// 键里原先放的是 TokenVersion,而那是**用户级**的代次:同一个人的每个并发会话共享
// 同一个值。于是受害者在自己浏览器里做过一次生产步进验证之后,一个被盗的 token 在
// 宽限期内(默认 30 分钟)对同一台实例也跟着免验 —— 它根本没有证明过持有第二因子
// (M-6)。改绑 jti 之后,一次验证只替发起它的那个会话担保。
//
// 代次仍然一起进键:logout / 改密 / 角色变更会 bump 它,那一下要让所有旧条目立刻
// 失配。实例也一起进键 —— 在一台生产库上被信任,不说明另一台也该被信任。
//
// 空的 SessionID 返回空键,调用方据此**不担保、也不记录**(见 mfaVouched)。没有会话
// 标识就说不清是谁在担保,而这里的失败方向必须是"再验一次"。
func mfaGraceKey(u *model.User, conn *model.Connection) string {
	if u.SessionID == "" {
		return ""
	}
	return fmt.Sprintf("%d:%d:%s:%d", u.ID, u.TokenVersion, u.SessionID, conn.ID)
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
