package service

import (
	"testing"
	"time"

	"velagateway/internal/gateway"
	"velagateway/internal/model"
)

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Skipf("时区库不可用(%s): %v", name, err)
	}
	return loc
}

func at(t *testing.T, tz, s string) time.Time {
	t.Helper()
	ts, err := time.ParseInLocation("2006-01-02 15:04", s, mustLoc(t, tz))
	if err != nil {
		t.Fatal(err)
	}
	return ts
}

// 周期班车:普通时段(不跨午夜)。
func TestWindowCovers_RecurringPlainRange(t *testing.T) {
	w := &model.ExecWindow{
		Enabled: true, Kind: model.WindowRecurring, Timezone: "Asia/Shanghai",
		StartMin: 2 * 60, EndMin: 4 * 60, // 02:00-04:00 每天
	}
	for _, c := range []struct {
		when string
		want bool
	}{
		{"2026-09-08 01:59", false},
		{"2026-09-08 02:00", true},  // 左闭
		{"2026-09-08 03:30", true},
		{"2026-09-08 04:00", false}, // 右开
		{"2026-09-08 12:00", false},
	} {
		if got := windowCovers(w, at(t, "Asia/Shanghai", c.when)); got != c.want {
			t.Errorf("%s 期望 %v,实际 %v", c.when, c.want, got)
		}
	}
}

// 跨午夜是这里最容易错的地方:22:00-02:00 的窗口,凌晨一点属于**前一天**那班车。
// 所以"仅周五"的窗口应当在周五晚上和周六凌晨开着,而不是周五凌晨。
func TestWindowCovers_RecurringCrossesMidnight(t *testing.T) {
	w := &model.ExecWindow{
		Enabled: true, Kind: model.WindowRecurring, Timezone: "Asia/Shanghai",
		StartMin: 22 * 60, EndMin: 2 * 60, Weekdays: "5", // 仅周五发车
	}
	// 2026-09-11 是周五,09-12 是周六。
	for _, c := range []struct {
		when string
		want bool
		why  string
	}{
		{"2026-09-11 01:00", false, "周五凌晨属于周四那班车,而周四不发车"},
		{"2026-09-11 21:59", false, "还没发车"},
		{"2026-09-11 22:00", true, "周五发车"},
		{"2026-09-11 23:30", true, "周五当晚"},
		{"2026-09-12 01:00", true, "周六凌晨,仍是周五那班车"},
		{"2026-09-12 02:00", false, "到点收车"},
		{"2026-09-12 22:00", false, "周六不发车"},
	} {
		if got := windowCovers(w, at(t, "Asia/Shanghai", c.when)); got != c.want {
			t.Errorf("%s 期望 %v(%s),实际 %v", c.when, c.want, c.why, got)
		}
	}
}

// 时区是窗口自己的属性:运维说的"凌晨两点"是他那边的两点。
func TestWindowCovers_UsesItsOwnTimezone(t *testing.T) {
	w := &model.ExecWindow{
		Enabled: true, Kind: model.WindowRecurring, Timezone: "Asia/Shanghai",
		StartMin: 2 * 60, EndMin: 4 * 60,
	}
	// 上海 03:00 == UTC 前一天 19:00。用 UTC 时刻去判,结果必须仍然是"在窗口内"。
	utcMoment := at(t, "Asia/Shanghai", "2026-09-08 03:00").UTC()
	if !windowCovers(w, utcMoment) {
		t.Error("同一时刻换成 UTC 表示后应当仍在窗口内 —— 判定要按窗口的时区换算")
	}
}

// 认不出的时区不生效,而不是退回服务器时区 —— 那会让窗口在完全没预料的时段开着。
func TestWindowCovers_UnknownTimezoneNeverOpens(t *testing.T) {
	w := &model.ExecWindow{
		Enabled: true, Kind: model.WindowRecurring, Timezone: "Mars/Olympus",
		StartMin: 0, EndMin: 24 * 60,
	}
	if windowCovers(w, time.Now()) {
		t.Error("时区认不出来时窗口不该生效")
	}
}

