// Package dto defines request/response payloads (the front/back contract).
package dto

import (
	"time"

	"velagateway/internal/model"
)

// ---- Auth ----

type LoginReq struct {
	Email    string `json:"email" binding:"required"`
	Password string `json:"password" binding:"required"`
	MfaCode  string `json:"mfaCode"` // TOTP code for an MFA-enrolled user (second step)
}

type LoginResp struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expiresAt"`
	User      MeResp `json:"user"`
}

// MeResp is returned by /auth/me — user + menus + capabilities.
type MeResp struct {
	ID           int64                        `json:"id"`
	Name         string                       `json:"name"`
	Email        string                       `json:"email"`
	Initials     string                       `json:"initials"`
	RoleID       int64                        `json:"roleId"` // primary role (display/JWT)
	RoleCode     string                       `json:"roleCode"`
	RoleName     string                       `json:"roleName"`
	Layer        string                       `json:"layer"`
	RoleIDs      []int64                      `json:"roleIds"` // every role held (union permissions)
	RoleNames    []string                     `json:"roleNames"`
	RoleCodes    []string                     `json:"roleCodes"` // codes of all held roles (e.g. contains "admin")
	CanApprove   bool                         `json:"canApprove"`
	MfaEnabled   bool                         `json:"mfaEnabled"`
	Menus        map[string]bool              `json:"menus"`
	Capabilities map[string]map[string]string `json:"capabilities"` // cap -> env -> level
}

// ---- Risk check ----

type RiskCheckReq struct {
	ConnectionID int64  `json:"connectionId" binding:"required"`
	SQL          string `json:"sql" binding:"required"`
	// Database 是这条命令要落到的库。判定本身不看它,但执行窗口按库开,不传就没法
	// 判断窗口是否覆盖 —— 预检会说"要审批",执行却放行,终端于是先弹一个多余的
	// 审批理由框。传了才对得上。
	Database string `json:"database"`
}

type RiskCheckResp struct {
	Risk             string `json:"risk"`   // high|mid|low
	Action           string `json:"action"` // allow|approve|deny
	RequiresApproval bool   `json:"requiresApproval"`
	MatchedRule      string `json:"matchedRule"`
	// MatchedRuleRef 是同一条规则的机器可读身份,给客户端按界面语言渲染用。
	// MatchedRule 仍然是规范中文串,也是认不出 code 时的回落。
	MatchedRuleRef *model.RuleRef `json:"matchedRuleRef,omitempty"`
	Command          string `json:"command"`
	ApprovalNo       string `json:"approvalNo,omitempty"`
	AuditID          string `json:"auditId,omitempty"`
}

// ---- Terminal exec ----

type ExecReq struct {
	ConnectionID int64  `json:"connectionId" binding:"required"`
	SQL          string `json:"sql" binding:"required"`
	Reason       string `json:"reason"`
	MfaCode      string `json:"mfaCode"`  // TOTP step-up for PROD ops when MFA is required
	Database     string `json:"database"` // selected target database within the instance
}

type ExecResp struct {
	Intercepted bool   `json:"intercepted"`
	ApprovalNo  string `json:"approvalNo,omitempty"`
	AuditID     string `json:"auditId,omitempty"`
	Risk        string `json:"risk"`
	Rule        string         `json:"rule,omitempty"` // matched rule text (why it was intercepted)
	RuleRef     *model.RuleRef `json:"ruleRef,omitempty"`
	Output string `json:"output,omitempty"`
	// OutputRef 是 Output 那句话的机器可读身份。Output 仍然是规范记录(进审计、
	// 进工单的 Result),OutputRef 只给界面用读者的语言重讲一遍 —— 认不出的 code
	// 回落到 Output,那是降级不是出错。与 Rule/RuleRef 同一套机制,见 model.RuleRef。
	OutputRef *model.RuleRef `json:"outputRef,omitempty"`
	Rows      int            `json:"rows,omitempty"`
	Ms        int            `json:"ms,omitempty"`
	// Columns/Data carry the real result set for a read (Data capped for the
	// terminal); empty for a simulated connection, where the client synthesises a
	// preview from Rows. Truncated marks a result set larger than the display cap.
	Columns   []string   `json:"columns,omitempty"`
	Data      [][]string `json:"data,omitempty"`
	Truncated bool       `json:"truncated,omitempty"`
}

