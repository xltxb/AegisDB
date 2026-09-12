package review

// 分区数上限那条规则从来没有触发过。
//
//	PARTITIONs+([A-Za-z_]…)
//
// 少了一个反斜杠 —— 它匹配的是「PARTITION 后面跟着一个或多个字母 s」,而不是
// 「PARTITION 后面跟着空白」。真实的 `PARTITION p1 VALUES …` 一个都对不上,于是
// `dws.partition.max`(RULE 20,error 级)数出来的分区数永远是 0,永远不超限。
//
// 这种错最难被发现:规则在列表里、状态是启用、跑起来不报错,只是**从来不说话**。
// 而"从来不报"和"一直合规"在界面上长得一模一样。

import (
	"fmt"
	"strings"
	"testing"
)

// dwsPartitionedTable 造一张带 n 个分区的 DWS 建表语句。
func dwsPartitionedTable(n int) string {
	var b strings.Builder
	b.WriteString("CREATE TABLE t_orders (id int, dt date) DISTRIBUTE BY HASH(id) PARTITION BY RANGE (dt) (")
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "PARTITION p%d VALUES LESS THAN ('2026-01-%02d')", i, i%28+1)
	}
	b.WriteString(")")
	return b.String()
}

func TestDWSPartitionCount_FiresAboveTheLimit(t *testing.T) {
	rules := allRules()

	// 规则的出厂上限是 1000,这里给一张远超的表。
	r := Check(DialectDWS, dwsPartitionedTable(1200), rules)
	if !hasCode(r, "dws.partition.max") {
		t.Errorf("分区数超限没被报出来 —— 这条规则从来没说过话。实际命中: %v", codesOf(r))
	}
}

// 没超限的表不该被报 —— 否则这条规则从"从不说话"变成"总在乱说",一样没人看。
func TestDWSPartitionCount_QuietWithinTheLimit(t *testing.T) {
	r := Check(DialectDWS, dwsPartitionedTable(3), allRules())
	if hasCode(r, "dws.partition.max") {
		t.Error("三个分区不该触发上限规则")
	}
}

// SUBPARTITION 不该被数成 PARTITION —— 单词边界是这条正则原本就该有的另一半。
func TestDWSPartitionCount_DoesNotCountSubpartitions(t *testing.T) {
	sql := "CREATE TABLE t (id int, dt date) DISTRIBUTE BY HASH(id) PARTITION BY RANGE (dt) (" +
		"PARTITION p0 VALUES LESS THAN ('''2026-01-01''') (SUBPARTITION s0, SUBPARTITION s1))"
	// 一个真分区 + 两个子分区,离 1000 的上限远得很。
	if r := Check(DialectDWS, sql, allRules()); hasCode(r, "dws.partition.max") {
		t.Error("子分区被当成分区数进去了")
	}
}
