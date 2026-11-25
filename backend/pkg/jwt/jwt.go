// Package jwt issues and verifies access tokens.
package jwt

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
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
	// AuthMethod 记这个会话是怎么登进来的:"sso" = SSO 登录;空 = 本地口令或其它(含此字段
	// 出现之前签发的老 token —— 一律按"不是 SSO 会话"处理,向后兼容)。登录时刻即 IssuedAt。
	// 用途:只经 SSO 登录的人绑 MFA 时不知道本地口令,刚经 SSO 登录的会话可以代替出示口令
	// (2026-10-26 回归 03)。
	AuthMethod string `json:"am,omitempty"`
	jwt.RegisteredClaims
}

// AuthMethodSSO 是经 SSO 登录签发的会话的 AuthMethod。
const AuthMethodSSO = "sso"

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

// CSRFToken 是一个会话(jti)的 CSRF 校验值:不用存,收到请求时重新算一遍比较。
func (m *Manager) CSRFToken(jti string) string {
	mac := hmac.New(sha256.New, m.secret)
	mac.Write([]byte("aegisdb:csrf:v1:" + jti))
	return hex.EncodeToString(mac.Sum(nil))
}

// Issue creates a signed token for the given identity using the default TTL.
func (m *Manager) Issue(userID, roleID int64, roleCode, name string, tokenVersion int64) (string, time.Time, error) {
	return m.IssueTTL(userID, roleID, roleCode, name, tokenVersion, m.ttl)
}

// IssueTTL creates a signed token with an explicit lifetime (falls back to the
// manager default when ttl <= 0). Used to honor the security.sessionTTL setting.
func (m *Manager) IssueTTL(userID, roleID int64, roleCode, name string, tokenVersion int64, ttl time.Duration) (string, time.Time, error) {
	return m.IssueSession(userID, roleID, roleCode, name, tokenVersion, ttl, "")
}

// IssueSession 同 IssueTTL,另记会话的登录方式(见 Claims.AuthMethod)。
func (m *Manager) IssueSession(userID, roleID int64, roleCode, name string, tokenVersion int64, ttl time.Duration, authMethod string) (string, time.Time, error) {
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
		AuthMethod:   authMethod,
		RegisteredClaims: jwt.RegisteredClaims{
			// jti:这一次登录的唯一标识。它存在的理由是"把某件事绑到**一个会话**上",
			// 而 TokenVersion 做不到这件事 —— 那是**用户级**的代次,同一个人的每个
			// 并发会话共享同一个值。MFA 步进验证的宽限期原先就键在 TokenVersion 上,
			// 于是受害者做过一次步进之后,一个被盗的 token 在宽限期内也跟着免验
			// (M-6)。绑到 jti 上,一个会话的验证只替它自己担保。
			ID:        newJTI(),
			ExpiresAt: jwt.NewNumericDate(exp),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Issuer:    "aegisdb",
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString(m.secret)
	return signed, exp, err
}

// newJTI returns a random per-token identifier. On the (practically impossible)
// failure of the system RNG it falls back to a timestamp: a weaker id still
// separates concurrent sessions, while returning an empty one would silently
// merge them back into the shared-key behaviour this exists to end.
func newJTI() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "t" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b[:])
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
