package middleware

import (
	"sync"
	"time"
)

// apiFailLimiter throttles failed open-API authentications per source IP (L-17).
//
// 两件事要挡,而它们是同一件事的两面:
//
//   - **暴破**。凭据是 key.secret,secret 是随机串,但端点原先对错误尝试没有任何代价,
//     于是猜多少次都行 —— 一个没有代价的验证端点,安全性只剩下那串随机数本身。
//   - **CPU 打满**。每一次尝试都要跑一次 bcrypt(那是**故意**慢的),所以不需要多少
//     并发就能把 CPU 吃光,而这条路在鉴权**之前**,任何人都能打。
//
// 只对**失败**计数:正常集成方一把凭据用到底,永远碰不到它;而拿字典打过来的,每一次
// 都在给自己记账。成功即清零,所以一台机器上偶尔一次配错不会累积成封禁。
//
// 与登录频控刻意保持同一套语义(阈值、窗口、只在越过阈值那一下起锁),两处行为一致,
// 运维只需要理解一种。进程内计数:多实例部署时各算各的 —— 这是已知的边界,真正的
// 分布式限流应当落在网关/反代上,而这里要挡的是"单机被一个脚本打穿"。
type apiFailLimiter struct {
	mu      sync.Mutex
	fails   map[string]*apiFailState
	max     int
	window  time.Duration
	lockout time.Duration
}

type apiFailState struct {
	count       int
	windowStart time.Time
	lockedUntil time.Time
}

func newAPIFailLimiter() *apiFailLimiter {
	return &apiFailLimiter{
		fails:   map[string]*apiFailState{},
		max:     10, // 比登录宽一些:集成方重试是常态,而它的密钥熵也高得多
		window:  5 * time.Minute,
		lockout: 5 * time.Minute,
	}
}

func (l *apiFailLimiter) blocked(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.fails[key]
	return s != nil && now.Before(s.lockedUntil)
}

func (l *apiFailLimiter) fail(key string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pruneLocked(now)
	s := l.fails[key]
	if s == nil || now.Sub(s.windowStart) > l.window {
		s = &apiFailState{windowStart: now}
		l.fails[key] = s
	}
	s.count++
	if s.count == l.max { // 只有越过阈值的那一下起锁,后续失败不再延长
		s.lockedUntil = now.Add(l.lockout)
	}
}

func (l *apiFailLimiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, key)
}

// pruneLocked 丢掉窗口已过且不在封禁中的条目,避免这张表随源 IP 无限长大。
func (l *apiFailLimiter) pruneLocked(now time.Time) {
	for k, s := range l.fails {
		if now.After(s.lockedUntil) && now.Sub(s.windowStart) > l.window {
			delete(l.fails, k)
		}
	}
}

// apiThrottle 是开放接口实际用的两道计数(2026-10 安全审计 L-API1)。
//
// 从前只有一张按来源 IP 的表,且**任何**一次成功都把这个 IP 清零:手里有一把自己的有效凭据,
// 每猜别人的 secret 几次就用自己的凭据登一次,计数永远到不了阈值 —— 与登录那边 R29 同一个洞。
// 现在:
//
//   - perKey,按 (来源, key):这把 key 的 secret 猜错计在这里,只有**这把 key**成功才清零。
//   - perIP,只按来源:key 根本不存在的失败计在这里,**从不**被成功清零 —— 它挡的是拿随机
//     key 打过来、每次都白跑一次 bcrypt 的那种(L-17)。perIP 的封禁也**只拦查不到的 key**:
//     存在的 key(有效凭据)不受同 IP 封禁影响(2026-10-26 回归 05),见 APIClientAuth。
//
// 挂在 APIClientAuth 的闭包里而不是包级变量:每个路由实例一份(测试里每个 app 各算各的)。
type apiThrottle struct {
	perKey *apiFailLimiter
	perIP  *apiFailLimiter
}

func newAPIThrottle() *apiThrottle {
	return &apiThrottle{perKey: newAPIFailLimiter(), perIP: newAPIFailLimiter()}
}

func apiKeyBucket(ip, key string) string { return ip + "|" + key }
