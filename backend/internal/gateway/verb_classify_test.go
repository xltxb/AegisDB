package gateway

import "testing"

// B7: NoWhere must use a WHERE *word*, not a substring. `DELETE FROM elsewhere`
// and `UPDATE nowhere_log ...` contain the letters "where" inside an identifier
// but have no WHERE clause — they are full-table mutations and must be flagged.
func TestNoWhere_WordBoundary(t *testing.T) {
	cases := []struct {
		sql  string
		want bool
	}{
		{"DELETE FROM elsewhere_tbl", true},
		{"UPDATE nowhere_log SET x = 1", true},
		{"DELETE FROM t WHERE id = 1", false},
		{"UPDATE t SET x = 1 WHERE id = 2", false},
		{"DELETE FROM orders", true},
		{"SELECT * FROM elsewhere", false}, // not a delete/update at all
	}
	for _, c := range cases {
		if got := NoWhere(c.sql); got != c.want {
			t.Errorf("NoWhere(%q) = %v, want %v", c.sql, got, c.want)
		}
	}
}

// B1: read/write classification must reflect the effective executed verb, not a
// leading-keyword substring. `EXPLAIN ANALYZE <DML>` executes the wrapped
// statement on PostgreSQL, so it must NOT be classified as a read.
func TestIsRead_ExplainAnalyzeIsWrite(t *testing.T) {
	cases := []struct {
		sql  string
		want bool
	}{
		{"SELECT 1", true},
		{"  select * from t", true},
		{"SHOW TABLES", true},
		{"EXPLAIN SELECT 1", true},
		{"EXPLAIN", true},
		{"EXPLAIN ANALYZE DELETE FROM t", false},
		{"explain analyze update t set x = 1", false},
		{"DELETE FROM t WHERE id = 1", false},
		{"UPDATE t SET x = 1", false},
	}
	for _, c := range cases {
		if got := IsRead(c.sql); got != c.want {
			t.Errorf("IsRead(%q) = %v, want %v", c.sql, got, c.want)
		}
	}
}
