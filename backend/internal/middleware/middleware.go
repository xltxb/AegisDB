// Package middleware holds Gin middlewares: CORS, JWT auth, menu guard, recovery.
package middleware

import (
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	jwtlib "github.com/golang-jwt/jwt/v5"

	"velagateway/internal/metrics"
	"velagateway/internal/model"
	"velagateway/internal/presence"
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

// BindRequestUser 把这个请求认出来的人挂到上下文上,**并且给他盖上来源地址**。
//
// 每一条认证路径都必须走这一个,不要自己 c.Set(ctxUser, …):审计行的来源 IP 就取自这
// 个对象(见 model.User.ClientIP),而漏掉那一行赋值的后果是**一整条路径的审计行都没有
// IP** —— 表面上一切正常,只是那一列是空的,而空在这张表里是合法值(0041 之前的老行就是
// 空),所以没有任何地方会报错。
//
// 这个坑已经踩过一次:WS 终端和开放接口各自 GetUserByID 取了一份干净的 user,于是**用得
// 最多的那条路**(终端里跑的每一条 SQL)全都没记下 IP,而 REST 和登录那两条记下了 —— 看
// 起来像"有的记有的不记",查起来要先想到"是谁把 user 放进上下文的"。
func BindRequestUser(c *gin.Context, u *model.User) {
	if u == nil {
		return
	}
	// ClientIP() 只在配置过的可信代理后面才认 X-Forwarded-For(router 调过
	// SetTrustedProxies,默认一台都不信),所以这一列伪造不了。
	u.ClientIP = c.ClientIP()
	// 同一个理由,审计行的「来源」也在这里盖(迁移 0063):按服务端的路由模板判,不取
	// 客户端报的任何东西。WS 终端每条消息重取一次 user 也走这里,所以终端里跑的每条
	// SQL 都会带上 terminal。
	u.AuditSource = model.AuditSourceForRoute(c.FullPath())
	c.Set(ctxUser, u)
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
			c.Header("Access-Control-Allow-Headers", "Authorization,Content-Type,"+CSRFHeader+","+WebClientHeader)
			c.Header("Access-Control-Allow-Credentials", "true")
		}
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

// JWTAuth parses the session token and injects ctx.User. Authorization: Bearer
// wins when present (scripts, Swagger); otherwise the console's HttpOnly cookie,
// where a state-changing request must also prove it read the CSRF cookie — see
// session_cookie.go.
func JWTAuth(mgr *jwt.Manager, repo *repository.Repo) gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.GetHeader("Authorization")
		fromCookie := !strings.HasPrefix(h, "Bearer ")
		tokenStr := strings.TrimPrefix(h, "Bearer ")
		if fromCookie {
			tokenStr = SessionTokenFromCookie(c)
			if tokenStr == "" {
				resp.Abort(c, resp.CodeUnauthorized, "未登录 / Token 失效")
				return
			}
		}
		claims, err := mgr.Parse(tokenStr)
		if err != nil {
			if fromCookie {
				ClearSessionCookies(c)
			}
			resp.Abort(c, resp.CodeUnauthorized, "未登录 / Token 失效")
			return
		}
		if fromCookie && !csrfExempt(c.Request.Method) && !CSRFMatches(mgr, claims.ID, c.GetHeader(CSRFHeader)) {
			resp.Abort(c, resp.CodeForbidden, "CSRF 校验失败,请刷新页面后重试")
			return
		}
		u, err := repo.GetUserByID(claims.UserID)
		if err != nil {
			resp.Abort(c, resp.CodeUnauthorized, "用户不存在")
			return
		}
		// 只有 active 放行。原先只拦 disabled,于是把一个账号改回 invited(或任何
		// 别的中间态)并不会让它已经发出去的 token 失效 —— 而管理员做那一下,想的
		// 正是"这个人现在不该再进来了"(L-3)。
		if u.Status != "active" {
			resp.Abort(c, resp.CodeForbidden, "账号不可用")
			return
		}
		// Reject tokens from a superseded session generation (logout / disable /
		// password reset bumps TokenVersion) — M1 revocation.
		if claims.TokenVersion != u.TokenVersion {
			resp.Abort(c, resp.CodeUnauthorized, "会话已失效，请重新登录")
			return
		}
		u.SessionID = claims.ID // 这一个会话的 jti,MFA 宽限期按它记(M-6)
		u.SessionAuth = claims.AuthMethod
		if claims.IssuedAt != nil {
			u.SessionLoginAt = claims.IssuedAt.Time
		}
		BindRequestUser(c, u)
		TouchPresence(c, repo, u, claims.IssuedAt)
		c.Next()
	}
}

