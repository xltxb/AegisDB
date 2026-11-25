package service

import (
	"time"

	"velagateway/internal/dto"
	"velagateway/internal/gateway"
)

// 终端标签页独占连接的回收时机(连接本身见 gateway/term_session.go)。

// TermSessionIdle:标签连接空闲多久后断开。跟随会话的空闲锁定 —— 页面空闲到点会被锁定登出,
// 标签也就没了,连接不该比它活得更久。锁定关掉时退回会话时长(登录态到期,标签同样作废)。
func (s *Services) TermSessionIdle() time.Duration {
	ttl := s.SessionTTL()
	if !s.settingBool("security.idleLock", true) {
		// 关掉了空闲锁定:不按会话时长(默认 8 小时)留着 —— 一个忘关的标签会占着目标库的连接
		// 一整个工作日,里面开着的事务就持锁一整天。最多 60 分钟(2026-10 审查)。
		if ttl > termIdleCapWithoutLock {
			return termIdleCapWithoutLock
		}
		return ttl
	}
	idle := time.Duration(s.settingInt("security.idleMinutes", 15)) * time.Minute
	if idle <= 0 || idle > ttl {
		idle = ttl // 与前端一样:空闲时长不超过会话时长
	}
	return idle
}

// SweepTermSessions 关掉空闲超时的标签连接 —— 浏览器直接关掉、断网时前端来不及通知关闭。
func (s *Services) SweepTermSessions() {
	gateway.SweepTermSessions(s.TermSessionIdle(), time.Now())
}

// CloseTermSession 关掉某个用户的一个标签连接。
func (s *Services) CloseTermSession(userID int64, id string) {
	gateway.CloseTermSession(userID, id)
}

// CloseTermSessionsOf 关掉某个用户全部标签连接(登出、空闲锁定)。
func (s *Services) CloseTermSessionsOf(userID int64) {
	gateway.CloseTermSessionsOf(userID)
}

// SessionPolicy 是所有登录用户都能读的会话策略:前端据此做空闲锁定。
//
// 从前前端从 GET /settings 读这几项,而那个接口只给管理员 —— 非管理员读不到,一律按默认 15 分钟
// 锁定,系统设置里改的分钟数对他们不生效。策略本身不敏感(只是几个时长),单独开放;/settings 仍只给管理员。
func (s *Services) SessionPolicy() dto.SessionPolicy {
	ttl := s.SessionTTL()
	mins := s.settingInt("security.idleMinutes", 15)
	if mins <= 0 || time.Duration(mins)*time.Minute > ttl {
		mins = int(ttl / time.Minute) // 与前端一致:空闲时长不超过会话时长
	}
	return dto.SessionPolicy{
		IdleLock:          s.settingBool("security.idleLock", true),
		IdleMinutes:       mins,
		SessionTTLMinutes: int(ttl / time.Minute),
	}
}

// TerminalPageSize:Web 命令行每批显示多少行(系统设置 terminal.resultPageSize,默认 1000,
// 范围 100–50000)。超出一批的行暂存给"加载更多",见 gateway/result_cache.go。
func (s *Services) TerminalPageSize() int {
	n := s.settingInt("terminal.resultPageSize", 1000)
	if n < gateway.MinResultPageSize {
		n = gateway.MinResultPageSize
	}
	if n > gateway.MaxResultPageSize {
		n = gateway.MaxResultPageSize
	}
	return n
}

// termIdleCapWithoutLock:关闭空闲锁定时,终端标签连接最多空闲多久就回收。
const termIdleCapWithoutLock = 60 * time.Minute
