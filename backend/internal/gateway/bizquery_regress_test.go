package gateway

import "testing"

// 2026-10-26 回归排查(8302027):PG / GaussDB 的全文检索 @@ 被当成 MySQL 系统变量拒;
// MySQL 上名叫 system_user / session_user / current_role 的列被当成信息函数拒。
func TestBizReadOnlyCheck_RestoredQueries(t *testing.T) {
	for _, c := range []struct{ engine, sql, db string }{
		{"PostgreSQL 15", "SELECT id FROM docs WHERE to_tsvector(body) @@ to_tsquery('foo')", "app"},
		{"GaussDB", "SELECT id FROM docs WHERE tsv @@ plainto_tsquery('english', 'foo bar')", "app"},
		{"MySQL 8.0", "SELECT system_user FROM t", "app"},
		{"MySQL 8.0", "SELECT session_user, current_role FROM t", "app"},
	} {
		if r := BizReadOnlyCheck(c.engine, c.sql, c.db, []string{c.db}); r != nil {
			t.Errorf("%s 上不该拒: %q → %+v", c.engine, c.sql, r)
		}
	}
	// 照旧拒:MySQL 读服务器信息的 @@变量与带括号的信息函数;PG 的裸 session_user
	for _, c := range []struct{ engine, sql string }{
		{"MySQL 8.0", "SELECT @@hostname"},
		{"MySQL 8.0", "SELECT SYSTEM_USER()"},
		{"PostgreSQL 15", "SELECT session_user"},
		{"PostgreSQL 15", "SELECT current_setting('data_directory')"},
	} {
		if r := BizReadOnlyCheck(c.engine, c.sql, "app", []string{"app"}); r == nil {
			t.Errorf("%s 上仍应拒: %q", c.engine, c.sql)
		}
	}
}
