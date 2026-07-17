package handler

import (
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