// 一次性窗口:左闭右开,过期即失效。
func TestWindowCovers_Once(t *testing.T) {
	start := at(t, "Asia/Shanghai", "2026-09-12 22:00")
	end := at(t, "Asia/Shanghai", "2026-09-13 02:00")
	w := &model.ExecWindow{Enabled: true, Kind: model.WindowOnce, StartsAt: &start, EndsAt: &end}

	if windowCovers(w, start.Add(-time.Minute)) {
		t.Error("开始前不该生效")
	}
	if !windowCovers(w, start) {
		t.Error("起点是闭区间")
	}
	if !windowCovers(w, end.Add(-time.Minute)) {
		t.Error("结束前一分钟仍在窗口内")
	}
	if windowCovers(w, end) {
		t.Error("终点是开区间")
	}
}

// 关掉的窗口、停运的班车、写反的时段,都不生效。
func TestWindowCovers_DisabledExpiredAndDegenerate(t *testing.T) {
	now := at(t, "Asia/Shanghai", "2026-09-08 03:00")

	off := &model.ExecWindow{Enabled: false, Kind: model.WindowRecurring, Timezone: "Asia/Shanghai", StartMin: 0, EndMin: 24 * 60}
	if windowCovers(off, now) {
		t.Error("停用的窗口不该生效")
	}

	past := now.Add(-24 * time.Hour)
	expired := &model.ExecWindow{
		Enabled: true, Kind: model.WindowRecurring, Timezone: "Asia/Shanghai",
		StartMin: 0, EndMin: 24 * 60, NotAfter: &past,
	}
	if windowCovers(expired, now) {
		t.Error("已过整体失效时间的班车不该生效")
	}

	// StartMin == EndMin 是个说不清的写法(零长?整天?),按不覆盖处理。
	degenerate := &model.ExecWindow{
		Enabled: true, Kind: model.WindowRecurring, Timezone: "Asia/Shanghai",
		StartMin: 120, EndMin: 120,
	}
	if windowCovers(degenerate, now) {
		t.Error("起止相同的时段不该覆盖任何时刻")
	}

	unknown := &model.ExecWindow{Enabled: true, Kind: "weekly-ish", Timezone: "Asia/Shanghai"}
	if windowCovers(unknown, now) {
		t.Error("不认识的时间模型不该生效")
	}
}

// 星期过滤:空串是每天,ISO 编号 1=周一 7=周日。
func TestWeekdayAllowed(t *testing.T) {
	if !weekdayAllowed("", time.Wednesday) {
		t.Error("空串应表示每天")
	}
	if !weekdayAllowed("7", time.Sunday) {
		t.Error("周日的 ISO 编号是 7")
	}
	if weekdayAllowed("1,2,3", time.Sunday) {
		t.Error("周日不在 1,2,3 里")
	}
	if !weekdayAllowed("1, 3 ,5", time.Friday) {
		t.Error("允许空格")
	}
	if weekdayAllowed("abc", time.Monday) {
		t.Error("写坏的班次不该放行任何一天")
	}
}

// 窗口改的是"要不要人来批",不是"有没有权限"。
//
// 这是整个功能的安全边界:能力矩阵判 deny 的语句,窗口开着也照样拒绝 —— 否则一个
// 只读角色会因为到了凌晨两点就能写生产库。allow 不需要放宽,原样返回。
func TestRelaxByWindow_OnlyTouchesApprove(t *testing.T) {
	s := &Services{}
	conn := &model.Connection{ID: 1, Engine: "mysql", Database: "orders"}
	now := time.Now()

	for _, action := range []string{gateway.ActionDeny, gateway.ActionAllow} {
		v, win := s.relaxByWindow(conn, gateway.Verdict{Action: action, Risk: model.RiskHigh}, now)
		if v.Action != action {
			t.Errorf("%s 不该被窗口改动,实际变成 %s", action, v.Action)
		}
		if win != nil {
			t.Errorf("%s 不该关联到窗口", action)
		}
	}
}

// 说不清是哪个库时不放行 —— 窗口是按库开的,库名为空就没有匹配依据。
func TestActiveWindow_NoDatabaseNoRelax(t *testing.T) {
	s := &Services{}
	if got := s.activeWindow(&model.Connection{ID: 1, Engine: "mysql"}, time.Now()); got != nil {
		t.Error("库名为空时不该匹配到任何窗口")
	}
	if got := s.activeWindow(nil, time.Now()); got != nil {
		t.Error("没有连接时不该匹配到任何窗口")
	}
}

