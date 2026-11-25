package gateway

// `SELECT *` 这条路上的脱敏。
//
// 它是最常见的查法,也是最容易被想当然的一条:列名由驱动回传,看起来"当然对得上"。
// 真正决定成败的是另一半 —— 按表限定的规则要先**认出这条语句查的是哪张表**才会激活,
// 而表名的写法有很多种。
//
// 这一组把那些写法逐个钉住。它是补票的:`SELECT * FROM ` + 反引号 + `t_user` 曾经**不打码** ——
// 取表名前先过了 blankQuoted,而它连引号标识符的内容一起抹成了空格,于是表名整个消失、
// 按表限定的规则不激活,一列身份证号原样回传。反引号是 MySQL 侧 ORM 与图形客户端的默认
// 写法,双引号在 PG / Oracle 上同样是常态 —— 这条漏法不需要谁刻意构造。

import (
	"strings"
	"testing"
)

func starMaskedNames(sql string, cols []string, rules []SensitiveRule) string {
	out := []string{}
	for _, i := range SensitiveMaskTargets(sql, cols, rules) {
		out = append(out, strings.ToLower(cols[i]))
	}
	return strings.Join(out, ",")
}

// 表名怎么写都要认得出来 —— 认不出来,按表限定的规则就不激活。
func TestSensitive_SelectStarMasksRegardlessOfHowTheTableIsWritten(t *testing.T) {
	rules := []SensitiveRule{{Table: "t_user", Column: "id_card", Style: MaskPartial}}
	cols := []string{"id", "name", "id_card"}

	for _, c := range []struct{ name, sql string }{
		{"裸表名", "SELECT * FROM t_user"},
		{"库名限定", "SELECT * FROM orders_db.t_user"},
		{"别名", "SELECT * FROM t_user u"},
		{"AS 别名", "SELECT * FROM t_user AS u"},
		{"大写", "SELECT * FROM T_USER"},
		{"反引号(MySQL 的默认写法)", "SELECT * FROM `t_user`"},
		{"双引号(PG / Oracle 的默认写法)", `SELECT * FROM "t_user"`},
		{"反引号 + 库名", "SELECT * FROM `orders_db`.`t_user`"},
		{"带前缀的星号", "SELECT u.* FROM t_user u"},
		{"JOIN", "SELECT * FROM t_user u JOIN t_order o ON o.uid = u.id"},
		{"星号后面还有别的列", "SELECT *, 1 AS flag FROM t_user"},
		{"注释与换行", "SELECT * -- 取全部\nFROM `t_user`"},
		{"WITH 子句", "WITH c AS (SELECT * FROM t_user) SELECT * FROM c"},
	} {
		if got := starMaskedNames(c.sql, cols, rules); got != "id_card" {
			t.Errorf("%s:打码列是 %q,期望 id_card —— 这种写法下敏感列原样回传了", c.name, got)
		}
	}
}

// Oracle 驱动把列名回传成大写,规则写的是小写 —— 两边都折成小写比。
func TestSensitive_SelectStarMatchesColumnNameCaseInsensitively(t *testing.T) {
	rules := []SensitiveRule{{Table: "t_user", Column: "id_card"}}
	if got := starMaskedNames("SELECT * FROM t_user", []string{"ID", "ID_CARD"}, rules); got != "id_card" {
		t.Errorf("大写列名没被认出来:%q", got)
	}
}

// 放开的那一半:不该打的别打。
//
// 字符串字面量里出现的表名不算数(否则一条 WHERE note = 'from t_user' 就能让别的表的
// 规则跟着激活),而聚合结果不是敏感数据 —— 把 count(1) 打码只会让人觉得这道闸没道理。
func TestSensitive_SelectStarDoesNotOverreach(t *testing.T) {
	rules := []SensitiveRule{{Table: "t_user", Column: "id_card"}}

	if got := starMaskedNames("SELECT count(1) FROM t_user WHERE id_card = 'x'", []string{"count(1)"}, rules); got != "" {
		t.Errorf("聚合结果被打码了:%q", got)
	}
	// 查的是另一张表,只是字面量里提到了 t_user
	if got := starMaskedNames("SELECT * FROM t_other WHERE note = 'from t_user'",
		[]string{"id", "id_card"}, rules); got != "" {
		t.Errorf("字面量里的表名让别的表跟着激活了:%q", got)
	}
}

// Table = "*" 的规则与表名写法无关:口令这类字段在哪张表上都不该被看到。
func TestSensitive_WildcardTableRuleAppliesEverywhere(t *testing.T) {
	rules := []SensitiveRule{{Table: "*", Column: "password", Style: MaskFull}}
	for _, sql := range []string{
		"SELECT * FROM whatever",
		"SELECT * FROM `whatever`",
		`SELECT * FROM "whatever"`,
	} {
		if got := starMaskedNames(sql, []string{"id", "password"}, rules); got != "password" {
			t.Errorf("%s:通配规则没生效,打码列是 %q", sql, got)
		}
	}
}
