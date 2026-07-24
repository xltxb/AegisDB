package bootstrap

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"

	"velagateway/internal/handler"
	"velagateway/internal/middleware"
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
	r.Use(gin.Logger(), middleware.Recovery(), middleware.CORS(cfg.Server.CORSOrigins), middleware.Latency())

	r.GET("/healthz", func(c *gin.Context) { resp.OK(c, gin.H{"status": "ok"}) })
	r.StaticFile("/openapi.yaml", "docs/openapi.yaml") // API contract (backend doc §11)

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
		a.GET("/scripts/config", menu("terminal"), h.ScriptConfig)
		a.POST("/scripts/scan", menu("terminal"), h.ScriptScan)
		a.POST("/scripts/execute", menu("terminal"), h.ScriptExecute)
		// upload-file management (per-user: list / upload / download / delete)
		a.GET("/scripts/uploads", menu("terminal"), h.ScriptUploads)
		a.POST("/scripts/upload", menu("terminal"), h.ScriptUpload)
		a.GET("/scripts/uploads/:id/download", menu("terminal"), h.ScriptUploadDownload)
		a.GET("/scripts/uploads/:id/content", menu("terminal"), h.ScriptUploadContent)
		a.DELETE("/scripts/uploads/:id", menu("terminal"), h.ScriptUploadDelete)

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

		// risk command dictionary — read is available to terminal operators (risk
		// inspector needs it); edits are rules-menu + admin-only
		a.GET("/risk-commands", menu("terminal"), h.ListRiskCommands)
		a.POST("/risk-commands", menu("rules"), admin, h.UpsertRiskCommand)
		a.PATCH("/risk-commands/:name", menu("rules"), admin, h.PatchRiskCommand)
		a.DELETE("/risk-commands/:name", menu("rules"), admin, h.DeleteRiskCommand)

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
	if assets := filepath.Join(dir, "assets"); dirExists(assets) {
		r.Static("/assets", assets)
	}
	for _, f := range []string{"favicon.ico", "favicon.svg", "vite.svg"} {
		if p := filepath.Join(dir, f); fileExists(p) {
			r.StaticFile("/"+f, p)
		}
	}
	r.NoRoute(func(c *gin.Context) {
		if c.Request.Method != http.MethodGet || strings.HasPrefix(c.Request.URL.Path, "/api/") {
			c.Status(http.StatusNotFound)
			return
		}
		c.File(index)
	})
}

func dirExists(p string) bool  { fi, err := os.Stat(p); return err == nil && fi.IsDir() }
func fileExists(p string) bool { fi, err := os.Stat(p); return err == nil && !fi.IsDir() }
