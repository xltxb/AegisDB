// Package dto defines request/response payloads (the front/back contract).
package dto

import (
	"time"

	"aegisdb/internal/model"
)

// ---- Auth ----

type LoginReq struct {
	Email    string `json:"email" binding:"required"`
	Password string `json:"password" binding:"required"`
	MfaCode  string `json:"mfaCode"` // TOTP code for an MFA-enrolled user (second step)
}

type LoginResp struct {
	// Token:非控制台客户端用的 JWT(Authorization: Bearer)。控制台走 HttpOnly Cookie,这里是空的。
	Token string `json:"token"`
	// Session:控制台的会话标记,不是凭据(见 middleware.SetSessionCookies)。
	Session   string `json:"session,omitempty"`
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

	// CanSeeAllReleases:发布页露不露「全部」切换,与 ListReleases 同一判断
	// (canSeeAllReleases),前端不再自己拿角色码拼。
	CanSeeAllReleases bool `json:"canSeeAllReleases"`
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
	Command        string         `json:"command"`
	ApprovalNo     string         `json:"approvalNo,omitempty"`
	AuditID        string         `json:"auditId,omitempty"`
	// Notice:"生产大表直接 DDL,可能长时间锁表"之类要特别留意的提示(终端先给人看见再提交)。
	Notice    string         `json:"notice,omitempty"`
	NoticeRef *model.RuleRef `json:"noticeRef,omitempty"`
}

// ---- Terminal exec ----

type ExecReq struct {
	ConnectionID int64  `json:"connectionId" binding:"required"`
	SQL          string `json:"sql" binding:"required"`
	Reason       string `json:"reason"`
	MfaCode      string `json:"mfaCode"`  // TOTP step-up for PROD ops when MFA is required
	Database     string `json:"database"` // selected target database within the instance
	// ServerOutput:终端开了 SET SERVEROUTPUT ON(Oracle)。开着时执行后读回 DBMS_OUTPUT。
	ServerOutput bool `json:"serverOutput"`
	// SessionID:终端标签页的会话标识。同一标签的语句都在它独占的一条连接上(gateway/term_session.go)。
	SessionID string `json:"sessionId"`
	// Binds:标签页上声明过的 SQL*Plus 绑定变量与当前值(Oracle),见 gateway/oracle_binds.go。
	Binds []model.BindVar `json:"binds,omitempty"`
}

// ScriptStmtResult:i 脚本里一条语句的执行结果,终端按顺序逐条打印 —— 与 mysql 客户端 source
// 一样,每条都看得到「影响了几行 / 查出了什么」(2026-10 用户要求)。SQL 是语句开头的一段,
// 只用来让人认出是哪一条。查询结果每条最多带回 gateway 的一屏上限(maxResultRows),不进
// 「加载更多」的暂存:一份脚本可能有几十个结果集。
type ScriptStmtResult struct {
	Pos int    `json:"pos"`
	SQL string `json:"sql"`
	ExecResp
}

