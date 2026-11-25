package gateway

import "testing"

// 2026-10-26 讨论 15:去掉标识符引号的那份读法只查函数清单。MySQL 双引号字符串里的 into、反引号列名
// `into` 不再让只读查询被去掉 ANALYZE;带引号的函数名(pg_sleep)、真正的 INTO / 加锁读照旧拦。
func TestAnalyzeIsReadOnly_QuotedKeywordsAreData(t *testing.T) {
	for _, q := range []string{
		`SELECT * FROM t WHERE note = "check into it"`,
		"SELECT `into` FROM t",
		`SELECT "for update" AS x FROM t`,
		`SELECT id FROM t WHERE a = 1`,
	} {
		if !analyzeIsReadOnly(q) {
			t.Errorf("只读查询不该被去掉 ANALYZE: %q", q)
		}
	}
	for _, q := range []string{
		`SELECT "pg_sleep"(100)`,
		"SELECT `sleep`(5)",
		`SELECT * FROM t FOR UPDATE`,
		`SELECT * INTO OUTFILE '/tmp/x' FROM t`,
		`SELECT * INTO new_t FROM t`,
	} {
		if analyzeIsReadOnly(q) {
			t.Errorf("有副作用的仍应去掉 ANALYZE: %q", q)
		}
	}
}
