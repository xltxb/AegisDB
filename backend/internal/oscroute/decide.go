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
// 顺序本身就是决定:
//
//	skip 覆盖   → 人对这一次说了不,不再问别的
//	force 覆盖  → 跳过行数判断,但**仍要通过语句识别**
//	autoRoute 关 → 平台没开这个功能
//	非 MySQL    → OSC 是 MySQL 专属的,再往下问没有意义
//	不是索引 DDL → 超出 OSC 的能力范围(ADR 0011 刻意收窄)
//	行数不够    → 小表上影子表的开销远大于收益
func Decide(sql, engine string, p Policy, ov Override, rowsOf func(table string) int64) Decision {
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
