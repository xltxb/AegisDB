package gateway

// 字典命中要取**最严**的那一条,不是文本里最靠前的那一条。
//
//	ALTER TABLE t_orders DROP PARTITION p_2024
//
// 字典里 ALTER=mid、DROP=high 时,`FindString` 取第一个命中 —— ALTER 在前,于是整条
// 语句判成 mid:一条删分区的语句按「需审批」走了普通流程,而运维把 DROP 设成 high 的
// 本意正是"这种事要按最高规格看"。
//
// 顺序在这里是**语法决定的**,不是危险程度决定的:谁在前取决于 SQL 怎么写。让它决定
// 判定结论,等于把闸门的松紧交给了句子的语序。
//
// 与批量判定取最严(A1)、Mongo 链取最危险的一环、DO 块取最狠的一条是同一条原则:
// 一条命令的风险等级,由它做的最危险的那件事决定。

import (
	"testing"

	"velagateway/internal/model"
)

func TestMatchCommand_TakesTheStrictestHitNotTheFirst(t *testing.T) {
	store := &fakeStore{cmds: []model.RiskCommand{
		{Command: "ALTER", TierCode: "prod", Level: model.RiskMid},
		{Command: "DROP", TierCode: "prod", Level: model.RiskHigh},
		{Command: "TRUNCATE", TierCode: "prod", Level: model.RiskHigh},
	}}
	e := NewRiskEngine(store)

	for _, c := range []struct {
		sql       string
		wantLevel string
		wantName  string
	}{
		// ALTER 在前、DROP 在后 —— 按 DROP 判。
		{"ALTER TABLE t_orders DROP PARTITION p_2024", model.RiskHigh, "DROP"},
		// 反过来写,结论必须一样:语序不该决定闸门的松紧。
		{"DROP TABLE t_orders", model.RiskHigh, "DROP"},
		// 只有 mid 的那个词时仍然是 mid。
		{"ALTER TABLE t_orders ADD COLUMN c INT", model.RiskMid, "ALTER"},
		// 两个 high 之间取谁都行,但不能降级。
		{"ALTER TABLE t TRUNCATE PARTITION p", model.RiskHigh, ""},
	} {
		name, lvl, err := e.matchCommand(c.sql, "prod")
		if err != nil {
			t.Fatalf("matchCommand: %v", err)
		}
		if lvl != c.wantLevel {
			t.Errorf("%q 判成 %s,期望 %s(命中 %q)—— 语序决定了闸门松紧", c.sql, lvl, c.wantLevel, name)
		}
		if c.wantName != "" && name != c.wantName {
			t.Errorf("%q 命中的是 %q,期望 %q —— 工单上要写清是哪个词让它高危的", c.sql, name, c.wantName)
		}
	}
}

// 一个词都没命中时仍然是 off。
func TestMatchCommand_NoHitStaysOff(t *testing.T) {
	store := &fakeStore{cmds: []model.RiskCommand{
		{Command: "DROP", TierCode: "prod", Level: model.RiskHigh},
	}}
	name, lvl, _ := NewRiskEngine(store).matchCommand("SELECT 1", "prod")
	if lvl != model.RiskOff || name != "" {
		t.Errorf("没命中时应当是 off/空,实际 %q/%q", name, lvl)
	}
}