// ---- Data export ----

type ExportReq struct {
	ConnectionID int64  `json:"connectionId" binding:"required"`
	SQL          string `json:"sql" binding:"required"`
	Name         string `json:"name"`
	Database     string `json:"database"` // target database within the instance (optional)
	// IncludeSensitive 要的是敏感字段的**原值**。默认 false = 照常打码。
	//
	// 勾上它不会让这次导出立刻跑:任务停在 awaiting 等审批,批准之后才由 worker
	// 后台执行。一份带原值的 CSV 出了网关就再也管不到了,放开它要有人签字。
	IncludeSensitive bool `json:"includeSensitive"`
}

// ExportResp describes an encrypted+compressed export archived on the server.
type ExportResp struct {
	File     string `json:"file"`     // absolute server path
	Filename string `json:"filename"` // basename (for the download)
	Password string `json:"password"` // AES password (shown once to the operator)
	Rows     int    `json:"rows"`
	Bytes    int64  `json:"bytes"` // encrypted archive size
}

// ---- Script scan ----

// TranscriptExportReq records that a terminal session log was written to a file.
//
// The FILE is built in the browser from what was already displayed; this request
// exists only so the act is audited. It therefore carries a description of the
// export, never the transcript itself — shipping the whole session back to be
// stored would create the second copy the audit row is meant to keep track of.
type TranscriptExportReq struct {
	ConnectionID int64  `json:"connectionId" binding:"required"`
	Filename     string `json:"filename"`
	Lines        int    `json:"lines"`
	// Dropped > 0 means the session outran the client buffer and the file starts
	// mid-session. Recorded so the audit row does not imply a complete record.
	Dropped  int    `json:"dropped"`
	Database string `json:"database"`
}

// SnippetReq creates or updates one terminal snippet. Slot is the hotkey 1-9,
// or 0 for "saved but unbound" — so it is NOT `binding:"required"`, which would
// reject the zero value and make unbinding a hotkey impossible to express.
type SnippetReq struct {
	Name string `json:"name"`
	Body string `json:"body"`
	Slot int    `json:"slot"`
}

type ScriptScanReq struct {
	ConnectionID int64 `json:"connectionId"`
	// Content is optional when UploadID names an already-uploaded script: the
	// gateway reads that file itself and ignores anything sent here. It was
	// `binding:"required"`, which made "the file is already on the server" an
	// impossible request to express.
	Content  string `json:"content"`
	Filename string `json:"filename"`
	MfaCode  string `json:"mfaCode"`
	UploadID int64  `json:"uploadId"` // >0 = already-uploaded script; don't save again
	Database string `json:"database"` // selected target database within the instance
}

type ScannedStmt struct {
	Index   int    `json:"index"`
	SQL     string `json:"sql"`
	Command string `json:"command"`
	Risk    string `json:"risk"` // high|mid|safe
	NoWhere bool   `json:"noWhere"`
}

type ScriptScanResp struct {
	Filename   string        `json:"filename"`
	Total      int           `json:"total"`
	High       int           `json:"high"`
	Mid        int           `json:"mid"`
	Safe       int           `json:"safe"`
	HasRisky   bool          `json:"hasRisky"`
	Statements []ScannedStmt `json:"statements"`
}

// ---- Connections ----

type ConnectionCreateReq struct {
	Name     string `json:"name" binding:"required"`
	Engine   string `json:"engine" binding:"required"`
	Host     string `json:"host" binding:"required"` // host:port
	Env      string `json:"env" binding:"required"`  // prod|staging|dev
	Policy   string `json:"policy" binding:"required"`
	Username string `json:"username"` // real-execution credentials (optional)
	Password string `json:"password"`
	Database string `json:"database"` // default schema / sqlite file
}