// TouchPresence 把这次请求记进在线记录(见 presence);每人每分钟至多一次把"最近活跃"写回库。
// 写库放到后台:它只是一个展示用的时间,不该让请求等它,写失败也不影响请求。
func TouchPresence(c *gin.Context, repo *repository.Repo, u *model.User, issued *jwtlib.NumericDate) {
	login := time.Time{}
	if issued != nil {
		login = issued.Time
	}
	if presence.Default.Touch(presence.Visit{
		SessionID: u.SessionID, UserID: u.ID, Name: u.Name, Email: u.Email,
		IP: c.ClientIP(), UserAgent: c.Request.UserAgent(), Path: c.Request.URL.Path, LoginAt: login,
	}) {
		id, at := u.ID, time.Now()
		go func() { _ = repo.TouchLastActive(id, at) }()
	}
}

// SecurityHeaders sets baseline security response headers on every response
// (issue 34). It runs before the handlers so the headers are present even on
// aborted responses (401/403). hsts adds Strict-Transport-Security and is only
// meaningful when the gateway itself terminates TLS — sending it over plain HTTP
// would pin clients to https for a scheme this listener doesn't serve.
//
// Content-Security-Policy(2026-10 安全审计 L-DEF1)只加不会把页面弄坏的那部分:
//   - /api 下是 JSON,从不需要加载任何东西:default-src 'none'。万一哪个响应被当成 HTML
//     渲染,里面的脚本一行也跑不起来。
//   - 页面(SPA):不许被嵌进别人的页面、不许改 <base>、不许插件对象、表单只能提交回本站,脚本只认本站的
//     文件(2026-11 审计 V-16):就算出现注入,内联脚本和站外脚本也跑不起来。构建产物没有内联脚本、
//     也不用 eval。style-src 不设:Vue 的样式绑定和 xterm 都依赖内联样式,样式也执行不了代码。
//   - Swagger UI(/docs,只在本地开发注册)带内联脚本,不限 script-src。
//   - 巡检报告的 iframe 有自己的响应头,见 handler.InspectionReportView:srcdoc 会继承父页面的 CSP。
const (
	cspAPI  = "default-src 'none'; frame-ancestors 'none'"
	cspDocs = "frame-ancestors 'none'; base-uri 'self'; object-src 'none'; form-action 'self'"
	cspPage = cspDocs + "; script-src 'self'"
)

func SecurityHeaders(hsts bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.Writer.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		if strings.HasPrefix(c.Request.URL.Path, "/api/") {
			h.Set("Content-Security-Policy", cspAPI)
		} else if strings.HasPrefix(c.Request.URL.Path, "/docs") {
			h.Set("Content-Security-Policy", cspDocs)
		} else {
			h.Set("Content-Security-Policy", cspPage)
		}
		if hsts {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		c.Next()
	}
}

