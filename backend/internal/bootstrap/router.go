package bootstrap

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"

	"aegisdb/internal/handler"
	"aegisdb/internal/middleware"
	"aegisdb/internal/model"
	"aegisdb/internal/repository"
	"aegisdb/internal/service"
	"aegisdb/pkg/jwt"
	"aegisdb/pkg/resp"
)

// NewRouter wires all routes, middleware and the three-layer guards.
func NewRouter(cfg *Config, h *handler.Handler, repo *repository.Repo, svc *service.Services, jwtMgr *jwt.Manager) *gin.Engine {
	if cfg.Server.Mode == "release" {
		gin.SetMode(gin.ReleaseMode)
	}
	r := gin.New()
	// Trust only the explicitly configured proxies (default: none) so a client
	// cannot spoof its source IP via X-Forwarded-For and bypass the IP allowlist.
	_ = r.SetTrustedProxies(cfg.Server.TrustedProxies)
	// hsts only when this listener terminates TLS itself (see SecurityHeaders).
	hsts := cfg.Server.TLSCert != "" && cfg.Server.TLSKey != ""
	r.Use(accessLogger(), middleware.Recovery(), middleware.SecurityHeaders(hsts), middleware.CORS(cfg.Server.CORSOrigins), middleware.Latency(), middleware.BodyLimit())

	// healthz 顺带把"审计写入失败次数"报出来。它平时是 0,一旦不是 0 就说明有动作
	// 没有留下审计行 —— 那是巡检当天就该知道的事,而不是等下一次 verify-audit 发现
	// 链上缺了一段(M-11)。
	r.GET("/healthz", func(c *gin.Context) {
		resp.OK(c, gin.H{"status": "ok", "auditWriteFailures": svc.AuditWriteFailures()})
	})
	// Interface docs are an internal development asset, not something an
	// outward-facing gateway should hand to anyone unauthenticated — the full
	// route map (admin endpoints, params) is a gift to an attacker (issue 35).
	// Serve them ONLY on a genuine local-dev run; every real deployment (prod,
	// staging, uat, …) omits them entirely.
	if isLocalDevEnv(cfg.EnvRaw) {
		// The contract, two ways: the raw file for tooling, and a rendered page for
		// people. Both unauthenticated — the same surface the login page is on, and
		// an API contract nobody can read is a contract nobody follows.
		r.GET("/openapi.yaml", h.OpenAPISpec) // API contract (backend doc §11);配置了系统名称时换掉 info.title
		r.GET("/docs", h.APIDocs)
		r.GET("/docs/*file", h.APIDocsAsset) // vendored Swagger UI assets, embedded in the binary
	}

	auth := middleware.JWTAuth(jwtMgr, repo)
	menu := func(key string) gin.HandlerFunc { return middleware.MenuGuard(repo, key) }
	// admin restricts role/permission configuration + user management to the
	// platform-admin role (viewing stays at the `perms` menu).
	admin := middleware.AdminOnly(repo)
	// auditReader:审计导出 / 链校验 / 单据轨迹 —— 平台管理员或审计员(管理后台拆分,工单 01),
	// 以及 DBA 负责人(2026-10-26 回归 18,见 middleware.AuditSupervisor)
	auditReader := middleware.AuditSupervisor(repo)
	// auditList:人人可读的审计列表上,全量那一份同样受管理后台 IP 白名单与空闲超时约束(2026-10 审查)
	auditList := middleware.AuditListGate(repo)

	v1 := r.Group("/api/v1", middleware.NoStore())

	// ---- public ----
	// Login is unauthenticated but still subject to the IP allowlist so it can't
	// be brute-forced from a non-allowlisted source when the allowlist is on (R29).
	v1.POST("/auth/login", middleware.IPAllowlist(repo), h.Login)
	// SSO(OIDC):config 告诉登录页要不要露出入口;oidc/start 跳去身份服务,oidc/callback 验身份,
	// sso 用回调留下的登录结果换网关会话。后两个与本地登录同样受 IP 白名单约束 —— 换了一扇门,
	// 门口的规矩不变。
	v1.GET("/auth/sso/config", h.SSOConfig)
	v1.GET("/auth/oidc/start", middleware.IPAllowlist(repo), h.OIDCStart)
	v1.GET("/auth/oidc/callback", middleware.IPAllowlist(repo), h.OIDCCallback)
	// 系统名称与 LOGO:登录页未登录就要显示。只读,改走 PUT /settings(管理员)。
	v1.GET("/auth/branding", h.Branding)
	v1.POST("/auth/sso", middleware.IPAllowlist(repo), h.SSOLogin)
	// The WS terminal authenticates itself (token via Sec-WebSocket-Protocol), so
	// it can't sit behind JWTAuth, but it MUST still honor the IP allowlist — the
	// execution channel is the most sensitive one (R4). Menu + live session checks
	// are enforced inside the handler.
	v1.GET("/terminal/ws", middleware.IPAllowlist(repo), h.TerminalWS)

	// ---- 开放接口 (external systems) ----
	//
	// A separate authentication surface, not the user JWT: the caller is a SYSTEM
	// holding a key/secret bound to a service account (middleware.APIClientAuth).
	// It deliberately does NOT sit behind the console's IP allowlist — that
	// setting answers "which offices may open the console", and folding a CI
	// runner's egress range into it would widen the console's perimeter to grant
	// a machine access. Each credential carries its own allowlist instead.
	//
	// Everything below this line goes through the SAME service layer as the
	// console: same flow templates, same review rules, same approval chain, same
	// execute-time re-judgement, same audit chain.
	open := v1.Group("/open", middleware.APIClientAuth(repo))
	{
		open.POST("/releases", middleware.RequireScope(model.ScopeReleaseCreate), h.OpenCreateRelease)
		open.GET("/releases/:relNo", middleware.RequireScope(model.ScopeReleaseRead), h.OpenGetRelease)
		open.POST("/releases/:relNo/abort", middleware.RequireScope(model.ScopeReleaseCreate), h.OpenAbortRelease)
		// 外部审批:替审批链上的人通过 / 驳回。release:approve 必须显式勾选(不在默认集合里)。
		open.POST("/releases/:relNo/approval", middleware.RequireScope(model.ScopeReleaseApprove), h.OpenDecideRelease)
		open.POST("/releases/:relNo/confirm", middleware.RequireScope(model.ScopeReleaseApprove), h.OpenConfirmRelease)
		// Review WITHOUT raising a ticket — the pre-merge gate a CI job runs.
		open.POST("/sql-review", middleware.RequireScope(model.ScopeReviewCheck), h.OpenReviewCheck)
		// AI 巡检报告上传(.scratch/inspection-report)。只认 inspection:write —— 给巡检系统单独发的凭据。
		open.POST("/inspection-reports", middleware.RequireScope(model.ScopeInspectionWrite), h.OpenUploadInspectionReport)
		// Discovery: what this credential may target, and which flows it may name.
		open.GET("/instances", middleware.RequireScope(model.ScopeReleaseRead), h.OpenListInstances)
		open.GET("/pipelines", middleware.RequireScope(model.ScopeReleaseRead), h.OpenListPipelines)
		// 查询授权(业务方数据查询):外部审批系统审批完调这里下发。两个 scope 都不在默认
		// 集合里,签发凭据时必须显式勾选(见 model.ScopeGrantRead)。
		open.GET("/query-sources", middleware.RequireScope(model.ScopeGrantRead), h.OpenListQuerySources)
		open.GET("/query-sources/:id/databases", middleware.RequireScope(model.ScopeGrantRead), h.OpenListSourceDatabases)
		open.GET("/query-grants", middleware.RequireScope(model.ScopeGrantRead), h.OpenListQueryGrants)
		open.PUT("/query-grants", middleware.RequireScope(model.ScopeGrantWrite), h.OpenUpsertQueryGrant)
		open.DELETE("/query-grants", middleware.RequireScope(model.ScopeGrantWrite), h.OpenRevokeQueryGrantByExternal)
		open.DELETE("/query-grants/:id", middleware.RequireScope(model.ScopeGrantWrite), h.OpenRevokeQueryGrant)
	}

	// ---- authenticated ----
	// IPAllowlist runs after auth so the source IP is enforced on every data API
	// (Session & Security · IP allowlist); loopback is always permitted.
	a := v1.Group("", auth, middleware.IPAllowlist(repo))
	{
		a.POST("/auth/logout", h.Logout)
		a.GET("/auth/me", h.Me)
		// 会话策略(空闲锁定时长):人人可读 —— 前端据此锁定,/settings 只给管理员
		a.GET("/auth/session-policy", h.SessionPolicy)
		// 管理后台的空闲超时(工单 09):人主动进入时重新计时;人在管理后台有操作时前端报心跳。
		// 两者都要平台管理员 / 审计员 + 管理后台 IP 白名单;它们本身不检查空闲。
		a.POST("/admin/enter", middleware.AdminPortalGate(repo), middleware.AdminEnter)
		a.POST("/admin/heartbeat", middleware.AdminPortalGate(repo), middleware.AdminHeartbeat(repo))
		a.GET("/gateway/stats", h.GatewayStats) // live gateway latency (no menu gate)
		a.GET("/approval-chain", h.ApprovalChain) // default chain for terminal/inspector

		// MFA (TOTP) enrollment for the current user
		a.POST("/auth/mfa/setup", h.MFASetup)
		a.POST("/auth/mfa/enable", h.MFAEnable)
		a.POST("/auth/mfa/disable", h.MFADisable)
		// 自助修改密码(黑盒 2026-10 工单 05):旧密码 + 新密码,成功后其它会话作废、当前会话重签
		a.POST("/auth/password", h.ChangePassword)
		// 审批机器人绑定(.scratch/chat-approval-cards):本人的聊天账号。挂在 /auth 下,审计来源因此记 auth
		a.GET("/auth/chat-bindings", h.ChatBindings)
		a.POST("/auth/chat-bindings/:channel/code", h.ChatBindCode)
		a.DELETE("/auth/chat-bindings/:channel", h.ChatUnbind)

		// notifications — every authenticated user has an inbox (no menu gate)
		a.GET("/notifications", h.ListNotifications)
		a.POST("/notifications/read", h.MarkNotificationsRead)



		// terminal
		a.POST("/risk/check", menu("terminal"), h.RiskCheck)
		a.POST("/terminal/exec", menu("terminal"), h.Exec)
		// "加载更多":取暂存结果的下一批(只取本人的;暂存 10 分钟)
		a.GET("/terminal/results/:id", menu("terminal"), h.TerminalResultPage)
		// 关自己的标签连接不设菜单闸:权限被收回的人也该能把连接还回去
		a.POST("/terminal/sessions/close", h.TermSessionClose)
		// Async (background) long-running SQL execution — submit → poll job + log.
		a.POST("/terminal/exec-async", menu("terminal"), h.ExecAsync)
		a.GET("/async-jobs", menu("terminal"), h.ListAsyncJobs)
		a.GET("/async-jobs/:id", menu("terminal"), h.GetAsyncJob)
		a.GET("/scripts/config", menu("terminal"), h.ScriptConfig)
		// Terminal session log export — audit only; the file is built in the
		// browser from lines already shown, so there is nothing here to gate.
		a.POST("/terminal/transcript-export", menu("terminal"), h.TranscriptExport)
		a.POST("/scripts/scan", menu("terminal"), h.ScriptScan)
		a.POST("/scripts/execute", menu("terminal"), h.ScriptExecute)
		a.GET("/scripts/progress/:id", menu("terminal"), h.ScriptProgress) // 上传脚本弹窗轮询扫描 / 执行进度
		// upload-file management (per-user: list / upload / download / delete)
		a.GET("/scripts/uploads", menu("terminal"), h.ScriptUploads)
		a.POST("/scripts/upload", menu("terminal"), h.ScriptUpload)
		a.GET("/scripts/uploads/:id/download", menu("terminal"), h.ScriptUploadDownload)
		a.GET("/scripts/uploads/:id/content", menu("terminal"), h.ScriptUploadContent)
		a.DELETE("/scripts/uploads/:id", menu("terminal"), h.ScriptUploadDelete)

		// terminal snippets — per-user saved scripts on hotkeys 1-9. There is no
		// execute route: the hotkey submits the snippet's text through /risk/check
		// and /terminal/exec above, so it is judged against the instance it is
		// aimed at when it fires rather than when it was saved.
		a.GET("/snippets", menu("terminal"), h.Snippets)
		a.GET("/snippets/limits", menu("terminal"), h.SnippetLimits)
		a.POST("/snippets", menu("terminal"), h.SnippetSave)
		a.PUT("/snippets/:id", menu("terminal"), h.SnippetSave)
		a.DELETE("/snippets/:id", menu("terminal"), h.SnippetDelete)

		// data export (SQL → compressed+encrypted archive on the server)
		a.GET("/export/config", menu("terminal"), h.ExportConfig)
		a.POST("/export", menu("terminal"), h.ExportData)
		a.GET("/export/jobs", menu("terminal"), h.ExportJobs)
		a.POST("/export/jobs/:id/cancel", menu("terminal"), h.CancelExport)
		a.GET("/export/download", menu("terminal"), h.ExportDownload)

		// connections — read is open (tag-filtered); instance config is admin-only
		a.GET("/connections", h.ListConnections) // tag-filtered per the caller's role
		a.GET("/tags", h.ListTags)
		// 项目 —— 数据库与升级单的归属(组织维度,不是安全边界:判定层不看它)。
		// 读开放给 db 菜单,增删改与实例配置同一档:admin-only。
		// 敏感字段:读开放给能进权限页的人(知道哪些字段被脱敏本身不敏感,
		// 反而是"为什么这列是星号"的答案),增删改仅管理员。
		// 读**刻意**放给所有持终端菜单的人,尽管这张清单确实是一份"哪张表的哪一列
		// 值得脱敏"的地图(审计 L-9)。
		//
		// 理由是脱敏的**可解释性**:结果里出现一串星号时,"这一列为什么看不到"必须有
		// 一个人自己查得到的答案。把它收成管理员专属,等于让每一次打码都变成一次
		// 需要去问人的意外 —— 而说不清道理的防护,最后都会被要求关掉。
		//
		// 这是一次有意识的取舍:泄露的是**字段名**(schema 元数据),不是数据;换来的
		// 是这道闸讲得清道理。写这条规则、改它、停用它仍然是管理员专属(见下三行),
		// 停用还会写审计 —— 真正危险的是"悄悄解除脱敏",不是"知道哪列被脱敏了"。
		a.GET("/sensitive-columns", menu("terminal"), h.ListSensitiveColumns)
		a.POST("/sensitive-columns", menu("perms"), admin, h.SaveSensitiveColumn)
		a.PUT("/sensitive-columns/:id", menu("perms"), admin, h.SaveSensitiveColumn)
		a.DELETE("/sensitive-columns/:id", menu("perms"), admin, h.DeleteSensitiveColumn)
		// 项目有自己的菜单键。读不设门槛:数据源配置页的归属下拉、发布单页的项目
		// 筛选都要用它,而那两处各有各的菜单 —— 把读绑在 project 上会让另外两个页面
		// 少半截功能。增删改则收在 project 菜单 + 管理员。
		a.GET("/projects", h.ListProjects)
		a.POST("/projects", menu("project"), admin, h.CreateProject)
		a.PATCH("/projects/:id", menu("project"), admin, h.UpdateProject)
		a.DELETE("/projects/:id", menu("project"), admin, h.DeleteProject)               // distinct connection tags (for pickers)
		a.POST("/connections", menu("db"), admin, h.CreateConnection)
		a.PUT("/connections/:id", menu("db"), admin, h.UpdateConnection)
		a.POST("/connections/:id/test", menu("db"), admin, h.TestConnection)
		a.PATCH("/connections/:id", menu("db"), admin, h.PatchConnection)
		a.PUT("/connections/:id/database-project", menu("db"), admin, h.SetDatabaseProject)
		a.GET("/connections/:id/schema", menu("terminal"), h.GetConnectionSchema)
		a.GET("/connections/:id/columns", menu("terminal"), h.GetTableColumns) // 终端 Tab 补全
		a.GET("/connections/:id/objects", menu("terminal"), h.GetConnectionObjects)
		a.GET("/connections/:id/object-source", menu("terminal"), h.GetConnectionObjectSource)
		// 业务方数据查询:只读、按库收窄、有行数上限与时限;查询权只来自查询授权(见 service/bizquery_exec.go)。
		a.GET("/bizquery/grants", menu("query"), h.MyQueryGrants)
		a.GET("/bizquery/tables", menu("query"), h.BizTables)
		a.GET("/bizquery/columns", menu("query"), h.BizColumns) // 自动补全:授权库里一张表的列
		a.POST("/bizquery/exec", menu("query"), h.BizQueryExec)
		a.POST("/bizquery/export", menu("query"), h.BizQueryExport)
		// 在网关里申请查询权(#173):生成一张审批单,走默认审批链;批准即生成授权。
		a.GET("/bizquery/apply/sources", menu("query"), h.ApplySources)
		a.GET("/bizquery/apply/sources/:id/databases", menu("query"), h.ApplySourceDatabases)
		a.GET("/bizquery/apply/mine", menu("query"), h.MyQueryGrantRequests)
		a.POST("/bizquery/apply", menu("query"), h.ApplyQueryGrant)
		a.POST("/bizquery/apply/:id/cancel", menu("query"), h.CancelQueryGrantRequest)
		// 查询授权总表:看归 perms(谁能查哪个库,是权限的一部分),撤销只给管理员。
		a.GET("/query-grants", menu("perms"), admin, h.ListQueryGrants)
		a.POST("/query-grants/:id/revoke", menu("perms"), admin, h.RevokeQueryGrant)
		a.POST("/query-grants", menu("perms"), admin, h.AdminCreateQueryGrant) // 控制台直接开通(#173)
		// 跨环境表结构对比:读两个库的目录,左右两个实例都要能访问(见 service/schemadiff.go)。
		// AI 巡检报告(.scratch/inspection-report):菜单 + 角色(平台管理员、DBA 负责人、DBA L2)
		inspection := middleware.InspectionViewer(repo)
		a.GET("/inspection-reports/latest", menu("inspection"), inspection, h.LatestInspectionReports)
		a.GET("/inspection-reports", menu("inspection"), inspection, h.ListInspectionReports)
		a.GET("/inspection-reports/:id", menu("inspection"), inspection, h.GetInspectionReport)
		a.GET("/inspection-reports/:id/html", menu("inspection"), inspection, h.InspectionReportHTML)
		a.GET("/inspection-reports/:id/view", menu("inspection"), inspection, h.InspectionReportView)
		a.GET("/schema-diff/connections", menu("schemadiff"), h.SchemaDiffConnections)
		a.GET("/schema-diff/connections/:id/databases", menu("schemadiff"), h.SchemaDiffDatabases)
		a.GET("/schema-diff/connections/:id/schemas", menu("schemadiff"), h.SchemaDiffSchemas)
		a.POST("/schema-diff", menu("schemadiff"), h.SchemaDiff)
		// Oracle 包/存储程序重新编译 —— 一次 DDL,判定与审计同终端(service.CompileObject)。
		a.POST("/connections/:id/objects/compile", menu("terminal"), h.CompileObject)
		// Oracle 程序包下载(.pck;多选打 zip)—— 只读,与查看源码同一道访问控制,另记导出审计。
		a.POST("/connections/:id/packages/download", menu("terminal"), h.DownloadPackages)
		// Oracle 无效对象:清点是只读,批量重编译是一批 DDL —— 整批一起判,判不过整批不执行。
		a.GET("/connections/:id/objects/invalid", menu("terminal"), h.InvalidObjects)
		// 元数据缓存:远端库的表清单与表结构,本地留一份(见 service/metadata.go)。
		// 读走终端菜单 —— 它服务的是同一件事:找到那张表。同步是**主动去连生产库**,
		// 所以要 admin:定时任务默认关着,而手动那一下同样不该谁都能按。
		a.GET("/metadata/search", menu("terminal"), h.SearchMetadata)
		a.GET("/connections/:id/metadata", menu("terminal"), h.ConnectionMetadata)
		a.POST("/connections/:id/metadata/sync", menu("db"), admin, h.SyncConnectionMetadata)
		a.GET("/connections/:id/metadata/sync", menu("db"), admin, h.ConnectionMetadataSyncStatus)
		a.POST("/connections/:id/objects/recompile", menu("terminal"), h.RecompileInvalid)

		// control tiers & environments — reads are open to any authenticated caller
		// (the terminal tree, instance labels and the connection form all render
		// from them); mutations are envtier-menu + admin, since a tier decides how
		// strictly its instances are governed.
		a.GET("/env-tiers", h.ListEnvTiers)
		a.POST("/env-tiers", menu("envtier"), admin, h.CreateEnvTier)
		a.PUT("/env-tiers/:code", menu("envtier"), admin, h.UpdateEnvTier)
		a.DELETE("/env-tiers/:code", menu("envtier"), admin, h.DeleteEnvTier)

		// 执行窗口(「班车」):读对进得来这个菜单的人开放 —— 一扇免审批的门开在哪、
		// 什么时候开,不该只有管理员知道;写限管理员,每次变动进审计链。
		a.GET("/exec-windows", h.ListExecWindows)
		// 回看一扇门开着的那几个小时里跑了什么。内容不按人收窄 —— 免审批的门是大家共用
		// 的,它放行过什么就该共同可见(service.WindowStatements)。
		//
		// 这里挂不上 menu():门槛是"execwindow 菜单**或**监督角色",而默认矩阵里审计角色
		// 没有 execwindow 键、只读角色却带着 audit 键 —— 单凭菜单键判,要么挡掉审计员,
		// 要么等于没判。认得出角色的只有 service,那条判断因此在 canReviewWindow。
		a.GET("/exec-windows/:id/statements", h.ExecWindowStatements)
		// 执行窗口有了自己的菜单键。申请**不再要求管理员** —— 这个功能的整个改动
		// 就在于"开门的人和签字的人不是同一个"。改和撤只有申请人自己或管理员能做,
		// 那条判断在 service 里(路由只认得菜单,认不出这一行是谁的)。
		a.POST("/exec-windows", menu("execwindow"), h.CreateExecWindow)
		a.PUT("/exec-windows/:id", menu("execwindow"), h.UpdateExecWindow)
		a.DELETE("/exec-windows/:id", menu("execwindow"), h.DeleteExecWindow)
		// 停用:申请人或平台管理员,不走审批(关门是收紧);管理后台全局管理页用它停别人的窗口(工单 07)
		a.POST("/exec-windows/:id/disable", h.DisableExecWindow)
		a.GET("/environments", h.ListEnvironments)
		a.GET("/environments/usage", menu("envtier"), admin, h.EnvironmentUsage)
		a.POST("/environments", menu("envtier"), admin, h.CreateEnvironment)
		a.PUT("/environments/:code", menu("envtier"), admin, h.UpdateEnvironment)
		a.DELETE("/environments/:code", menu("envtier"), admin, h.DeleteEnvironment)

		// roles & permissions — read is perms-menu; every mutation is admin-only
		// (prevents privilege escalation by non-admin perms holders like the DBA lead)
		a.GET("/roles", menu("perms"), admin, h.ListRoles)
		a.POST("/roles", menu("perms"), admin, h.CreateRole)
		a.GET("/roles/:id", menu("perms"), admin, h.GetRole)
		a.PATCH("/roles/:id", menu("perms"), admin, h.UpdateRole)
		a.DELETE("/roles/:id", menu("perms"), admin, h.DeleteRole)
		a.PUT("/roles/:id/menus", menu("perms"), admin, h.SetRoleMenus)
		a.PUT("/roles/:id/capabilities", menu("perms"), admin, h.SetRoleCapabilities)
		a.PUT("/roles/:id/tags", menu("perms"), admin, h.SetRoleTags)
		a.POST("/roles/:id/members", menu("perms"), admin, h.AddRoleMember)
		a.DELETE("/roles/:id/members/:userId", menu("perms"), admin, h.RemoveRoleMember)

		// users — read is perms-menu; mutations are admin-only
		a.GET("/online-users", menu("perms"), admin, h.OnlineUsers)
		// 在线变更任务(.scratch/osc-product):不挂菜单,看得到实例就能看进度,有 DDL 权限才能控制
		a.GET("/osc/jobs/:id", h.OSCJob)
		a.POST("/osc/jobs/:id/:action", h.OSCControl)
		a.GET("/users", menu("perms"), admin, h.ListUsers)
		a.POST("/users", menu("perms"), admin, h.CreateUser)
		a.POST("/users/invite", menu("perms"), admin, h.InviteUser)
		// SSO 绑定:按邮箱与 SSO 名单核对存量账号;绑定后该账号只能经 SSO 登录。
		a.POST("/users/:id/sso-unbind", menu("perms"), admin, h.SSOUnbind)
		a.PATCH("/users/:id", menu("perms"), admin, h.PatchUser)
		a.POST("/users/:id/password", menu("perms"), admin, h.SetUserPassword)
		a.POST("/users/:id/mfa/reset", menu("perms"), admin, h.ResetUserMFA)
		a.POST("/users/:id/mfa/bind", menu("perms"), admin, h.BindUserMFA)
		a.DELETE("/users/:id/chat-bindings", menu("perms"), admin, h.ChatUnbindUser)
			// Per-user data-access scope (overrides the role scope; see model.UserTag)
			a.GET("/users/:id/tags", menu("perms"), admin, h.UserTags)
			a.PUT("/users/:id/tags", menu("perms"), admin, h.SetUserTags)

		// risk command dictionary — read is available to terminal operators (risk
		// inspector needs it); edits are rules-menu + admin-only
		a.GET("/risk-commands", menu("terminal"), h.ListRiskCommands)
		a.POST("/risk-commands", menu("rules"), admin, h.UpsertRiskCommand)
		a.PATCH("/risk-commands/:name", menu("rules"), admin, h.PatchRiskCommand)
		a.DELETE("/risk-commands/:name", menu("rules"), admin, h.DeleteRiskCommand)

		// 数据库规范审查规则库 — the library is READ by terminal operators (the
		// check endpoint is how a developer self-checks a change before submitting
		// it, and the console needs the rule list to render the result), while
		// editing it is rules-menu + admin: a rule's level decides whether a
		// release is blocked, so lowering one is a policy change.
		a.GET("/sql-review/rules", menu("terminal"), h.ListReviewRules)
		a.GET("/sql-review/catalog", menu("terminal"), h.ReviewCatalog)
		a.POST("/sql-review/check", menu("terminal"), h.ReviewCheck)
		a.POST("/sql-review/rules", menu("rules"), admin, h.SaveReviewRule)
		a.PUT("/sql-review/rules/:id", menu("rules"), admin, h.SaveReviewRule)
		a.DELETE("/sql-review/rules/:id", menu("rules"), admin, h.DeleteReviewRule)

		// 发布流程 (CI/CD) — templates are configuration (admin), releases are work.
		// A release still executes through the same gate as the terminal, so the
		// menu grants the ability to RAISE one, not to bypass anything.
		a.GET("/pipelines", menu("pipeline"), h.ListPipelines)
		a.GET("/pipelines/:id", menu("pipeline"), h.GetPipeline)
		a.POST("/pipelines", menu("pipeline"), admin, h.SavePipeline)
		a.PUT("/pipelines/:id", menu("pipeline"), admin, h.SavePipeline)
		a.DELETE("/pipelines/:id", menu("pipeline"), admin, h.DeletePipeline)
		a.GET("/releases", menu("pipeline"), h.ListReleases)
		a.POST("/releases", menu("pipeline"), h.CreateRelease)
		a.GET("/releases/:id", menu("pipeline"), h.GetRelease)
		a.POST("/releases/:id/abort", menu("pipeline"), h.AbortRelease)
		a.POST("/releases/:id/stages/:stageId/continue", menu("pipeline"), h.ContinueStage)

		// approvals
		a.GET("/approvals", menu("approve"), h.ListApprovals)
		// 右上角那枚"待办":此刻在等**当前登录者**动手的工单。不挂菜单闸 —— 待执行的
		// 单子在总览页就能执行,而总览人人可见;没有任何待办的人拿到的是零。
		a.GET("/approvals/todo", h.TodoApprovals)
		// 脚本工单的全文:不挂菜单,可见范围由 service 按工单判(发起人 / 审批链 / 可执行的同事)
		a.GET("/approvals/:id/script", h.ApprovalScript)
		a.POST("/approvals/:id/approve", menu("approve"), h.ApproveApproval)
		// 执行由**发起人**发起,不是审批动作的一部分 —— 所以它是一个独立的接口,
		// 而不是 approve 的副作用。
		a.POST("/approvals/:id/execute", menu("terminal"), h.ExecuteApproval)
		a.POST("/approvals/:id/reject", menu("approve"), h.RejectApproval)
		// 撤回挂在 terminal 菜单下,不是 approve:发起人常常是从终端提交的那个人,
		// 他不一定有审批菜单 —— 而这张单本来就是他的。
		a.POST("/approvals/:id/cancel", menu("terminal"), h.CancelApproval)

		// audit
		a.GET("/audit", auditList, h.ListAudit) // 人人可读:全量只给管理员 / 审计员,其他人只看到自己的(「我的操作记录」,见 service.auditActor)
		// 筛选下拉里的实例清单。和列表同一个菜单键、同一条可见性 —— 它列的就是这个人
		// 在审计里见得到的那些实例。
		a.GET("/audit/instances", auditList, h.AuditInstances)
		a.GET("/audit/approval/:apNo", menu("audit"), auditReader, h.AuditTrail) // 一张审批单的全部操作记录
		// 校验哈希链。菜单之外还要监督角色,那条判断在 service(CanVerifyAuditChain)——
		// 它扫的是全表,越过了"没有监督角色只看得见自己"那条线,门槛得单独立。
		a.GET("/audit/verify", menu("audit"), auditReader, h.VerifyAudit)
		a.GET("/audit/export", menu("audit"), auditReader, h.ExportAudit)

		// settings & webhook — read is settings-menu; changes are admin-only
		a.GET("/settings", menu("settings"), admin, h.GetSettings)
		a.PUT("/settings", menu("settings"), admin, h.SaveSettings)
		a.PUT("/settings/webhook", menu("settings"), admin, h.SaveWebhook)
		a.POST("/settings/webhook/test", menu("settings"), admin, h.TestWebhook)
		a.POST("/settings/lark/test", menu("settings"), admin, h.TestLark)
		a.GET("/settings/chat-status", menu("settings"), admin, h.ChatStatus)
		a.POST("/settings/chat-test", menu("settings"), admin, h.ChatTest)
		// 投递记录里带着目标 URL(飞书机器人的 hook URL 本身就是凭据)与响应体,
		// 读它等于读凭据 —— 与写配置同级,收归管理员(L-13)。
		a.GET("/settings/webhook/deliveries", menu("settings"), admin, h.WebhookDeliveries)

		// 服务账号 — the machine principal an API credential acts as. Creation is
		// admin-only for the same reason issuing a credential is: together they
		// are a standing grant of change rights to an external system. Lifecycle
		// (disable, roles, tags) reuses the ordinary /users management.
		a.GET("/service-accounts", menu("settings"), admin, h.ListServiceAccounts)
		a.POST("/service-accounts", menu("settings"), admin, h.CreateServiceAccount)

		// 开放接口凭据 — a credential is a standing right to raise production
		// changes, so issuing and revoking one is admin-only; viewing the list
		// (which carries no secret) is settings-menu.
		a.GET("/api-clients", menu("settings"), admin, h.ListAPIClients)
		a.POST("/api-clients", menu("settings"), admin, h.CreateAPIClient)
		a.PUT("/api-clients/:id", menu("settings"), admin, h.UpdateAPIClient)
		a.DELETE("/api-clients/:id", menu("settings"), admin, h.DeleteAPIClient)
	}

	// Serve the built SPA (production): static assets + index.html fallback for
	// client-side routes. Non-matched, non-/api paths return index.html.
	if cfg.Server.WebDir != "" {
		serveSPA(r, cfg.Server.WebDir)
	}

	return r
}

