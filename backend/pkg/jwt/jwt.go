// Package jwt issues and verifies access tokens.
package jwt

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Claims carried in the access token.
type Claims struct {
	UserID   int64  `json:"uid"`
	RoleID   int64  `json:"rid"`
	RoleCode string `json:"rc"`
	Name     string `json:"name"`
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
func (m *Manager) Issue(userID, roleID int64, roleCode, name string, tokenVersion int64) (string, time.Time, error) {
	return m.IssueTTL(userID, roleID, roleCode, name, tokenVersion, m.ttl)
}

// IssueTTL creates a signed token with an explicit lifetime (falls back to the
// manager default when ttl <= 0). Used to honor the security.sessionTTL setting.
func (m *Manager) IssueTTL(userID, roleID int64, roleCode, name string, tokenVersion int64, ttl time.Duration) (string, time.Time, error) {
	if ttl <= 0 {
		ttl = m.ttl
	}
	exp := time.Now().Add(ttl)
	claims := Claims{
		UserID:       userID,
		RoleID:       roleID,
		RoleCode:     roleCode,
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
