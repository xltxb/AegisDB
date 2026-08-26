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

	"velagateway/internal/handler"
	"velagateway/internal/middleware"
	"velagateway/internal/model"
	"velagateway/internal/repository"
	"velagateway/internal/service"
	"velagateway/pkg/jwt"
	"velagateway/pkg/resp"
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
	r.Use(accessLogger(), middleware.Recovery(), middleware.CORS(cfg.Server.CORSOrigins), middleware.Latency())

	r.GET("/healthz", func(c *gin.Context) { resp.OK(c, gin.H{"status": "ok"}) })
	// The contract, two ways: the raw file for tooling, and a rendered page for
	// people. Both are unauthenticated — the same surface the console's login
	// page is on, and an API contract nobody can read is a contract nobody follows.
	r.StaticFile("/openapi.yaml", handler.SpecPath) // API contract (backend doc §11)
	r.GET("/docs", h.APIDocs)
	r.GET("/docs/*file", h.APIDocsAsset) // vendored Swagger UI assets, embedded in the binary

	auth := middleware.JWTAuth(jwtMgr, repo)
	menu := func(key string) gin.HandlerFunc { return middleware.MenuGuard(repo, key) }
	// admin restricts role/permission configuration + user management to the
	// platform-admin role (viewing stays at the `perms` menu).
	admin := middleware.AdminOnly(repo)

	v1 := r.Group("/api/v1")

	// ---- public ----
	// Login is unauthenticated but still subject to the IP allowlist so it can't
	// be brute-forced from a non-allowlisted source when the allowlist is on (R29).
	v1.POST("/auth/login", middleware.IPAllowlist(repo), h.Login)
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
		// Review WITHOUT raising a ticket — the pre-merge gate a CI job runs.
		open.POST("/sql-review", middleware.RequireScope(model.ScopeReviewCheck), h.OpenReviewCheck)
		// Discovery: what this credential may target, and which flows it may name.
		open.GET("/instances", middleware.RequireScope(model.ScopeReleaseRead), h.OpenListInstances)
		open.GET("/pipelines", middleware.RequireScope(model.ScopeReleaseRead), h.OpenListPipelines)
	}

	// 审批魔方 approval-result callback: authenticated by a shared secret (Authorization:
	// Bearer, or ?secret= query) + its own optional source-IP allowlist inside the
	// handler, NOT by the user JWT / user IP allowlist — the caller is the approval
	// service, not a console user.
	v1.POST("/approvals/lark/callback", h.LarkApprovalCallback)

	// ---- authenticated ----
	// IPAllowlist runs after auth so the source IP is enforced on every data API
	// (Session & Security · IP allowlist); loopback is always permitted.
	a := v1.Group("", auth, middleware.IPAllowlist(repo))
	{
		a.POST("/auth/logout", h.Logout)
		a.GET("/auth/me", h.Me)
		a.GET("/gateway/stats", h.GatewayStats) // live gateway latency (no menu gate)
		a.GET("/approval-chain", h.ApprovalChain) // default chain for terminal/inspector

		// MFA (TOTP) enrollment for the current user
		a.POST("/auth/mfa/setup", h.MFASetup)
		a.POST("/auth/mfa/enable", h.MFAEnable)
		a.POST("/auth/mfa/disable", h.MFADisable)

		// notifications — every authenticated user has an inbox (no menu gate)
		a.GET("/notifications", h.ListNotifications)
		a.POST("/notifications/read", h.MarkNotificationsRead)



		// terminal
		a.POST("/risk/check", menu("terminal"), h.RiskCheck)
		a.POST("/terminal/exec", menu("terminal"), h.Exec)
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
		a.GET("/export/download", menu("terminal"), h.ExportDownload)

		// connections — read is open (tag-filtered); instance config is admin-only
		a.GET("/connections", h.ListConnections) // tag-filtered per the caller's role
		a.GET("/tags", h.ListTags)               // distinct connection tags (for pickers)
		a.POST("/connections", menu("db"), admin, h.CreateConnection)
		a.PUT("/connections/:id", menu("db"), admin, h.UpdateConnection)
		a.POST("/connections/:id/test", menu("db"), admin, h.TestConnection)
		a.PATCH("/connections/:id", menu("db"), admin, h.PatchConnection)
		a.GET("/connections/:id/schema", menu("terminal"), h.GetConnectionSchema)
		a.GET("/connections/:id/objects", menu("terminal"), h.GetConnectionObjects)
		a.GET("/connections/:id/object-source", menu("terminal"), h.GetConnectionObjectSource)
		// Oracle 包/存储程序重新编译 —— 一次 DDL,判定与审计同终端(service.CompileObject)。
		a.POST("/connections/:id/objects/compile", menu("terminal"), h.CompileObject)

		// control tiers & environments — reads are open to any authenticated caller
		// (the terminal tree, instance labels and the connection form all render
		// from them); mutations are envtier-menu + admin, since a tier decides how
		// strictly its instances are governed.
		a.GET("/env-tiers", h.ListEnvTiers)
		a.POST("/env-tiers", menu("envtier"), admin, h.CreateEnvTier)
		a.PUT("/env-tiers/:code", menu("envtier"), admin, h.UpdateEnvTier)
		a.DELETE("/env-tiers/:code", menu("envtier"), admin, h.DeleteEnvTier)
		a.GET("/environments", h.ListEnvironments)
		a.GET("/environments/usage", menu("envtier"), h.EnvironmentUsage)
		a.POST("/environments", menu("envtier"), admin, h.CreateEnvironment)
		a.PUT("/environments/:code", menu("envtier"), admin, h.UpdateEnvironment)
		a.DELETE("/environments/:code", menu("envtier"), admin, h.DeleteEnvironment)

		// roles & permissions — read is perms-menu; every mutation is admin-only
		// (prevents privilege escalation by non-admin perms holders like the DBA lead)
		a.GET("/roles", menu("perms"), h.ListRoles)
		a.GET("/roles/:id", menu("perms"), h.GetRole)
		a.PATCH("/roles/:id", menu("perms"), admin, h.UpdateRole)
		a.PUT("/roles/:id/menus", menu("perms"), admin, h.SetRoleMenus)
		a.PUT("/roles/:id/capabilities", menu("perms"), admin, h.SetRoleCapabilities)
		a.PUT("/roles/:id/tags", menu("perms"), admin, h.SetRoleTags)
		a.POST("/roles/:id/members", menu("perms"), admin, h.AddRoleMember)
		a.DELETE("/roles/:id/members/:userId", menu("perms"), admin, h.RemoveRoleMember)

		// users — read is perms-menu; mutations are admin-only
		a.GET("/users", menu("perms"), h.ListUsers)
		a.POST("/users", menu("perms"), admin, h.CreateUser)
		a.POST("/users/invite", menu("perms"), admin, h.InviteUser)
		a.PATCH("/users/:id", menu("perms"), admin, h.PatchUser)
		a.POST("/users/:id/password", menu("perms"), admin, h.SetUserPassword)
		a.POST("/users/:id/mfa/reset", menu("perms"), admin, h.ResetUserMFA)
		a.POST("/users/:id/mfa/bind", menu("perms"), admin, h.BindUserMFA)
			// Per-user data-access scope (overrides the role scope; see model.UserTag)
			a.GET("/users/:id/tags", menu("perms"), h.UserTags)
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
		a.POST("/approvals/:id/approve", menu("approve"), h.ApproveApproval)
		a.POST("/approvals/:id/reject", menu("approve"), h.RejectApproval)

		// audit
		a.GET("/audit", menu("audit"), h.ListAudit)
		a.GET("/audit/export", menu("audit"), h.ExportAudit)

		// settings & webhook — read is settings-menu; changes are admin-only
		a.GET("/settings", menu("settings"), h.GetSettings)
		a.PUT("/settings", menu("settings"), admin, h.SaveSettings)
		a.PUT("/settings/webhook", menu("settings"), admin, h.SaveWebhook)
		a.POST("/settings/webhook/test", menu("settings"), admin, h.TestWebhook)
		a.POST("/settings/lark/test", menu("settings"), admin, h.TestLark)
		a.GET("/settings/webhook/deliveries", menu("settings"), h.WebhookDeliveries)

		// 服务账号 — the machine principal an API credential acts as. Creation is
		// admin-only for the same reason issuing a credential is: together they
		// are a standing grant of change rights to an external system. Lifecycle
		// (disable, roles, tags) reuses the ordinary /users management.
		a.GET("/service-accounts", menu("settings"), h.ListServiceAccounts)
		a.POST("/service-accounts", menu("settings"), admin, h.CreateServiceAccount)

		// 开放接口凭据 — a credential is a standing right to raise production
		// changes, so issuing and revoking one is admin-only; viewing the list
		// (which carries no secret) is settings-menu.
		a.GET("/api-clients", menu("settings"), h.ListAPIClients)
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
// cannot send headers (审批魔方 registers a callback URL and nothing else), so the
// credential legitimately rides in the URL — but gin's default formatter writes
// path+rawQuery, which would file a working approve-anything secret into the
// access log for anyone with log access, un-rotated (EA3).
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