type ExecResp struct {
	Intercepted bool           `json:"intercepted"`
	ApprovalNo  string         `json:"approvalNo,omitempty"`
	AuditID     string         `json:"auditId,omitempty"`
	Risk        string         `json:"risk"`
	Rule        string         `json:"rule,omitempty"` // matched rule text (why it was intercepted)
	RuleRef     *model.RuleRef `json:"ruleRef,omitempty"`
	Output      string         `json:"output,omitempty"`
	// OutputRef 是 Output 那句话的机器可读身份。Output 仍然是规范记录(进审计、
	// 进工单的 Result),OutputRef 只给界面用读者的语言重讲一遍 —— 认不出的 code
	// 回落到 Output,那是降级不是出错。与 Rule/RuleRef 同一套机制,见 model.RuleRef。
	OutputRef *model.RuleRef `json:"outputRef,omitempty"`
	// Notice 是要特别留意的一句话(目前只有"生产大表直接 DDL,可能长时间锁表"),NoticeRef 给界面按语言显示。
	Notice    string         `json:"notice,omitempty"`
	NoticeRef *model.RuleRef `json:"noticeRef,omitempty"`
	// OSC:这条命令被改成了在线变更工单(.scratch/osc-product);OSCBlockers:前置检查的阻塞项(没生成工单)。
	OSC         *OSCInfo  `json:"osc,omitempty"`
	OSCBlockers []OSCItem `json:"oscBlockers,omitempty"`
	Rows        int       `json:"rows,omitempty"`
	Ms          int       `json:"ms,omitempty"`
	// 耗时分段(毫秒):取连接 / 库端执行 / 读取结果,见 gateway.ExecResult.ConnMs
	ConnMs  int `json:"connMs,omitempty"`
	QueryMs int `json:"queryMs,omitempty"`
	FetchMs int `json:"fetchMs,omitempty"`
	// 行读完之后网关自己的处理(敏感字段打码)
	ProcessMs int `json:"processMs,omitempty"`
	// Columns/Data carry the real result set for a read (Data capped for the
	// terminal); empty for a simulated connection, where the client synthesises a
	// preview from Rows. Truncated marks a result set larger than the display cap.
	Columns   []string   `json:"columns,omitempty"`
	Data      [][]string `json:"data,omitempty"`
	// Binds:执行后绑定变量的新值(PL/SQL 改了它们);没有绑定时为空。
	Binds []model.BindVar `json:"binds,omitempty"`
	Truncated bool       `json:"truncated,omitempty"`
	// 分批显示:还有后续行时给结果编号,取下一批走 GET /terminal/results/:id;TotalRows 读到的总行数;
	// BeyondCap 超过了网关一次读取的上限(真实总数更多,要全部请用数据导出)。
	ResultID  string `json:"resultId,omitempty"`
	TotalRows int    `json:"totalRows,omitempty"`
	BeyondCap bool   `json:"beyondCap,omitempty"`
	// ServerOutput:读回的 DBMS_OUTPUT 行(终端开了 SET SERVEROUTPUT ON,Oracle)。
	ServerOutput          []string `json:"serverOutput,omitempty"`
	ServerOutputTruncated bool     `json:"serverOutputTruncated,omitempty"`
	// Failed:目标库拒绝了这条语句(含 Oracle 建存储程序编译失败)。脚本据此停在这一条,而不是照样计数报成功。
	Failed bool `json:"failed,omitempty"`
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
	// Format:csv(默认)| sql。TargetTable 是 SQL 格式时 INSERT 写入的表,留空则从查询的
	// FROM 识别(联表、子查询识别不出,必须填)。
	Format      string `json:"format"`
	TargetTable string `json:"targetTable"`
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
	// SessionID:终端标签页的会话标识。脚本里的安全语句在这个标签独占的连接上跑(gateway/term_session.go)。
	SessionID string `json:"sessionId"`
	// ProgressID:前端生成的进度标识。带上时,扫描 / 执行逐条记进度,前端轮询 GET /scripts/progress/:id 画进度条。
	ProgressID string `json:"progressId"`
}

// SessionPolicy:所有登录用户可读的会话策略(空闲锁定),见 service.SessionPolicy。
type SessionPolicy struct {
	IdleLock          bool `json:"idleLock"`
	IdleMinutes       int  `json:"idleMinutes"` // 已按会话时长封顶
	SessionTTLMinutes int  `json:"sessionTtlMinutes"`
	// AdminIdleMinutes:管理后台空闲多少分钟退回用户后台(工单 09;后端同一口径见 middleware.AdminIdleMinutes)
	AdminIdleMinutes int `json:"adminIdleMinutes"`
}

// TermSessionCloseReq:关掉一个终端标签页时释放它的数据库连接。
type TermSessionCloseReq struct {
	SessionID string `json:"sessionId" binding:"required"`
}

type ScannedStmt struct {
	Index   int    `json:"index"`
	SQL     string `json:"sql"`
	Command string `json:"command"`
	Risk    string `json:"risk"` // high|mid|safe
	NoWhere bool   `json:"noWhere"`
	// Action 是这条语句对**当前用户、目标实例**的完整判定(能力矩阵 + 字典 + 无 WHERE +
	// 执行窗口):allow | approve | deny。Rule 是那条判定的规则文本(非 allow 时)。
	Action string `json:"action,omitempty"`
	Rule   string `json:"rule,omitempty"`
}

