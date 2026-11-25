package middleware

// 管理后台的独立空闲超时(管理后台拆分,工单 09)。
//
// 前端在管理后台空闲满 N 分钟会自己退回用户后台;这里是后端那一半:页面被挂起、前端计时失效时,
// 一个闲置的管理会话也调不动管理接口。
//
// 认的"活跃"只有两种:人主动进管理后台(POST /admin/enter),以及人在管理后台里有操作时前端报的
// 心跳(POST /admin/heartbeat)。管理接口本身**不算**活跃 —— 否则管理总览里定时刷新的面板会让
// 这个超时永远到不了。超时之后心跳也救不回来,必须人再点一次「管理后台」(enter)。
//
// 一个会话第一次调管理接口时开始计时(而不是要求先 enter):持有 token 的客户端本来就能自己调
// enter,这道闸挡的是"忘在那里的标签页",不是攻击者;这样也不会让重启后的会话、别的登录路径先吃一个错。
// 状态在内存里,网关重启后从下一次调用重新计时。

import (
	"encoding/json"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"velagateway/internal/repository"
	"velagateway/pkg/resp"
)

var adminActivity = struct {
	sync.Mutex
	m map[string]time.Time
}{m: map[string]time.Time{}}

// AdminIdleMinutes:管理后台空闲多少分钟后失效。默认 5;不超过会话空闲锁定的时长(锁定开着时)。
func AdminIdleMinutes(repo *repository.Repo) int {
	mins := settingInt(repo, "security.adminIdleMinutes", 5)
	if mins <= 0 {
		mins = 5
	}
	if settingBool(repo, "security.idleLock", true) {
		if lock := settingInt(repo, "security.idleMinutes", 15); lock > 0 && mins > lock {
			mins = lock
		}
	}
	return mins
}

func adminSessionKey(c *gin.Context) string {
	if u := CurrentUser(c); u != nil && u.SessionID != "" {
		return u.SessionID
	}
	return ""
}

// adminIdleGrace:后端判空闲时多留的余量。前端心跳是节流的(最长 30 秒补报一次),后端看到的
// "最后一次操作"最多落后这么久;不留余量,人在空闲时长内还在操作也会吃 40304(2026-10 审查)。
const adminIdleGrace = 30 * time.Second

// adminSessionFresh:这个会话还在管理后台的活跃期内。第一次见到时开始计时。
func adminSessionFresh(repo *repository.Repo, c *gin.Context) bool {
	key := adminSessionKey(c)
	if key == "" {
		return true // 没有会话标识(老 token):不在这里挡,其余校验照旧
	}
	idle := time.Duration(AdminIdleMinutes(repo)) * time.Minute
	now := time.Now()
	adminActivity.Lock()
	defer adminActivity.Unlock()
	last, ok := adminActivity.m[key]
	if !ok {
		adminActivity.m[key] = now
		pruneAdminActivity(now)
		return true
	}
	return now.Sub(last) <= idle+adminIdleGrace
}

func touchAdminSession(c *gin.Context) {
	if key := adminSessionKey(c); key != "" {
		adminActivity.Lock()
		adminActivity.m[key] = time.Now()
		adminActivity.Unlock()
	}
}

// pruneAdminActivity 防止表无限长:一天没动静的会话记录删掉(调用方持锁)。
func pruneAdminActivity(now time.Time) {
	if len(adminActivity.m) < 1000 {
		return
	}
	for k, t := range adminActivity.m {
		if now.Sub(t) > 24*time.Hour {
			delete(adminActivity.m, k)
		}
	}
}

func abortAdminIdle(c *gin.Context) {
	resp.Abort(c, resp.CodeAdminIdle, "管理后台已空闲超时,请重新进入")
}

// AdminEnter:人主动进入管理后台,重新计时。前面要挂角色与 IP 校验(见路由)。
func AdminEnter(c *gin.Context) {
	touchAdminSession(c)
	resp.OK(c, gin.H{"ok": true})
}

// AdminHeartbeat:人在管理后台里有操作。已经超时的不救 —— 要重新 enter。
func AdminHeartbeat(repo *repository.Repo) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !adminSessionFresh(repo, c) {
			abortAdminIdle(c)
			return
		}
		touchAdminSession(c)
		resp.OK(c, gin.H{"ok": true})
	}
}

// AdminPortalGate:进管理后台的门 —— 平台管理员或审计员,且来源 IP 在管理后台白名单内。
// 给 enter / heartbeat 用:它们本身不检查空闲(enter 就是用来重新计时的)。
func AdminPortalGate(repo *repository.Repo) gin.HandlerFunc {
	return func(c *gin.Context) {
		u := CurrentUser(c)
		if u == nil {
			resp.Abort(c, resp.CodeUnauthorized, "未登录")
			return
		}
		ok := false
		for _, code := range repo.RoleCodesForIDs(repo.EffectiveRoleIDs(u)) {
			if code == "admin" || code == "audit" {
				ok = true
				break
			}
		}
		if !ok {
			resp.Abort(c, resp.CodeForbidden, "仅平台管理员与审计员可进入管理后台")
			return
		}
		if !AdminIPAllowed(repo, c) {
			resp.Abort(c, resp.CodeAdminIPBlocked, "当前 IP 不在管理后台白名单内")
			return
		}
		c.Next()
	}
}

func settingInt(repo *repository.Repo, key string, def int) int {
	v, err := repo.GetSetting(key)
	if err != nil || v == "" {
		return def
	}
	var n json.Number
	if json.Unmarshal([]byte(v), &n) == nil {
		if i, err := strconv.Atoi(n.String()); err == nil {
			return i
		}
	}
	if i, err := strconv.Atoi(v); err == nil {
		return i
	}
	return def
}

// AdminSessionFresh 给少数在 handler 里认管理员的地方用(与中间件同一口径)。
func AdminSessionFresh(repo *repository.Repo, c *gin.Context) bool { return adminSessionFresh(repo, c) }

// AgeAdminSessionForTest 把一个会话的最近活跃时间往前拨(仅测试用:验证空闲超时不必真等几分钟)。
func AgeAdminSessionForTest(sessionID string, d time.Duration) {
	adminActivity.Lock()
	defer adminActivity.Unlock()
	if t, ok := adminActivity.m[sessionID]; ok {
		adminActivity.m[sessionID] = t.Add(-d)
	}
}
