package gateway

// UNION 的第二个及之后的分支同样要看。
//
// 结果集只带回列名,而列名来自**第一个**分支:
//
//	SELECT phone AS c FROM a UNION ALL SELECT id_card FROM t_user
//
// 回来的列叫 c。按列名匹配当然匹配不上;而「第 k 项是怎么算出来的」这一步只读了第一个
// SELECT 的列表,于是第二个分支里那个 id_card 从头到尾没有被看见 —— 身份证号原样刷在
// 屏幕上,那一列的名字还是个人畜无害的 c。
//
// ADR 0009 写的是「宁可多打」,而这里是实实在在的**漏打**。
//
// 按位置合并:第 k 列对应每个分支的第 k 项,任一分支命中就打第 k 列。UNION 本来就要求
// 各分支列数一致、第 k 列是同一个东西,所以这个对应关系是 SQL 自己保证的。

import "testing"

func TestSensitive_MasksAcrossUnionBranches(t *testing.T) {
	for _, c := range []struct {
		name string
		sql  string
		cols []string
		want string // 必须被打码的那一列
	}{
		{"UNION ALL 的第二个分支",
			`SELECT phone AS c FROM a UNION ALL SELECT id_card FROM t_user`,
			[]string{"c"}, "c"},
		{"UNION 的第二个分支",
			`SELECT nick AS c FROM a UNION SELECT id_card FROM t_user`,
			[]string{"c"}, "c"},
		{"第三个分支",
			`SELECT nick AS c FROM a UNION ALL SELECT nick FROM b UNION ALL SELECT phone FROM t_user`,
			[]string{"c"}, "c"},
		{"多列:只打命中的那一列",
			`SELECT nick AS a1, nick AS a2 FROM a UNION ALL SELECT nick, id_card FROM t_user`,
			[]string{"a1", "a2"}, "a2"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := maskedNames(c.sql, c.cols)
			if !hasAll(got, c.want) {
				t.Errorf("%q 没被打码 —— 敏感列靠一个 UNION 分支就原样回传了。实际打码: %v", c.want, got)
			}
		})
	}
}

// 不能因此变成"见 UNION 就全打" —— 多打一列是看不到本可以看的数据,天天误打的结果
// 是有人来要求把这道闸整个关掉。
func TestSensitive_UnionWithoutSensitiveColumnsStaysClear(t *testing.T) {
	got := maskedNames(
		`SELECT nick AS c FROM a UNION ALL SELECT nick FROM b`,
		[]string{"c"})
	if len(got) != 0 {
		t.Errorf("这条查询里没有敏感列,不该打码:%v", got)
	}
}
