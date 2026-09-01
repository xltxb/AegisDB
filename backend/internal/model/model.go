// Package model holds the GORM data models (tbl_ prefixed, no physical FKs —
// relational constraints are maintained at the application layer, per backend doc §9).
package model

import "time"

// The five built-in tier codes and what each one MEANS. The code is the lookup
// key; the meaning lives here and in the seeded display names, because the code
// alone has misled before — "gli" was documented and seeded as 灰度 (grey
// release) when it is the 法务 (legal) environment, and "staging" as 演练UAT when
// it is 预发布. An environment's NAME never decides what kind of environment it
// is; the tier it binds to does, and the rules hang off the tier.
const (
	EnvProd    = "prod"    // 生产环境
	EnvUat     = "uat"     // 演练环境
	EnvGli     = "gli"     // 法务环境 — control profile mirrors staging's
	EnvDev     = "dev"     // 开发环境
	EnvStaging = "staging" // 预发布环境
)

// EnvTier — a control tier: the unit the RULES are keyed by. RoleCapability and
// RiskCommand both store one row per (…, tier), and both lookups treat a missing
// row as permission granted (repository.CapabilityLevel → allow, matchCommand →
// off). A tier without its rule rows is therefore not a label — it is an
// unregulated environment, which is why creating one must clone them in the same
// transaction.
//
// The boolean columns exist so the gateway can ask "does this tier require MFA"
// instead of "is this tier called prod": the four hardcoded == EnvProd tests
// (forced MFA, danger banner, pending count, script-scan baseline) become
// property reads, and a second production tier gets the same protection as the
// first.
//
// NOT to be confused with Connection.Tags, which is the data-access scope
// (which databases a role/user may reach). Nothing here is called a "tag".
type EnvTier struct {
	Code        string `gorm:"primaryKey;size:16" json:"code"`
	DisplayName string `gorm:"size:64;not null" json:"displayName"`
	SortOrder   int    `gorm:"not null;default:0" json:"sortOrder"`

	RequireMFA      bool `gorm:"not null;default:false" json:"requireMfa"`
	DangerBanner    bool `gorm:"not null;default:false" json:"dangerBanner"`
	CountsInPending bool `gorm:"not null;default:false" json:"countsInPending"`
	// ScanBaseline marks the tier whose dictionary ScanScript judges uploaded
	// scripts against. Exactly one tier holds it: with none, matchCommand finds
	// no rows and every script scans clean with no error at all.
	ScanBaseline bool `gorm:"not null;default:false" json:"scanBaseline"`

	// Derived connection defaults, previously the hardcoded connEnvMeta map.
	ConnLayer   string `gorm:"size:64" json:"connLayer"`
	DefaultRole string `gorm:"size:64" json:"defaultRole"`
}

func (EnvTier) TableName() string { return "tbl_env_tier" }

// Environment — a group of instances, bound to exactly one EnvTier. One tier
// backs N environments, so a second production cluster (prod-hk, prod-sh) is a
// new Environment on the existing prod tier: it inherits the full rule set with
// nothing copied and no window in which it is unregulated.
//
// Connection.Env holds an Environment.Code. The four seeded environments are
// named after their tier, which is what lets existing rows stand unchanged.
type Environment struct {
	Code        string `gorm:"primaryKey;size:32" json:"code"`
	DisplayName string `gorm:"size:64;not null" json:"displayName"`
	TierCode    string `gorm:"size:16;index:idx_environment_tier;not null" json:"tierCode"`
	SortOrder   int    `gorm:"not null;default:0" json:"sortOrder"`
}

func (Environment) TableName() string { return "tbl_environment" }

// Capability matrix levels.
const (
	LevelAllow   = "allow"
	LevelApprove = "approve"
	LevelDeny    = "deny"
)

// Release change types — see Release.ChangeType.
const (
	ChangeDML = "dml"
	ChangeDDL = "ddl"
)

// CapRelease is the capability dimension for RAISING a release (发起发布单),
// judged per control tier like every other dimension.
//
// It is not redundant with the SQL's own capability. `write` and `ddl` answer
// "may this role change data/structure here at all", and the release runner
// still asks them at execute time. This one answers a different question — "may
// this role start a RELEASE against this tier" — which an organisation wants to
// answer separately: self-service releases in dev, an approval-backed flow in
// staging, and no externally-raised production changes at all, without having to
// deny `ddl` on prod (which would also block the terminal and the approval
// channel that production DBAs depend on).
//
// The three levels read as:
//   - allow   — may raise a release on this tier
//   - approve — may raise one, but only through a flow that contains an approval
//     stage, even when the statement itself would have been allowed
//   - deny    — may not raise a release on this tier at all
const CapRelease = "release"

// Capabilities is the full set of matrix dimensions, in console display order.
// The console renders one row per entry; seeding and backfill iterate it, so a
// dimension added here reaches every role rather than only new installs.
var Capabilities = []string{"select", "write", "ddl", "grant", "conn", "approve", "explain", CapRelease}

// Risk dictionary levels (per command per env).
const (
	RiskHigh = "high"
	RiskMid  = "mid"
	RiskOff  = "off"
	RiskLow  = "low"
)

// Approval / audit result states.
const (
	StatusPending  = "pending"
	StatusApproved = "approved"
	StatusRejected = "rejected"
	StatusExpired  = "expired"

	ResultExecuted = "executed"
	ResultPending  = "pending"
	ResultRejected = "rejected"
	ResultWarn     = "warn"
	// ResultExported records data leaving the console as a file. The rows were
	// already shown to this user, so nothing new was read — but a copy now exists
	// outside the gateway, and /export already records that. A terminal that wrote
	// the same data to disk without a trace would be the hole in the pair.
	ResultExported = "exported"
)

// Role — a tiered RBAC role (L0..L3).
type Role struct {
	ID              int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	Code            string    `gorm:"size:64;uniqueIndex:idx_role_code;not null" json:"code"`
	Name            string    `gorm:"size:64;not null" json:"name"`
	Layer           string    `gorm:"size:32;not null" json:"layer"`
	Description     string    `gorm:"size:255" json:"description"`
	DefaultConnRole string    `gorm:"size:64" json:"defaultConnRole"`
	CanApprove      bool      `gorm:"not null;default:false" json:"canApprove"`
	Icon            string    `gorm:"size:32" json:"icon"`
	CreatedAt       time.Time `json:"createdAt"`
}

