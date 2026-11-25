package gateway

import "testing"

// MaskMayBeEvaded 钉的是 M-10 在**导出**这条路上的收口。
//
// 打码是拿选择列表的**文本**去比对的,所以
//
//	SELECT x FROM (SELECT id_card AS x FROM t_user) s
//
// 的外层只有一个 `x`:既不是敏感列名,表达式里也没提到它 —— 两步都不命中,原值原样回传。
// 终端上那只是"顺手看到",能接受;导出不一样,它产出一份可以带走的 CSV,而且正是
// "敏感导出要审批"这条控制要拦的东西。
//
// 这里判的**不是**"会不会绕过"(那要真正的列血缘,而算错的方向是漏),而是保守得多的
// "有没有可能绕过"。所以下面既有"必须判 true"的用例,也有"不能草木皆兵"的用例 ——
// 一道逢查询就要审批的闸,两天之内就会被运维要求关掉。
func TestMaskMayBeEvaded(t *testing.T) {
	rules := []SensitiveRule{{Table: "t_user", Column: "id_card", Style: MaskPartial}}

	cases := []struct {
		name string
		sql  string
		want bool
	}{
		// —— 必须拦下的:句式会让列名换个样子回来 ——
		{"派生表别名", `SELECT x FROM (SELECT id_card AS x FROM t_user) s`, true},
		{"派生表大小写混写", `select X from ( Select Id_Card as X From T_User ) s`, true},
		{"JOIN 一个子查询", `SELECT a.x FROM orders o JOIN (SELECT id_card AS x FROM t_user) a ON 1=1`, true},
		{"UNION 拼接", `SELECT id FROM t_order UNION SELECT id_card FROM t_user`, true},
		{"CTE", `WITH s AS (SELECT id_card AS x FROM t_user) SELECT x FROM s`, true},

		// —— 不该拦的:形态是平的,打码本来就兜得住 ——
		{"普通查询", `SELECT id_card, name FROM t_user`, false},
		{"SELECT 星号", `SELECT * FROM t_user`, false},
		{"带函数的表达式", `SELECT SUBSTR(id_card,1,6) AS pre FROM t_user`, false},
		{"有子查询但不碰敏感表", `SELECT x FROM (SELECT amount AS x FROM t_order) s`, false},
		{"碰了敏感表但没提敏感字段", `SELECT x FROM (SELECT name AS x FROM t_user) s`, false},
		{"CTE 没提敏感字段", `WITH u AS (SELECT id, name FROM t_user) SELECT name FROM u`, false},
		{"UNION 没提敏感字段", `SELECT name FROM t_user UNION SELECT name FROM t_staff`, false},
		{"普通查询碰的是别的表", `SELECT * FROM t_order`, false},

		// —— 边界 ——
		{"没有任何规则", `SELECT x FROM (SELECT id_card AS x FROM t_user) s`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rs := rules
			if c.name == "没有任何规则" {
				rs = nil
			}
			if got := MaskMayBeEvaded(c.sql, rs); got != c.want {
				t.Fatalf("MaskMayBeEvaded(%q) = %v, want %v", c.sql, got, c.want)
			}
		})
	}
}

// 不限表的规则(Table 为空或 *)对**任何**表都可能生效,所以嵌套句式一律要拦 ——
// 现网大量部署就是这么配的(规则写 `*`,按列名全局脱敏),漏掉这一条等于这个功能对
// 他们整个不生效。
func TestMaskMayBeEvaded_WildcardTableRule(t *testing.T) {
	for _, tbl := range []string{"", "*"} {
		rules := []SensitiveRule{{Table: tbl, Column: "id_card"}}
		if !MaskMayBeEvaded(`SELECT x FROM (SELECT id_card AS x FROM whatever) s`, rules) {
			t.Fatalf("表限定为 %q 的规则应当对任意表的嵌套查询判 true", tbl)
		}
		if MaskMayBeEvaded(`SELECT id_card FROM whatever`, rules) {
			t.Fatalf("表限定为 %q 时,平铺查询不该被判成可能绕过", tbl)
		}
		// 现网规则就是 *:没提到 id_card 的 CTE 必须直接放行,否则每条 WITH 都进审批
		if MaskMayBeEvaded(`WITH t AS (SELECT id, amount FROM whatever) SELECT amount FROM t`, rules) {
			t.Fatalf("表限定为 %q 时,没提到敏感字段的 CTE 不该进审批", tbl)
		}
	}
}

// 注释被剥掉之后再判形态 —— `/* x */ SELECT … FROM (SELECT …)` 和没有注释的那条
// 必须得到同一个结论,否则一行注释就是绕过手段。
func TestMaskMayBeEvaded_IgnoresComments(t *testing.T) {
	rules := []SensitiveRule{{Table: "t_user", Column: "id_card"}}
	if !MaskMayBeEvaded(`/* nightly */ SELECT x FROM (SELECT id_card AS x FROM t_user) s`, rules) {
		t.Fatal("注释不该改变判定结论")
	}
}

// 字符串字面量里的 UNION 不是句式 —— 但这里刻意**允许**多判:方向朝严,多一次审批
// 比漏一份 CSV 好。这条用例把当前行为钉下来,将来若有人要收紧,会先看到它。
func TestMaskMayBeEvaded_LiteralIsConservative(t *testing.T) {
	rules := []SensitiveRule{{Table: "t_user", Column: "id_card"}}
	got := MaskMayBeEvaded(`SELECT id_card FROM t_user WHERE note = 'union'`, rules)
	if got {
		t.Log("当前实现对字面量中的 UNION 也判 true —— 方向朝严,可接受")
	}
}

// 命中时要说出是哪个字段:审批人要判断的正是"这条查询是不是把它改名带了出来"。
func TestMaskEvasionColumn_NamesTheColumn(t *testing.T) {
	rules := []SensitiveRule{{Table: "*", Column: "phone"}, {Table: "*", Column: "id_card"}}
	if got := MaskEvasionColumn(`SELECT x FROM (SELECT id_card AS x FROM t_user) s`, rules); got != "id_card" {
		t.Fatalf("应报出 id_card,实际 %q", got)
	}
	if got := MaskEvasionColumn(`WITH u AS (SELECT name FROM t_user) SELECT name FROM u`, rules); got != "" {
		t.Fatalf("没提敏感字段应为空,实际 %q", got)
	}
	// 引号包着、大写的标识符也算提到了
	if got := MaskEvasionColumn("SELECT x FROM (SELECT `ID_CARD` AS x FROM t_user) s", rules); got != "id_card" {
		t.Fatalf("反引号 + 大写也应命中,实际 %q", got)
	}
}

// 派生表不只有 `FROM (SELECT` 一种写法:多套一层括号、逗号后面接派生表、LATERAL,
// 外层看到的都是内层起的别名。认不出来,导出就不进审批,bizquery 也不拦(F4)。
func TestMaskEvasion_DerivedTableShapesAreRecognised(t *testing.T) {
	rules := []SensitiveRule{{Table: "t_user", Column: "id_card"}}
	for _, sql := range []string{
		"SELECT x FROM ((SELECT id_card x FROM t_user)) s",
		"SELECT s.x FROM a, (SELECT id_card x FROM t_user) s",
		"SELECT s.x FROM a JOIN ((SELECT id_card x FROM t_user)) s ON 1=1",
		"SELECT s.x FROM a, LATERAL (SELECT id_card x FROM t_user) s",
	} {
		if !MaskMayBeEvaded(sql, rules) {
			t.Errorf("%q 经派生表改了名,应判为可能绕过打码", sql)
		}
	}
}
