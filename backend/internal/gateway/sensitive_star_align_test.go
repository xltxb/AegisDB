package gateway

import "testing"

// H1(2026-10 安全审计):选择列表里有 `*` 时项数与列数对不上,"第 k 项怎么算出来的"这一步
// 整个被跳过 —— 星号旁边把敏感列改个名,结果集里就是一列原值。
//
// 只有一个星号时位置是确定的:它前面的项从左数、后面的项从右数。多个星号对不上,就看选择
// 列表提没提到敏感列 —— 提到了,整张结果按最严样式打码(错位地打漏比多打更糟)。

func TestSensitive_StarWithRenamedColumnBesideIt(t *testing.T) {
	rules := []SensitiveRule{{Table: "t_user", Column: "id_card", Style: MaskPartial}}
	cols := []string{"id", "name", "id_card", "x"} // * 展开成前三列,第四列是改了名的 id_card

	if got := starMaskedNames("SELECT *, id_card AS x FROM t_user", cols, rules); got != "id_card,x" {
		t.Errorf("星号在前:打码列 %q,期望 id_card,x", got)
	}
	cols2 := []string{"x", "id", "name", "id_card"}
	if got := starMaskedNames("SELECT id_card AS x, * FROM t_user", cols2, rules); got != "x,id_card" {
		t.Errorf("星号在后:打码列 %q,期望 x,id_card", got)
	}
	// 星号旁边是无关的列:不多打
	cols3 := []string{"id", "name", "id_card", "flag"}
	if got := starMaskedNames("SELECT *, 1 AS flag FROM t_user", cols3, rules); got != "id_card" {
		t.Errorf("无关的列不该打码:%q", got)
	}
}

func TestSensitive_MultipleStarsFallBackToMaskingEverything(t *testing.T) {
	rules := []SensitiveRule{{Table: "t_user", Column: "id_card", Style: MaskPartial}}
	cols := []string{"id", "id_card", "id", "id_card", "x"}
	got := starMaskedNames("SELECT a.*, b.*, b.id_card AS x FROM t_user a JOIN t_user b ON a.id = b.id", cols, rules)
	if got != "id,id_card,id,id_card,x" {
		t.Errorf("多个星号且提到了敏感列:应整张打码,实际 %q", got)
	}
	// 多个星号但没提到敏感列:按列名照常
	if got := starMaskedNames("SELECT a.*, b.* FROM t_user a JOIN t_user b ON a.id = b.id", cols[:4], rules); got != "id_card,id_card" {
		t.Errorf("没提到敏感列时按列名打码,实际 %q", got)
	}
}

// count(*) / 乘号里的星号不是"选择全部":从前一见星号就放弃对位,改名的敏感列于是漏掉
func TestSensitive_CountStarDoesNotDisablePositionalPass(t *testing.T) {
	rules := []SensitiveRule{{Table: "t_user", Column: "id_card", Style: MaskPartial}}
	if got := starMaskedNames("SELECT count(*) AS n, id_card AS x FROM t_user GROUP BY id_card", []string{"n", "x"}, rules); got != "x" {
		t.Errorf("count(*) 旁边改名的敏感列应打码,实际 %q", got)
	}
}

// 2026-10-22 生产反馈:EXPLAIN 的执行计划被整张打成 ******。认不出选择列表的语句(EXPLAIN、SHOW、
// CALL…)返回的不是表里的数据行;H1 的"整张打码"兜底只该用在确实是 SELECT、却因多个星号对不上位的时候,
// 认不出选择列表时回到只按列名打码。
func TestSensitive_ExplainPlanIsNotMasked(t *testing.T) {
	rules := []SensitiveRule{{Table: "*", Column: "bank_account_no", Style: MaskFull}, {Table: "*", Column: "mobile_no", Style: MaskFull}}
	cols := []string{"id", "estRows", "task", "access object", "operator info"}
	sql := "explain select count(*) from ( select mt.id, mt.PAYMENT_BANK_ACCOUNT_NO, tpt.BANK_ACCOUNT_NO MOBILE_NO from t_merchant_tran mt left join t_product_tran tpt ON tpt.BILL_NO = mt.bill_no ) t"
	if got := starMaskedNames(sql, cols, rules); got != "" {
		t.Errorf("执行计划不是数据行,不应打码,实际打码列 %q", got)
	}
	if got := starMaskedNames("SHOW CREATE TABLE t_user", []string{"Table", "Create Table"}, []SensitiveRule{{Table: "*", Column: "id_card"}}); got != "" {
		t.Errorf("SHOW 的结果不应整张打码,实际 %q", got)
	}
	// 结果集列名本身就是敏感列时,照样按列名打码
	if got := starMaskedNames("CALL get_user(1)", []string{"id", "mobile_no"}, rules); got != "mobile_no" {
		t.Errorf("按列名的那一步照旧,实际 %q", got)
	}
}
