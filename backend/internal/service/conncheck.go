package service

// 定时连通性巡检:周期性地拨号每一台实例,把结论记在连接上。
//
// ## 为什么需要它
//
// 巡检结论本来只在有人点「批量巡检」时才更新。于是「仅看异常」这个筛选器只对**刚点过
// 巡检的那一刻**有意义 —— 而排障时人打开这一页,恰恰是想不点任何东西就看出哪台有事。
//
// ## 默认关着
//
// 打开它意味着这台网关会周期性地登录你的每一台生产实例。那必须是一次明确的决定,
// 不能因为升级了一个版本就自己开始跑(与 meta.sync 同一条规矩)。
//
// ## 认证失败的实例,自动巡检**不再重试**
//
// 这不是省事,是防一次真实的事故:Oracle 的 FAILED_LOGIN_ATTEMPTS 默认是 10。一台凭据
// 配错的实例,按 10 分钟一轮算,不到两小时就能把网关的账号锁掉 —— 那时受影响的不只是
// 巡检,是所有走这台实例的执行。
//
// 所以自动巡检跳过"上次因认证失败"的实例,留着那条旧结论(界面会显示它的年龄)。
// **手动巡检不受此限** —— 人点下去,就是人认为凭据已经改好了。

import (
	"log/slog"
	"time"

	"velagateway/internal/gateway"
	"velagateway/internal/model"
)

// 设置项。默认值就是"不开、十分钟一轮、四台并发"。
const (
	setConnCheckEnabled     = "conn.check.enabled"
	setConnCheckIntervalMin = "conn.check.intervalMinutes"
	setConnCheckConcurrency = "conn.check.concurrency"
)

// ConnCheckEnabled 给 main.go 的定时器用:关着就连间隔都不必读。
func (s *Services) ConnCheckEnabled() bool { return s.settingBool(setConnCheckEnabled, false) }

// ConnCheckInterval 让 main.go 的定时器每轮读一次当前设置 —— 间隔是运行时可改的。
func (s *Services) ConnCheckInterval() time.Duration {
	m := s.settingInt(setConnCheckIntervalMin, 10)
	if m < 1 {
		m = 1 // 比一分钟更密的拨号已经不是"巡检",是压测
	}
	if m > 1440 {
		m = 1440
	}
	return time.Duration(m) * time.Minute
}

func (s *Services) connCheckConcurrency() int {
	n := s.settingInt(setConnCheckConcurrency, 4)
	if n < 1 {
		n = 1
	}
	if n > 16 {
		n = 16
	}
	return n
}

// SweepConnChecks 拨号一轮。关着就什么都不做,安全地反复调用。
//
// 与 SweepMetadata 同一种形状:一个可以挂在 ticker 上的幂等方法。它不返回错误 ——
// 单台的结论记在连接上,而整轮的失败没有调用方可以处理。
func (s *Services) SweepConnChecks() {
	if !s.ConnCheckEnabled() {
		return
	}
	started := time.Now()
	tally, err := s.forEachRealConn(s.connCheckConcurrency(), func(c *model.Connection) bool {
		return s.CheckConnection(c).OK()
	})
	if err != nil {
		slog.Warn("conn check: list connections failed", "err", err)
		return
	}
	slog.Info("conn check finished",
		"ok", tally.ok, "bad", tally.bad, "skipped", tally.skipped, "took", time.Since(started).String())
}

// CheckConnection 探一次并把结论记下来 —— "探完就记"这条规则只在这里写一次。
//
// 在此之前 Test 和 SaveConnCheck 是在两个调用方手工配对的;下一个探测连接的调用方
// (导入、开放接口的健康检查)会再次把结论扔掉,重现 0037 要修的那个 bug。
//
// 没配凭据**不记**:那不是远端的状况,写成"连不上"会让它永久挂红,而定时巡检又刻意
// 跳过它,于是那条假的"连不上"永远不会被刷新。
func (s *Services) CheckConnection(conn *model.Connection) gateway.CheckResult {
	r := gateway.Check(conn)
	if r.Kind == gateway.CheckNoCredentials {
		return r
	}
	if err := s.Repo.SaveConnCheck(conn.ID, string(r.Kind), r.OK(), r.Msg); err != nil {
		slog.Warn("conn check: save failed", "conn", conn.Name, "err", err)
	}
	return r
}

// skipAutoCheck 说明这台实例为什么不参加自动巡检。空串 = 要巡检。
//
// 两个定时拨号者(这里和 SweepMetadata)共用它 —— 防锁账号那条只写在一处,另一处
// 照样按小时重拨凭据错误的实例,不到半天就把账号锁掉。手动巡检不走它:人点下去,
// 就是人认为凭据已经改好了。
//
// 三种跳过,各有各的理由,而且都**不是**"它没问题":
//
//	维护态      人标的"别碰"。定时任务不该是那个例外。
//	没有凭据    没有远端可拨。报"连不上"会把"我们没配凭据"说成"那台库坏了"。
//	上次认证失败 见文件顶部:重试会把账号锁掉。
func skipAutoCheck(c *model.Connection) string {
	if c.Status == model.ConnStatusMaint {
		return "maint"
	}
	if !gateway.RealExecSupported(c) {
		return "no-credentials"
	}
	if c.LastCheckKind == string(gateway.CheckAuthFailed) {
		return "auth-failed"
	}
	return ""
}
