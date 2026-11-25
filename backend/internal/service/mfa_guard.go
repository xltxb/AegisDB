package service

import (
	"fmt"
	"sync"
	"time"

	"velagateway/internal/model"
)

// MFA 验证码失败次数限制(2026-10 安全审计 H4)。
//
// 登录有频控,但登录之后的两处 TOTP 校验没有:生产操作的步进验证(checkMFA)和自助关闭 MFA
// (MFADisable)。拿到别人登录态的人可以一直猜 6 位码,猜中后还能把第二因子关掉。这里按**用户**
// 计失败次数(第二因子保护的是账号,换 IP 不该重新计数):15 分钟内失败 5 次,锁 15 分钟,
// 锁定期间对的码也不认;成功一次清零。触发锁定写审计。

// ErrMFALocked:验证码错误次数过多,暂时锁定。处理器回 42900。
var ErrMFALocked = fmt.Errorf("mfa locked: too many failed attempts")

const (
	mfaMaxFails = 5
	mfaWindow   = 15 * time.Minute
	mfaLockout  = 15 * time.Minute
)

type failState struct {
	count       int
	windowStart time.Time
	lockedUntil time.Time
}

// failBook 是"窗口期内失败 max 次就锁 lockout"的计数表,按 K 分开计。MFA 按用户、
// 审批回调按来源 IP(M2)。零值不可用:用 newFailBook。
type failBook[K comparable] struct {
	max     int
	window  time.Duration
	lockout time.Duration

	mu sync.Mutex
	m  map[K]*failState
}

func newFailBook[K comparable](max int, window, lockout time.Duration) *failBook[K] {
	return &failBook[K]{max: max, window: window, lockout: lockout, m: map[K]*failState{}}
}

// locked 报告 k 此刻是否处在锁定期。
func (b *failBook[K]) locked(k K, now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	st := b.m[k]
	return st != nil && now.Before(st.lockedUntil)
}

// fail 记一次失败;返回这一次是否触发了锁定(只有越过阈值的那一次返回 true)。
func (b *failBook[K]) fail(k K, now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for id, st := range b.m { // 顺手清掉过期的
		if now.After(st.lockedUntil) && now.Sub(st.windowStart) > b.window {
			delete(b.m, id)
		}
	}
	st := b.m[k]
	if st == nil || now.Sub(st.windowStart) > b.window {
		st = &failState{windowStart: now}
		b.m[k] = st
	}
	st.count++
	if st.count == b.max {
		st.lockedUntil = now.Add(b.lockout)
		return true
	}
	return false
}

func (b *failBook[K]) succeed(k K) {
	b.mu.Lock()
	delete(b.m, k)
	b.mu.Unlock()
}

// mfaFailed 记一次校验失败并原样返回 bad;越过阈值的那一次写审计。调用方要先用 mfaFails.locked
// 判锁定 —— 锁定期间根本不去校验,免得白白消耗一个有效码(码是一次性的)。
func (s *Services) mfaFailed(u *model.User, what string, bad error) error {
	if s.mfaFails.fail(u.ID, time.Now()) {
		s.auditAdminAction(u, fmt.Sprintf("mfa.locked %s:验证码连续错误 %d 次,锁定 %d 分钟", what, mfaMaxFails, int(mfaLockout/time.Minute)))
	}
	return bad
}