// NoStore 让 /api/v1 下的响应不被浏览器 / 中间代理缓存(黑盒 2026-10 工单 05 第 2 条)。
// 这些响应几乎都带着登录后才能看的数据,共用电脑上"后退"或代理缓存都不该再把它们端出来。
// 下载类接口不受影响:no-store 只管"别留副本",Content-Disposition 附件照常下载;
// 个别处理器若自己设了 Cache-Control,以它为准(它写在本中间件之后)。
func NoStore() gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.Writer.Header()
		h.Set("Cache-Control", "no-store")
		h.Set("Pragma", "no-cache")
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
		// Any role the user holds granting the admin code satisfies the guard
		// (union semantics — a user may be admin via a secondary role).
		isAdmin := false
		for _, code := range repo.RoleCodesForIDs(repo.EffectiveRoleIDs(u)) {
			if code == "admin" {
				isAdmin = true
				break
			}
		}
		if !isAdmin {
			resp.Abort(c, resp.CodeForbidden, "仅平台管理员可执行此操作")
			return
		}
		if !AdminIPAllowed(repo, c) {
			resp.Abort(c, resp.CodeAdminIPBlocked, "当前 IP 不在管理后台白名单内")
			return
		}
		if !adminSessionFresh(repo, c) { // 管理后台空闲超时(工单 09)
			abortAdminIdle(c)
			return
		}
		c.Next()
	}
}

// AuditReader 放行平台管理员与审计员(role code `audit`):全量审计只读。
//
// 审计员要能**独立于管理员**查账,所以它不能挂在 AdminOnly 后面;但它也只到审计为止,
// 别的管理接口仍然只认 admin。从前审计接口只校验菜单,而审计菜单人人都有(管理后台
// 拆分,.scratch/admin-user-split 工单 01)。
func AuditReader(repo *repository.Repo) gin.HandlerFunc {
	return func(c *gin.Context) {
		u := CurrentUser(c)
		if u == nil {
			resp.Abort(c, resp.CodeUnauthorized, "未登录")
			return
		}
		for _, code := range repo.RoleCodesForIDs(repo.EffectiveRoleIDs(u)) {
			if code == "admin" || code == "audit" {
				if !AdminIPAllowed(repo, c) {
					resp.Abort(c, resp.CodeAdminIPBlocked, "当前 IP 不在管理后台白名单内")
					return
				}
				if !adminSessionFresh(repo, c) { // 管理后台空闲超时(工单 09)
					abortAdminIdle(c)
					return
				}
				c.Next()
				return
			}
		}
		resp.Abort(c, resp.CodeForbidden, "仅平台管理员与审计员可查看全量审计")
	}
}

// AuditSupervisor 挂在审计导出、审计链校验、单据轨迹(/audit/export、/audit/verify、
// /audit/approval/:apNo)前:平台管理员 / 审计员照 AuditReader(管理后台 IP 白名单 + 空闲超时),
// 另放行 DBA 负责人(owner)。
//
// 2026-10-26 回归 18:10-14 的决策给 DBA 负责人恢复了"看全员审计"(用户后台的操作记录页),
// 导出、链校验、轨迹却没一起恢复(从前 canVerify 与导出都给 owner)。owner 进不了管理后台,
// 它用这三样是在用户后台,所以不套管理后台的 IP 白名单与空闲计时 —— 与它看全员审计的口径一致。
func AuditSupervisor(repo *repository.Repo) gin.HandlerFunc {
	reader := AuditReader(repo)
	return func(c *gin.Context) {
		u := CurrentUser(c)
		if u == nil {
			resp.Abort(c, resp.CodeUnauthorized, "未登录")
			return
		}
		if !hasRoleCode(repo, u, "admin", "audit") && hasRoleCode(repo, u, "owner") {
			c.Next()
			return
		}
		reader(c)
	}
}

// auditOwnOnlyKey:AuditListGate 判定这次只能看自己的记录时,在 gin.Context 上设的标记。
const auditOwnOnlyKey = "vela.auditOwnOnly"