// ConnectionUpdateReq edits an existing instance. Name/Engine/Host/Env are required
// (the edit form pre-fills them); Password empty means "keep the stored password".
type ConnectionUpdateReq struct {
	Name     string `json:"name" binding:"required"`
	Engine   string `json:"engine" binding:"required"`
	Host     string `json:"host" binding:"required"`
	Env      string `json:"env" binding:"required"`
	Policy   string `json:"policy" binding:"required"`
	Username string `json:"username"`
	Password string `json:"password"` // empty = keep existing
	Database string `json:"database"`
}

type ConnectionStatusReq struct {
	Status string  `json:"status"`           // online|maint (empty = toggle)
	Tags   *string `json:"tags,omitempty"`   // when present, replace the connection's tags
	Policy *string `json:"policy,omitempty"` // when present, set the gateway policy
}

// RoleTagsReq assigns the DB tags a role (user group) may access.
type RoleTagsReq struct {
	Tags []string `json:"tags"`
}

// EnvTierCreateReq creates a control tier. TemplateCode is required: a tier
// without rule rows is an environment where every lookup falls through to
// "allowed", so the rules are cloned from an existing tier in the same
// transaction that creates it.
type EnvTierCreateReq struct {
	Code            string `json:"code"`
	DisplayName     string `json:"displayName"`
	TemplateCode    string `json:"templateCode"`
	SortOrder       int    `json:"sortOrder"`
	RequireMFA      bool   `json:"requireMfa"`
	DangerBanner    bool   `json:"dangerBanner"`
	CountsInPending bool   `json:"countsInPending"`
	ScanBaseline    bool   `json:"scanBaseline"`
	StrictNoWhere   bool   `json:"strictNoWhere"`
	ConnLayer       string `json:"connLayer"`
	DefaultRole     string `json:"defaultRole"`
}

// EnvTierUpdateReq edits a tier. The code is immutable — it is the key the rule
// rows and every environment carry.
type EnvTierUpdateReq struct {
	DisplayName     string `json:"displayName"`
	SortOrder       int    `json:"sortOrder"`
	RequireMFA      bool   `json:"requireMfa"`
	DangerBanner    bool   `json:"dangerBanner"`
	CountsInPending bool   `json:"countsInPending"`
	ScanBaseline    bool   `json:"scanBaseline"`
	StrictNoWhere   bool   `json:"strictNoWhere"`
	ConnLayer       string `json:"connLayer"`
	DefaultRole     string `json:"defaultRole"`
}

// ExecWindowReq 建/改一个执行窗口(「班车」)。
//
// Kind 决定哪几个字段有意义:
//   once      —— StartsAt / EndsAt(绝对时刻,不看时区)
//   recurring —— Timezone + Weekdays + StartMin/EndMin,NotAfter 可选
// 校验在 service.fillExecWindow,从严:写坏的窗口要么白配,要么开在没预料的时间,
// 而后者是安全问题,所以说不清的定义一律拒绝。
type ExecWindowReq struct {
	Name         string `json:"name"`
	Enabled      bool   `json:"enabled"`
	ConnectionID int64  `json:"connectionId"`
	Database     string `json:"database"`
	Kind         string `json:"kind"`
	Timezone     string `json:"timezone"`

	StartsAt *time.Time `json:"startsAt,omitempty"`
	EndsAt   *time.Time `json:"endsAt,omitempty"`

	Weekdays string     `json:"weekdays"`
	StartMin int        `json:"startMin"`
	EndMin   int        `json:"endMin"`
	NotAfter *time.Time `json:"notAfter,omitempty"`

	Reason string `json:"reason"`
}

// EnvironmentCreateReq adds an instance group on an existing tier. Nothing is
// cloned — the tier already owns the rules.
type EnvironmentCreateReq struct {
	Code        string `json:"code"`
	DisplayName string `json:"displayName"`
	TierCode    string `json:"tierCode"`
	SortOrder   int    `json:"sortOrder"`
}

// EnvironmentUpdateReq edits an environment, including rebinding it to another
// tier. Rebinding governs future commands only; history keeps its own snapshot.
type EnvironmentUpdateReq struct {
	DisplayName string `json:"displayName"`
	TierCode    string `json:"tierCode"`
	SortOrder   int    `json:"sortOrder"`
}

