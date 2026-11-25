package handler

import (
	"net"
	"strings"
	"sync"
	"time"
)

// loginLimiter throttles failed logins per source IP to blunt online brute-force
// (R29). It is keyed by IP (not email) on purpose: an attacker can only lock out
// the address they're attacking from, never a victim's account.
type loginLimiter struct {
	mu      sync.Mutex
	fails   map[string]*failState
	max     int           // failures within the window before a lockout
	window  time.Duration // sliding window for counting failures
	lockout time.Duration // how long a locked key stays blocked
}

type failState struct {
	count       int
	windowStart time.Time
	lockedUntil time.Time
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{
		fails:   map[string]*failState{},
		max:     5,
		window:  5 * time.Minute,
		lockout: 5 * time.Minute,
	}
}

// blocked reports whether key is currently locked out.
func (l *loginLimiter) blocked(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.fails[key]
	return s != nil && now.Before(s.lockedUntil)
}

// fail records a failed attempt, starting a lockout the moment the threshold is
// crossed (not on every subsequent failure — that would let one attacker keep
// extending the lockout and collaterally lock a shared-NAT IP indefinitely).
func (l *loginLimiter) fail(key string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pruneLocked(now)
	s := l.fails[key]
	if s == nil || now.Sub(s.windowStart) > l.window {
		s = &failState{windowStart: now}
		l.fails[key] = s
	}
	s.count++
	if s.count == l.max { // only the crossing attempt arms the lockout
		s.lockedUntil = now.Add(l.lockout)
	}
}

// pruneLocked drops entries whose counting window has elapsed and whose lockout
// (if any) has expired, so the map can't grow without bound from rotating source
// IPs. Caller must hold l.mu.
func (l *loginLimiter) pruneLocked(now time.Time) {
	for k, s := range l.fails {
		if now.After(s.lockedUntil) && now.Sub(s.windowStart) > l.window {
			delete(l.fails, k)
		}
	}
}

// reset clears a key's failures after a successful login.
func (l *loginLimiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, key)
}

// loginThrottle is the throttle the login endpoints actually use: two limiters
// with different keys and different reset rules.
//
// Keyed by IP alone, with any success clearing the IP, one account of the
// attacker's own was enough to defeat it: guess a victim's password four times,
// log into your own account, repeat — the counter never reached the threshold, so
// passwords and TOTP codes could be guessed without limit. Hence:
//
//   - perAccount, keyed by (IP, account): cleared only when THAT account logs in.
//     A user who mistyped and then got in starts clean, and nobody else's
//     success touches it.
//   - perIP, keyed by IP alone, with a higher threshold: NEVER cleared by a
//     success. It caps spraying one guess each across many accounts.
//
// Both stay keyed by source IP, so an attacker can still only lock out the
// address they attack from, never a victim's account everywhere (R29).
//
//   - account,只按账号、不分来源:失败到 accountBackoffAfter 次之后进入退避 —— 每次失败后
//     要等一段(指数增长,封顶 accountBackoffMax)才接受下一次尝试,期间直接拒、不验口令。
//     换着来源猜同一个账号不再不受约束(2026-10 安全审计 M7)。用退避而不是长时间锁定,
//     是为了守住 R29:攻击者顶多让受害者等几十秒,锁不死他的账号。
//
// IP 一律经 ipKey 归一:IPv6 按 /64 计 —— 一台机器手里通常有整段 /64,按单个地址计数等于不限。
type loginThrottle struct {
	perAccount *loginLimiter
	perIP      *loginLimiter

	mu       sync.Mutex
	accounts map[string]*accountBackoff
}

type accountBackoff struct {
	fails       int
	windowStart time.Time
	nextAllowed time.Time
}

const (
	accountBackoffAfter  = 10
	accountBackoffWindow = 15 * time.Minute
	accountBackoffMax    = 30 * time.Second
)

// ipKey 把来源地址归一成计数用的键:IPv4 原样,IPv6 取所在的 /64。
func ipKey(ip string) string {
	p := net.ParseIP(strings.TrimSpace(ip))
	if p == nil || p.To4() != nil {
		return ip
	}
	return p.Mask(net.CIDRMask(64, 128)).String() + "/64"
}

func newLoginThrottle() *loginThrottle {
	return &loginThrottle{
		perAccount: newLoginLimiter(),
		perIP: &loginLimiter{
			fails:   map[string]*failState{},
			max:     20,
			window:  5 * time.Minute,
			lockout: 5 * time.Minute,
		},
		accounts: map[string]*accountBackoff{},
	}
}

func accountKey(ip, account string) string {
	return ip + "|" + normAccount(account)
}

// blocked reports whether a login from ip for account (empty = not known, e.g.
// SSO) is currently refused.
func (t *loginThrottle) blocked(ip, account string, now time.Time) bool {
	ip = ipKey(ip)
	if t.perIP.blocked(ip, now) {
		return true
	}
	if account == "" {
		return false
	}
	if t.perAccount.blocked(accountKey(ip, account), now) {
		return true
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	b := t.accounts[normAccount(account)]
	return b != nil && now.Before(b.nextAllowed)
}

func (t *loginThrottle) fail(ip, account string, now time.Time) {
	ip = ipKey(ip)
	t.perIP.fail(ip, now)
	if account == "" {
		return
	}
	t.perAccount.fail(accountKey(ip, account), now)

	t.mu.Lock()
	defer t.mu.Unlock()
	for k, b := range t.accounts { // 顺手清掉过期的
		if now.Sub(b.windowStart) > accountBackoffWindow && now.After(b.nextAllowed) {
			delete(t.accounts, k)
		}
	}
	k := normAccount(account)
	b := t.accounts[k]
	if b == nil || now.Sub(b.windowStart) > accountBackoffWindow {
		b = &accountBackoff{windowStart: now}
		t.accounts[k] = b
	}
	b.fails++
	if over := b.fails - accountBackoffAfter; over >= 0 {
		wait := accountBackoffMax
		if over < 5 {
			wait = min(time.Second<<over, accountBackoffMax) // 1s 2s 4s 8s 16s,再往后 30s
		}
		b.nextAllowed = now.Add(wait)
	}
}

// succeed clears this account's own failures from this IP — and nothing else.
func (t *loginThrottle) succeed(ip, account string) {
	if account == "" {
		return
	}
	t.perAccount.reset(accountKey(ipKey(ip), account))
	t.mu.Lock()
	delete(t.accounts, normAccount(account))
	t.mu.Unlock()
}

func normAccount(account string) string { return strings.ToLower(strings.TrimSpace(account)) }
