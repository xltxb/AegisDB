package gateway

// 敏感字段脱敏 —— 哪几列该被打码。
//
// 难点只有一个:**结果集只带回列名,不带回它来自哪张表**,而且
//
//	SELECT id_card FROM t_user            → 列名 id_card,认得出
//	SELECT id_card AS x FROM t_user       → 列名 x,按列名匹配就漏了
//	SELECT SUBSTR(id_card,1,6) AS x       → 同上
//
// 所以除了按**列名**匹配,还要按**这一列是怎么算出来的**匹配:选择列表里第 k 项的
// 表达式里提到了敏感字段,第 k 列就打码。这条方向上是"宁可多打" —— 和分句器"宁可
// 多切"一样,多打一列是看不到本可以看的数据,漏打一列是敏感数据直接回传。
//
// 它有明确的上限,写在 SensitiveMaskTargets 的注释里:一个铁了心要拿数据的人,
// 总能构造出映射不回去的表达式。这道闸防的是**顺手看到**,真正的边界是不给表权限。

import (
	"strings"
	"testing"
)

func rules() []SensitiveRule {
	return []SensitiveRule{
		{Table: "t_user", Column: "id_card"},
		{Table: "t_user", Column: "phone"},
		{Table: "t_card", Column: "bank_card_no"},
	}
}

func maskedNames(sql string, cols []string) []string {
	idx := SensitiveMaskTargets(sql, cols, rules())
	out := []string{}
	for _, i := range idx {
		out = append(out, cols[i])
	}
	return out
}

