package middleware

import (
	"crypto/subtle"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"aegisdb/pkg/jwt"
)

// 控制台的会话放在 HttpOnly Cookie 里(2026-11 审计 V-16)。
//
// 从前 JWT 存在 localStorage:页面上一旦出现 XSS,脚本就能把令牌读走,拿到别的机器上继续用。放进 HttpOnly
// Cookie 之后脚本读不到它。代价是 Cookie 会被浏览器自动带上,所以要防 CSRF:
//
//   - aegisdb_session:JWT 本身。HttpOnly、SameSite=Strict。
//   - aegisdb_csrf:由 jti 派生的校验值,脚本能读。写操作要把它放进 X-CSRF-Token,WebSocket 握手放进子协议。
//     别的站点读不到这个 Cookie,也就发不出带对值的请求;它和会话绑定,登录一次换一个。
//
// 只有声明了 X-AegisDB-Client: web 的客户端才走这条路(SPA 每个请求都带)。脚本、Swagger、老客户端照旧从登录
// 响应里拿 token、走 Authorization: Bearer —— Bearer 要靠代码显式附加,不存在 CSRF 问题,也不校验 CSRF。
const (
	SessionCookie   = "aegisdb_session"
	CSRFCookie      = "aegisdb_csrf"
	CSRFHeader      = "X-CSRF-Token"
	WebClientHeader = "X-AegisDB-Client"
)

// IsWebClient 报告这个请求是不是来自控制台 SPA。
func IsWebClient(c *gin.Context) bool { return c.GetHeader(WebClientHeader) == "web" }

// 经 ELB / 反向代理终止 TLS 时,到网关这一段是明文,看 X-Forwarded-Proto。它只决定 Cookie 带不带
// Secure:伪造它最多让 Secure 多带或少带,在明文上多带只是登录不上,不会放大任何权限。
func requestIsHTTPS(c *gin.Context) bool {
	if c.Request.TLS != nil {
		return true
	}
	proto, _, _ := strings.Cut(c.GetHeader("X-Forwarded-Proto"), ",")
	return strings.EqualFold(strings.TrimSpace(proto), "https")
}

func setCookie(c *gin.Context, name, value string, exp time.Time, httpOnly bool) {
	maxAge := int(time.Until(exp).Seconds())
	if value == "" {
		maxAge = -1
	}
	http.SetCookie(c.Writer, &http.Cookie{
		Name: name, Value: value, Path: "/", Expires: exp, MaxAge: maxAge,
		HttpOnly: httpOnly, Secure: requestIsHTTPS(c), SameSite: http.SameSiteStrictMode,
	})
}

// SetSessionCookies 把刚签发的会话写进 Cookie,返回给前端的会话标记 "<uid>.<csrf>":前端靠它知道已登录、
// 分辨"还是不是同一个人"。标记不是凭据,单拿它什么也做不了。
func SetSessionCookies(c *gin.Context, mgr *jwt.Manager, token string, exp time.Time) (string, error) {
	claims, err := mgr.Parse(token)
	if err != nil {
		return "", err
	}
	csrf := mgr.CSRFToken(claims.ID)
	setCookie(c, SessionCookie, token, exp, true)
	setCookie(c, CSRFCookie, csrf, exp, false)
	return strconv.FormatInt(claims.UserID, 10) + "." + csrf, nil
}

// ClearSessionCookies 清掉会话 Cookie(登出、Cookie 里的会话已失效)。
func ClearSessionCookies(c *gin.Context) {
	past := time.Unix(0, 0)
	setCookie(c, SessionCookie, "", past, true)
	setCookie(c, CSRFCookie, "", past, false)
}

// SessionTokenFromCookie 取 Cookie 里的会话令牌;没有返回空串。
func SessionTokenFromCookie(c *gin.Context) string {
	ck, err := c.Request.Cookie(SessionCookie)
	if err != nil {
		return ""
	}
	return ck.Value
}

// CSRFMatches 报告 got 是不是这个会话的 CSRF 校验值。
func CSRFMatches(mgr *jwt.Manager, jti, got string) bool {
	return got != "" && subtle.ConstantTimeCompare([]byte(mgr.CSRFToken(jti)), []byte(got)) == 1
}

// csrfExempt:只读方法不改状态,不校验。
func csrfExempt(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
}
