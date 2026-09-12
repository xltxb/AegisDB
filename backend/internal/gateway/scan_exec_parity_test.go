package gateway

// 扫描和执行必须用同一把尺子。
//
// 执行那一路(EvaluateFor)对**会话级设置**短路:`ALTER SESSION SET CURRENT_SCHEMA = x`
// 既不读也不写数据,只配置这条连接,所以它不过字典、不过严格模式 —— 在 Oracle 上切
// schema 正是读数据的前置步骤,拦掉它只读用户就什么都干不了了。
//
// 扫描那一路没有这道短路。于是同一条语句:
//
//	脚本扫描 → 命中字典里的 ALTER,报 high
//	实际执行 → 短路放行
//
// ADR 0014 写的是「扫描必须和执行同一把尺子」,ADR 0015 也专门告诫过「终端免审批、
// 后台却要审批」这种分叉 —— 它比单纯报错更糟:人照着扫描报告去拆语句、去提审批,
// 而那份报告说的事根本不会发生。

import (
	"testing"

	"velagateway/internal/model"
)

func TestScanStatement_SessionScopedMatchesExecution(t *testing.T) {
	// 字典里 ALTER 是 high —— 这正是运维为了拦 `ALTER TABLE` 会写的那一条。
	store := &fakeStore{cmds: []model.RiskCommand{
		{Command: "ALTER", TierCode: "prod", Level: model.RiskHigh},
	}, strict: true}
	e := NewRiskEngine(store)

	for _, sql := range []string{
		`ALTER SESSION SET CURRENT_SCHEMA = app_owner`,
		`SET search_path TO app_owner`,
	} {
		// 执行那一路:短路放行。
		v := e.EvaluateFor([]int64{1}, "oracle", "prod", sql)
		if v.Action != ActionAllow {
			t.Fatalf("前置条件不成立:执行路径本应放行会话级设置,实际 %s", v.Action)
		}
		// 扫描那一路必须给同一个答案。
		_, risk, _ := e.ScanStatement("oracle", "prod", sql)
		if risk != "safe" {
			t.Errorf("%q 扫描报 %s、执行却放行 —— 人会照着这份报告去拆语句、去提审批,"+
				"而它说的事根本不会发生", sql, risk)
		}
	}
}

// 真正的 ALTER TABLE 不受影响 —— 短路只给会话级设置,不给改表。
func TestScanStatement_RealAlterStillReported(t *testing.T) {
	store := &fakeStore{cmds: []model.RiskCommand{
		{Command: "ALTER", TierCode: "prod", Level: model.RiskHigh},
	}}
	_, risk, _ := NewRiskEngine(store).ScanStatement("oracle", "prod", `ALTER TABLE t_orders ADD c INT`)
	if risk != "high" {
		t.Errorf("改表仍然该报 high,实际 %s", risk)
	}
}
