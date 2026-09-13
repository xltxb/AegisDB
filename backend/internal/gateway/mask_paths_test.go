package gateway

// 两条打码路必须给出同一份结果。
//
// 控制台的结果集走 maskResultSet,导出走 maskStream(它是流式的,列在表头时就定下来,
// 之后每行套同一套改写)。两段代码里"取规则 → 算目标列 → 为每列定打码样式"是逐字
// 相同的八行。
//
// 抄两遍在这里的代价很具体:它们分叉,就意味着**导出下来的文件和界面上看到的不一样**。
// 而先看见的那一份是界面 —— 人以为自己看到的就是会导出的东西,于是把一份没打码的
// 手机号带出了网关。这种分叉不会报错,也不会有人发现,除非有人正好两边对着看。

import (
	"reflect"
	"testing"
)

func withRules(t *testing.T, rules []SensitiveRule) {
	t.Helper()
	prev := SensitiveRulesProvider
	SensitiveRulesProvider = func() []SensitiveRule { return rules }
	t.Cleanup(func() { SensitiveRulesProvider = prev })
}

func TestMaskPaths_ConsoleAndExportAgree(t *testing.T) {
	withRules(t, []SensitiveRule{
		{Column: "phone", Style: MaskPartial},
		{Column: "id_card", Style: MaskFull},
		{Column: "Email", Style: MaskPartial}, // 大小写不该把两条路分开
	})

	const sql = "SELECT id, phone, id_card, email, note FROM tbl_customer"
	cols := []string{"id", "phone", "id_card", "email", "note"}
	row := []string{"7", "13800001111", "310101199001011234", "a@vela.io", "备注"}

	// 控制台那一路:就地改写。
	console := [][]string{append([]string(nil), row...)}
	consoleNames := maskResultSet(sql, cols, console)

	// 导出那一路:先拿到一个逐行的改写器。
	apply, exportNames := maskStream(sql, cols)
	exported := append([]string(nil), row...)
	apply(exported)

	if !reflect.DeepEqual(console[0], exported) {
		t.Errorf("同一行数据,界面上是 %q、导出是 %q —— 两条路打码不一致", console[0], exported)
	}
	if !reflect.DeepEqual(consoleNames, exportNames) {
		t.Errorf("标记为已脱敏的列不一致:界面 %v、导出 %v", consoleNames, exportNames)
	}
	// 免得两条路一起什么都没做,用例还显示通过。
	if console[0][1] == row[1] {
		t.Fatalf("phone 根本没被打码:%q", console[0][1])
	}
}

// 一条规则都没配时,两条路也要一致地什么都不做。
func TestMaskPaths_AgreeWhenNothingIsConfigured(t *testing.T) {
	withRules(t, nil)
	const sql = "SELECT phone FROM tbl_customer"
	cols := []string{"phone"}

	console := [][]string{{"13800001111"}}
	consoleNames := maskResultSet(sql, cols, console)

	apply, exportNames := maskStream(sql, cols)
	exported := []string{"13800001111"}
	apply(exported)

	if console[0][0] != exported[0] || console[0][0] != "13800001111" {
		t.Errorf("没配规则却动了数据:界面 %q、导出 %q", console[0][0], exported[0])
	}
	if len(consoleNames) != 0 || len(exportNames) != 0 {
		t.Errorf("没配规则却标了脱敏列:界面 %v、导出 %v", consoleNames, exportNames)
	}
}
