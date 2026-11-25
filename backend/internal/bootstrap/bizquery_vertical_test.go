package bootstrap

import "testing"

// 2026-11-12:数据查询里结尾的 \G(mysql 客户端的竖排显示)剥掉再判定、再执行,不再被当成反斜杠歧义拒绝。
func TestBizQuery_TrailingBackslashG(t *testing.T) {
	b := newBizEnv(t, 0)
	for _, q := range []string{`SELECT id FROM orders WHERE id <= 2\G`, `SELECT id FROM orders WHERE id <= 2 \g;`, "SELECT id FROM orders WHERE id <= 2\\G\n"} {
		r, out := b.exec(q)
		eq(t, r.Code, 0, q)
		if len(out.Rows) != 2 || out.Error != "" {
			t.Errorf("%q 应返回 2 行,实际 %d 行 err=%q", q, len(out.Rows), out.Error)
		}
	}
	// 只剥结尾:中间的 \G 照旧按原文判定
	if r, out := b.exec(`SELECT id FROM orders\G SELECT 1`); r.Code == 0 && out.Error == "" {
		t.Error(`中间夹着 \G 的语句不该被剥掉后执行成功`)
	}
	// 整条就是一个 \G:没有语句
	if r, _ := b.exec(`\G`); r.Code == 0 {
		t.Error(`只有 \G 不该执行`)
	}
}
