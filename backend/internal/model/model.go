// Package model holds the GORM data models (tbl_ prefixed, no physical FKs —
// relational constraints are maintained at the application layer, per backend doc §9).
package model

import (
	"strings"
	"time"

	"gorm.io/gorm"

	"velagateway/pkg/crypto"
)

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
	// StrictNoWhere gates a DELETE / UPDATE carrying no WHERE — the third
	// judgement layer, after the capability matrix and the high-risk dictionary.
	//
	// It lives HERE, on the tier, because the other two layers do. It used to be
	// one process-wide flag, which made it the only layer an estate could not aim:
	// dev deliberately sets its whole dictionary to `off` so a developer can empty
	// a scratch table, yet strict mode still graded that DELETE high and demanded
	// an approval, and the only way to stop it was to switch the layer off for
	// PROD as well.
	// The column name is pinned rather than inflected: GORM turns StrictNoWhere
	// into `strict_no_where`, while migration 0030 (the authoritative schema on
	// MySQL) adds `strict_nowhere`. Left to the inflector the two schema paths
	// disagree, and the MySQL side then reads a column GORM never writes.
	//
	// 这里与下面两个开关都不带 GORM 的 default:true(库里的 DEFAULT 在迁移里):GORM 建行时
	// 会把零值 false 换成标签默认值,"建一个关着的开关"会存成开着的。任何布尔字段都不许
	// 带非零默认值,见 bootstrap.TestModels_NoNonZeroBoolDefaults。
	StrictNoWhere bool `gorm:"column:strict_nowhere;not null" json:"strictNoWhere"`
	// ScanBaseline marks the REFERENCE tier — the strictest one, held by exactly
	// one tier. Two things read it: a newly added dictionary command prefills at
	// the strictest level here (defaultRiskLevel), and the tier cannot be deleted.
	//
	// It no longer governs script scanning. That used to be its whole job, and it
	// meant a script bound for UAT was graded by PROD's dictionary — the report
	// described a run that was never going to happen. Scanning now uses the target
	// instance's own tier (see Services.ScanScript).
	ScanBaseline bool `gorm:"not null;default:false" json:"scanBaseline"`
	// MaskSensitive:这一层上的敏感字段要不要脱敏(终端结果、导出、数据查询,以及数据查询里"改名
	// 绕过脱敏"的拦截)。BizExportRestrict:数据查询的导出要不要受全局开关与授权「允许导出」约束。
	// 两者默认开,dev / uat 默认关(2026-10-29,迁移 0056)—— 开发、演练库上的数据不需要这层保护,
	// 打码只碍事。列名钉死,理由同 strict_nowhere。
	MaskSensitive     bool `gorm:"column:mask_sensitive;not null" json:"maskSensitive"`
	BizExportRestrict bool `gorm:"column:biz_export_restrict;not null" json:"bizExportRestrict"`
	// AutoExecute:这一层上的发布流程能不能用「自动执行」阶段(StageAutoExecute)—— 前序阶段通过就
	// 直接下发,不等人点「确认执行」。默认关(迁移 0058):新建的、自定义的分层都按生产对待,
	// 只有 dev / uat 回填成开。保存流程时与执行时各查一次(见 service.tierAllowsAutoExecute)。
	AutoExecute bool `gorm:"column:auto_execute;not null;default:false" json:"autoExecute"`

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
	// StatusCancelled —— 发起人自己撤回的。
	//
	// 它和 rejected 刻意分开:驳回是审批人看过之后说"不行",是一次留在记录里的决定;
	// 撤回是发起人说"这张不用了",没有人对它做过判断。合成一个状态的话,记录上就看
	// 不出到底有没有人拒绝过什么 —— 而那正是这条记录存在的意义。
	StatusCancelled = "cancelled"

	// 一次批准换来的那一次下发,库那边收没收下。见 Approval.ExecStatus —— 它和上面
	// 四个是两回事:跑挂了不会把一张已批准的工单变回没批准。
	ExecStatusSuccess = "success"
	ExecStatusFailed  = "failed"
	// ExecStatusRunning:已经占住、命令正在库上跑,还没有结论。占位与它**同一条 UPDATE**
	// 写下(ClaimApprovalExecution),所以不存在"有执行时间、却说不清在不在跑"的空档 ——
	// 从前界面只能拿 executed_at 判断,点下执行的那一刻就显示成了"已执行"。
	ExecStatusRunning = "running"

	ResultExecuted = "executed"
	// ResultSimulated:这条命令**根本没有接触任何数据库** —— 它走的是模拟路径
	// (没有凭据的连接,且当前允许模拟;见 gateway/simulation.go)。
	//
	// 单独一个值,而不是记成 executed:审计链要回答的是"谁在什么时候对哪台库执行了
	// 什么",一条从未发生的执行记成"已执行",是在这条链上写下一句假话 —— 而这条链
	// 的全部价值就在于它不说假话。也不记成 warn:没有任何东西出错。
	//
	// 生产环境不会出现这个值(模拟一律被拒),它只可能来自开发/演示环境。
	ResultSimulated = "simulated"
	ResultPending   = "pending"
	ResultRejected = "rejected"
	// 工单被发起人撤回。与 rejected 分开,理由同 StatusCancelled。
	ResultCancelled = "cancelled"
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
	// MFASecret 是 base32 的 TOTP 种子,**静态加密**后入库(L-5)。
	//
	// 它和连接口令是同一类东西:拿到它就能给自己算出对方的第二因子,而第二因子的全部
	// 意义在于"只有本人算得出"。明文存着,等于把 MFA 降级成一个稍长一点的口令字段 ——
	// 任何一条读到 tbl_user 的路径(备份、只读从库、一次 SELECT *)都能把它带走。
	//
	// 读写经由 AfterFind / UpdateUserMFA 自动加解密,业务代码拿到的一直是明文。
	// 列宽 255:密文是 enc:v2:(旧的 enc:v1:)+ base64(nonce+密文+tag),比 32 位的 base32 长得多。
	MFASecret    string    `gorm:"size:255" json:"-"`
	MFALastCtr   int64     `gorm:"not null;default:0" json:"-"`                     // last consumed TOTP counter (anti-replay, M3)
	TokenVersion int64     `gorm:"not null;default:0" json:"-"`                     // session generation; bumped to revoke tokens (M1)
	// SessionID 是**这一个 token** 的 jti,请求期才有,永不入库(与 ClientIP 同一个
	// 路子:由中间件在鉴权时盖上)。TokenVersion 回答"这一代会话还算不算数",它回答
	// "是哪一个会话" —— MFA 宽限期要的是后者(M-6)。
	SessionID string `gorm:"-" json:"-"`
	// SessionAuth / SessionLoginAt:这一个会话的登录方式("sso" 或空)与登录时刻(token 的 iat),
	// 与 SessionID 同样请求期由鉴权中间件盖上、永不入库。老 token 没有登录方式 = 不算 SSO 会话。
	SessionAuth    string    `gorm:"-" json:"-"`
	SessionLoginAt time.Time `gorm:"-" json:"-"`
	PasswordHash string    `gorm:"size:255" json:"-"`
	Dept         string    `gorm:"size:64" json:"dept"`
	// SSO 绑定(迁移 0042):管理员按邮箱与 SSO 名单核对过的账号。绑定后只能走 SSO 登录,
	// 管理员除外(口令留作应急)。见 service/sso_bind.go。
	SSOBound    bool       `gorm:"column:sso_bound;not null;default:false" json:"ssoBound"`
	SSOUserName string     `gorm:"column:sso_user_name;size:128;not null;default:''" json:"ssoUserName"`
	SSOBoundAt  *time.Time `gorm:"column:sso_bound_at" json:"ssoBoundAt,omitempty"`
	Initials     string    `gorm:"size:8" json:"initials"`
	LastActive   string    `gorm:"size:32" json:"lastActive"`
	CreatedAt    time.Time `json:"createdAt"`
	// ClientIP 是**这一次请求**的来源地址,不是这个人的属性 —— 所以它既不入库
	// (gorm:"-")也不出接口(json:"-")。
	//
	// 它搭这班车,是因为审计行要记下 IP,而写审计的地方在 service 深处(60 多个调用
	// 点),离 *gin.Context 很远。鉴权中间件每个请求都新取一份 User 对象,把 IP 挂在
	// 这份对象上,写审计时顺手就取到了 —— 比给 60 多个调用点各加一个参数要少得多的
	// 改动面,也不必让 service 去认识 HTTP。
	//
	// 正因为它是请求级的,**不要**把带着它的 User 存回库:gorm:"-" 保证了即使存也不
	// 会落到表上。
	ClientIP string `gorm:"-" json:"-"`
	// AuditSource 是**这一次请求从哪个入口进来**(数据查询 / Web 命令行 / 开放接口……),
	// 与 ClientIP 同一个路子:请求级、不入库、不出接口,由 middleware.BindRequestUser 按
	// 路由盖上(见 AuditSourceForRoute)。写审计时 appendAudit 从这里取,除非调用点自己
	// 说了来源(导出任务、发布阶段、审批这类入口与路由无关的,见 recordAuditFrom)。
	AuditSource string `gorm:"-" json:"-"`
}

