package sqlutil

import (
	"reflect"
	"testing"
)

func TestSplitStatements_RespectsQuotesAndComments(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"semicolon in string literal", `INSERT INTO t VALUES('a;b'); SELECT 1`,
			[]string{`INSERT INTO t VALUES('a;b')`, `SELECT 1`}},
		{"semicolon in quoted identifier", "UPDATE `a;b` SET x=1; SELECT 2",
			[]string{"UPDATE `a;b` SET x=1", "SELECT 2"}},
		{"semicolon in line comment", "SELECT 1 -- drop; me\n; SELECT 2",
			[]string{"SELECT 1", "SELECT 2"}},
		{"semicolon in block comment", "SELECT 1 /* a; b */; SELECT 2",
			[]string{"SELECT 1", "SELECT 2"}},
		{"escaped quote inside literal", `INSERT INTO t VALUES('it''s; ok'); SELECT 3`,
			[]string{`INSERT INTO t VALUES('it''s; ok')`, `SELECT 3`}},
		{"backslash does NOT escape (SQL standard, safe over-split)", `INSERT INTO t VALUES('a\'); DROP TABLE u; SELECT 1`,
			[]string{`INSERT INTO t VALUES('a\')`, `DROP TABLE u`, `SELECT 1`}},
		{"trailing empty", "SELECT 1;;  ;",
			[]string{"SELECT 1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SplitStatements(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("SplitStatements(%q) = %#v, want %#v", tc.in, got, tc.want)
			}
		})
	}
}