func (Role) TableName() string { return "tbl_role" }

// User — an account that belongs to a role.
// User kinds. A SERVICE account is a principal for machines: it holds roles,
// tags and audit attribution exactly like a human, but it can never log into
// the console — its only door is an API client credential bound to it
// (tbl_api_client.user_id). The kind is what the login path and the MFA
// mandate key off; everything else (capability matrix, tag scope, audit)
// deliberately cannot tell the difference.
const (
	UserKindHuman   = "human"
	UserKindService = "service"
)

type User struct {
	ID           int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	Name         string    `gorm:"size:64;not null" json:"name"`
	Email        string    `gorm:"size:128;uniqueIndex:idx_user_email;not null" json:"email"`
	RoleID       int64     `gorm:"index:idx_user_role;not null" json:"roleId"`
	Status       string    `gorm:"size:16;not null;default:active" json:"status"` // active|disabled|invited
	Kind         string    `gorm:"size:16;not null;default:human" json:"kind"`    // human|service — see UserKind*
	MFAEnabled   bool      `gorm:"not null;default:false" json:"mfaEnabled"`
	MFASecret    string    `gorm:"size:64" json:"-"`                                // base32 TOTP secret (never serialized)
	MFALastCtr   int64     `gorm:"not null;default:0" json:"-"`                     // last consumed TOTP counter (anti-replay, M3)
	TokenVersion int64     `gorm:"not null;default:0" json:"-"`                     // session generation; bumped to revoke tokens (M1)
	PasswordHash string    `gorm:"size:255" json:"-"`
	Dept         string    `gorm:"size:64" json:"dept"`
	Initials     string    `gorm:"size:8" json:"initials"`
	LastActive   string    `gorm:"size:32" json:"lastActive"`
	CreatedAt    time.Time `json:"createdAt"`
}

func (User) TableName() string { return "tbl_user" }

// RoleMenu — which menus a role can see/enter (composite PK).
type RoleMenu struct {
	RoleID  int64  `gorm:"primaryKey" json:"roleId"`
	MenuKey string `gorm:"primaryKey;size:32" json:"menuKey"` // terminal|approve|db|rules|perms|audit|settings
	Enabled bool   `gorm:"not null;default:false" json:"enabled"`
}

func (RoleMenu) TableName() string { return "tbl_role_menu" }

// RoleMember — role↔user membership (many-to-many).
// A user has one *primary* role (tbl_user.role_id, drives auth/capabilities) but
// may appear as a member of several roles in the role view (per prototype).
type RoleMember struct {
	RoleID int64 `gorm:"primaryKey" json:"roleId"`
	UserID int64 `gorm:"primaryKey" json:"userId"`
}

func (RoleMember) TableName() string { return "tbl_role_member" }

// RoleCapability — capability × TIER → level (composite PK).
//
// TierCode holds an EnvTier.Code, never an Environment.Code. The column was
// called `env` until 2026-08-12, from before tiers and environments were separate
// things; the name outlived the meaning and read as though rules were bound to
// environments. They never were — prod-hk and prod-sh are governed by the rows of
// the prod TIER they bind to, which is what makes adding a production cluster
// copy nothing.
type RoleCapability struct {
	RoleID     int64  `gorm:"primaryKey" json:"roleId"`
	Capability string `gorm:"primaryKey;size:32" json:"capability"` // select|write|ddl|grant|conn|approve|explain
	TierCode   string `gorm:"primaryKey;size:16;column:tier_code" json:"tierCode"`
	Level      string `gorm:"size:16;not null" json:"level"` // allow|approve|deny
}

func (RoleCapability) TableName() string { return "tbl_role_capability" }

// Connection — a managed database instance proxied by the gateway.
type Connection struct {
	ID          int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	Name        string    `gorm:"size:64;uniqueIndex:idx_connection_name;not null" json:"name"`
	Engine      string    `gorm:"size:32;not null" json:"engine"`
	Host        string    `gorm:"size:128;not null" json:"host"`
	Port        int       `gorm:"not null" json:"port"`
	// Env holds an Environment.Code, so it must be as wide as one (32). It was
	// sized 16 back when the only legal values were the four built-in strings.
	Env         string    `gorm:"size:32;index:idx_connection_env;not null" json:"env"`
	Policy      string    `gorm:"size:32;not null" json:"policy"` // strict|approve-1|audit-only
	DefaultRole string    `gorm:"size:64" json:"defaultRole"`
	Layer       string    `gorm:"size:64" json:"layer"`
	Username    string    `gorm:"size:64" json:"username"`             // real-execution credentials
	Password    string    `gorm:"size:255" json:"-"`                   // never serialized
	Database    string    `gorm:"column:db_name;size:128" json:"database"` // default schema / sqlite file
	Tags        string    `gorm:"size:255" json:"tags"` // comma-separated labels for group access
	Status      string    `gorm:"size:16;not null;default:online" json:"status"` // online|maint
	CreatedAt   time.Time `json:"createdAt"`
}

func (Connection) TableName() string { return "tbl_connection" }