// AfterFind 把库里的 TOTP 密文还原成明文,业务代码因此不必知道它是加密存的(L-5)。
//
// 解不开时**清空**而不是原样留下:留下一段密文,ValidateWithCounter 会拿它当种子去算,
// 于是用户的码永远对不上,而错误信息是"验证码错误" —— 一个说谎的提示。清空则退化成
// "未启用 MFA"(checkMFA 的既有语义),管理员可以用 mfa/reset 把人救回来。
//
// 空值与老的明文密钥原样返回:DecryptSecret 对没有 enc: 前缀的输入直接放行,这正是
// 升级路径 —— 存量明文行照常可用,下一次 UpdateUserMFA 时自动转成密文。
func (u *User) AfterFind(*gorm.DB) error {
	if u.MFASecret == "" {
		return nil
	}
	plain, err := crypto.DecryptSecret(u.MFASecret)
	if err != nil {
		u.MFASecret = ""
		return nil
	}
	u.MFASecret = plain
	return nil
}

func (User) TableName() string { return "tbl_user" }

// RoleMenu — which menus a role can see/enter (composite PK).
type RoleMenu struct {
	RoleID  int64  `gorm:"primaryKey" json:"roleId"`
	MenuKey string `gorm:"primaryKey;size:32" json:"menuKey"` // terminal|approve|db|rules|perms|audit|settings
	Enabled bool   `gorm:"not null;default:false" json:"enabled"`
}

func (RoleMenu) TableName() string { return "tbl_role_menu" }

// AdminOnlyMenus 是只有平台管理员角色能有的菜单(管理后台拆分,.scratch/admin-user-split 工单 03):
// 实例配置、风险规则、分层、权限、系统设置。别的角色不能勾,存量的在启动时收回。
//
// 不在其列的:pipeline(同时管着用户后台的发布单;流水线编排页由管理后台入口挡住)、
// audit(审计员要用,普通用户的「我的操作记录」也挂在它下面)、project(项目页留在用户后台)。
var AdminOnlyMenus = []string{"db", "rules", "envtier", "perms", "settings"}

// IsAdminOnlyMenu 说明一个菜单键是否只给平台管理员。
func IsAdminOnlyMenu(key string) bool {
	for _, k := range AdminOnlyMenus {
		if k == key {
			return true
		}
	}
	return false
}

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
// Connection.Status 的两个取值。
//
// 它是**人为标的维护态**,与"连不连得上"无关(那是 LastCheck* 那一组)。加常量是因为
// 这个字面量在 service 里散落了十几处 —— 拼错一个字母,比较永远为假,而表现是"某条
// 判定悄悄失效",没有任何报错。
const (
	ConnStatusOnline = "online"
	ConnStatusMaint  = "maint"
)

