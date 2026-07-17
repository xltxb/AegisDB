package handler

import (
	"testing"
	"time"
)

func TestLoginLimiter_ThresholdNoReextendAndPrune(t *testing.T) {
	l := &loginLimiter{fails: map[string]*failState{}, max: 3, window: time.Minute, lockout: time.Minute}
	now := time.Now()
	ip := "1.2.3.4"

	// under the threshold → not blocked
	l.fail(ip, now)
	l.fail(ip, now)
	if l.blocked(ip, now) {
		t.Fatal("2 failures (max 3) must not block")
	}
	// crossing the threshold → blocked
	l.fail(ip, now)
	if !l.blocked(ip, now) {
		t.Fatal("3 failures must block")
	}
	lockedAt := l.fails[ip].lockedUntil

	// further failures must NOT extend the lockout (no NAT amplification)
	l.fail(ip, now.Add(time.Second))
	if !l.fails[ip].lockedUntil.Equal(lockedAt) {
		t.Error("a failure after lockout must not push lockedUntil back")
	}

	// reset clears the key
	l.reset(ip)
	if l.blocked(ip, now) {
		t.Error("reset must clear the lockout")
	}

	// stale (past-window, unlocked) entries are pruned — map can't grow unbounded
	l.fail("9.9.9.9", now)
	l.pruneLocked(now.Add(2 * time.Minute))
	if _, ok := l.fails["9.9.9.9"]; ok {
		t.Error("a stale entry past its window must be pruned")
	}
}
