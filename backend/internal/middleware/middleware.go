// Package middleware holds Gin middlewares: CORS, JWT auth, menu guard, recovery.
package middleware

import (
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"velagateway/internal/metrics"
	"velagateway/internal/model"
	"velagateway/internal/repository"
	"velagateway/pkg/jwt"
	"velagateway/pkg/resp"
)

const ctxUser = "vela_user"

// CurrentUser returns the authenticated user from the gin context.
func CurrentUser(c *gin.Context) *model.User {
	if v, ok := c.Get(ctxUser); ok {
		if u, ok := v.(*model.User); ok {
			return u
		}
	}
	return nil
}

// CORS allows the SPA dev origins with credentials + Bearer headers.
func CORS(origins []string) gin.HandlerFunc {
	allow := map[string]bool{}
	for _, o := range origins {
		allow[o] = true
	}
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		// Only reflect an explicitly allowlisted origin. Never echo an arbitrary
		// origin together with Allow-Credentials:true (that would let any site
		// make credentialed cross-origin calls). An empty allowlist means
		// same-origin only (prod serves the SPA from web_dir) → no CORS headers.
		if origin != "" && allow[origin] {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Vary", "Origin")
			c.Header("Access-Control-Allow-Methods", "GET,POST,PUT,PATCH,DELETE,OPTIONS")
			c.Header("Access-Control-Allow-Headers", "Authorization,Content-Type")
			c.Header("Access-Control-Allow-Credentials", "true")
		}
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

// JWTAuth parses the Bearer token and injects ctx.User.
func JWTAuth(mgr *jwt.Manager, repo *repository.Repo) gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.GetHeader("Authorization")
		if !strings.HasPrefix(h, "Bearer ") {
			resp.Abort(c, resp.CodeUnauthorized, "未登录 / Token 失效")
			return
		}
		claims, err := mgr.Parse(strings.TrimPrefix(h, "Bearer "))
		if err != nil {
			resp.Abort(c, resp.CodeUnauthorized, "未登录 / Token 失效")
			return
		}
		u, err := repo.GetUserByID(claims.UserID)
		if err != nil {
			resp.Abort(c, resp.CodeUnauthorized, "用户不存在")
			return
		}
		if u.Status == "disabled" {
			resp.Abort(c, resp.CodeForbidden, "账号已停用")
			return
		}
		// Reject tokens from a superseded session generation (logout / disable /
		// password reset bumps TokenVersion) — M1 revocation.
		if claims.TokenVersion != u.TokenVersion {
			resp.Abort(c, resp.CodeUnauthorized, "会话已失效，请重新登录")
			return
		}
		c.Set(ctxUser, u)
		c.Next()
	}
}

// Latency records each request's real duration into the metrics recorder so the
// UI can display live gateway latency instead of a hard-coded number.
func Latency() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		metrics.Default.Record(time.Since(start))
	}
}

// AdminOnly restricts an endpoint to the platform-admin role. Role-permission
// configuration (capability matrix, menus, tags, members) and user-credential
// management must not be editable by other privileged roles such as the DBA
// lead — that would allow privilege escalation.
func AdminOnly(repo *repository.Repo) gin.HandlerFunc {
	return func(c *gin.Context) {
		u := CurrentUser(c)
		if u == nil {
			resp.Abort(c, resp.CodeUnauthorized, "未登录")
			return
		}
		role, err := repo.GetRole(u.RoleID)
		if err != nil || role == nil || role.Code != "admin" {
			resp.Abort(c, resp.CodeForbidden, "仅平台管理员可执行此操作")
			return
		}
		c.Next()
	}
}

// MenuGuard enforces that the user's role can access the given menu key.
func MenuGuard(repo *repository.Repo, key string) gin.HandlerFunc {
	return func(c *gin.Context) {
		u := CurrentUser(c)
		if u == nil {
			resp.Abort(c, resp.CodeUnauthorized, "未登录")
			return
		}
		menus, _ := repo.MenusForRole(u.RoleID)
		if !menus[key] {
			resp.Abort(c, resp.CodeForbidden, "无菜单权限")
			return
		}
		c.Next()
	}
}

// IPAllowlist enforces the Session & Security IP allowlist: when
// security.ipAllowEnabled is on, only source IPs matching a listed IP/CIDR may
// reach the gateway. Loopback is always permitted so an operator cannot lock
// themselves out locally. Disabled (default) → pass-through.
func IPAllowlist(repo *repository.Repo) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !settingBool(repo, "security.ipAllowEnabled", false) {
			c.Next()
			return
		}
		// ClientIP honors X-Forwarded-For ONLY from configured trusted proxies
		// (router calls SetTrustedProxies; default = trust none). So a client
		// cannot forge XFF to spoof a loopback / allowlisted address (H2): with no
		// trusted proxy, ClientIP == the real TCP peer; behind a trusted reverse
		// proxy, it is the genuine forwarded client IP.
		ip := net.ParseIP(c.ClientIP())
		if ip == nil || ip.IsLoopback() {
			c.Next()
			return
		}
		list := settingStr(repo, "security.ipAllowlist", "")
		for _, entry := range splitCIDRs(list) {
			if ipMatches(entry, ip) {
				c.Next()
				return
			}
		}
		resp.Abort(c, resp.CodeIPBlocked, "来源 IP 不在允许列表内")
	}
}

func ipMatches(entry string, ip net.IP) bool {
	if strings.Contains(entry, "/") {
		_, network, err := net.ParseCIDR(entry)
		return err == nil && network.Contains(ip)
	}
	other := net.ParseIP(entry)
	return other != nil && other.Equal(ip)
}

func splitCIDRs(s string) []string {
	fields := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == '\n' || r == ' ' || r == '\t' })
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// settingStr / settingBool read a JSON-encoded setting straight from the repo
// (middleware has no service dependency).
func settingStr(repo *repository.Repo, key, def string) string {
	v, err := repo.GetSetting(key)
	if err != nil || v == "" {
		return def
	}
	var s string
	if json.Unmarshal([]byte(v), &s) == nil {
		return s
	}
	return strings.Trim(v, "\"")
}

func settingBool(repo *repository.Repo, key string, def bool) bool {
	v, err := repo.GetSetting(key)
	if err != nil || v == "" {
		return def
	}
	var b bool
	if json.Unmarshal([]byte(v), &b) == nil {
		return b
	}
	return def
}

// Recovery converts panics into a 500 envelope.
func Recovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if err := recover(); err != nil {
				slog.Error("panic recovered", "err", err, "path", c.Request.URL.Path)
				resp.Abort(c, resp.CodeInternalError, "服务器内部错误")
			}
		}()
		c.Next()
	}
}
