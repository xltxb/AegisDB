// Package jwt issues and verifies access tokens.
package jwt

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Claims carried in the access token.
type Claims struct {
	UserID int64  `json:"uid"`
	Name   string `json:"name"`
	// 刻意**不带角色**。
	//
	// 这里曾经有 RoleID / RoleCode,而全仓没有一处读它们:每个请求都从库里取用户、按
	// EffectiveRoleIDs 现算权限(见 middleware)。多角色之后「主角色」这个概念本身也不
	// 成立了 —— token 里放一个不准确、又没人看的字段,只会让下一个读到它的人以为权限
	// 是从这里来的。
	//
	// 顺带:JWT 的 payload 只是 base64,拿到 token 的人能直接读出里面的一切。少放一样
	// 就少泄露一样。
	// TokenVersion pins the token to the user's current session generation.
	// Logout / disable / password reset bumps the user's version, instantly
	// invalidating every token issued before it (M1).
	TokenVersion int64 `json:"tv"`
	jwt.RegisteredClaims
}

// Manager issues/parses tokens with a fixed secret + TTL.
type Manager struct {
	secret []byte
	ttl    time.Duration
}

func New(secret string, ttlHours int) *Manager {
	if ttlHours <= 0 {
		ttlHours = 8
	}
	return &Manager{secret: []byte(secret), ttl: time.Duration(ttlHours) * time.Hour}
}

// Issue creates a signed token for the given identity using the default TTL.
func (m *Manager) Issue(userID int64, name string, tokenVersion int64) (string, time.Time, error) {
	return m.IssueTTL(userID, name, tokenVersion, m.ttl)
}

// IssueTTL creates a signed token with an explicit lifetime (falls back to the
// manager default when ttl <= 0). Used to honor the security.sessionTTL setting.
func (m *Manager) IssueTTL(userID int64, name string, tokenVersion int64, ttl time.Duration) (string, time.Time, error) {
	if ttl <= 0 {
		ttl = m.ttl
	}
	exp := time.Now().Add(ttl)
	claims := Claims{
		UserID:       userID,
		Name:         name,
		TokenVersion: tokenVersion,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(exp),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Issuer:    "vela-gateway",
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString(m.secret)
	return signed, exp, err
}

// Parse validates a token string and returns its claims.
func (m *Manager) Parse(tokenStr string) (*Claims, error) {
	claims := &Claims{}
	tok, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return m.secret, nil
	})
	if err != nil {
		return nil, err
	}
	if !tok.Valid {
		return nil, errors.New("invalid token")
	}
	return claims, nil
}