// EnvironmentDeleteReq removes an environment, moving its instances to MoveTo.
// The target is mandatory: an instance pointing at a code that no longer exists
// resolves to no tier, and therefore to no rules at all.
type EnvironmentDeleteReq struct {
	MoveTo string `json:"moveTo"`
}

// ConnectionSchemaResp is the db→table tree of a connection. For a connection with
// real credentials it is introspected live; Error carries the reason when live
// introspection fails (empty tree otherwise) so the UI can explain the empty state.
type ConnectionSchemaResp struct {
	ConnectionID int64         `json:"connectionId"`
	Databases    []SchemaDBDTO `json:"databases"`
	Error        string        `json:"error,omitempty"`
}

type SchemaDBDTO struct {
	Name    string            `json:"name"`
	// 归属项目 —— 组织维度,判定层不看。0 = 未归属,是合法状态。
	ProjectID   int64  `json:"projectId,omitempty"`
	ProjectName string `json:"projectName,omitempty"`
	Schemas []SchemaSchemaDTO `json:"schemas,omitempty"` // database → schema → tables (PostgreSQL)
	Tables  []SchemaTableDTO  `json:"tables"`            // database → tables (flat engines)
}

// SchemaSchemaDTO is a schema within a database and its tables.
type SchemaSchemaDTO struct {
	Name   string           `json:"name"`
	Tables []SchemaTableDTO `json:"tables"`
}

type SchemaTableDTO struct {
	Name string `json:"name"`
}

// DbObjectsResp lists one database's (or schema's/owner's) programmable objects
// grouped by kind; kinds an engine lacks stay empty. Error mirrors
// ConnectionSchemaResp: the reason live introspection failed, for the tree to show.
type DbObjectsResp struct {
	Functions  []string `json:"functions"`
	Procedures []string `json:"procedures"`
	Packages   []string `json:"packages"`
	Triggers   []string `json:"triggers"`
	Error      string   `json:"error,omitempty"`
}

// ObjectSourceResp carries one programmable object's source text.
type ObjectSourceResp struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	Source string `json:"source"`
}

// ---- Roles ----

type RoleDetailResp struct {
	ID        int64                        `json:"id"`
	Code      string                       `json:"code"`
	Name      string                       `json:"name"`
	Layer     string                       `json:"layer"`
	Icon      string                       `json:"icon"`
	Menus     map[string]bool              `json:"menus"`
	Matrix    map[string]map[string]string `json:"matrix"` // cap -> env -> level
	Members   []MemberDTO                  `json:"members"`
	MemberIDs []int64                      `json:"memberIds"`
	Tags      []string                     `json:"tags"` // DB tags this group may access
}

type MemberDTO struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Initials string `json:"initials"`
	Dept     string `json:"dept"`
	// Kind 让调用方分得出真人与服务账号 —— 服务账号登录不了控制台,不能审批,
	// 发布流程编辑器据此把它排除在"将由谁审批"之外(与后端 decidableApprovers 同口径)。
	Kind string `json:"kind"`
}

// ProjectReq creates or edits a project. Owner/Description are pointers so an
// edit that only renames cannot blank the fields it never mentioned.
type ProjectReq struct {
	Name        string  `json:"name"`
	Owner       *string `json:"owner"`
	Description *string `json:"description"`
}

// DatabaseProjectReq files one of a connection's databases under a project.
// ProjectID 0 取消归属 —— 它是合法取值,不是"没填"。
type DatabaseProjectReq struct {
	Database  string `json:"database"`
	ProjectID int64  `json:"projectId"`
}

// SensitiveColumnReq 定义一条敏感字段规则。TableName 留空按"所有表"处理。
type SensitiveColumnReq struct {
	TableName  string `json:"tableName"`
	ColumnName string `json:"columnName"`
	MaskStyle  string `json:"maskStyle"` // partial|full|hash;空 = partial
	Enabled    bool   `json:"enabled"`
	Note       string `json:"note"`
}

