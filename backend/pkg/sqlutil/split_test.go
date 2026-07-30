package sqlutil

import (
	"strings"
	"reflect"
	"testing"
)

// A PostgreSQL dollar-quoted string ($$...$$) is a literal, so a single quote
// inside it must not open a literal that swallows the statement separator. When
// it does, `SELECT $$'$$ ; DROP TABLE t` looks like ONE read-only statement to
// the risk engine while the server (simple query protocol) really runs the
// stacked DROP — the capability matrix is bypassed entirely (ER1).
func TestSplitStatements_DollarQuoteDoesNotMergeStatements(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"quote inside dollar quote", `SELECT $$'$$ ; DROP TABLE t`,
			[]string{`SELECT $$'$$`, `DROP TABLE t`}},
		{"tagged dollar quote", `SELECT $tag$'$tag$ ; DROP TABLE t`,
			[]string{`SELECT $tag$'$tag$`, `DROP TABLE t`}},
		{"separator inside dollar quote is not a split", `SELECT $$a;b$$ ; SELECT 2`,
			[]string{`SELECT $$a;b$$`, `SELECT 2`}},
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

// In a PostgreSQL escape string (E'...') a backslash escapes the next byte, so
// E'\'' is a complete literal holding one quote. Applying the standard
// doubled-quote rule instead treats the trailing '' as an escaped quote, leaving
// the literal open so it swallows the separator and hides a stacked DROP (ER1).
func TestSplitStatements_EscapeStringDoesNotMergeStatements(t *testing.T) {
	got := SplitStatements(`SELECT E'\'' ; DROP TABLE t`)
	want := []string{`SELECT E'\''`, `DROP TABLE t`}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SplitStatements = %#v, want %#v", got, want)
	}
}

// '#' starts a line comment only in MySQL; in PostgreSQL it is an operator, so
// treating it as a comment DELETES a real stacked statement from the judged text
// while the server still runs it (ER2). Splitting on the separator instead is
// safe in both directions: on PostgreSQL the DROP gets judged, and on MySQL the
// extra fragment is merely judged redundantly — over-splitting can only
// over-block, never let a statement through unjudged.
func TestSplitStatements_HashDoesNotHideStackedStatement(t *testing.T) {
	got := SplitStatements("SELECT 1 #x; DROP TABLE orders")
	want := []string{"SELECT 1 #x", "DROP TABLE orders"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SplitStatements = %#v, want %#v", got, want)
	}
}

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

// MySQL runs the body of an executable comment (/*!ver ... */), so deleting it
// like an ordinary block comment hides real SQL from the judgement while the
// server still executes it. That is how a "read-only" export smuggled an INTO
// OUTFILE past the verb whitelist (EX2). risk.StripComments already keeps these
// bodies (A3); the splitter must agree with it.
func TestSplitStatements_KeepsExecutableCommentBody(t *testing.T) {
	got := SplitStatements(`SELECT 1 FROM dual /*!40000 INTO OUTFILE '/tmp/pwn' */`)
	if len(got) != 1 || !strings.Contains(strings.ToUpper(got[0]), "INTO OUTFILE") {
		t.Errorf("SplitStatements dropped the executable comment body: %#v", got)
	}
	// A stacked statement inside an executable comment must still separate.
	stacked := SplitStatements(`SELECT 1 /*!40000 ; DROP TABLE t */`)
	if len(stacked) != 2 {
		t.Errorf("stacked statement inside an executable comment was not split: %#v", stacked)
	}
}
