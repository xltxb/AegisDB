package review

// 审查说"带了 WHERE",判定说"没带" —— 两把尺子必须是同一把。
//
// ADR 0014:扫描要和执行同一把尺子。审查这一层也一样,而且更要紧 —— 它是人在**提交
// 之前**看的那份报告。报告说这条 UPDATE 合规,人就照着提了;真到执行时严格模式把它
// 拦下,或者更糟,那一层的严格模式是关着的,于是它就这么跑了。
//
// 两边各有一个"有没有 WHERE"的判断,而它们抹字面量的方式不一样:判定会把反引号标识符
// 的内容也抹掉,审查不会。于是一个名字里带 where 这个词的表,让审查以为语句带了条件。
//
//	UPDATE `where` SET a = 1
//
// 表名写成 `where` 当然少见。但这条规则的名字叫「UPDATE/DELETE 必须带 WHERE」,而它
// 放过的是一条**全表更新** —— 这条规则存在的全部理由就是拦住它。

import (
	"testing"

	"velagateway/internal/gateway"
)

func TestRequireWhere_AgreesWithTheRiskEngine(t *testing.T) {
	cases := []string{
		"UPDATE `where` SET a = 1",
		"DELETE FROM `where`",
		"UPDATE users SET note = 'where'",
		"UPDATE users SET note = 'where' WHERE id = 1",
		"DELETE FROM t -- WHERE id=1",
		"DELETE FROM t /* WHERE id=1 */",
		"UPDATE t SET a = 'a\\'b' WHERE id = 1",
		"WITH d AS (DELETE FROM t) SELECT 1",
		"WITH d AS (DELETE FROM t WHERE id=1) SELECT 1",
		"UPDATE t SET a = 1 WHERE EXISTS (SELECT 1 FROM u)",
		"update t set a = 1",
		"-- WHERE id=1\nUPDATE t SET a = 1", // 前导注释归这条语句,但它不是条件
		"UPDATE `where` SET a = 1 WHERE id = 1",
	}
	for _, sql := range cases {
		unscoped := gateway.DialectFor("mysql").UnscopedMutation(sql)
		flagged := false
		for _, f := range Check(DialectMySQL, sql+";", allRules()).Findings {
			if f.Code == "dml.require.where" {
				flagged = true
			}
		}
		if unscoped != flagged {
			t.Errorf("%q:判定说全表=%v,而审查%s —— 两层给了不同的答案",
				sql, unscoped, map[bool]string{true: "报了", false: "没报"}[flagged])
		}
	}
}