type SensitiveColumnView struct {
	ID         int64     `json:"id"`
	TableName  string    `json:"tableName"`
	ColumnName string    `json:"columnName"`
	MaskStyle  string    `json:"maskStyle"`
	Enabled    bool      `json:"enabled"`
	Note       string    `json:"note"`
	CreatedAt  time.Time `json:"createdAt"`
}

type ProjectView struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Owner       string    `json:"owner"`
	Description string    `json:"description"`
	Databases   int       `json:"databases"`
	Releases    int       `json:"releases"`
	CreatedAt   time.Time `json:"createdAt"`
}

type RoleUpdateReq struct {
	Name            string `json:"name"`
	Description     string `json:"description"`
	DefaultConnRole string `json:"defaultConnRole"`
	CanApprove      *bool  `json:"canApprove"`
}

type MenusReq struct {
	Menus map[string]bool `json:"menus" binding:"required"`
}

type CapabilitiesReq struct {
	Matrix map[string]map[string]string `json:"matrix" binding:"required"`
}

type MemberReq struct {
	UserID int64 `json:"userId" binding:"required"`
}

// ---- Risk commands ----

type RiskCommandUpsertReq struct {
	Command string `json:"command" binding:"required"`
	// Keyed by TIER code, not environment: the dictionary is per control tier, and
	// prod-hk inherits prod's rows rather than owning any. Levels the caller omits
	// are left as they are on tiers that already have a row, and filled with a
	// default on tiers that have none — see Repo.UpsertRiskCommand.
	Tiers map[string]string `json:"tiers" binding:"required"` // tier -> high|mid|off
}

type RiskCommandPatchReq struct {
	Tier  string `json:"tier" binding:"required"` // tier code
	Level string `json:"level" binding:"required"`
}

// ---- Approvals ----

type ApprovalActionReq struct {
	Comment string `json:"comment"`
}

// ---- Users ----

type UserPatchReq struct {
	Status  string  `json:"status"`
	RoleID  *int64  `json:"roleId"`  // set the single primary role (kept for compatibility)
	RoleIDs []int64 `json:"roleIds"` // replace the full role set (multi-role); first is primary
}

type InviteReq struct {
	Email  string `json:"email" binding:"required"`
	RoleID int64  `json:"roleId" binding:"required"`
}

// UserCreateReq provisions an active account from the admin console with an
// initial password and one or more roles (first is the primary role).
type UserCreateReq struct {
	Email    string  `json:"email" binding:"required"`
	Name     string  `json:"name"`
	Password string  `json:"password" binding:"required"`
	RoleIDs  []int64 `json:"roleIds" binding:"required"`
}

// AdminPasswordReq carries a new password for an admin-driven reset.
type AdminPasswordReq struct {
	Password string `json:"password" binding:"required"`
}

// ---- Settings / Webhook ----

// ---- View payloads ----

type RoleBrief struct {
	ID    int64  `json:"id"`
	Code  string `json:"code"`
	Name  string `json:"name"`
	Layer string `json:"layer"`
	Icon  string `json:"icon"`
	Count int    `json:"count"`
}

type UserView struct {
	ID            int64    `json:"id"`
	Name          string   `json:"name"`
	Email         string   `json:"email"`
	Initials      string   `json:"initials"`
	Dept          string   `json:"dept"`
	Roles         []string `json:"roles"`
	RoleIDs       []int64  `json:"roleIds"` // every role held (for the multi-role editor)
	PrimaryRoleID int64    `json:"primaryRoleId"`
	Status        string   `json:"status"`
	Kind          string   `json:"kind"` // human|service — 服务账号在用户列表里要能一眼认出来
	MFAEnabled    bool     `json:"mfaEnabled"`
	LastActive    string   `json:"lastActive"`
	// 这一行现在能不能被停用,以及不能的话为什么。由服务端算好带下来(与 canDecide /
	// canExecute 同一套做法):否则"确认停用林伟?"→点确定→"不能停用自己"这个序列
	// 会让人白白确认一次,而前端自己判一遍,两份判断迟早会不一致。
	// 启用方向永远允许,所以这两个字段只对还没停用的账户有意义。
	CanDisable   bool   `json:"canDisable"`
	DisableBlock string `json:"disableBlock"`
}