// AuditListGate 挂在 GET /audit 与 /audit/instances 前(2026-10 审查)。
//
// 这两个接口人人可读:管理员 / 审计员拿到全量,其他人只看到自己的。全量审计属于管理后台,
// 要和 AuditReader 一样受管理后台 IP 白名单与空闲超时约束 —— 从前它们只按角色放全量,
// 白名单外、空闲超时的管理员照样拉得到全部审计。
//
// 但用户后台的工作台也会取最近的审计(不带 mine)。在那儿空闲 5 分钟就回 40304,前端会
// 把人"退回用户后台"——人本来就在用户后台。所以分两种:
//   - full=1(管理后台的审计页、管理总览):检查不过就照常拒绝(40303 / 40304);
//   - 没带 full:不看管理后台空闲(2026-10-26 回归 16);只有 IP 白名单检查不过时不报错,
//     降级成只看自己的记录。
//
// mine=1、或者本来就只能看自己的人,不受影响。
func AuditListGate(repo *repository.Repo) gin.HandlerFunc {
	return func(c *gin.Context) {
		u := CurrentUser(c)
		if u == nil || c.Query("mine") == "1" || !hasRoleCode(repo, u, "admin", "audit") {
			c.Next()
			return
		}
		ipOK := AdminIPAllowed(repo, c)
		if c.Query("full") != "1" {
			// 不带 full(用户后台工作台 / 总览的审计卡片):**不看**管理后台空闲计时,始终按这个人
			// 的审计可见范围(2026-10-26 回归 16)。从前空闲超时后悄悄降级成只看本人,管理员不进
			// 管理后台约 5 分钟后卡片只剩自己的记录,副标题却仍写「所有人」。这里也不去碰计时器 ——
			// 从前它会让"第一次碰到管理口径检查"提前到加载总览那一刻。管理后台 IP 白名单照旧:
			// 白名单外仍降级成只看自己的。
			if !ipOK {
				c.Set(auditOwnOnlyKey, true)
			}
			c.Next()
			return
		}
		if ipOK && adminSessionFresh(repo, c) {
			c.Next()
			return
		}
		if !ipOK {
			resp.Abort(c, resp.CodeAdminIPBlocked, "当前 IP 不在管理后台白名单内")
		} else {
			abortAdminIdle(c)
		}
	}
}

// AuditOwnOnly:AuditListGate 是否把这次请求降级成只看自己的记录。
func AuditOwnOnly(c *gin.Context) bool { return c.GetBool(auditOwnOnlyKey) }

func hasRoleCode(repo *repository.Repo, u *model.User, codes ...string) bool {
	for _, have := range repo.RoleCodesForIDs(repo.EffectiveRoleIDs(u)) {
		for _, want := range codes {
			if have == want {
				return true
			}
		}
	}
	return false
}

