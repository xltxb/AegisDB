package oscroute

import "strings"
import "testing"

// 判定顺序是这一层的全部内容,而顺序本身就是决定:覆盖排在策略前面,因为它是人对
// 这一次的明确指令;引擎排在语句识别前面,因为对着一个 PostgreSQL 实例讨论"这是不是
// 索引 DDL"没有意义。
//
// 每条判定都要给出 Reason —— 它直接进阶段日志。一次"本该走 OSC 却没走"必须看得见,
// 否则这个功能失效时没有任何迹象。

const bigTable = 8_000_000

func policy() Policy { return Policy{AutoRoute: true, MinRows: 2_000_000} }

// rowsFn 造一个固定的行数来源。真实现背后是一次 information_schema 查询。
func rowsFn(n int64) func(string) int64 { return func(string) int64 { return n } }

func TestDecide_RoutesABigTableIndexChange(t *testing.T) {
	d := Decide("ALTER TABLE t_order ADD INDEX idx_memo (memo)", "mysql", policy(), OverrideNone, rowsFn(bigTable))

	if !d.UseOSC {
		t.Fatalf("八百万行的表上加索引没有走 OSC:%s", d.Reason)
	}
	if d.Table != "t_order" || d.Alter != "ADD INDEX idx_memo (memo)" {
		t.Errorf("交给 OSC 的是 %+v", d)
	}
	if d.Reason == "" {
		t.Error("走了 OSC 却没有说为什么 —— 阶段日志里会是一片空白")
	}
}

func TestDecide_SkipsASmallTable(t *testing.T) {
	d := Decide("ALTER TABLE t_order ADD INDEX idx_memo (memo)", "mysql", policy(), OverrideNone, rowsFn(1_000))

	if d.UseOSC {
		t.Error("一千行的表也走了 OSC —— 影子表和 binlog 订阅的开销远大于收益")
	}
	// 理由里要有那个数,否则人只知道"没走",不知道"差多少"。
	if !strings.Contains(d.Reason, "1000") && !strings.Contains(d.Reason, "1,000") {
		t.Errorf("理由 %q 里没有实际行数", d.Reason)
	}
}

func TestDecide_ThresholdItselfIsNotOver(t *testing.T) {
	// 正好等于阈值不算超。与 osc.shouldPause 对限流阈值的立场一致:一个恰好卡在
	// 线上的值反复触发,会让行为看起来随机。
	d := Decide("ALTER TABLE t_order ADD INDEX i (c)", "mysql", policy(), OverrideNone, rowsFn(2_000_000))

	if d.UseOSC {
		t.Error("行数正好等于阈值时走了 OSC")
	}
}

func TestDecide_SkipOverrideWinsOverEverything(t *testing.T) {
	d := Decide("ALTER TABLE t_order ADD INDEX i (c)", "mysql", policy(), OverrideSkip, rowsFn(bigTable))

	if d.UseOSC {
		t.Error("发起人明确选了直发,却仍然走了 OSC")
	}
	if !strings.Contains(d.Reason, "发起人") {
		t.Errorf("理由 %q 没有说明这是人的选择 —— 事后查起来会被当成判定出错", d.Reason)
	}
}

func TestDecide_ForceOverrideSkipsTheRowCheck(t *testing.T) {
	// 估算行数可能偏得很离谱(InnoDB 的 TABLE_ROWS)。force 是人对这件事的纠正。
	d := Decide("ALTER TABLE t_order ADD INDEX i (c)", "mysql", policy(), OverrideForce, rowsFn(10))

	if !d.UseOSC {
		t.Errorf("发起人强制走 OSC,却没有走:%s", d.Reason)
	}
}

func TestDecide_ForceStillRefusesWhatOSCCannotDo(t *testing.T) {
	// force 是"跳过行数判断",不是"把任何语句都塞给 OSC"。一条改列语句交过去,
	// 会在 Preflight 那里失败,而人看到的是一张失败的发布单。
	d := Decide("ALTER TABLE t_order MODIFY COLUMN memo VARCHAR(128)", "mysql", policy(), OverrideForce, rowsFn(bigTable))

	if d.UseOSC {
		t.Error("强制模式把一条改列语句交给了 OSC")
	}
}