// Oracle 的库名取 TargetSchema:它的 Database 装的是服务名,不是库。
func TestEffectiveDatabase_OraclePrefersSchema(t *testing.T) {
	ora := &model.Connection{Engine: "oracle", Database: "ORCLPDB1", TargetSchema: "SCOTT"}
	if got := effectiveDatabase(ora); got != "SCOTT" {
		t.Errorf("Oracle 应取 schema,实际 %q", got)
	}
	my := &model.Connection{Engine: "mysql", Database: "orders", TargetSchema: "ignored"}
	if got := effectiveDatabase(my); got != "orders" {
		t.Errorf("非 Oracle 应取 Database,实际 %q", got)
	}
}

// ---- 到期(windowExpired)----
//
// 原先界面只分得出"开着"和"没开着",于是一个上周就结束的一次性窗口一直显示成
// "未到点" —— 一句读起来像"再等等就到了"的话,而它永远不会到。

// 一次性窗口:过了结束时刻就是到期,边界与 windowCovers 一样左闭右开。
func TestWindowExpired_Once(t *testing.T) {
	tz := "Asia/Shanghai"
	s, e := at(t, tz, "2026-09-08 02:00"), at(t, tz, "2026-09-08 04:00")
	w := &model.ExecWindow{Enabled: true, Kind: model.WindowOnce, Timezone: tz, StartsAt: &s, EndsAt: &e}

	for _, c := range []struct {
		when string
		want bool
	}{
		{"2026-09-08 01:59", false}, // 还没发车
		{"2026-09-08 03:00", false}, // 正开着
		{"2026-09-08 04:00", true},  // 右开:到点那一瞬就算到期
		{"2026-09-09 10:00", true},
	} {
		if got := windowExpired(w, at(t, tz, c.when)); got != c.want {
			t.Errorf("%s: expired=%v, want %v", c.when, got, c.want)
		}
	}

	// 覆盖与到期之间不留缝:任一时刻不会两者都为假又"说不清"。
	end := at(t, tz, "2026-09-08 04:00")
	if windowCovers(w, end) || !windowExpired(w, end) {
		t.Error("结束时刻应当是「不覆盖且已到期」")
	}
}

// 周期班车:没设停运时刻就没有到期这回事 —— 它每周都会再来一次。
func TestWindowExpired_RecurringWithoutNotAfterNeverExpires(t *testing.T) {
	tz := "Asia/Shanghai"
	w := &model.ExecWindow{
		Enabled: true, Kind: model.WindowRecurring, Timezone: tz,
		StartMin: 2 * 60, EndMin: 4 * 60,
	}
	if windowExpired(w, at(t, tz, "2099-01-01 00:00")) {
		t.Error("没有停运时刻的周期班车不该到期")
	}
}

// 设了停运时刻的周期班车:过了就是到期。
func TestWindowExpired_RecurringAfterNotAfter(t *testing.T) {
	tz := "Asia/Shanghai"
	na := at(t, tz, "2026-09-09 00:00")
	w := &model.ExecWindow{
		Enabled: true, Kind: model.WindowRecurring, Timezone: tz,
		StartMin: 2 * 60, EndMin: 4 * 60, NotAfter: &na,
	}
	if windowExpired(w, at(t, tz, "2026-09-08 03:00")) {
		t.Error("停运前不该算到期")
	}
	if !windowExpired(w, at(t, tz, "2026-09-09 00:00")) {
		t.Error("到停运时刻那一瞬就该算到期")
	}
}

// 到期只描述**时间表**:停用的、还在等审批的窗口,时段过了同样是到期。
//
// 这一条要单独钉住,因为顺手加个 `w.Enabled &&` 或 `status == approved` 看起来很自然,
// 而那会让"申请了窗口、审批没批下来、时间已经过去"这种最该被看见的情形重新隐身。
func TestWindowExpired_IgnoresEnabledAndStatus(t *testing.T) {
	tz := "Asia/Shanghai"
	s, e := at(t, tz, "2026-09-08 02:00"), at(t, tz, "2026-09-08 04:00")
	now := at(t, tz, "2026-09-09 10:00")
	for _, w := range []*model.ExecWindow{
		{Enabled: false, Kind: model.WindowOnce, Timezone: tz, StartsAt: &s, EndsAt: &e, Status: model.WindowApproved},
		{Enabled: true, Kind: model.WindowOnce, Timezone: tz, StartsAt: &s, EndsAt: &e, Status: model.WindowPending},
	} {
		if !windowExpired(w, now) {
			t.Errorf("时段已过就该算到期(enabled=%v status=%s)", w.Enabled, w.Status)
		}
	}
}