// InspectionViewer 放行能看 AI 巡检报告的角色:平台管理员、DBA 负责人、DBA L2(多角色取并集)。
// 挂在 menu("inspection") 之后 —— 菜单勾给了别的角色也看不到,与 2026-10-15 的结论
// 「DBA 都可以看」一致(.scratch/inspection-report)。不按实例标签收窄:巡检是运维视角,看全局。
func InspectionViewer(repo *repository.Repo) gin.HandlerFunc {
	return func(c *gin.Context) {
		u := CurrentUser(c)
		if u == nil {
			resp.Abort(c, resp.CodeUnauthorized, "未登录")
			return
		}
		if !hasRoleCode(repo, u, "admin", "owner", "l2") {
			resp.Abort(c, resp.CodeForbidden, "巡检报告只对平台管理员、DBA 负责人、DBA L2 开放")
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
		menus, _ := repo.MenusForRoles(repo.EffectiveRoleIDs(u))
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
		// 认不出来源地址:开着白名单就不放行(2026-10 安全审计 L-AUTH3,从前这里放行)
		if ip == nil {
			resp.Abort(c, resp.CodeIPBlocked, "无法识别来源 IP")
			return
		}
		warnUntrustedLocalProxy(c, ip)
		if directLocal(c, ip) {
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

// AdminIPAllowlistForcedOff 是管理后台 IP 白名单的部署层总闸(config admin_ip.allowlist_disabled /
// 环境变量 VELA_ADMIN_IP_ALLOWLIST_DISABLED),由 main 设置。拉下时白名单一律不生效。
var AdminIPAllowlistForcedOff bool

// AdminIPAllowed:管理接口的来源 IP 检查(管理后台拆分,工单 08)。只在 AdminOnly / AuditReader 之后、
// 以及少数在 handler 里认管理员的地方调用 —— 用户后台的接口不受它影响。
//
// 往"放行"那边倒的几种情况,都是为了不把所有管理员锁在外面:总闸拉下、开关没开、列表为空、
// 本机回环。客户端 IP 的取法与登录白名单(IPAllowlist)相同:只信配置过的可信代理头。
func AdminIPAllowed(repo *repository.Repo, c *gin.Context) bool {
	if AdminIPAllowlistForcedOff || !settingBool(repo, "security.adminIpAllowEnabled", false) {
		return true
	}
	list := splitCIDRs(settingStr(repo, "security.adminIpAllowlist", ""))
	if len(list) == 0 {
		return true
	}
	ip := net.ParseIP(c.ClientIP())
	if ip == nil {
		return false // 认不出来源地址不放行(L-AUTH3)
	}
	if directLocal(c, ip) {
		return true
	}
	for _, entry := range list {
		if ipMatches(entry, ip) {
			return true
		}
	}
	return false
}

// directLocal:真的是在本机上直接访问(白名单对它放行,免得把人锁在外面)。
//
// 只看来源是不是回环不够(2026-10 审查):同机 Nginx 反代、又没配 trusted_proxies 时,ClientIP 对
// **每个**请求都是 127.0.0.1,两道白名单形同虚设。带着转发头的回环请求是代理转来的 —— 代理被信任时
// ClientIP 已经是真实客户端,走不到这里;代理不被信任时不知道真实来源,不放行(去比对列表,通常被拒)。
// 运维 ssh 上机直接 curl 本机端口不带这些头,照样进得来,改得了设置。
func directLocal(c *gin.Context, ip net.IP) bool {
	return ip.IsLoopback() && c.GetHeader("X-Forwarded-For") == "" && c.GetHeader("X-Real-IP") == ""
}

// untrustedProxyWarnEvery:同一种告警最多多久打一次(2026-10-26 回归 17)。
const untrustedProxyWarnEvery = 10 * time.Minute

// untrustedProxyWarnedAt:上一次打"本机反代未被信任"告警的时刻(UnixNano);0 = 从没打过。
var untrustedProxyWarnedAt atomic.Int64

// warnUntrustedLocalProxy:登录 IP 白名单开着,而请求从回环地址进来、又带着 X-Forwarded-For /
// X-Real-IP,且这个回环对端**没有**被当成可信代理(ClientIP 仍是对端本身)—— 典型是同机 Nginx
// 反代、配置里没有 trusted_proxies。directLocal 加固之后这类请求不再一律放行,全员可能登录
// 不了(2026-10-26 回归 17:保留加固,但要让运维从日志里一眼看出原因)。限频:每 10 分钟至多一条。
func warnUntrustedLocalProxy(c *gin.Context, ip net.IP) {
	if !ip.IsLoopback() || (c.GetHeader("X-Forwarded-For") == "" && c.GetHeader("X-Real-IP") == "") {
		return
	}
	peer := net.ParseIP(c.RemoteIP())
	if peer == nil || !peer.IsLoopback() || !peer.Equal(ip) {
		return // 对端被信任、ClientIP 已经换成了转发来的地址:配置是对的
	}
	now := time.Now().UnixNano()
	last := untrustedProxyWarnedAt.Load()
	if last != 0 && time.Duration(now-last) < untrustedProxyWarnEvery {
		return
	}
	if !untrustedProxyWarnedAt.CompareAndSwap(last, now) {
		return // 并发的另一个请求刚打过
	}
	slog.Warn("login ip allowlist: request from loopback carries X-Forwarded-For/X-Real-IP but the local proxy is not trusted "+
		"(server.trusted_proxies not configured?) — such requests are no longer allowed as local; add the proxy address "+
		"(e.g. 127.0.0.1) to server.trusted_proxies in config.yaml",
		"peer", c.RemoteIP(), "xff", c.GetHeader("X-Forwarded-For"), "xRealIp", c.GetHeader("X-Real-IP"), "path", c.Request.URL.Path)
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