// serveSPA serves the built frontend from dir, with an index.html fallback for
// any GET that isn't an API/known route (so deep links / refresh work).
func serveSPA(r *gin.Engine, dir string) {
	index := filepath.Join(dir, "index.html")
	// A misdeployed web dir (missing entirely, or extracted without assets/) used
	// to fail silently: the fallback below answered every asset request with
	// index.html and the browser reported a cryptic MIME error. Say so at startup.
	if !fileExists(index) {
		slog.Warn("web_dir has no index.html — the SPA cannot be served", "dir", dir)
	}
	assets := filepath.Join(dir, "assets")
	if !dirExists(assets) {
		slog.Warn("web_dir has no assets/ — the SPA will not load", "dir", dir)
	} else {
		// Vite content-hashes every filename under assets/, so a hit may be
		// cached forever; a new deploy changes the name, never the content.
		// Registered BEFORE Static so the route's handler chain includes it.
		r.Use(func(c *gin.Context) {
			if strings.HasPrefix(c.Request.URL.Path, "/assets/") {
				c.Header("Cache-Control", "public, max-age=31536000, immutable")
			}
			c.Next()
		})
		r.Static("/assets", assets)
	}
	for _, f := range []string{"favicon.ico", "favicon.svg", "vite.svg"} {
		if p := filepath.Join(dir, f); fileExists(p) {
			r.StaticFile("/"+f, p)
		}
	}
	r.NoRoute(func(c *gin.Context) {
		p := c.Request.URL.Path
		if c.Request.Method != http.MethodGet || strings.HasPrefix(p, "/api/") {
			c.Status(http.StatusNotFound)
			return
		}
		// A missing FILE must be a 404, never index.html. gin's Static falls
		// through to NoRoute when the file does not exist, so a stale cached
		// index.html asking for last release's hashed bundle — or an assets/
		// dir that never made it onto the box — got index.html served AS the
		// module script: the browser's "Expected a JavaScript module but got
		// text/html" with no hint the deploy was broken. Only extension-less
		// paths are client-side routes eligible for the SPA fallback.
		if strings.HasPrefix(p, "/assets/") || path.Ext(p) != "" {
			c.Status(http.StatusNotFound)
			return
		}
		// index.html is the one file that must always revalidate: it carries
		// this release's hashed asset URLs, and a cached copy after a deploy
		// points at bundles that no longer exist.
		c.Header("Cache-Control", "no-cache")
		c.File(index)
	})
}