type RiskCommandView struct {
	Command string `json:"command"`
	// tier code -> high|mid|off. Named for what it holds: the previous name said
	// `env` and every reader concluded rules were bound to environments.
	Tiers map[string]string `json:"tiers"`
}

// AsyncSubmitResp is the result of submitting a long-running SQL for background
// execution: a jobId on the allow path, or an approval ticket when the command
// is gated (same three-layer verdict as a normal exec).
type AsyncSubmitResp struct {
	JobID       int64  `json:"jobId,omitempty"`
	Intercepted bool   `json:"intercepted,omitempty"`
	ApprovalNo  string `json:"approvalNo,omitempty"`
	Risk        string         `json:"risk,omitempty"`
	Rule        string         `json:"rule,omitempty"`
	RuleRef     *model.RuleRef `json:"ruleRef,omitempty"`
	// Output carries a terminal-style notice when the submission was accepted but
	// nothing was queued — e.g. the instance is in maintenance (ER6). Mirrors how
	// the synchronous exec path reports the same restriction.
	Output string `json:"output,omitempty"`
}

// LarkApprovalCallbackReq is the payload审批魔方 POSTs to our callback_url when a
//飞书 approval reaches a terminal state. Correlation uses ExternalTaskID, which
// echoes back the ApNo we sent. `approved` (bool) is the decision; `approver` is
// the list of飞书 accounts that acted (any one is enough — OR semantics).
type LarkApprovalCallbackReq struct {
	TaskID         string   `json:"task_id"`
	Approved       bool     `json:"approved"`
	Reason         string   `json:"reason"`
	MessageID      string   `json:"message_id"` // real Lark message id (om_...)
	RequestID      string   `json:"request_id"`
	ExternalTaskID string   `json:"external_task_id"` // == our ApNo (correlation key)
	Question       string   `json:"question"`
	User           string   `json:"user"`
	AiGroup        string   `json:"ai_group"`
	Approver       []string `json:"approver"`
	UpdatedAt      string   `json:"updated_at"`
}

type WebhookConfigReq struct {
	Endpoint string `json:"endpoint"`
	Secret   string `json:"secret"` // bearer token sent as `Authorization: Bearer <secret>`; empty keeps the stored one
	// Events is the authoritative subscribed-event-type list (comma-separated, e.g.
	// "exec,login"). A pointer so an explicit empty list ("subscribe to nothing")
	// is distinguishable from the field being omitted (keep the stored list).
	Events   *string `json:"events"`
	RetryMax int     `json:"retryMax"`
	Enabled  bool    `json:"enabled"`
}

// ---- MFA (TOTP) ----

// MFASetupResp is returned when a user begins enrollment: the shared secret and
// an otpauth:// URI to render as a QR code. Enrollment completes via /auth/mfa/enable.
type MFASetupResp struct {
	Secret     string `json:"secret"`
	OtpauthURI string `json:"otpauthUri"`
}

// MFACodeReq carries a 6-digit TOTP code for enable/disable.
type MFACodeReq struct {
	Code string `json:"code"`
}

// ---- Notifications ----

// NotificationsResp is the inbox payload: recent items + current unread count.
type NotificationsResp struct {
	Items  any   `json:"items"` // []model.Notification
	Unread int64 `json:"unread"`
}

// MarkReadReq marks notifications read; empty IDs marks all of the user's.
type MarkReadReq struct {
	IDs []int64 `json:"ids"`
}

// CompileReq asks to recompile one Oracle stored program. Scope is the owner /
// schema; Type is package|procedure|function|trigger|type.
type CompileReq struct {
	Scope    string `json:"scope"`
	Type     string `json:"type"`
	Name     string `json:"name"`
	Database string `json:"database"`
}

// RecompileReq 批量重编译一个 schema 下的无效对象。
//
// Names 为空 = 该 schema 下**全部**无效对象;非空 = 只编这几个(界面上勾选的情形)。
// 区分这两者是有意义的:全量重编译是变更窗口里的动作,勾选几个是定点修复。
type RecompileReq struct {
	Scope    string   `json:"scope"`
	Database string   `json:"database"`
	Names    []string `json:"names"`
}
