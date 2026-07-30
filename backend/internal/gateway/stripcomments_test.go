package gateway

import (
	"strings"
	"testing"
)

// A3: an executable comment whose body opens with a nested plain comment must
// still expose the real verb to the risk heuristics. MySQL executes the body of
// `/*!ver ... */` with inner `/* */` treated as whitespace, so
// `/*!40000 /* c */ DROP TABLE y */` really runs DROP TABLE y. The old
// non-greedy unwrap terminated at the inner `*/` and the following block-comment
// pass swallowed the DROP, leaving an empty string (verdict: allow) — a bypass.
func TestStripComments_InnerCommentDoesNotSwallowVerb(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want string // a token that must survive stripping
	}{
		{"inner comment then DROP", "/*!40000 /* c */ DROP TABLE y */", "DROP"},
		{"no-space inner then DROP", "/*!40000/**/DROP TABLE y */", "DROP"},
		{"inner comment then GRANT", "/*!40000 /*x*/ GRANT ALL ON db.* TO u */", "GRANT"},
		{"plain R2 case", "/*!32302 DROP TABLE x */", "DROP"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := StripComments(c.sql)
			if !strings.Contains(strings.ToUpper(out), c.want) {
				t.Errorf("StripComments(%q) = %q; expected it to still contain %q", c.sql, out, c.want)
			}
			if v := ParseVerb(c.sql); !strings.EqualFold(v, c.want) {
				t.Errorf("ParseVerb(%q) = %q; want %q", c.sql, v, c.want)
			}
		})
	}
}

// '#' is a MySQL line comment but a PostgreSQL operator. Treating it as a
// comment deletes whatever follows from the text the dictionary scan and the
// WHERE heuristic see, while PostgreSQL still executes it (ER2). Keeping the
// text can only make a statement look more dangerous, which is the safe
// direction for both engines.
func TestStripComments_HashDoesNotSwallowFollowingText(t *testing.T) {
	if got := StripComments("SELECT 1 #x; DROP TABLE orders"); !strings.Contains(strings.ToUpper(got), "DROP TABLE ORDERS") {
		t.Errorf("StripComments dropped the stacked command: %q", got)
	}
	// A '#' must not be able to hide the WHERE clause either.
	if NoWhere("UPDATE t SET a=1 #c\n WHERE id=2") {
		t.Error("NoWhere reported a full-table UPDATE, but a WHERE clause is present")
	}
}