func TestDecide_AutoRouteOffMeansNever(t *testing.T) {
	p := policy()
	p.AutoRoute = false

	d := Decide("ALTER TABLE t_order ADD INDEX i (c)", "mysql", p, OverrideNone, rowsFn(bigTable))

	if d.UseOSC {
		t.Error("自动路由关着却仍然走了 OSC")
	}
}

func TestDecide_NonMySQLNeverRoutes(t *testing.T) {
	// OSC 是 MySQL 专属的(binlog + 影子表)。对着 PostgreSQL 讨论这件事没有意义,
	// 而理由要说得出是引擎的原因 —— 否则人会去查自己的阈值配置。
	d := Decide("ALTER TABLE t_order ADD INDEX i (c)", "postgres", policy(), OverrideNone, rowsFn(bigTable))

	if d.UseOSC {
		t.Error("在 PostgreSQL 上走了 OSC")
	}
	if !strings.Contains(d.Reason, "MySQL") {
		t.Errorf("理由 %q 没有点出引擎", d.Reason)
	}
}

func TestDecide_DoesNotCountRowsForStatementsItWillNotRoute(t *testing.T) {
	// 行数背后是一次 information_schema 查询。一张单里十条 UPDATE,为每一条都查
	// 一次是白费的往返 —— 而且只有认出这是索引 DDL 之后才知道该查哪张表。
	called := 0
	rows := func(string) int64 { called++; return bigTable }

	Decide("UPDATE t_order SET memo = 'x'", "mysql", policy(), OverrideNone, rows)
	Decide("ALTER TABLE t_order ADD INDEX i (c)", "postgres", policy(), OverrideNone, rows)

	if called != 0 {
		t.Errorf("为不会路由的语句查了 %d 次行数", called)
	}
}

func TestDecide_NonIndexDDLNeverRoutes(t *testing.T) {
	d := Decide("UPDATE t_order SET memo = 'x'", "mysql", policy(), OverrideNone, rowsFn(bigTable))

	if d.UseOSC {
		t.Error("把一条 UPDATE 交给了 OSC")
	}
}

func TestDecide_ForceWinsOverTheAutoRouteSwitch(t *testing.T) {
	// 平台把自动路由关了,发起人仍然可以对这一单说"走 OSC"。
	//
	// 这是判定顺序的直接后果(覆盖排在策略前面):策略是对一类情况的默认,
	// 而覆盖是人对这一次的明确指令。有人日后把条件简化成 !p.AutoRoute,
	// 现场那个想强制走 OSC 的人会被判成"不走",而没有任何测试会红。
	p := policy()
	p.AutoRoute = false

	d := Decide("ALTER TABLE t_order ADD INDEX i (c)", "mysql", p, OverrideForce, rowsFn(bigTable))

	if !d.UseOSC {
		t.Errorf("自动路由关着时 force 失效了:%s", d.Reason)
	}
}

func TestDecide_ForceStillRefusesANonMySQLTarget(t *testing.T) {
	// force 跳过的是**行数判断**,不是引擎。OSC 是 MySQL 专属的(影子表 + binlog),
	// 把一条 PostgreSQL 上的索引 DDL 塞进去,要到 Preflight 才失败 ——
	// 而人看到的是一张失败的发布单,不是"这条语句不该走这条路"。
	d := Decide("ALTER TABLE t_order ADD INDEX i (c)", "postgres", policy(), OverrideForce, rowsFn(bigTable))

	if d.UseOSC {
		t.Error("强制模式把一条 PostgreSQL 上的变更交给了 OSC")
	}
	if !strings.Contains(d.Reason, "MySQL") {
		t.Errorf("理由 %q 没有点出引擎 —— 人会去查自己的阈值配置", d.Reason)
	}
}