type Connection struct {
	ID          int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	Name        string    `gorm:"size:64;uniqueIndex:idx_connection_name;not null" json:"name"`
	Engine      string    `gorm:"size:32;not null" json:"engine"`
	// TargetSchema 是本次请求选中的 schema,只活在内存里(gorm:"-",不入库、不出 JSON)。
	//
	// 它为 Oracle 而存在。别的引擎里"切库"就是换 Database 字段,而 Oracle 的 Database
	// 装的是**服务名**,不是 schema —— 覆盖它会连错实例。Oracle 换 schema 的正确做法
	// 是在会话上 ALTER SESSION SET CURRENT_SCHEMA,所以选中的 owner 得单独带到执行层。
	//
	// 见 service.applyTargetDatabase(写入)与 gateway.RealRun(生效)。
	TargetSchema string `gorm:"-" json:"-"`
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
	// 上一次连通性巡检的结论。它**不是** Status —— Status 是人为标的维护态,和连不连
	// 得上没有关系。两者混在一起,就是"仅看异常"永远筛不出连不上的实例的原因。
	//
	// LastCheckOK 是**指针**:nil = 从没探过。这和"探过、通了"是两回事,页面上必须
	// 分开说 —— 一个从没被探过的实例显示成"正常",就是在替它担保。
	LastCheckAt  *time.Time `json:"lastCheckAt,omitempty"`
	LastCheckOK  *bool      `json:"lastCheckOk,omitempty"`
	// 失败原因原样留着:"i/o timeout"和"Access denied"指向完全不同的处置动作。
	// 成功时清空 —— 留着上次的错误会让一台已经好了的实例永远显示成坏的。
	LastCheckErr string     `gorm:"size:512" json:"lastCheckErr,omitempty"`
	// LastCheckKind 是结论的种类(gateway.CheckKind:ok / unreachable / auth)。
	// 定时巡检据它跳过"上次认证失败"的实例 —— 靠错误文本猜种类会把 PostgreSQL 的
	// permission denied(授权错误,不锁账号)也认成认证失败。
	LastCheckKind string    `gorm:"size:16" json:"lastCheckKind,omitempty"`
	Tags        string    `gorm:"size:255" json:"tags"` // comma-separated labels for group access
	// BizQuery 标记这台实例是**业务查询源**:只有它能被查询授权(QueryGrant)引用,外部
	// 审批系统拉取可授权的实例时也只看得到它。约定它指向从库、用只读库账号 —— 主库永远
	// 不会出现在授权候选里。
	BizQuery bool `gorm:"column:biz_query;not null;default:false" json:"bizQuery"`
	// 从库(只读副本)。配了之后业务方的数据查询(执行、导出、库表下拉)都由从库提供,主库只承担
	// 终端、审批执行、发布这些要写或要读最新数据的流量。端口 / 账号 / 密码留空沿用主库;
	// 从库连不上时数据查询自动回落主库,结果与审计里注明。迁移 0047。
	ReplicaHost     string `gorm:"size:128;not null;default:''" json:"replicaHost"`
	ReplicaPort     int    `gorm:"not null;default:0" json:"replicaPort"`
	ReplicaUsername string `gorm:"size:64;not null;default:''" json:"replicaUsername"`
	ReplicaPassword string `gorm:"size:255;not null;default:''" json:"-"` // AES 加密,同 Password;空 = 沿用主库
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

// ExecWindow —— 执行窗口(「班车」)。
//
// 它做的事只有一件:在**指定时间**、对**指定的库**,把本来需要审批的中/高风险语句
// 直接放行。它改的是"要不要人来批",不是"有没有权限":能力矩阵判 deny 的仍然 deny,
// 只读角色不会因为开了窗口就能写库。见 service.relaxByWindow。
//
// 为什么按库而不是按实例或分层:一台实例底下往往混着不同业务的库,而一次变更通常
// 只动其中一两个。放开面越小,窗口开着的那几个小时里能出的事就越少。
//
// 两种时间模型:
//   once      —— 一次性,起止时刻,用完即废(本周六 22:00 到周日 02:00)
//   recurring —— 周期班车,固定时段反复生效(每天 02:00-04:00),可设整体失效时间
//
// 时区是窗口自己的属性,不是服务器的。运维说的"凌晨两点"是他所在时区的两点,而网关
// 可能跑在 UTC 上 —— 存 IANA 名字,判定时按它换算。
type ExecWindow struct {
	ID      int64  `gorm:"primaryKey;autoIncrement" json:"id"`
	Name    string `gorm:"size:64;not null" json:"name"`
	Enabled bool   `gorm:"not null" json:"enabled"`

	// 作用范围:库级。两者都必须有值 —— 空库名会让一个窗口悄悄覆盖整台实例。
	//
	// 一张窗口可以勾同一台实例上的多个库(2026-10-22):Databases 是全部(子表 tbl_exec_window_db,
	// 由 repository 读写);Database 是其中第一个,留着给老数据、老客户端与按单个库展示的地方。
	ConnectionID int64  `gorm:"index:idx_window_scope,priority:1;not null" json:"connectionId"`
	Database     string `gorm:"column:db_name;index:idx_window_scope,priority:2;size:128;not null" json:"database"`
	Databases    []string `gorm:"-" json:"databases"`

	Kind     string `gorm:"size:16;not null" json:"kind"` // once | recurring
	Timezone string `gorm:"size:64;not null" json:"timezone"`

	// once
	StartsAt *time.Time `json:"startsAt,omitempty"`
	EndsAt   *time.Time `json:"endsAt,omitempty"`

	// recurring:Weekdays 为 ISO 星期(1=周一…7=周日)的逗号列表,空串表示每天。
	// StartMin/EndMin 是从当地 00:00 起的分钟数;EndMin <= StartMin 表示跨午夜。
	Weekdays string     `gorm:"size:32" json:"weekdays"`
	StartMin int        `gorm:"not null;default:0" json:"startMin"`
	EndMin   int        `gorm:"not null;default:0" json:"endMin"`
	NotAfter *time.Time `json:"notAfter,omitempty"` // 周期班车的整体失效时刻,可空

	// Reason 不是备注,是这扇门为什么被打开的记录 —— 窗口放行的每一条命令都会在
	// 审计里指回这个窗口,而审计要回答的正是"当时凭什么不用审批"。
	Reason    string    `gorm:"size:255" json:"reason"`
	CreatedBy int64     `gorm:"not null;default:0" json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`

	// Status —— 申请 → 审批 → 生效。判定只认 approved。
	//
	// 这是这个功能的闸门本身:窗口开着的那几个小时里,本该有人签字的中/高风险语句
	// 会一条不落地直接下发,所以"谁可以打开这扇门"必须不是一个人。等待审批期间它
	// 一行都不放行 —— 见 repository.ExecWindowsFor。
	Status     string     `gorm:"size:16;not null;default:pending" json:"status"`
	ApprovalID int64      `gorm:"not null;default:0" json:"approvalId,omitempty"`
	ApNo       string     `gorm:"size:32" json:"apNo,omitempty"`
	DecidedAt  *time.Time `json:"decidedAt,omitempty"`

	// Active 是"此刻这扇门开着吗",列表时算出来给界面看,不入库。
	//
	// 算在后端而不是前端:跨午夜和时区换算是这个功能里最容易出错的两处,在 TS 里
	// 再写一遍就等着两份实现慢慢分叉 —— 而分叉的表现是"界面说开着,网关说没开"。
	Active bool `gorm:"-" json:"active"`

	// CreatedByName:申请人姓名,只在管理后台的全局列表里填(工单 07),不入库。
	CreatedByName string `gorm:"-" json:"createdByName,omitempty"`

	// Expired 是"这扇门再也不会开了"。同样是算出来给界面看的。
	//
	// 没有它,界面只能分出"开着"和"没开着",于是一个上周就结束的一次性窗口会一直
	// 显示成"未到点" —— 一句读起来像"再等等就到了"的话,而它永远不会到。一次性窗口
	// 过了结束时刻算到期;周期班车只有设了停运时刻(NotAfter)并且已过才算,否则它
	// 每周都会再来一次,本来就没有到期这回事。
	//
	// 它只描述**时间表**,与审批状态和启用与否无关:一张还在等审批、时段却已经过去
	// 的窗口,同样是到期的 —— 那恰恰是最该被看见的一种。
	Expired bool `gorm:"-" json:"expired"`
}

func (ExecWindow) TableName() string { return "tbl_exec_window" }

// ExecWindowDB 是一张窗口覆盖的一个库(多库窗口,迁移 0055)。判定按 (实例, 库) 取窗口时走 db_name 索引。
type ExecWindowDB struct {
	WindowID int64  `gorm:"primaryKey;autoIncrement:false" json:"windowId"`
	Database string `gorm:"column:db_name;primaryKey;size:128;index:idx_window_db_name" json:"database"`
}

func (ExecWindowDB) TableName() string { return "tbl_exec_window_db" }

// DBs 是这张窗口覆盖的全部库;老数据(子表里还没有行)退回单个 Database。
func (w *ExecWindow) DBs() []string {
	if len(w.Databases) > 0 {
		return w.Databases
	}
	if w.Database != "" {
		return []string{w.Database}
	}
	return nil
}

// 执行窗口的两种时间模型。
const (
	WindowOnce      = "once"
	WindowRecurring = "recurring"
)

// 执行窗口的审批状态。只有 approved 会被判定层认。
const (
	WindowPending  = "pending"
	WindowApproved = "approved"
	WindowRejected = "rejected"
	// WindowCancelled —— 申请人自己撤回的,不是被谁驳回的。
	//
	// 和 Approval 那边的 cancelled 是同一条道理:显示成"已驳回"就等于说有人看过并
	// 拒绝了它,而实际上没有任何人对它做过判断。两者对判定层是一样的(都不是
	// approved,一行都不放行),但对读记录的人不是。
	WindowCancelled = "cancelled"
)

// Export job / task states.
const (
	// ExportAwaiting:含敏感字段的导出在批准之前停在这里,不进队列。
	ExportAwaiting = "awaiting"
	ExportPending  = "pending"
	ExportRunning = "running"
	ExportDone    = "done"
	ExportFailed  = "failed"
	// ExportExpired — the archive was deleted by the retention sweep. The row
	// STAYS: what was exported, by whom, how many rows and when is the part an
	// auditor asks about, and it outlives the file by design. Only the pointer to
	// the bytes (Files) and the archive password go, because a password guarding
	// something that no longer exists is a liability with no use left.
	ExportExpired = "expired"
	// ExportCancelled — 提交人(或管理员)在系统里终止了它。执行中的会同时终止目标库上的查询
	// (MySQL 系 KILL QUERY、PostgreSQL 取消请求、Oracle break),已写出的分卷删除。Error 里写明
	// 谁终止的。
	ExportCancelled = "cancelled"
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
	Status       string     `gorm:"size:16;not null;default:pending" json:"status"` // awaiting|pending|running|done|failed|expired|cancelled
	// IncludeSensitive:这份导出要不要**原值**。默认(false)敏感字段照常打码。
	//
	// 它不是一个可以自己勾了就生效的开关:带着它的任务不会直接进队列,而是停在
	// awaiting 等审批。一份带原值的 CSV 落到磁盘、发进聊天工具,比在终端上看一眼
	// 跑得远得多 —— 这正是脱敏在导出这一路最要紧的原因,所以放开它要有人签字。
	IncludeSensitive bool  `gorm:"not null;default:false" json:"includeSensitive"`
	// Format:csv(默认)| sql —— 写成目标库可直接执行的 INSERT(迁移 0052)。
	// TargetTable 是 SQL 格式时 INSERT 写入的表名(提交时留空则从查询的 FROM 识别)。
	Format      string `gorm:"column:export_format;size:8;not null;default:csv" json:"format"`
	TargetTable string `gorm:"size:128" json:"targetTable,omitempty"`
	ApprovalID       int64 `gorm:"index:idx_export_approval;not null;default:0" json:"approvalId"`
	ApNo             string `gorm:"size:32" json:"apNo"`
	Rows         int        `json:"rows"`
	Bytes        int64      `json:"bytes"`                              // total encrypted size across parts
	Parts        int        `json:"parts"`                             // number of ~100MB CSV files
	Files        string     `gorm:"type:text" json:"files"`            // newline-joined part paths
	Password     string     `gorm:"size:128" json:"password"`          // holds the AES-encrypted archive password (~68 chars)
	Error        string     `gorm:"size:255" json:"error"`
	// ErrorRef 是 Error 那句话的机器可读身份(JSON 的 RuleRef,apiMsg / outText,迁移 0060),界面按读者语言显示;
	// 空 = 固定句(读出时按原句补)或 0060 之前的老行(界面回落到原串)。
	ErrorRef     string     `gorm:"type:text" json:"errorRef,omitempty"`
	CreatedAt    time.Time  `json:"createdAt"`
	FinishedAt   *time.Time `json:"finishedAt"`
	// 下载情况(迁移 0057):下过的分卷路径(换行分隔)与最近一次下载时间。列表据此标「已下载」,
	// 省得人重复下载;再次下载照样可以(文件可能丢了)。
	DownloadedAt    *time.Time `json:"downloadedAt"`
	DownloadedFiles string     `gorm:"type:mediumtext" json:"downloadedFiles"`
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
	// LogRefs:Log 里网关自己写的那几行(开始 / 完成 / 失败……)的身份,JSON 的 apimsg.LineRef 数组(迁移 0061)。
	// NOTICE 是数据库的原话,不在其中;界面逐行比对原文换成译文。
	LogRefs string `gorm:"type:mediumtext" json:"logRefs,omitempty"`
	Rows         int        `json:"rows"`
	Error        string     `gorm:"size:512" json:"error"`
	// ErrorRef 是 Error 那句话的机器可读身份(JSON 的 RuleRef,apiMsg / outText,迁移 0060),界面按读者语言显示;
	// 空 = 固定句(读出时按原句补)或 0060 之前的老行(界面回落到原串)。
	ErrorRef     string     `gorm:"type:text" json:"errorRef,omitempty"`
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
	Path      string    `gorm:"size:512" json:"path"`  // server path;响应里只给文件名(见 script_upload_json.go)
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
	// ExternalDispatched:这张单已经(或正在)外发给审批魔方(迁移 0062)。task id 是异步写回的,审批魔方
	// 也不保证回,所以回调不能只靠它;没外发过的单不该接受回调(见 DecideApprovalExternal)。
	ExternalDispatched bool `gorm:"column:external_dispatched" json:"-"`
	// ReleaseID links a ticket raised by a release pipeline's approve stage back
	// to its release. It also keeps the ticket OUT of the manual execute path
	// (ExecuteApproved): the pipeline owns the execute stage, and letting someone
	// run it by hand from the approvals page would apply the change twice — once
	// there, once in the pipeline that still believes it has not run yet.
	// Zero for every ordinary ticket.
	ReleaseID int64 `gorm:"not null;default:0;index:idx_approval_release" json:"releaseId,omitempty"`
	// ExportJobID links a ticket raised by a sensitive-field export back to its
	// job. 它和 ReleaseID 起的是同一个作用:把这张单挡在**手动执行**那条路之外。
	//
	// 这里不挡的后果很具体:导出单的 Command 是一句描述("EXPORT [含敏感字段] …"),
	// 发起人若能点"执行",网关会把它当成一条命令发给数据库。而这张单真正授权的事
	// 情是"让导出 worker 去跑",不是"在终端里执行一句 SQL"。
	ExportJobID int64 `gorm:"not null;default:0;index:idx_approval_export" json:"exportJobId,omitempty"`
	// WindowID 把一张单指回它要开的那扇执行窗口。作用和上面两个一样:
	//
	//   1. 通过之后要做的事是"让那个窗口开始生效",不是执行一条命令;
	//   2. 把这张单挡在**手动执行**那条路之外 —— 窗口单的 Command 是一句描述
	//      ("开启执行窗口「…」"),发起人若能点"执行",网关会把那句话当 SQL 发出去。
	WindowID int64 `gorm:"not null;default:0;index:idx_approval_window" json:"windowId,omitempty"`
	// OSCJobID 把一张单指回它要做的那次在线加 / 删索引(.scratch/osc-product)。和上面几个一样:
	// 通过之后"执行"= 启动那次迁移,不是把 Command 当一条 SQL 发给数据库。迁移 0049。
	OSCJobID int64 `gorm:"column:osc_job_id;not null;default:0" json:"oscJobId,omitempty"`
	// Notice 是要审批人特别留意的一句话(规范中文),NoticeRef 是它的机器可读形式,界面按读者语言显示。
	// 目前只有一种:生产大表上、在线变更做不了的 DDL ——"将直接执行,可能长时间锁表"。迁移 0049。
	Notice    string   `gorm:"type:text" json:"notice,omitempty"`
	NoticeRef *RuleRef `gorm:"column:notice_ref;type:text;serializer:json" json:"noticeRef,omitempty"`
	// CommandRef 是 Command 的机器可读形式(见 text_ref.go 的 apText):Command 是一句在 Go 里
	// 拼好的中文描述(窗口单),规范记录照旧存它;界面按 CommandRef 用读者的语言显示。
	// 只有描述类的单子有;SQL 命令本身不需要翻译,留空。迁移 0046。
	CommandRef *RuleRef `gorm:"column:command_ref;type:text;serializer:json" json:"commandRef,omitempty"`
	Result       string     `gorm:"type:text" json:"result"`     // execution output once approved
	// ResultRef 是 Result 那句话的机器可读身份(JSON 的 RuleRef,apiMsg / outText,迁移 0060),界面按读者语言显示;
	// 空 = 固定句(读出时按原句补)或 0060 之前的老行(界面回落到原串)。
	ResultRef    string     `gorm:"type:text" json:"resultRef,omitempty"`
	ResultRows   int        `json:"resultRows"`
	// ExecStatus 是命令在**目标库上真的跑成了没有**,和 Status 是两件事。
	//
	// Status 记的是审批的结论:批了就是批了,跑挂了并不会把它变回没批准 —— 能不能
	// 执行、按状态筛选,走的都是它。但"这张单最后怎么样了"要看的是这一列。从前它
	// 没有落库:失败只写进了审计行,工单本身和成功的长得一模一样,而失败的原因就写
	// 在 Result 里,却没有任何东西说那段文字是一次失败。
	//
	// running = 正在库上跑(点下执行到结果落库之间);网关在这期间重启的,启动时判为
	// failed 并写明结果未知(见 FailStuckApprovalExecutions)。
	//
	// 空 = 还没执行,或者是这一列存在之前就跑过的历史工单 —— 后者的成败无从得知,
	// 界面照旧只说"已执行",不替它编一个结果。
	ExecStatus   string     `gorm:"size:16" json:"execStatus"`
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

// AwaitingExecution 是审批通过、尚未执行的工单写在 Result 里的那句话。
//
// 它同时是一个**判据**:升级到"通过不再代执行"之后,历史工单(旧行为下批准即执行)
// 的 executed_at 是空的,与一张真正在等执行的新工单在行级别一模一样 —— 唯一分得开
// 它们的,就是这句话是不是新代码写下的。所以它必须是一个常量:写它的地方和读它的地方
// 各写一遍字面量,哪天改一个字,回填就会把等待执行的工单当成历史单标成已执行。
const AwaitingExecution = "· 已批准,等待发起人执行"

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
	ApprovalNo   string    `gorm:"size:32;index:idx_audit_approval" json:"approvalNo"`
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
	// EventType 说这一行是**哪一类事件**:被拦下来、被审批、真执行了、还是一次登录。
	//
	// 它此前只是一个传给 recordAudit 的参数,用来决定 webhook 要不要发,之后就丢掉了。
	// 而它推不出来:(Risk, Result) 区分不开这几类 —— intercept 会配 rejected 也会配
	// pending,approve 同样两者都配。
	//
	// 空 = 这一行写下时还没有这一列(迁移 0039 之前)。**不回填**:按 result 猜一个填
	// 进去,等于在一张哈希链审计表里造证据。
	//
	// 0039 起进哈希载荷 —— 一个改了也不破链的字段不算证据。
	EventType string `gorm:"size:16;index:idx_audit_event;not null;default:''" json:"eventType"`
	// DataAction 说这条命令对**数据**做了什么:增 / 删 / 改 / 查(create/delete/update/read),
	// 或者 other —— 它根本不是对数据的操作(登录、配置变更、流程标签),也可能是认不出的语句。
	//
	// 与 EventType 正交:那个回答"这一步在流程里是什么",这个回答"它动了数据没有"。一条
	// approve 放行的可能是 DROP,也可能是 SELECT。
	//
	// 结论由 gateway.DataAction 在写行时算好并存下来(迁移 0040),不是读的时候再推 ——
	// 分类规则以后会变,而这一行要说的是**当时**判成了什么。
	//
	// 空 = 0040 之前写下的行。空与 other 不是一回事:空是"没记",other 是"记了、结论是
	// 它不属于增删改查"。
	DataAction string `gorm:"size:16;index:idx_audit_action;not null;default:''" json:"dataAction"`
	// ClientIP 是这一下**从哪台机器发出来的**(迁移 0041)。
	//
	// actor 回答"谁",它回答"人在哪"。同一个账号昨夜从两个网段各删过一次数据,只看
	// actor 是一样的两行 —— 账号被盗用、凭据被拿去别处跑,都只在这一列上露头。
	//
	// 取自 gin 的 ClientIP():只在配置过的可信代理后面才认 X-Forwarded-For(默认一台
	// 都不信),所以客户端伪造不了它 —— 与 IP 白名单读的是同一个值。
	//
	// 空 = 0041 之前写下的行,或者这一下不是从 HTTP 请求来的(定时清扫、启动期回填这类
	// 后台动作本来就没有客户端)。不回填。
	ClientIP string `gorm:"size:45;not null;default:''" json:"clientIp"`
	// Source 是这一行**从网关的哪个入口进来**(迁移 0063):数据查询、Web 命令行、上传脚本、
	// 数据导出、审批单、变更发布、开放接口、登录认证、管理操作、系统任务 —— 取值见
	// AuditSources。
	//
	// 同样是一条 SELECT,从数据查询页来和从终端来是两件事(前者走授权与只读守卫,后者走
	// 角色与判定层),而此前这张表上看不出区别。
	//
	// 空 = 0063 之前写下的行,界面显示「未知 / 旧记录」。**不回填**:按命令文本去猜当时
	// 是从哪个页面进来的,等于在哈希链审计表上补写证据(与 0039-0041 同一条)。
	//
	// 0063 起进哈希载荷(第六代,"source" 键):改了来源就破链。
	Source string `gorm:"column:entry_source;size:16;index:idx_audit_source;not null;default:''" json:"source"`
}

// 审计事件的四类。它们同时是 webhook 的订阅项(WebhookConfig.Events 里逗号分隔的那些),
// 所以两边必须是同一组字符串 —— 订阅一个写错的类型,表现是"这类事件永远不推",而不是报错。
const (
	AuditEventIntercept = "intercept" // 命令被拦下来,开了审批单
	AuditEventApprove   = "approve"   // 审批链上的一次决定(通过/驳回/撤回)
	AuditEventExec      = "exec"      // 真的落到目标库上了
	AuditEventLogin     = "login"     // 登录
)

// AuditEventTypes 是上面那四类,顺序固定,供筛选白名单与界面下拉共用。
var AuditEventTypes = []string{AuditEventIntercept, AuditEventApprove, AuditEventExec, AuditEventLogin}

// IsAuditEventType 认得出才算数。拼错的取值不能原样进 WHERE —— 查出来是空列表,而空列表
// 和"确实没有"长得一模一样,调用方看不出自己写错了(与 status / type 同一条原则)。
// 审计行的来源 —— 这一下是从网关的哪个入口进来的(AuditLog.Source)。
//
// 口径(2026-11 与用户对齐):
//   - 发布单的每一行都是 release,哪怕它是外部系统经开放接口提交的 —— 那一层由 operator
//     列的「API:系统名」说明;api 只给发布流水线以外的开放接口调用(SQL 审查、巡检上传、
//     开放授权)。
//   - 数据查询页的「导出」归 export,不归 query:按来源筛一次就该看到全部导出。
//   - 非数据操作分三类:auth(登录 / 会话 / MFA / 改密)、admin(管理后台与其余页面操作)、
//     system(没有请求方的后台任务,如导出保留期清理)。
const (
	AuditSourceQuery    = "query"    // 数据查询页
	AuditSourceTerminal = "terminal" // Web 命令行(含后台执行、在线变更、对象编译)
	AuditSourceScript   = "script"   // 上传脚本 / 整脚本执行
	AuditSourceExport   = "export"   // 数据导出(终端导出、查询页导出)
	AuditSourceApproval = "approval" // 审批单:审批决定、执行审批单、撤回、超时
	AuditSourceRelease  = "release"  // 变更发布 / 流水线(RELEASE-*)
	AuditSourceAPI      = "api"      // 开放接口(发布流水线以外)
	AuditSourceAuth     = "auth"     // 登录认证
	AuditSourceAdmin    = "admin"    // 管理操作
	AuditSourceSystem   = "system"   // 系统后台任务
	// AuditSourceUnknown 只出现在**筛选参数**里:筛「未知 / 旧记录」= source 为空的行。
	// 它永远不会被写进表。
	AuditSourceUnknown = "unknown"
)

// AuditSources 是可写入的来源,顺序固定,供筛选白名单与界面下拉共用。
var AuditSources = []string{
	AuditSourceQuery, AuditSourceTerminal, AuditSourceScript, AuditSourceExport,
	AuditSourceApproval, AuditSourceRelease, AuditSourceAPI,
	AuditSourceAuth, AuditSourceAdmin, AuditSourceSystem,
}

// IsAuditSourceFilter 认得出才算数 —— 供 ?source= 白名单用;unknown 也算(筛旧记录)。
func IsAuditSourceFilter(s string) bool {
	if s == AuditSourceUnknown {
		return true
	}
	for _, k := range AuditSources {
		if k == s {
			return true
		}
	}
	return false
}

// AuditSourceForRoute 按**路由模板**(gin 的 FullPath,如 /api/v1/bizquery/exec)判这一次
// 请求是从哪个入口进来的。由 middleware.BindRequestUser 调用,盖到 User.AuditSource 上。
//
// 只看路由、不看请求内容:路由是服务端定的,客户端改不了它 —— 来源是要进哈希链拿来
// 下结论的字段,不能由调用方自己报。
//
// 入口与路由无关的那几类(后台导出任务、发布阶段、审批决定、登录)由服务层在写审计时
// 显式指定(recordAuditFrom),不依赖这里。
func AuditSourceForRoute(fullPath string) string {
	p := strings.TrimPrefix(fullPath, "/api/v1")
	has := func(prefixes ...string) bool {
		for _, x := range prefixes {
			if p == x || strings.HasPrefix(p, x+"/") {
				return true
			}
		}
		return false
	}
	switch {
	case p == "":
		return ""
	case has("/open"):
		return AuditSourceAPI
	case has("/auth"):
		return AuditSourceAuth
	case has("/bizquery/export", "/export"):
		return AuditSourceExport
	case has("/bizquery"):
		return AuditSourceQuery
	case has("/scripts"):
		return AuditSourceScript
	case has("/terminal", "/risk", "/async-jobs", "/osc"),
		strings.HasPrefix(p, "/connections/:id/objects"),
		strings.HasPrefix(p, "/connections/:id/packages"):
		return AuditSourceTerminal
	case has("/approvals"):
		return AuditSourceApproval
	case has("/releases", "/pipelines"):
		return AuditSourceRelease
	}
	return AuditSourceAdmin
}

func IsAuditEventType(s string) bool {
	for _, k := range AuditEventTypes {
		if k == s {
			return true
		}
	}
	return false
}

func (AuditLog) TableName() string { return "tbl_audit_log" }

// WebhookConfig — audit event push target.
type WebhookConfig struct {
	ID       int64  `gorm:"primaryKey;autoIncrement" json:"id"`
	Endpoint string `gorm:"size:255;not null" json:"endpoint"`
	Secret   string `gorm:"size:512;not null" json:"secret"` // bearer token: sent as `Authorization: Bearer <secret>`

	Events string `gorm:"size:255;not null" json:"events"` // intercept,approve,exec,login
	// Envs 是推送的环境(Environment.Code,逗号分隔);空 = 全部环境。按审计行上那一刻的环境快照
	// (AuditLog.Env)匹配;不属于任何实例的事件(登录、改设置……)没有环境,不受这一栏约束。
	Envs     string `gorm:"size:1024;not null;default:''" json:"envs"`
	RetryMax int    `gorm:"not null;default:5" json:"retryMax"`
	Enabled  bool   `gorm:"not null" json:"enabled"`
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

// ---------------------------------------------------------------- 元数据缓存
//
// 远端库的**表清单与表结构**在本地留一份。
//
// 在此之前,树每展开一次就实时探一次目标库,而"搜索所有实例里的某张表"意味着挨个连
// 88 台生产实例问一遍 —— 慢,而且是把一次界面操作变成一轮对生产的扫描。
//
// 这份缓存是**只读的副本,不是真相**。判定、执行、导出一律仍然走实时的目标库:一份
// 可能过期的结构如果被用来做判断,它就从"快"变成了"错"。所以每一行都带 SyncedAt,
// 界面必须把"这份数据是什么时候的"显示出来 —— 一个不说明年龄的缓存,读的人会当成现状。

// MetaTable — 远端一张表(或视图)在本地的一行。
//
// Schema 为空表示扁平引擎(MySQL / SQLite / Oracle 按 owner 归组时 owner 落在
// DBName 上),PostgreSQL 家族才有 schema 这一层 —— 与 gateway.SchemaGroup 的形状一致。
type MetaTable struct {
	ID           int64  `gorm:"primaryKey;autoIncrement" json:"id"`
	ConnectionID int64  `gorm:"index:idx_meta_table_scope,priority:1;not null" json:"connectionId"`
	DBName       string `gorm:"column:db_name;index:idx_meta_table_scope,priority:2;size:128;not null" json:"database"`
	SchemaName   string `gorm:"column:schema_name;size:128;not null" json:"schema"`
	Name         string `gorm:"column:table_name;index:idx_meta_table_name;size:128;not null" json:"name"`
	Kind         string `gorm:"size:16;not null" json:"kind"` // table | view
	Comment      string `gorm:"size:512" json:"comment"`
	SyncedAt     time.Time `json:"syncedAt"`
}

func (MetaTable) TableName() string { return "tbl_meta_table" }

// MetaColumn — 远端一张表的一列。
//
// 不做外键指向 MetaTable:同步是"整台实例删了重写",外键只会让那次删除变成一场级联
// 风暴,而这份数据本来就没有引用完整性可言 —— 它是一张照片。定位靠与 MetaTable 相同
// 的四元组(连接 / 库 / schema / 表名)。
type MetaColumn struct {
	ID           int64  `gorm:"primaryKey;autoIncrement" json:"id"`
	ConnectionID int64  `gorm:"index:idx_meta_col_scope,priority:1;not null" json:"connectionId"`
	DBName       string `gorm:"column:db_name;index:idx_meta_col_scope,priority:2;size:128;not null" json:"database"`
	SchemaName   string `gorm:"column:schema_name;size:128;not null" json:"schema"`
	TableName_   string `gorm:"column:table_name;index:idx_meta_col_scope,priority:3;size:128;not null" json:"table"`
	Ordinal      int    `gorm:"not null" json:"ordinal"`
	Name         string `gorm:"column:column_name;index:idx_meta_col_name;size:128;not null" json:"name"`
	DataType     string `gorm:"size:128;not null" json:"dataType"`
	// 不能带 default:true。GORM 建行时把零值换成 default 标签的值,NOT NULL 列(false)
	// 于是全都以 true 落库:缓存里的"可空"是错的,而按照片比对时这些列每一轮都对不上、
	// 被删了重插(2026-11 生产每台实例每轮多写几百毫秒)。库里的 DEFAULT 1 留在迁移里,
	// 同步总是显式给值,用不到它。
	Nullable     bool   `gorm:"not null" json:"nullable"`
	ColDefault   string `gorm:"column:col_default;size:512" json:"default"`
	Comment      string `gorm:"size:512" json:"comment"`
	IsPK         bool   `gorm:"column:is_pk;not null;default:false" json:"isPk"`
	SyncedAt     time.Time `json:"syncedAt"`
}

func (MetaColumn) TableName() string { return "tbl_meta_column" }

// MetaSync — 每台实例最近一次同步的结果。
//
// 没有它,"这台实例为什么一张表都没有"就没有答案:是还没轮到它、连不上、账号没权限,
// 还是它真的空着?这四种在界面上长得一模一样,而只有第一种是正常的。
type MetaSync struct {
	ConnectionID int64     `gorm:"primaryKey" json:"connectionId"`
	StartedAt    time.Time `json:"startedAt"`
	FinishedAt   time.Time `json:"finishedAt"`
	// 列名是 db_count / table_count / column_count,不是 databases / tables / columns:
	// `databases` 是 MySQL 8 的保留字(生产上第一次 migrate 就炸在这)。Go 字段名与
	// JSON 保持原样 —— 保留字是 SQL 的事,接口不必跟着别扭。
	Databases int `gorm:"column:db_count;not null;default:0" json:"databases"`
	Tables    int `gorm:"column:table_count;not null;default:0" json:"tables"`
	Columns   int `gorm:"column:column_count;not null;default:0" json:"columns"`
	// Err 是最近一次失败的原因。成功时清空 —— 留着上次的错误会让一台已经好了的实例
	// 永远显示成坏的。
	Err string `gorm:"size:512" json:"err,omitempty"`
}

func (MetaSync) TableName() string { return "tbl_meta_sync" }

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
	Enabled   bool      `gorm:"not null" json:"enabled"`
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
	Enabled    bool      `gorm:"not null" json:"enabled"`
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
	Enabled    bool       `gorm:"not null" json:"enabled"`
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
	// 查询授权(业务方数据查询)。**不在 AllScopes 里**:AllScopes 是没勾任何范围时的
	// 默认集合,一把没留意勾选的 CI 凭据不该顺带拿到给人开生产数据查询权的能力。
	ScopeGrantRead  = "grant:read"
	ScopeGrantWrite = "grant:write"
	// 上传 AI 巡检报告(.scratch/inspection-report)。同样**不在 AllScopes 里**:只发给巡检系统那一把
	// 凭据,它只能上传报告;别的凭据没勾就没有。
	ScopeInspectionWrite = "inspection:write"
	// 外部审批系统替审批链上的人决定发布单的审批阶段。**不在 AllScopes 里**:审批是职责分离的
	// 那一半,提单的 CI 凭据不该顺带拿到。凭据只证明系统可信,审批人仍须在链上(见 service/open_release_approve.go)。
	ScopeReleaseApprove = "release:approve"
)

// AllScopes is what a client gets when none are named.
var AllScopes = []string{ScopeReleaseCreate, ScopeReleaseRead, ScopeReviewCheck}

// KnownScopes 是这个版本认得的全部范围,包括必须显式勾选的授权范围。
var KnownScopes = []string{ScopeReleaseCreate, ScopeReleaseRead, ScopeReviewCheck, ScopeGrantRead, ScopeGrantWrite, ScopeInspectionWrite, ScopeReleaseApprove}

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
	// 自动执行 — the same execution as StageExecute minus the human 「确认执行」 click.
	// Only allowed on tiers with EnvTier.AutoExecute on; anywhere else it falls back
	// to waiting for that click, exactly like StageExecute.
	StageAutoExecute = "auto_execute"
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
	Enabled   bool      `gorm:"not null" json:"enabled"`
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
	// Precheck 是**收单那一刻**的语法与对象检查结果(JSON,见 service.PrecheckResult)。
	//
	// 存在单子上而不是只随响应返回:提单的是 CI,后面看这张单的是审批的人、执行的人、
	// 出事回头查的人 —— 只在响应里,这几个人一个都看不到。
	//
	// 空字符串有两种含义,界面必须分开说:0036 之前的老单子没有这一栏,以及这次检查
	// 一条都没能做成。两者都不是"检查过、没问题"。
	Precheck     string `gorm:"type:mediumtext" json:"precheck,omitempty"`
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
	// ErrorRef 是 Error 那句话的机器可读身份(JSON 的 RuleRef,rlLog.),界面按读者语言显示;
	// 空 = 0059 之前的老单,界面回落到 Error 原串。见 model.RenderRelLog。
	ErrorRef   string     `gorm:"type:text" json:"errorRef,omitempty"`
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
	// LogRefs 是 Log 的逐行 RuleRef(JSON 数组,rlLog.),界面按读者语言渲染;Log 仍是规范记录。
	// 每次写 log 都要一并写它(写空 = 界面回落到 Log),否则两份对不上。见 model.RenderRelLog。
	LogRefs   string `gorm:"type:mediumtext" json:"logRefs,omitempty"`
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

// 工单的**类型** —— 一张审批单到底是哪一类事。
//
// 它不是存在表上的一列,而是由 ReleaseID / WindowID / ExportJobID 三个外键推出来的:
// 三个都为 0 就是一条普通命令单。不加一列是有意的 —— 加了就有两个真相,而它们迟早
// 会对不上(一张单的类型改了、外键没改,或者反过来)。
//
// 列出来的理由是这四类**要人做的事完全不同**:命令单批完要发起人自己去执行;升级单的
// 执行归流水线;窗口单批准即生效;导出单批完由后台任务跑。而在列表上,后三类的"命令"
// 其实是一句描述,连表名都解析不出来 —— 不说类型,那一行看起来就只是一张内容古怪的
// 命令单。
const (
	ApTypeCommand = "cmd"
	ApTypeRelease = "release"
	ApTypeWindow  = "window"
	ApTypeExport  = "export"
	// ApTypeOSC:生产大表的在线加 / 删索引。通过之后"执行"= 启动那次迁移(.scratch/osc-product)。
	ApTypeOSC = "osc"
)

// ApprovalType 按外键判定一张工单的类型。判定顺序不重要:三个外键互斥,一张单不可能
// 同时是升级单和窗口单。
func ApprovalType(a *Approval) string {
	switch {
	case a == nil:
		return ApTypeCommand
	case a.ReleaseID > 0:
		return ApTypeRelease
	case a.WindowID > 0:
		return ApTypeWindow
	case a.ExportJobID > 0:
		return ApTypeExport
	case a.OSCJobID > 0:
		return ApTypeOSC
	}
	return ApTypeCommand
}

// Notification kinds delivered to a user's in-app inbox.
const (
	NotifApprovalApproved = "approval-approved"
	NotifApprovalRejected = "approval-rejected"
	NotifApprovalExpired  = "approval-expired"
	// 通过了的工单执行失败(在线变更失败 / 中止、别人替你执行但失败了)。不能沿用
	// approval-approved:界面按类型着色,失败会弹成一条绿色的"成功"提示。
	NotifExecFailed = "exec-failed"
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
	// BodyRef 是 Body 那句话的机器可读身份(JSON 的 RuleRef,apiMsg / outText,迁移 0060),界面按读者语言显示;
	// 空 = 固定句(读出时按原句补)或 0060 之前的老行(界面回落到原串)。
	BodyRef   string    `gorm:"type:text" json:"bodyRef,omitempty"`
	// TitleRef 不存库:标题都是固定句,读出时按原句查表(老通知也能翻译)。
	TitleRef string `gorm:"-" json:"titleRef,omitempty"`
	RefNo     string    `gorm:"size:32" json:"refNo"` // related ticket, e.g. AP-2295
	Read      bool      `gorm:"column:is_read;not null;default:false" json:"read"`
	CreatedAt time.Time `json:"createdAt"`
}

func (Notification) TableName() string { return "tbl_notification" }
