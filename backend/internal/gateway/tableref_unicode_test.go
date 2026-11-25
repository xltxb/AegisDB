package gateway

import "testing"

// referencedTables 的正则原先用 [A-Za-z_…] 这样的 ASCII 字符类抓表名。中文表名整个抓
// 不到,于是**按表限定**的规则在中文表上永远不激活 —— 与 mentionsColumn 的 \b 是同一类
// 问题:标识符是什么文字,不该决定规则管不管用。不限表(*)的规则不受影响,所以这个洞
// 在规则全写成 * 的部署里看不见,而一旦有人给某张中文表单独配规则,它就静静地不生效。
func TestReferencedTables_NonASCIINames(t *testing.T) {
	cases := []struct {
		sql  string
		want string
	}{
		{"SELECT id_card FROM 用户表", "用户表"},
		{"SELECT id_card FROM 业务库.用户表 u", "用户表"},
		{"SELECT a.x FROM t_order a JOIN 用户表 b ON a.uid = b.id", "用户表"},
		{"SELECT id_card FROM `用户表`", "用户表"},
		{"SELECT id_card FROM \"用户表\"", "用户表"},
	}
	for _, c := range cases {
		got := referencedTables(c.sql)
		if !got[c.want] {
			t.Errorf("referencedTables(%q) 缺少 %q,实际 %v", c.sql, c.want, keys(got))
		}
	}
}

// 按表限定到中文表的规则,平铺查询要打码,嵌套改名要进审批。
func TestSensitiveRules_ScopedToNonASCIITable(t *testing.T) {
	rules := []SensitiveRule{{Table: "用户表", Column: "id_card", Style: MaskPartial}}

	got := SensitiveMaskTargets("SELECT id_card, name FROM 用户表", []string{"id_card", "name"}, rules)
	if len(got) != 1 || got[0] != 0 {
		t.Fatalf("中文表上的 id_card 应打码,实际 %v", got)
	}
	if col := MaskEvasionColumn("SELECT x FROM (SELECT id_card AS x FROM 用户表) s", rules); col != "id_card" {
		t.Fatalf("中文表上改名带出 id_card 应进审批,实际 %q", col)
	}
	// 别的表不受这条规则管
	if got := SensitiveMaskTargets("SELECT id_card FROM t_other", []string{"id_card"}, rules); len(got) != 0 {
		t.Fatalf("规则限定在 用户表,t_other 不该打码,实际 %v", got)
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