type ScriptScanResp struct {
	Filename   string        `json:"filename"`
	Total      int           `json:"total"`
	High       int           `json:"high"`
	Mid        int           `json:"mid"`
	Safe       int           `json:"safe"`
	HasRisky   bool          `json:"hasRisky"`
	Statements []ScannedStmt `json:"statements"`
	// Blocked:脚本里有语句被判定为禁止 —— 整份脚本不执行、也不送审。BlockedRule 说明是哪条。
	Blocked     bool   `json:"blocked,omitempty"`
	BlockedRule string `json:"blockedRule,omitempty"`
	// BlockedRuleRef:BlockedRule 的机器可读身份(apiMsg,规则本身嵌成 ruleText 子句),界面按读者语言显示
	BlockedRuleRef *model.RuleRef `json:"blockedRuleRef,omitempty"`
	// Window:有语句本该审批、因执行窗口(班车)生效而放行时,是哪个窗口。HasRisky 此时为 false ——
	// 它说的是"要不要送审",不是"有没有高危语句"(高危条数仍在 High 里)。
	Window string `json:"window,omitempty"`
}

// ---- Connections ----

type ConnectionCreateReq struct {
	Name     string `json:"name" binding:"required"`
	Engine   string `json:"engine" binding:"required"`
	Host     string `json:"host" binding:"required"` // host:port
	Env      string `json:"env" binding:"required"`  // prod|staging|dev
	// Policy 不参与判定,界面上已拿掉;可以不传(新建落 strict)。传了仍校验。
	Policy   string `json:"policy"`
	Username string `json:"username"` // real-execution credentials (optional)
	Password string `json:"password"`
	Database string `json:"database"` // default schema / sqlite file
	// Port 单独给端口(可选):host 里没带端口时合并进去;两处都给且不一致时拒绝。
	// 从前这两个字段被静默忽略 —— 传了 port 照样落成 3306(工单 02 第 10 条)。
	Port *int `json:"port,omitempty"`
	// Tags 建库时一并打标签(逗号分隔),与 PATCH {tags} 同一套规整。
	Tags string `json:"tags"`
}

// ConnectionUpdateReq edits an existing instance. Name/Engine/Host/Env are required
// (the edit form pre-fills them); Password empty means "keep the stored password".
type ConnectionUpdateReq struct {
	Name     string `json:"name" binding:"required"`
	Engine   string `json:"engine" binding:"required"`
	Host     string `json:"host" binding:"required"`
	Env      string `json:"env" binding:"required"`
	// Policy 不参与判定;不传 = 保持原值。
	Policy   string `json:"policy"`
	Username string `json:"username"`
	Password string `json:"password"` // empty = keep existing
	Database string `json:"database"`
	// 从库(只读副本,业务方数据查询用)。ReplicaHost 不传 = 不动;传空串 = 去掉从库;
	// host 或 host:port(不带端口沿用主库端口)。账号留空沿用主库账号密码;
	// ReplicaPassword 留空 = 保持已存的。
	ReplicaHost     *string `json:"replicaHost,omitempty"`
	ReplicaUsername string  `json:"replicaUsername"`
	ReplicaPassword string  `json:"replicaPassword"`
}

type ConnectionStatusReq struct {
	Status string  `json:"status"`           // online|maint (empty = toggle)
	Tags   *string `json:"tags,omitempty"`   // when present, replace the connection's tags
	Policy *string `json:"policy,omitempty"` // when present, set the gateway policy
	// BizQuery 在场时设置「业务查询源」标记(见 model.Connection.BizQuery)。
	BizQuery *bool `json:"bizQuery,omitempty"`
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
	// 不带 = 沿用模板分层的值(老客户端、表单没这两项时不会悄悄建出一个不脱敏的分层)
	MaskSensitive     *bool  `json:"maskSensitive"`
	BizExportRestrict *bool  `json:"bizExportRestrict"`
	// 同上:不带 = 沿用模板分层(从 dev 复制出来的分层默认也允许自动执行,从 prod 复制的不允许)
	AutoExecute *bool `json:"autoExecute"`
	ConnLayer       string `json:"connLayer"`
	DefaultRole     string `json:"defaultRole"`
}

