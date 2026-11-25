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

// TiDB 的特性注释 `/*T! … */`(含 `/*T![feature] … */`)和 MySQL 的 `/*!ver … */` 一样会被
// TiDB 执行。从前它被当成普通注释整段剥掉:单条语句下发的是原文,于是 `/*T! DROP TABLE x */`
// 在 TiDB 上真的删表,判定层却只看到一段空白(2026-10)。
func TestStripComments_TiDBFeatureCommentBodyIsJudged(t *testing.T) {
	for _, c := range []struct{ sql, want string }{
		{"/*T! DROP TABLE t_x */", "DROP TABLE T_X"},
		{"/*T![auto_rand] DROP TABLE t_x */", "DROP TABLE T_X"},
		{"CREATE TABLE t (id BIGINT /*T![auto_rand] AUTO_RANDOM(5) */ PRIMARY KEY)", "AUTO_RANDOM(5)"},
	} {
		out := StripComments(c.sql)
		if !strings.Contains(strings.ToUpper(out), c.want) {
			t.Errorf("StripComments(%q) = %q,应保留 %q", c.sql, out, c.want)
		}
		if strings.Contains(out, "/*") || strings.Contains(out, "[auto_rand]") {
			t.Errorf("StripComments(%q) = %q,标记应去掉", c.sql, out)
		}
	}
	if !NoWhere("/*T! DELETE FROM t_x */") {
		t.Error("/*T! DELETE FROM t_x */ 在 TiDB 上会整表删除,应判为无 WHERE")
	}
	if ParseVerb("/*T! DROP TABLE t_x */") != "DROP" {
		t.Errorf("动词应为 DROP,实际 %q", ParseVerb("/*T! DROP TABLE t_x */"))
	}
}