// Project groups databases and the releases raised against them, so a team can
// follow what is happening to the databases it owns. It is an ORGANISATIONAL
// axis only — see service/project.go for why it deliberately is not a security
// boundary (Connection.Tags already is one).
type Project struct {
	ID          int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	Name        string    `gorm:"size:64;uniqueIndex:uk_project_name;not null" json:"name"`
	Owner       string    `gorm:"size:64" json:"owner"`
	Description string    `gorm:"size:512" json:"description"`
	CreatedBy   int64     `gorm:"not null;default:0" json:"createdBy"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

func (Project) TableName() string { return "tbl_project" }

// DatabaseProject files ONE DATABASE under a project.
//
// The unit is the database, not the connection: one instance routinely hosts
// databases owned by different teams, and hanging the filing on the instance
// would force people to split instances along org lines — org structure
// dictating database topology, which is backwards.
//
// Databases are DISCOVERED, never registered (the tree introspects them live),
// so the key is the NAME as the tree reports it — the same string a release
// records as its target. A row whose database later disappears is harmless:
// nothing lists it, and no lookup matches it.
//
// No row = 未归属, which is a legitimate state rather than a gap to be fixed.
type DatabaseProject struct {
	ID           int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	ConnectionID int64     `gorm:"uniqueIndex:uk_db_project,priority:1;not null" json:"connectionId"`
	Database     string    `gorm:"column:db_name;uniqueIndex:uk_db_project,priority:2;size:128;not null" json:"database"`
	ProjectID    int64     `gorm:"not null;index:idx_db_project_project" json:"projectId"`
	CreatedBy    int64     `gorm:"not null;default:0" json:"createdBy"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

func (DatabaseProject) TableName() string { return "tbl_database_project" }

// Export job / task states.
const (
	ExportPending = "pending"
	ExportRunning = "running"
	ExportDone    = "done"
	ExportFailed  = "failed"
	// ExportExpired — the archive was deleted by the retention sweep. The row
	// STAYS: what was exported, by whom, how many rows and when is the part an
	// auditor asks about, and it outlives the file by design. Only the pointer to
	// the bytes (Files) and the archive password go, because a password guarding
	// something that no longer exists is a liability with no use left.
	ExportExpired = "expired"
)

// ExportJob is one asynchronous data-export task. Multiple run in parallel; the
// result (encrypted archive + password) is recorded here so the user can list
// finished jobs and download them later.
type ExportJob struct {
	ID           int64      `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID       int64      `gorm:"index:idx_export_user;not null" json:"userId"`
	ConnectionID int64      `json:"connectionId"`
	Instance     string     `gorm:"size:96" json:"instance"`
	Database     string     `gorm:"column:db_name;size:128" json:"database"` // target database the export ran against
	SQL          string     `gorm:"type:mediumtext" json:"sql"` // 64KB TEXT rejected long IN-list exports (migration 0018)
	Name         string     `gorm:"size:128" json:"name"`
	Status       string     `gorm:"size:16;not null;default:pending" json:"status"` // pending|running|done|failed
	Rows         int        `json:"rows"`
	Bytes        int64      `json:"bytes"`                              // total encrypted size across parts
	Parts        int        `json:"parts"`                             // number of ~100MB CSV files
	Files        string     `gorm:"type:text" json:"files"`            // newline-joined part paths
	Password     string     `gorm:"size:128" json:"password"`          // holds the AES-encrypted archive password (~68 chars)
	Error        string     `gorm:"size:255" json:"error"`
	CreatedAt    time.Time  `json:"createdAt"`
	FinishedAt   *time.Time `json:"finishedAt"`
}

func (ExportJob) TableName() string { return "tbl_export_job" }

// AsyncJob — a long-running SQL execution run in the background (30–60min+),
// decoupled from the HTTP request so it can't time out. Server progress messages
// (PostgreSQL/DWS RAISE NOTICE) stream into Log as they arrive; submit → poll.
type AsyncJob struct {
	ID           int64      `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID       int64      `gorm:"index:idx_async_user;not null" json:"userId"`
	ConnectionID int64      `json:"connectionId"`
	Instance     string     `gorm:"size:96" json:"instance"`
	Database     string     `gorm:"column:db_name;size:128" json:"database"`
	SQL          string     `gorm:"type:mediumtext" json:"sql"` // see migration 0018
	Reason       string     `gorm:"size:512" json:"reason"`
	Status       string     `gorm:"size:16;not null;default:pending" json:"status"` // pending|running|done|failed
	// Risk is the verdict that authorised this job, captured at submit time. The
	// worker audits when the job finishes — possibly an hour later — and the
	// dictionary may have changed by then, so the level that actually permitted
	// the run is the one worth recording. It used to be hardcoded to "mid" at
	// audit time, which made the field meaningless for filtering (ER7).
	Risk string `gorm:"size:16" json:"risk"` // high|mid|low
	Log  string `gorm:"type:mediumtext" json:"log"` // streamed NOTICE / progress lines
	Rows         int        `json:"rows"`
	Error        string     `gorm:"size:512" json:"error"`
	CreatedAt    time.Time  `json:"createdAt"`
	StartedAt    *time.Time `json:"startedAt"`
	FinishedAt   *time.Time `json:"finishedAt"`
}

func (AsyncJob) TableName() string { return "tbl_async_job" }

// Async job statuses.
const (
	AsyncPending = "pending"
	AsyncRunning = "running"
	AsyncDone    = "done"
	AsyncFailed  = "failed"
)

// ScriptUpload is one uploaded script file, owned by the uploader. Each user
// only sees/manages their own uploads.
type ScriptUpload struct {
	ID        int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID    int64     `gorm:"index:idx_upload_user;not null" json:"userId"`
	Filename  string    `gorm:"size:255;not null" json:"filename"`
	Path      string    `gorm:"size:512" json:"path"`  // server path, shown to the owner
	Size      int64     `json:"size"`
	Source    string    `gorm:"size:16" json:"source"` // upload|terminal
	CreatedAt time.Time `json:"createdAt"`
}

func (ScriptUpload) TableName() string { return "tbl_script_upload" }

// TerminalSnippet is a short SQL script the operator writes in the terminal and
// binds to a hotkey, so a query they run twenty times a day is one keystroke.
// Owned by its author; nobody else lists, runs or edits it.
//
// Slot is the hotkey number 1-9, or 0 for "saved but not bound". A user's bound
// slots are unique — binding a slot that is taken releases the other snippet
// rather than failing, because two snippets answering to the same key would make
// the key mean whichever row the database returned first.
//
// A snippet is NOT a stored decision about whether something may run. It holds
// text and nothing else: pressing the hotkey submits that text through the same
// path as typing it, so the capability matrix, the dictionary and strict mode all
// judge it against the instance it is actually aimed at, at the moment it is
// fired. Judging at save time would freeze a verdict reached on some other
// instance under some earlier version of the rules.
type TerminalSnippet struct {
	ID        int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID    int64     `gorm:"index:idx_snippet_user;not null" json:"userId"`
	Name      string    `gorm:"size:64;not null" json:"name"`
	Body      string    `gorm:"type:text;not null" json:"body"`
	Slot      int       `gorm:"not null;default:0" json:"slot"` // 1-9 hotkey, 0 = unbound
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func (TerminalSnippet) TableName() string { return "tbl_terminal_snippet" }

// Snippet limits. The body cap is deliberately small: this is a shortcut, not a
// migration. Anything bigger belongs in an uploaded file, which is streamed and
// re-read server-side instead of being carried in a row (see service/script_ref).
// The cap counts BYTES, not characters — the column is MySQL TEXT (65,535 bytes)
// and a Chinese comment costs three bytes a character, so a character-based cap
// would accept a snippet the column then truncates.
const (
	MaxSnippetBytes = 8192
	MaxSnippetName  = 64
	MaxSnippetSlot  = 9
	MaxSnippets     = 50 // per user; keeps the list (and the picker) finite
)

// RoleTag grants a role (user group) access to connections carrying the tag.
// A role with no tags is unrestricted (sees every connection).
// UserTag scopes ONE user's data access, overriding the role-derived scope.
//
// Tags are otherwise grants on roles, where "no tags" means unrestricted and the
// most permissive role wins. That composes well for groups but cannot express
// "this particular person": unioned with the role grants, a user-level tag would
// do nothing for anyone whose role is already unrestricted — exactly the people
// most often scoped. So a user-level grant is the MORE SPECIFIC statement and
// replaces the role scope; a user with no rows here keeps the role behaviour.
type UserTag struct {
	UserID int64  `gorm:"primaryKey" json:"userId"`
	Tag    string `gorm:"primaryKey;size:64" json:"tag"`
}

func (UserTag) TableName() string { return "tbl_user_tag" }

type RoleTag struct {
	RoleID int64  `gorm:"primaryKey" json:"roleId"`
	Tag    string `gorm:"primaryKey;size:64" json:"tag"`
}

func (RoleTag) TableName() string { return "tbl_role_tag" }

// RiskCommand — high-risk command dictionary entry (command × TIER → level).
//
// TierCode holds an EnvTier.Code. See RoleCapability for why the column stopped
// being called `env`: matchCommand looks a command up by TIER, and a lookup keyed
// by an environment code would find no rows — which reads as RiskOff, i.e.
// permitted.
type RiskCommand struct {
	Command  string `gorm:"primaryKey;size:32" json:"command"`
	TierCode string `gorm:"primaryKey;size:16;column:tier_code" json:"tierCode"`
	Level    string `gorm:"size:16;not null;default:high" json:"level"` // high|mid|off
}

func (RiskCommand) TableName() string { return "tbl_risk_command" }

// Approval — a high-risk approval ticket.
type Approval struct {
	ID           int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	ApNo         string    `gorm:"size:32;uniqueIndex:idx_approval_apno;not null" json:"apNo"`
	ConnectionID int64     `gorm:"not null" json:"connectionId"`
	// Env and TierCode are a DUAL SNAPSHOT taken when the ticket was raised: where
	// it ran (environment) and what it was judged under (control tier). Both are
	// plain strings with no foreign key, and neither is ever rewritten.
	//
	// Two things they answer that a live lookup cannot: an environment may be
	// rebound to a different tier later, and an instance may be moved to a
	// different environment — after either, resolving the connection today would
	// report a control level that was never the one applied. Rewriting them to
	// match would not be a correction; it would forge the audit trail.
	//
	// TierCode is empty on rows written before this split. Callers show it as
	// unknown rather than inferring one.
	Env          string    `gorm:"size:32;not null" json:"env"`
	TierCode     string    `gorm:"size:16" json:"tierCode"`
	Instance     string    `gorm:"size:64;not null" json:"instance"`
	Command      string    `gorm:"type:mediumtext;not null" json:"command"` // see migration 0018
	// A script approval keeps the script in the file the initiator uploaded and
	// records a REFERENCE to it, not its body: Command then holds a bounded,
	// readable excerpt. The body of a real migration runs to megabytes, and
	// `command` is TEXT — 64KB on MySQL — so storing it outright simply failed;
	// it also dragged that payload through every approvals-list page.
	//
	// ScriptSHA256 is the digest of exactly the bytes that were scanned and
	// reviewed. Execution re-reads the file and refuses unless it still hashes to
	// this, because between review and approval the file on disk can change and
	// nothing else would notice.
	ScriptUploadID int64  `gorm:"not null;default:0" json:"scriptUploadId,omitempty"`
	ScriptSHA256   string `gorm:"size:64" json:"scriptSha256,omitempty"`
	Keyword      string    `gorm:"size:32" json:"keyword"`
	Database     string    `gorm:"column:db_name;size:128" json:"database"` // selected target database
	InitiatorID  int64     `gorm:"index:idx_approval_initiator;not null" json:"initiatorId"`
	Initiator    string    `gorm:"size:64" json:"initiator"`
	Reason       string    `gorm:"size:512" json:"reason"`
	RiskLevel    string    `gorm:"size:16;not null" json:"riskLevel"` // high|mid|low
	Status       string     `gorm:"size:16;index:idx_approval_status;not null;default:pending" json:"status"`
	AuditID      string     `gorm:"size:32" json:"auditId"`
	// External (审批魔方) integration: ExternalTaskID is the vendor's task_id, used
	// by the timeout PATCH write-back. Callback correlation uses our ApNo (echoed
	// back as external_task_id), not this.
	ExternalTaskID string   `gorm:"size:128;index:idx_approval_ext" json:"externalTaskId,omitempty"`
	// ReleaseID links a ticket raised by a release pipeline's approve stage back
	// to its release. It also keeps the ticket OUT of the manual execute path
	// (ExecuteApproved): the pipeline owns the execute stage, and letting someone
	// run it by hand from the approvals page would apply the change twice — once
	// there, once in the pipeline that still believes it has not run yet.
	// Zero for every ordinary ticket.
	ReleaseID int64 `gorm:"not null;default:0;index:idx_approval_release" json:"releaseId,omitempty"`
	Result       string     `gorm:"type:text" json:"result"`     // execution output once approved
	ResultRows   int        `json:"resultRows"`
	Escalated    bool       `gorm:"not null;default:false" json:"-"` // timeout escalation fired once (R13)
	// ExecutedAt 把"批准了"和"跑过了"分成两件事。
	//
	// 审批通过不再顺带执行:命令在审批人点下去的那一刻跑,意味着发起人可能不在
	// 现场,而执行时机(业务低峰、应用是否已停、备份是否就绪)只有他知道。所以
	// 通过之后工单停在这里等发起人来执行,这个字段就是"等"与"跑过了"的分界。
	//
	// 它也是一次性的闸:占住它才允许执行,所以一次批准只换一次执行。
	ExecutedAt   *time.Time `json:"executedAt"`
	DecidedAt    *time.Time `json:"decidedAt"`                   // when approved/rejected
	CreatedAt    time.Time  `json:"createdAt"`
}

func (Approval) TableName() string { return "tbl_approval" }

// ApprovalStep — one node in an approval chain.
type ApprovalStep struct {
	ID         int64      `gorm:"primaryKey;autoIncrement" json:"id"`
	ApprovalID int64      `gorm:"index:idx_step_approval;not null" json:"approvalId"`
	StepOrder  int        `gorm:"not null" json:"stepOrder"`
	ApproverID int64      `gorm:"not null;index:idx_approval_step_approver" json:"approverId"`
	Approver   string     `gorm:"size:64" json:"approver"`
	Status     string     `gorm:"size:16;not null;default:waiting" json:"status"` // waiting|active|approved|rejected
	ActedAt    *time.Time `json:"actedAt"`
}

func (ApprovalStep) TableName() string { return "tbl_approval_step" }

// AuditLog — append-only, hash-chained record of every command.
type AuditLog struct {
	ID           int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	OccurredAt   time.Time `gorm:"index:idx_audit_time" json:"occurredAt"`
	ActorID      int64     `gorm:"index:idx_audit_actor;not null" json:"actorId"`
	ActorName    string    `gorm:"size:64" json:"actor"`
	ConnectionID int64     `json:"connectionId"`
	Instance     string    `gorm:"size:64" json:"instance"`
	// The same dual snapshot the approval carries (see Approval.Env / TierCode),
	// and for the same reason: this row states that a command was judged `high`,
	// and only the tier in force at that moment explains why. Both are covered by
	// the chain hash, so neither can be edited after the fact without breaking it.
	// Empty on rows predating the split.
	Env          string    `gorm:"size:32" json:"env"`
	TierCode     string    `gorm:"size:16" json:"tierCode"`
	Database     string    `gorm:"column:db_name;size:128" json:"database"` // target database the command ran against
	Command      string    `gorm:"type:mediumtext;not null" json:"command"` // full query on purpose (EX5) — see migration 0018
	Risk         string    `gorm:"size:16;index:idx_audit_risk;not null" json:"risk"`   // high|mid|low
	Result       string    `gorm:"size:16;not null" json:"result"`                       // executed|pending|rejected|warn
	ApprovalNo   string    `gorm:"size:32" json:"approvalNo"`
	// Operator is who actually authorised/performed the action when that is not
	// the actor — an external 飞书 approver, or an administrator acting on another
	// user's account. Empty means actor and operator are the same person. Without
	// it an externally-approved command was recorded as if the initiator had
	// simply run it, and the real approver appeared nowhere in the chain (EA4).
	Operator string `gorm:"size:128" json:"operator"`
	// PrevHash carries a UNIQUE index so the hash chain cannot fork at the database
	// layer: two rows can never chain onto the same predecessor, even across
	// connections/processes where the in-process auditMu doesn't reach (A4). The
	// single genesis row uses "" as its predecessor. Matches 0001/0002 SQL.
	PrevHash string `gorm:"type:char(64);uniqueIndex:uk_audit_prev" json:"prevHash"` // fixed-length SHA-256 hex
	Hash     string `gorm:"type:char(64);not null" json:"hash"`
}

func (AuditLog) TableName() string { return "tbl_audit_log" }

// WebhookConfig — audit event push target.
type WebhookConfig struct {
	ID       int64  `gorm:"primaryKey;autoIncrement" json:"id"`
	Endpoint string `gorm:"size:255;not null" json:"endpoint"`
	Secret   string `gorm:"size:128;not null" json:"secret"` // bearer token: sent as `Authorization: Bearer <secret>`

	Events   string `gorm:"size:255;not null" json:"events"` // intercept,approve,exec,login
	RetryMax int    `gorm:"not null;default:5" json:"retryMax"`
	Enabled  bool   `gorm:"not null;default:true" json:"enabled"`
}

func (WebhookConfig) TableName() string { return "tbl_webhook_config" }

// WebhookDelivery — the recorded outcome of one webhook delivery attempt-chain,
// so admins can review delivery results (US#48).
type WebhookDelivery struct {
	ID        int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	Event     string    `gorm:"size:32;not null" json:"event"` // intercept|approve|exec|login|test
	Endpoint  string    `gorm:"size:255" json:"endpoint"`
	Success   bool      `gorm:"not null" json:"success"`
	Status    string    `gorm:"size:128" json:"status"` // e.g. "200 OK" or the error text
	Attempts  int       `gorm:"not null" json:"attempts"`
	CreatedAt time.Time `json:"createdAt"`
}

func (WebhookDelivery) TableName() string { return "tbl_webhook_delivery" }

// SchemaObject — one database.table the gateway exposes for a connection's tree
// (simulated, like the executor; real introspection is out of scope).
type SchemaObject struct {
	ID           int64  `gorm:"primaryKey;autoIncrement" json:"id"`
	ConnectionID int64  `gorm:"index:idx_schema_conn;not null" json:"connectionId"`
	Database     string `gorm:"size:64;not null" json:"database"`
	Tbl          string `gorm:"column:table_name;size:64;not null" json:"table"`
}

func (SchemaObject) TableName() string { return "tbl_schema_object" }

// Setting — generic JSON key/value system settings.
type Setting struct {
	K string `gorm:"primaryKey;size:64" json:"k"`
	V string `gorm:"type:text;not null" json:"v"` // JSON-encoded value
}

func (Setting) TableName() string { return "tbl_setting" }

// ---------------------------------------------------------------- SQL 规范审查

// SQL-review rule levels. `error` blocks a release pipeline, `warn` records the
// finding and lets it through, `info` is advice only.
//
// Level is NOT the same thing as the standard's classification (Spec below):
// level is what a finding costs HERE, classification is how the company's
// written standard grades the requirement. Lowering a rule to `warn` is an
// operational decision; it does not rewrite the standard, and storing the two
// in one column would make it look as though it did.
//
// Level is NOT the same thing as the standard's classification (Spec below):
// level is what a finding costs HERE, classification is how the company's
// written standard grades the requirement. Lowering a rule to `warn` is an
// operational decision; it does not rewrite the standard, and storing the two
// in one column would make it look as though it did.
const (
	ReviewError = "error"
	ReviewWarn  = "warn"
	ReviewInfo  = "info"
)

// Review rule kinds. A builtin rule's logic lives in package review, keyed by
// Code; a regex rule is one an operator wrote in the console and carries its
// pattern in Params.
const (
	ReviewKindBuiltin = "builtin"
	ReviewKindRegex   = "regex"
)

// SQLReviewRule — one entry in the 规范审查规则库.
//
// Dialect is a comma-separated set of dialect codes ("mysql,tidb") or "all". It
// is matched against the dialect derived from the target connection's engine, so
// an Oracle-only naming rule never fires on a TiDB statement — a review that
// reports rules the target database cannot violate trains people to ignore it.
//
// Level is what the finding COSTS, and it is deliberately per rule rather than
// per finding: whether a missing table comment stops a release is a policy
// decision an organisation makes once, not something the checker should decide
// each time it runs.
//
// Params carries the rule's knobs as JSON (a naming pattern, a length cap, a
// list of forbidden types). Builtin rules ship defaults; an empty Params means
// "use the built-in default", never "no constraint".
type SQLReviewRule struct {
	ID        int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	Code      string    `gorm:"size:64;uniqueIndex:idx_review_code;not null" json:"code"`
	Name      string    `gorm:"size:128;not null" json:"name"`
	Dialect   string    `gorm:"size:64;not null;default:all" json:"dialect"`  // all|mysql|tidb|dws|oracle (comma-separated)
	Category  string    `gorm:"size:32;not null" json:"category"`             // naming|structure|index|dml|ddl|security|perf
	Level     string    `gorm:"size:16;not null;default:warn" json:"level"`   // error|warn|info
	// Spec / SpecRef 是**引文**:这条规则出自公司规范的哪一级、哪一节。
	// 空 = 规范未覆盖,是平台自带的防护 —— 标出来,免得有人把它当成规范原文去引用。
	Spec    string `gorm:"size:16;not null;default:''" json:"spec"`     // critical|mandatory|recommended|''
	SpecRef string `gorm:"size:128;not null;default:''" json:"specRef"` // 如 "Huawei DWS 規範 §5.3 分布鍵"
	Kind      string    `gorm:"size:16;not null;default:builtin" json:"kind"` // builtin|regex
	Enabled   bool      `gorm:"not null;default:true" json:"enabled"`
	Params    string    `gorm:"type:text" json:"params"`  // JSON knobs; empty = builtin defaults
	Message   string    `gorm:"size:512" json:"message"`  // what the operator is told when it fires
	SortOrder int       `gorm:"not null;default:0" json:"sortOrder"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func (SQLReviewRule) TableName() string { return "tbl_sql_review_rule" }

// SensitiveColumn — 一条"这张表的这个字段是敏感的"。
//
// 命中的列在**结果离开网关之前**就被打码,回传给前端的数据本身已经是脱敏后的。
// 让前端去打码等于把明文发到浏览器再请它别显示 —— 抓个包就绕过了。
//
// TableName 为 "*" 表示所有表:口令、密钥这类字段在哪张表上都不该被看到。
type SensitiveColumn struct {
	ID         int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	// 字段不能叫 TableName —— 那是 GORM 用来问"这个模型对应哪张表"的方法名。
	Tbl        string    `gorm:"column:table_name;size:128;uniqueIndex:uk_sensitive_col,priority:1;not null" json:"tableName"`
	ColumnName string    `gorm:"size:128;uniqueIndex:uk_sensitive_col,priority:2;not null" json:"columnName"`
	MaskStyle  string    `gorm:"size:16;not null;default:partial" json:"maskStyle"` // partial|full|hash
	Enabled    bool      `gorm:"not null;default:true" json:"enabled"`
	Note       string    `gorm:"size:255" json:"note"`
	CreatedBy  int64     `gorm:"not null;default:0" json:"createdBy"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

func (SensitiveColumn) TableName() string { return "tbl_sensitive_column" }

// ---------------------------------------------------------------- 开放接口 (API clients)

// APIClient is one external system allowed to raise SQL release tickets through
// the open API (a DevOps platform, a CI job, a change-management system).
//
// It is NOT a login. The credential is a key + secret pair, and the client acts
// as a SERVICE ACCOUNT (UserID): the capability matrix, the tag scope, the MFA
// policy and the audit trail are all keyed by a user, so an external caller with
// no subject would be an execution channel nothing could judge. Giving each
// integration its own client also makes revocation and attribution per system
// rather than "someone with the shared secret".
//
// SecretHash stores a bcrypt hash — the secret itself is shown once, at creation,
// and never again; a leaked list of API secrets would be a list of production
// write credentials.
type APIClient struct {
	ID   int64  `gorm:"primaryKey;autoIncrement" json:"id"`
	Name string `gorm:"size:64;not null" json:"name"`
	// Key is the public half, safe to log and to show in the console.
	Key        string `gorm:"size:64;uniqueIndex:idx_apiclient_key;not null" json:"key"`
	SecretHash string `gorm:"size:255;not null" json:"-"`
	UserID     int64  `gorm:"not null" json:"userId"`
	UserName   string `gorm:"size:64" json:"userName"` // display snapshot
	// AllowIPs is a comma-separated IP/CIDR allowlist for THIS client. Empty means
	// any source (the secret still gates it) — a deliberate default, because a CI
	// runner's egress address is often unknown at the time the client is created.
	AllowIPs string `gorm:"size:512" json:"allowIps"`
	// Scopes limits what the credential can do: release:create, release:read,
	// review:check. A read-only integration (a dashboard polling ticket status)
	// should not hold a credential that can raise a production change.
	Scopes     string     `gorm:"size:255;not null" json:"scopes"`
	// PipelineID 绑定这把凭据建单要走的发布流程(网关侧策略,外部请求不可指定;
	// 见 migrations/0023)。0 = 未绑定,走目标分层的默认流程。
	PipelineID int64      `gorm:"not null;default:0" json:"pipelineId"`
	Enabled    bool       `gorm:"not null;default:true" json:"enabled"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
	CreatedBy  int64      `json:"createdBy"`
	CreatedAt  time.Time  `json:"createdAt"`
}

func (APIClient) TableName() string { return "tbl_api_client" }

// The scopes an API client can hold.
const (
	ScopeReleaseCreate = "release:create"
	ScopeReleaseRead   = "release:read"
	ScopeReviewCheck   = "review:check"
)

// AllScopes is what a client gets when none are named.
var AllScopes = []string{ScopeReleaseCreate, ScopeReleaseRead, ScopeReviewCheck}

// Release sources — who raised the ticket.
const (
	ReleaseSourceConsole = "console"
	ReleaseSourceAPI     = "api"
)

// ---------------------------------------------------------------- 发布流水线 (CI/CD)

// Pipeline stage types. Each is a step a release RUN performs; the set is closed
// because every type needs an executor in service/pipeline.go — an unknown type
// would either be skipped (a review nobody ran) or crash the runner.
const (
	StageReview  = "review"  // 规范审查 — run the rule library against the release SQL
	StageApprove = "approve" // 人工审批 — raise an approval ticket and wait for it
	StageBackup  = "backup"  // 备份/回滚点 — run the configured backup SQL
	StageExecute = "execute" // 执行变更 — run the release SQL against the target
	StageVerify  = "verify"  // 执行后校验 — run the configured verification query
	StageManual  = "manual"  // 人工确认 — wait for an operator to continue
	StageNotify  = "notify"  // 通知 — fire the webhook/Lark event
)

// Release + stage run states. `waiting` is distinct from `running`: the pipeline
// is alive but blocked on a HUMAN (an approval ticket, a manual gate), which is
// what the UI has to draw differently from work in progress.
const (
	RunPending = "pending"
	RunRunning = "running"
	RunWaiting = "waiting"
	RunSuccess = "success"
	RunFailed  = "failed"
	RunSkipped = "skipped"
	RunAborted = "aborted"
)

// Stage failure policy: stop the release, or record the failure and carry on.
const (
	OnFailureAbort    = "abort"
	OnFailureContinue = "continue"
)

// Pipeline — a customisable release flow: an ordered list of stages a release
// runs through. Templates are per organisation, not per environment, so the same
// flow can govern several tiers; TierCode narrows a template to one control tier
// when a stricter flow is wanted for production.
type Pipeline struct {
	ID          int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	Name        string    `gorm:"size:128;not null" json:"name"`
	Description string    `gorm:"size:512" json:"description"`
	// TierCode empty = applies to every control tier. Holds an EnvTier.Code, never
	// an Environment.Code (see RoleCapability for why that distinction matters).
	TierCode  string    `gorm:"size:16;index:idx_pipeline_tier" json:"tierCode"`
	Enabled   bool      `gorm:"not null;default:true" json:"enabled"`
	IsDefault bool      `gorm:"not null;default:false" json:"isDefault"`
	CreatedBy int64     `json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func (Pipeline) TableName() string { return "tbl_pipeline" }

// PipelineStage — one stage DEFINITION in a template.
//
// Config is per-type JSON: {"failOn":"error"} for review, {"sql":"…"} for
// backup/verify, {"roleId":2} for approve, {"note":"…"} for manual.
type PipelineStage struct {
	ID         int64  `gorm:"primaryKey;autoIncrement" json:"id"`
	PipelineID int64  `gorm:"index:idx_pstage_pipeline;not null" json:"pipelineId"`
	StepOrder  int    `gorm:"not null" json:"stepOrder"`
	Name       string `gorm:"size:64;not null" json:"name"`
	Type       string `gorm:"size:16;not null" json:"type"`
	Config     string `gorm:"type:text" json:"config"`
	OnFailure  string `gorm:"size:16;not null;default:abort" json:"onFailure"`
}

func (PipelineStage) TableName() string { return "tbl_pipeline_stage" }

// Release — one execution of a pipeline against one instance: the CI/CD unit.
//
// The pipeline NAME and the stage list are snapshotted into the run
// (ReleaseStage rows) when it starts, for the same reason Approval snapshots its
// env/tier: a template edited next month must not rewrite what this release
// actually did. Env/TierCode are the same dual snapshot.
//
// SQL is the change being released. A release whose body is an uploaded script
// carries ScriptUploadID + ScriptSHA256 instead and re-reads the file at execute
// time, exactly like a script approval.
type Release struct {
	ID       int64  `gorm:"primaryKey;autoIncrement" json:"id"`
	RelNo    string `gorm:"size:32;uniqueIndex:idx_release_relno;not null" json:"relNo"`
	Title    string `gorm:"size:128;not null" json:"title"`
	PipelineID   int64  `gorm:"not null" json:"pipelineId"`
	PipelineName string `gorm:"size:128" json:"pipelineName"` // snapshot
	ConnectionID int64  `gorm:"not null" json:"connectionId"`
	Instance     string `gorm:"size:96" json:"instance"`
	Database     string `gorm:"column:db_name;size:128" json:"database"`
	Env          string `gorm:"size:32" json:"env"`
	TierCode     string `gorm:"size:16" json:"tierCode"`
	Engine       string `gorm:"size:32" json:"engine"` // snapshot: which dialect it was reviewed as
	// ChangeType 是变更类型:dml(数据订正)或 ddl(结构变更)。提交时声明或由
	// 内容推断,两类语句不得同单 —— 审批人按类型评估风险(DDL 锁表、DML 影响
	// 行数),混装让两种评估都失效。见 service.releaseChangeType 的分类口径。
	ChangeType   string `gorm:"size:8" json:"changeType"`
	// 归属项目,提交时从目标库快照 —— 库以后改挂别的项目,历史单据不跟着改账。
	ProjectID    int64  `gorm:"not null;default:0;index:idx_release_project" json:"projectId"`
	ProjectName  string `gorm:"size:64" json:"projectName"`
	SQL          string `gorm:"type:mediumtext" json:"sql"`
	ScriptUploadID int64  `gorm:"not null;default:0" json:"scriptUploadId,omitempty"`
	ScriptSHA256   string `gorm:"size:64" json:"scriptSha256,omitempty"`
	Reason       string `gorm:"size:512" json:"reason"`
	CreatorID    int64  `gorm:"index:idx_release_creator;not null" json:"creatorId"`
	Creator      string `gorm:"size:64" json:"creator"`
	// Source says which door the ticket came in by, and ClientName snapshots WHICH
	// external system raised it. The creator is the service account the client
	// acts as — true, but not the whole truth, and the audit rows say
	// "actor=svc-devops, operator=API:DevOps 平台" precisely so the trail names the
	// system that asked as well as the identity it borrowed.
	Source     string `gorm:"size:16;not null;default:console" json:"source"` // console|api
	ClientID   int64  `gorm:"not null;default:0" json:"clientId,omitempty"`
	ClientName string `gorm:"size:64" json:"clientName,omitempty"`
	// ExternalRef is the caller's own ticket id (a change number, a CI build id),
	// carried through so both systems can talk about the same release.
	ExternalRef string `gorm:"size:128;index:idx_release_extref" json:"externalRef,omitempty"`
	// IdemKey is "<clientID>:<externalRef>" and exists only to carry a UNIQUE
	// index: an external caller retries, and a retry that raised a SECOND release
	// would apply the same change twice. It is a POINTER so an absent key is NULL
	// rather than "", because NULLs do not collide in a unique index while empty
	// strings do — every console release would otherwise be a duplicate of the
	// first one. Uniqueness is enforced by the database, not by a check-then-insert
	// that two concurrent retries can both pass.
	IdemKey *string `gorm:"size:160;uniqueIndex:uk_release_idem" json:"-"`
	Status       string `gorm:"size:16;index:idx_release_status;not null;default:pending" json:"status"`
	// Risk is the gateway verdict captured when the release was submitted — the
	// same field AsyncJob carries, and for the same reason: the dictionary may
	// change between submit and execute, and the level that authorised the run is
	// the one worth recording.
	Risk       string     `gorm:"size:16" json:"risk"`
	Error      string     `gorm:"size:512" json:"error"`
	CreatedAt  time.Time  `json:"createdAt"`
	StartedAt  *time.Time `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt"`
}

func (Release) TableName() string { return "tbl_release" }

// ReleaseStage — one stage of one run. This is what the pipeline view draws.
//
// Findings holds the review stage's JSON result so the operator can see WHY a
// release was stopped without re-running the check against a rule library that
// may have changed since.
type ReleaseStage struct {
	ID        int64  `gorm:"primaryKey;autoIncrement" json:"id"`
	ReleaseID int64  `gorm:"index:idx_rstage_release;not null" json:"releaseId"`
	StepOrder int    `gorm:"not null" json:"stepOrder"`
	Name      string `gorm:"size:64;not null" json:"name"`
	Type      string `gorm:"size:16;not null" json:"type"`
	Config    string `gorm:"type:text" json:"config"`
	OnFailure string `gorm:"size:16;not null;default:abort" json:"onFailure"`
	Status    string `gorm:"size:16;not null;default:pending" json:"status"`
	Log       string `gorm:"type:mediumtext" json:"log"`
	Findings  string `gorm:"type:mediumtext" json:"findings"`
	// ApprovalID/ApNo link an approve stage to the ticket it is waiting on. The
	// sweeper reads them to resume the run once the ticket is decided, which is
	// why the link lives on the stage and not only in the log.
	// ConfirmedBy 是 execute 阶段人工闸的放行人(空 = 未确认,阶段到达即停)。
	// 审批回答"可不可以做",这里回答"现在做" —— 见 migrations/0024。
	ConfirmedBy string `gorm:"size:64;not null;default:''" json:"confirmedBy"`
	ApprovalID int64      `gorm:"index:idx_rstage_approval" json:"approvalId"`
	ApprovalNo string     `gorm:"size:32" json:"approvalNo"`
	Rows       int        `json:"rows"`
	StartedAt  *time.Time `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt"`
}

func (ReleaseStage) TableName() string { return "tbl_release_stage" }

// Notification kinds delivered to a user's in-app inbox.
const (
	NotifApprovalApproved = "approval-approved"
	NotifApprovalRejected = "approval-rejected"
	NotifApprovalExpired  = "approval-expired"
	// Release pipeline outcomes, delivered to the release creator.
	// 异步导出跑完(或失败)也要通知。它和审批、发布是同一类事:人提交完就走了,
	// 结果什么时候出来他不知道,不主动告诉他,就只能靠他自己想起来回去刷一下。
	NotifExportDone   = "export-done"
	NotifExportFailed = "export-failed"

	NotifReleaseDone   = "release-done"
	NotifReleaseFailed = "release-failed"
	NotifReleaseWait   = "release-waiting"
)

// Notification — an in-app message to a single user (e.g. their approval was
// decided). Read state is per-notification. Column is is_read because `read` is
// a MySQL reserved word.
type Notification struct {
	ID        int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID    int64     `gorm:"index:idx_notif_user;not null" json:"userId"`
	Type      string    `gorm:"size:32;not null" json:"type"` // approval-approved|approval-rejected|approval-expired
	Title     string    `gorm:"size:128;not null" json:"title"`
	Body      string    `gorm:"size:512" json:"body"`
	RefNo     string    `gorm:"size:32" json:"refNo"` // related ticket, e.g. AP-2295
	Read      bool      `gorm:"column:is_read;not null;default:false" json:"read"`
	CreatedAt time.Time `json:"createdAt"`
}

func (Notification) TableName() string { return "tbl_notification" }