func hasAll(got []string, want ...string) bool {
	for _, w := range want {
		found := false
		for _, g := range got {
			if g == w {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return len(got) == len(want)
}

// 最直接的一种:列名就是敏感字段名。
func TestMaskTargets_ByColumnName(t *testing.T) {
	got := maskedNames(`SELECT id_card, name FROM t_user`, []string{"id_card", "name"})
	if !hasAll(got, "id_card") {
		t.Errorf("应只打码 id_card,实际 %v", got)
	}
}

// SELECT * 反而是最安全的一种:驱动回传的就是真实列名。
func TestMaskTargets_SelectStarUsesRealColumnNames(t *testing.T) {
	got := maskedNames(`SELECT * FROM t_user`, []string{"id", "name", "id_card", "phone"})
	if !hasAll(got, "id_card", "phone") {
		t.Errorf("应打码 id_card 与 phone,实际 %v", got)
	}
}

// 别名是这件事的核心难点:按列名匹配会漏,必须看这一列是怎么算出来的。
func TestMaskTargets_AliasDoesNotDefeatIt(t *testing.T) {
	cases := []struct {
		sql  string
		cols []string
	}{
		{`SELECT id_card AS x, name FROM t_user`, []string{"x", "name"}},
		{`SELECT id_card x, name FROM t_user`, []string{"x", "name"}},
		{`SELECT u.id_card AS x, u.name FROM t_user u`, []string{"x", "name"}},
		{`SELECT SUBSTR(id_card, 1, 6) AS x, name FROM t_user`, []string{"x", "name"}},
		{`SELECT CONCAT(id_card, '') AS x, name FROM t_user`, []string{"x", "name"}},
	}
	for _, c := range cases {
		got := maskedNames(c.sql, c.cols)
		if !hasAll(got, "x") {
			t.Errorf("别名/表达式没兜住: %q → 打码了 %v,应包含 x", c.sql, got)
		}
	}
}

// 规则是按 表名+字段 定的:同名字段在别的表上不该被连坐。
func TestMaskTargets_ScopedToTheTableTheRuleNames(t *testing.T) {
	got := maskedNames(`SELECT id_card FROM t_other`, []string{"id_card"})
	if len(got) != 0 {
		t.Errorf("规则只针对 t_user.id_card,查 t_other 不该打码,实际 %v", got)
	}
	// 但 JOIN 里出现了 t_user,就该生效。
	got = maskedNames(`SELECT u.id_card, o.amount FROM t_other o JOIN t_user u ON u.id = o.uid`,
		[]string{"id_card", "amount"})
	if !hasAll(got, "id_card") {
		t.Errorf("JOIN 到 t_user 时应打码 id_card,实际 %v", got)
	}
}

// 敏感字段出现在 WHERE 里不该牵连输出列 —— 否则 `SELECT count(1) … WHERE id_card = ?`
// 会把计数也打码,而计数本身不是敏感数据。
func TestMaskTargets_WhereClauseDoesNotMaskTheOutput(t *testing.T) {
	got := maskedNames(`SELECT count(1) AS n FROM t_user WHERE id_card = 'x'`, []string{"n"})
	if len(got) != 0 {
		t.Errorf("WHERE 里的敏感字段不该让输出列被打码,实际 %v", got)
	}
}

// 字符串字面量里出现字段名不算 —— 那只是一段文本。
func TestMaskTargets_LiteralMentionIsNotAColumn(t *testing.T) {
	got := maskedNames(`SELECT 'id_card' AS label, name FROM t_user`, []string{"label", "name"})
	if len(got) != 0 {
		t.Errorf("字面量不该触发打码,实际 %v", got)
	}
}

// 规则可以用 * 覆盖所有表 —— 有些字段(如口令)在哪张表上都不该看到。
func TestMaskTargets_WildcardTableAppliesEverywhere(t *testing.T) {
	rs := []SensitiveRule{{Table: "*", Column: "passwd"}}
	idx := SensitiveMaskTargets(`SELECT passwd FROM anything`, []string{"passwd"}, rs)
	if len(idx) != 1 {
		t.Errorf("表名为 * 的规则应在任何表上生效,实际打码 %d 列", len(idx))
	}
}

// 大小写不敏感:库里写 ID_CARD、SQL 里写 id_card,是同一个东西。
func TestMaskTargets_CaseInsensitive(t *testing.T) {
	rs := []SensitiveRule{{Table: "T_USER", Column: "ID_CARD"}}
	idx := SensitiveMaskTargets(`select id_card from t_user`, []string{"ID_CARD"}, rs)
	if len(idx) != 1 {
		t.Errorf("大小写不该影响匹配,实际打码 %d 列", len(idx))
	}
}

// ---------------------------------------------------------------- 打码本身

func TestMaskValue_KeepsEnoughToBeUsableButNotEnoughToBeTheData(t *testing.T) {
	// 部分打码:留头留尾,中间抹掉 —— 够用来核对,不够用来泄露。
	got := MaskValue("110101199003071234", MaskPartial)
	if got == "110101199003071234" {
		t.Fatal("没有打码")
	}
	if len(got) == 0 {
		t.Fatal("打码后不能是空 —— 空值会被误读成'这行没有数据'")
	}
	if got[:3] != "110" {
		t.Errorf("部分打码应保留开头几位,实际 %q", got)
	}

	// 短值全抹:留头留尾会把一个 4 位数几乎原样露出来。
	if got := MaskValue("1234", MaskPartial); got == "1234" {
		t.Errorf("短值应整体打码,实际 %q", got)
	}
	// 空值保持空:把 NULL 打成 *** 会让人以为那里有数据。
	if got := MaskValue("", MaskPartial); got != "" {
		t.Errorf("空值应保持空,实际 %q", got)
	}
	// 全打码
	if got := MaskValue("110101199003071234", MaskFull); strings.ContainsAny(got, "0123456789") {
		t.Errorf("full 模式不该留下原值的任何字符,实际 %q", got)
	}
	// 哈希:同值同码,可用来核对是否同一个人,但看不出是谁。
	a, b := MaskValue("abc", MaskHash), MaskValue("abc", MaskHash)
	if a != b {
		t.Error("hash 模式同值应得到同码,否则没法核对")
	}
	if c := MaskValue("abd", MaskHash); c == a {
		t.Error("hash 模式不同值应得到不同码")
	}
}