// EnvTierUpdateReq edits a tier. The code is immutable — it is the key the rule
// rows and every environment carry.
//
// 局部更新:每个字段都是指针,缺席 = 保持原值(工单 02 第 9 条)。从前只传名称会把所有开关
// 清成 false —— 一次改名顺手关掉了生产的 MFA 与横幅,而且没人会发现。
type EnvTierUpdateReq struct {
	DisplayName     *string `json:"displayName"`
	SortOrder       *int    `json:"sortOrder"`
	RequireMFA      *bool   `json:"requireMfa"`
	DangerBanner    *bool   `json:"dangerBanner"`
	CountsInPending *bool   `json:"countsInPending"`
	ScanBaseline    *bool   `json:"scanBaseline"`
	StrictNoWhere   *bool   `json:"strictNoWhere"`
	MaskSensitive     *bool   `json:"maskSensitive"`
	BizExportRestrict *bool   `json:"bizExportRestrict"`
	AutoExecute       *bool   `json:"autoExecute"`
	ConnLayer       *string `json:"connLayer"`
	DefaultRole     *string `json:"defaultRole"`
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
	// Databases:同一台实例上要放行的库(可多选);老客户端只传 Database(单个库)也认。
	Database     string   `json:"database"`
	Databases    []string `json:"databases"`
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

// WindowStatement 是窗口期里下发过的一条语句 —— 回看一扇门"那几个小时里到底跑了
// 什么"时,列表上的一行。
//
// 它是审计行的一个投影,不是审计行本身:这一页要的是语句内容与谁跑的,而哈希、前哈希、
// 分层快照那些是审计页的事。
type WindowStatement struct {
	AuditID    int64     `json:"auditId"` // 指回审计行,点得过去
	OccurredAt time.Time `json:"occurredAt"`
	Actor      string    `json:"actor"`
	Command    string    `json:"command"` // 原文(密码字面量已在入库时抹掉)
	Risk       string    `json:"risk"`
	Result     string    `json:"result"`     // executed | warn | simulated
	DataAction string    `json:"dataAction"` // 增删改查;空 = 迁移 0040 之前的行
	ApprovalNo string    `json:"approvalNo,omitempty"`
	// ByWindow:这一条是**这扇门放行的**(本来要审批)。判据是审计行的 Operator 指回
	// 本窗口,那是执行当时写进哈希链的,事后改不了 —— 不是这里按时间反推的。
	//
	// 其余的行只是"恰好在窗口期里跑的":低风险语句本来就不用审批,窗口开不开都一样。
	// 两者混在一起会让人把一扇门的责任看得比实际大。
	ByWindow bool `json:"byWindow"`
}

// WindowStatements 是一扇门的回看结果。
type WindowStatements struct {
	WindowID int64             `json:"windowId"`
	Items    []WindowStatement `json:"items"`
	// ByWindow 是其中被这扇门放行的条数 —— 这一页真正要回答的那个数。
	ByWindow int `json:"byWindow"`
	// Truncated:扫描到了上限,更早的窗口期没看。界面要说出来 —— 一个被截断的列表
	// 看起来和"就这么多"一模一样。
	Truncated bool `json:"truncated"`
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
	Name string `json:"name"`
	// 归属项目 —— 组织维度,判定层不看。0 = 未归属,是合法状态。
	ProjectID   int64             `json:"projectId,omitempty"`
	ProjectName string            `json:"projectName,omitempty"`
	Schemas     []SchemaSchemaDTO `json:"schemas,omitempty"` // database → schema → tables (PostgreSQL)
	Tables      []SchemaTableDTO  `json:"tables"`            // database → tables (flat engines)
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
	// 编辑表单要回显的三项:从前不回传,前端只能按 code 猜「可审批」,保存时会把自定义角色悄悄改掉
	Description     string `json:"description"`
	DefaultConnRole string `json:"defaultConnRole"`
	CanApprove      bool   `json:"canApprove"`
	// Deletable:自定义角色(经「新建角色」建的)才能删,界面据此决定出不出删除按钮
	Deletable bool `json:"deletable"`
	Menus     map[string]bool              `json:"menus"`
	Matrix    map[string]map[string]string `json:"matrix"` // cap -> env -> level
	Members   []MemberDTO                  `json:"members"`
	MemberIDs []int64                      `json:"memberIds"`
	Tags      []string                     `json:"tags"` // DB tags this group may access
	// NoInstances:还没选标签、也不开放任何实例(新建角色的初始状态)。Tags 为空且它为 false 才是"不限"。
	NoInstances bool `json:"noInstances,omitempty"`
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

// RoleCreateReq:管理后台新建角色。code 由服务端生成,不让人填(见 service.CreateRole)。
type RoleCreateReq struct {
	Name            string `json:"name"`
	Layer           string `json:"layer"`
	Description     string `json:"description"`
	DefaultConnRole string `json:"defaultConnRole"`
	CanApprove      bool   `json:"canApprove"`
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

// ChangePasswordReq is the self-service password change (POST /auth/password).
// oldPassword 不设 required:空的旧口令只会被当成"原密码错误",没有本地口令的账号则先得到那句明确的拒绝。
type ChangePasswordReq struct {
	OldPassword string `json:"oldPassword"`
	NewPassword string `json:"newPassword" binding:"required"`
}

// ChangePasswordResp carries the re-issued token for the current session — every
// other session of the user is revoked by the change.
type ChangePasswordResp struct {
	Token     string `json:"token"`
	Session   string `json:"session,omitempty"` // 控制台的会话标记,见 LoginResp
	ExpiresAt string `json:"expiresAt"`
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
	// 已绑定公司 SSO:只能经 SSO 登录(管理员除外)
	SSOBound    bool   `json:"ssoBound"`
	SSOUserName string `json:"ssoUserName,omitempty"`
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
	JobID       int64          `json:"jobId,omitempty"`
	Intercepted bool           `json:"intercepted,omitempty"`
	ApprovalNo  string         `json:"approvalNo,omitempty"`
	Risk        string         `json:"risk,omitempty"`
	Rule        string         `json:"rule,omitempty"`
	RuleRef     *model.RuleRef `json:"ruleRef,omitempty"`
	// Output carries a terminal-style notice when the submission was accepted but
	// nothing was queued — e.g. the instance is in maintenance (ER6). Mirrors how
	// the synchronous exec path reports the same restriction.
	Output string `json:"output,omitempty"`
	// OutputRef:Output 的机器可读身份(同 ExecResp.OutputRef)。
	OutputRef *model.RuleRef `json:"outputRef,omitempty"`
}

type WebhookConfigReq struct {
	Endpoint string `json:"endpoint"`
	Secret   string `json:"secret"` // bearer token sent as `Authorization: Bearer <secret>`; empty keeps the stored one
	// Events is the authoritative subscribed-event-type list (comma-separated, e.g.
	// "exec,login"). A pointer so an explicit empty list ("subscribe to nothing")
	// is distinguishable from the field being omitted (keep the stored list).
	Events *string `json:"events"`
	// Envs 是推送的环境代码(逗号分隔),空串 = 全部环境;省略这个字段 = 保留原来的。
	Envs     *string `json:"envs"`
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
	// MfaCode 是生产步进验证的那一次码。编译会真的改变库上对象的状态,与终端执行
	// 同级,所以它走同一道 MFA 闸(L-7)。
	MfaCode string `json:"mfaCode"`
}

// PackageDownloadReq 下载一个或多个 Oracle 程序包。Scope 是包所在的 owner;Names 一个
// 给 .pck,多个打成 zip。
type PackageDownloadReq struct {
	Scope    string   `json:"scope"`
	Database string   `json:"database"`
	Names    []string `json:"names"`
}

// RecompileReq 批量重编译一个 schema 下的无效对象。
//
// Names 为空 = 该 schema 下**全部**无效对象;非空 = 只编这几个(界面上勾选的情形)。
// 区分这两者是有意义的:全量重编译是变更窗口里的动作,勾选几个是定点修复。
type RecompileReq struct {
	// MfaCode:与单个编译同一道生产步进闸(L-7)。批量重编译改的对象更多,没有理由
	// 比单个编译宽松。
	MfaCode  string   `json:"mfaCode"`
	Scope    string   `json:"scope"`
	Database string   `json:"database"`
	Names    []string `json:"names"`
}

// OSCInfo 是终端改道成在线变更工单时的说明。
type OSCInfo struct {
	JobID    int64     `json:"jobId"`
	Table    string    `json:"table"`
	Rows     int64     `json:"rows"`
	Bytes    int64     `json:"bytes"`
	Warnings []OSCItem `json:"warnings,omitempty"`
}

// OSCItem 是在线变更前置检查的一条阻塞 / 警告:Code 给界面按语言显示,Reason 是规范中文。
type OSCItem struct {
	Code   string `json:"code"`
	Reason string `json:"reason"`
}
