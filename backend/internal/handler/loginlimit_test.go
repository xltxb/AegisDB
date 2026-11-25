package handler

import (
	"fmt"
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

// M7(2026-10 安全审计):IPv6 一台机器手里通常有整段 /64,按单个地址计数等于不限;
// 换着来源猜同一个账号也不受任何一个计数器约束。

func TestLoginThrottle_IPv6CountedPerSlash64(t *testing.T) {
	th := newLoginThrottle()
	now := time.Now()
	for i := 0; i < 20; i++ {
		th.fail("2001:db8:1:2::"+itoaHex(i+1), "", now)
	}
	if !th.blocked("2001:db8:1:2::ffff", "", now) {
		t.Fatal("同一 /64 里换地址应共用一个计数")
	}
	if th.blocked("2001:db8:1:3::1", "", now) {
		t.Fatal("别的 /64 不应受牵连")
	}
	// IPv4 照旧按单个地址
	if ipKey("10.0.0.1") == ipKey("10.0.0.2") {
		t.Fatal("IPv4 仍按单个地址计")
	}
}

func TestLoginThrottle_AccountBackoffAcrossSources(t *testing.T) {
	th := newLoginThrottle()
	now := time.Now()
	const acct = "victim@vela.io"
	for i := 0; i < accountBackoffAfter; i++ {
		th.fail("10.1.0."+fmt.Sprint(i+1), acct, now) // 每次换一个来源
	}
	if !th.blocked("10.9.9.9", acct, now) {
		t.Fatal("同一账号跨来源失败多次后,应进入退避(任何来源都要等)")
	}
	if th.blocked("10.9.9.9", "other@vela.io", now) {
		t.Fatal("别的账号不受影响")
	}
	if th.blocked("10.9.9.9", acct, now.Add(accountBackoffMax+time.Second)) {
		t.Fatal("退避期过后应能再试")
	}
	th.succeed("10.9.9.9", acct)
	if th.blocked("10.9.9.9", acct, now) {
		t.Fatal("该账号登录成功后退避清零")
	}
}

func itoaHex(i int) string { return fmt.Sprintf("%x", i) }
