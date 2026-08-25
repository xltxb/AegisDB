package middleware

import (
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"velagateway/internal/model"
	"velagateway/internal/repository"
	"velagateway/pkg/crypto"
	"velagateway/pkg/resp"
)

const ctxAPIClient = "vela_api_client"

// CurrentAPIClient returns the external client behind an open-API request, or
// nil for a console request.
func CurrentAPIClient(c *gin.Context) *model.APIClient {
	if v, ok := c.Get(ctxAPIClient); ok {
		if cl, ok := v.(*model.APIClient); ok {
			return cl
		}
	}
	return nil
}

// APIClientAuth authenticates an external system on the open API.
//
// Credential format: `Authorization: Bearer <key>.<secret>`. The key is the
// public half (safe to log, used for the lookup); the secret is compared against
// a bcrypt hash. `X-Vela-Key` + `X-Vela-Secret` headers are accepted as well, for
// callers whose HTTP client reserves the Authorization header.
//
// It FAILS CLOSED on everything: unknown key, wrong secret, disabled client,
// missing or disabled service account, source IP outside the client's allowlist.
// This endpoint creates changes that end up executing against production, so a
// half-authenticated caller must never be treated as an anonymous one.
//
// The service account is injected as the request's user, which is what lets
// every downstream layer — capability matrix, tag scope, MFA policy, audit
// attribution — work unchanged on this path.
func APIClientAuth(repo *repository.Repo) gin.HandlerFunc {
	return func(c *gin.Context) {
		key, secret := apiCredential(c)
		if key == "" || secret == "" {
			resp.Abort(c, resp.CodeUnauthorized, "缺少 API 凭据")
			return
		}
		cl, err := repo.GetAPIClientByKey(key)
		if err != nil {
			// Absorb a bcrypt comparison so an unknown key and a wrong secret take
			// comparable time — otherwise the endpoint answers "does this key
			// exist?" to anyone who asks.
			crypto.CheckPassword(dummyAPISecretHash, secret)
			slog.Warn("open api: unknown key", "key", clipKey(key), "ip", c.ClientIP())
			resp.Abort(c, resp.CodeUnauthorized, "API 凭据无效")
			return
		}
		if !crypto.CheckPassword(cl.SecretHash, secret) {
			slog.Warn("open api: bad secret", "client", cl.Name, "ip", c.ClientIP())
			resp.Abort(c, resp.CodeUnauthorized, "API 凭据无效")
			return
		}
		if !cl.Enabled {
			resp.Abort(c, resp.CodeForbidden, "API 凭据已停用")
			return
		}
		if !apiIPAllowed(cl.AllowIPs, c.ClientIP()) {
			slog.Warn("open api: source ip rejected", "client", cl.Name, "ip", c.ClientIP())
			resp.Abort(c, resp.CodeIPBlocked, "来源 IP 不在该凭据的白名单内")
			return
		}
		u, uerr := repo.GetUserByID(cl.UserID)
		if uerr != nil {
			resp.Abort(c, resp.CodeForbidden, "该凭据绑定的服务账号不存在")
			return
		}
		if u.Status != "active" {
			// Disabling the service account is a legitimate way to stop every
			// integration that borrows it, so it must actually stop them.
			resp.Abort(c, resp.CodeForbidden, "该凭据绑定的服务账号已停用")
			return
		}
		_ = repo.TouchAPIClient(cl.ID, time.Now())
		c.Set(ctxAPIClient, cl)
		c.Set(ctxUser, u)
		c.Next()
	}
}

// RequireScope refuses a credential that does not hold the named scope. A
// read-only integration must not be able to raise a production change just
// because it can read one.
func RequireScope(scope string) gin.HandlerFunc {
	return func(c *gin.Context) {
		cl := CurrentAPIClient(c)
		if cl == nil {
			resp.Abort(c, resp.CodeUnauthorized, "缺少 API 凭据")
			return
		}
		for _, s := range strings.Split(cl.Scopes, ",") {
			if strings.TrimSpace(s) == scope {
				c.Next()
				return
			}
		}
		resp.Abort(c, resp.CodeForbidden, "该凭据没有 "+scope+" 权限")
	}
}

// apiCredential reads the key/secret from either supported carrier.
func apiCredential(c *gin.Context) (key, secret string) {
	if k := strings.TrimSpace(c.GetHeader("X-Vela-Key")); k != "" {
		return k, strings.TrimSpace(c.GetHeader("X-Vela-Secret"))
	}
	tok := strings.TrimSpace(c.GetHeader("Authorization"))
	const p = "bearer "
	if len(tok) > len(p) && strings.EqualFold(tok[:len(p)], p) {
		tok = strings.TrimSpace(tok[len(p):])
	} else {
		return "", ""
	}
	// key.secret — the key is generated without dots, so the FIRST dot splits.
	i := strings.Index(tok, ".")
	if i <= 0 || i == len(tok)-1 {
		return "", ""
	}
	return tok[:i], tok[i+1:]
}

// apiIPAllowed reports whether the source is permitted by this client's list.
// An empty list means "any source" — see model.APIClient.AllowIPs.
func apiIPAllowed(list, clientIP string) bool {
	entries := splitCIDRs(list)
	if len(entries) == 0 {
		return true
	}
	ip := net.ParseIP(clientIP)
	if ip == nil {
		return false
	}
	for _, e := range entries {
		if ipMatches(e, ip) {
			return true
		}
	}
	return false
}

// clipKey shortens a key for logs: enough to identify the client, not enough to
// be a credential half worth harvesting from a log file.
func clipKey(k string) string {
	if len(k) <= 8 {
		return k
	}
	return k[:8] + "…"
}

// dummyAPISecretHash equalises the timing of an unknown-key lookup against a
// real bcrypt comparison (same reasoning as service.dummyPasswordHash).
var dummyAPISecretHash, _ = crypto.HashPassword("vela-open-api-timing-equalizer")
