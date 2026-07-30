// Package model holds the GORM data models (tbl_ prefixed, no physical FKs —
// relational constraints are maintained at the application layer, per backend doc §9).
package model

import "time"

// Env values used across capability matrix, risk dictionary and connections.
const (
	EnvProd    = "prod"
	EnvStaging = "staging"
	EnvGli     = "gli" // 灰度 — mirrors staging's risk/capability tier
	EnvDev     = "dev"
)

// Capability matrix levels.
const (
	LevelAllow   = "allow"
	LevelApprove = "approve"
	LevelDeny    = "deny"
)

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
type User struct {
	ID           int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	Name         string    `gorm:"size:64;not null" json:"name"`
	Email        string    `gorm:"size:128;uniqueIndex:idx_user_email;not null" json:"email"`
	RoleID       int64     `gorm:"index:idx_user_role;not null" json:"roleId"`
	Status       string    `gorm:"size:16;not null;default:active" json:"status"` // active|disabled|invited
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

// RoleCapability — capability × env → level (composite PK).
type RoleCapability struct {
	RoleID     int64  `gorm:"primaryKey" json:"roleId"`
	Capability string `gorm:"primaryKey;size:32" json:"capability"` // select|write|ddl|grant|conn|approve
	Env        string `gorm:"primaryKey;size:16" json:"env"`        // prod|staging|dev
	Level      string `gorm:"size:16;not null" json:"level"`        // allow|approve|deny
}

func (RoleCapability) TableName() string { return "tbl_role_capability" }

// Connection — a managed database instance proxied by the gateway.
type Connection struct {
	ID          int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	Name        string    `gorm:"size:64;uniqueIndex:idx_connection_name;not null" json:"name"`
	Engine      string    `gorm:"size:32;not null" json:"engine"`
	Host        string    `gorm:"size:128;not null" json:"host"`
	Port        int       `gorm:"not null" json:"port"`
	Env         string    `gorm:"size:16;index:idx_connection_env;not null" json:"env"`
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

// Export job / task states.
const (
	ExportPending = "pending"
	ExportRunning = "running"
	ExportDone    = "done"
	ExportFailed  = "failed"
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
	SQL          string     `gorm:"type:text" json:"sql"`
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
	SQL          string     `gorm:"type:text" json:"sql"`
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

// RoleTag grants a role (user group) access to connections carrying the tag.
// A role with no tags is unrestricted (sees every connection).
type RoleTag struct {
	RoleID int64  `gorm:"primaryKey" json:"roleId"`
	Tag    string `gorm:"primaryKey;size:64" json:"tag"`
}

func (RoleTag) TableName() string { return "tbl_role_tag" }

// RiskCommand — high-risk command dictionary entry (command × env → level).
type RiskCommand struct {
	Command string `gorm:"primaryKey;size:32" json:"command"`
	Env     string `gorm:"primaryKey;size:16" json:"env"`
	Level   string `gorm:"size:16;not null;default:high" json:"level"` // high|mid|off
}

func (RiskCommand) TableName() string { return "tbl_risk_command" }

// Approval — a high-risk approval ticket.
type Approval struct {
	ID           int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	ApNo         string    `gorm:"size:32;uniqueIndex:idx_approval_apno;not null" json:"apNo"`
	ConnectionID int64     `gorm:"not null" json:"connectionId"`
	Env          string    `gorm:"size:16;not null" json:"env"`
	Instance     string    `gorm:"size:64;not null" json:"instance"`
	Command      string    `gorm:"type:text;not null" json:"command"`
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
	Result       string     `gorm:"type:text" json:"result"`     // execution output once approved
	ResultRows   int        `json:"resultRows"`
	Escalated    bool       `gorm:"not null;default:false" json:"-"` // timeout escalation fired once (R13)
	DecidedAt    *time.Time `json:"decidedAt"`                   // when approved/rejected
	CreatedAt    time.Time  `json:"createdAt"`
}

func (Approval) TableName() string { return "tbl_approval" }

// ApprovalStep — one node in an approval chain.
type ApprovalStep struct {
	ID         int64      `gorm:"primaryKey;autoIncrement" json:"id"`
	ApprovalID int64      `gorm:"index:idx_step_approval;not null" json:"approvalId"`
	StepOrder  int        `gorm:"not null" json:"stepOrder"`
	ApproverID int64      `gorm:"not null" json:"approverId"`
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
	Database     string    `gorm:"column:db_name;size:128" json:"database"` // target database the command ran against
	Command      string    `gorm:"type:text;not null" json:"command"`
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

// Notification kinds delivered to a user's in-app inbox.
const (
	NotifApprovalApproved = "approval-approved"
	NotifApprovalRejected = "approval-rejected"
	NotifApprovalExpired  = "approval-expired"
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
