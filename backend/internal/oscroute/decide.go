package oscroute

import (
	"fmt"
	"strings"
)

// Policy 是平台对"什么样的变更该走 OSC"的默认立场,来自设置项。
type Policy struct {
	AutoRoute bool  // osc.autoRoute.enabled
	MinRows   int64 // osc.autoRoute.minRows
}

// Override 是发起人对**这一单**的明确指令。
type Override string

const (
	OverrideNone  Override = ""
	OverrideForce Override = "force"
	OverrideSkip  Override = "skip"
)

// Decision 是一条语句的去向。Reason 直接进阶段日志 —— 每一条都要说得出为什么,
// 因为"本该走 OSC 却没走"必须看得见:看不见的话,这个功能哪天失效了不会有任何迹象。
type Decision struct {
	UseOSC bool
	Reason string
	Table  string
	Alter  string
}

// Decide 判断一条语句该不该改走 OSC。
//
// targetSchema 是发布单的目标库(rel.Database)—— 用来识破 I1 那类错认:一条带着
// 库名前缀的语句,若前缀和目标库不是同一个,不能被认下来。
//
// 顺序本身就是决定:
//
//	skip 覆盖   → 人对这一次说了不,不再问别的
//	force 覆盖  → 跳过行数判断,但**仍要通过语句识别与 schema 校验**
//	autoRoute 关 → 平台没开这个功能
//	非 MySQL    → OSC 是 MySQL 专属的,再往下问没有意义
//	不是索引 DDL → 超出 OSC 的能力范围(ADR 0011 刻意收窄)
//	schema 不符  → 语句实际指向的库和发布单目标库不是同一个,认下来是错认
//	行数不够    → 小表上影子表的开销远大于收益
func Decide(sql, engine, targetSchema string, p Policy, ov Override, rowsOf func(table string) int64) Decision {
	if ov == OverrideSkip {
		return Decision{Reason: "直发:发起人对本单选择了不走 OSC"}
	}
	if ov != OverrideForce && !p.AutoRoute {
		return Decision{Reason: "直发:自动路由未启用(osc.autoRoute.enabled)"}
	}
	if !strings.EqualFold(engine, "mysql") {
		return Decision{Reason: fmt.Sprintf("直发:目标引擎是 %s,OSC 只做 MySQL", engine)}
	}
	ddl, ok := ParseIndexDDL(sql)
	if !ok {
		// force 也拦在这里 —— "跳过行数判断"不等于"把任何语句都塞给 OSC"。
		return Decision{Reason: "直发:不是一条纯粹的索引变更,超出 OSC 的能力范围"}
	}
	// I1:带库名前缀的语句被认下来之后,前缀会被丢在一边 —— routeStatement 拿
	// rel.Database(目标库)当 schema 交给 osc.StartRequest,不管语句本来写的是
	// 哪个库。两个库都有同名表时(常见),这会让 OSC 在目标库里找到一张完全无关
	// 的表加上索引,而语句真正想改的那张表一个字没动。这是错认,比"认不出、照常
	// 直发"贵得多 —— 所以和"不是索引 DDL"一样拦在 force 前面:force 跳过的是
	// 行数判断,不是"把任何语句都塞给 OSC"。
	if ddl.Schema != "" && !strings.EqualFold(ddl.Schema, targetSchema) {
		return Decision{Reason: fmt.Sprintf(
			"直发:语句带着库名前缀 %s,与发布单目标库 %s 不同,不认这条路由", ddl.Schema, targetSchema)}
	}
	if ov == OverrideForce {
		return Decision{UseOSC: true, Table: ddl.Table, Alter: ddl.Alter,
			Reason: "走 OSC:发起人对本单强制指定"}
	}
	// 行数到这里才查:它背后是一次 information_schema 查询,而上面每一条分支都
	// 已经足以决定去向 —— 一张单里十条 UPDATE 不该换来十次往返。
	rows := rowsOf(ddl.Table)
	// 正好等于阈值不算超:一个恰好卡在线上的值反复触发,会让行为看起来随机。
	if rows <= p.MinRows {
		return Decision{Reason: fmt.Sprintf("直发:约 %d 行,未超过阈值 %d", rows, p.MinRows)}
	}
	return Decision{UseOSC: true, Table: ddl.Table, Alter: ddl.Alter,
		Reason: fmt.Sprintf("走 OSC:约 %d 行,超过阈值 %d", rows, p.MinRows)}
}
