package gateway

import "testing"

// 打码强度跟着**数据来自哪个字段**走,不跟着列叫什么走。
//
// 样式原先按结果列名查:`id_card AS x` 的列名 x 查不到样式,回落成 partial —— 规则写的
// full 被降成"留头 3 位、尾 4 位",再用几个错开起点的 SUBSTR 窗口就能拼回全号。
// `id_card AS phone` 更直接:套用了 phone 那条更弱的样式。
func TestMaskStyle_FollowsTheSourceColumnNotTheAlias(t *testing.T) {
	prev := SensitiveRulesProvider
	SensitiveRulesProvider = func() []SensitiveRule {
		return []SensitiveRule{
			{Table: "*", Column: "id_card", Style: MaskFull},
			{Table: "*", Column: "phone", Style: MaskPartial},
		}
	}
	t.Cleanup(func() { SensitiveRulesProvider = prev })

	const v = "310101199001011234"
	for _, c := range []struct{ sql, col string }{
		{"SELECT id_card FROM t", "id_card"},
		{"SELECT id_card AS x FROM t", "x"},
		{"SELECT id_card AS phone FROM t", "phone"},
		{"SELECT SUBSTR(id_card, 2, 17) AS x FROM t", "x"},
	} {
		data := [][]string{{v}}
		maskResultSet(c.sql, []string{c.col}, data)
		if data[0][0] != MaskValue(v, MaskFull) {
			t.Errorf("%q → %q,应按 id_card 的 full 打码", c.sql, data[0][0])
		}
	}
}