func dirExists(p string) bool  { fi, err := os.Stat(p); return err == nil && fi.IsDir() }
func fileExists(p string) bool { fi, err := os.Stat(p); return err == nil && !fi.IsDir() }

// secretQueryKeys are query parameters that carry a credential. Some callers
// cannot send headers (WebSocket 握手带 ?token=,曾经的审批回调带 ?secret=), so the
// credential legitimately rides in the URL — but gin's default formatter writes
// path+rawQuery, which would file a working secret into the access log for
// anyone with log access, un-rotated (EA3).
var secretQueryKeys = map[string]bool{"secret": true, "token": true, "access_token": true}

// redactQuery rewrites a raw query string with the values of secretQueryKeys
// replaced, preserving the rest so the log still shows what was called with
// which non-sensitive parameters.
func redactQuery(raw string) string {
	if raw == "" {
		return ""
	}
	vals, err := url.ParseQuery(raw)
	if err != nil {
		return "REDACTED" // unparseable — drop it wholesale rather than risk a leak
	}
	for k := range vals {
		if secretQueryKeys[strings.ToLower(k)] {
			vals.Set(k, "REDACTED")
		}
	}
	return vals.Encode()
}

// accessLogger is gin.Logger with credential-bearing query parameters redacted.
func accessLogger() gin.HandlerFunc {
	return gin.LoggerWithFormatter(func(p gin.LogFormatterParams) string {
		// NOT p.Path — gin has already appended the raw query to it, which is the
		// very string being redacted here.
		path := p.Request.URL.Path
		if q := redactQuery(p.Request.URL.RawQuery); q != "" {
			path += "?" + q
		}
		return fmt.Sprintf("[GIN] %v | %3d | %13v | %15s | %-7s %#v\n%s",
			p.TimeStamp.Format("2006/01/02 - 15:04:05"), p.StatusCode, p.Latency,
			p.ClientIP, p.Method, path, p.ErrorMessage)
	})
}
