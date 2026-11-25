package gateway

import "testing"

// EXPLAIN splits in two, and the split is the whole point:
//
//	EXPLAIN <stmt>          asks the planner. Executes nothing.
//	EXPLAIN ANALYZE <stmt>  RUNS <stmt> and reports what happened.
//
// The first is a read whatever it wraps; the second is exactly as dangerous as
// its inner verb. Judging both by the inner verb (what this used to do) meant a
// DBA could not get a plan for a DELETE without the approval the DELETE needs —
// the plan being the thing that decides whether to propose it. Judging both as
// reads would be far worse: EXPLAIN would become a way to run anything.

func TestPlanOnly_TellsThePlannerFromTheExecutor(t *testing.T) {
	planning := []string{
		`EXPLAIN SELECT * FROM orders`,
		`EXPLAIN DELETE FROM orders WHERE id = 1`,
		`EXPLAIN INSERT INTO a SELECT * FROM b`,
		`explain update t set x = 1`,
		`EXPLAIN VERBOSE SELECT 1`,
		`EXPLAIN (FORMAT JSON) DELETE FROM orders`,
		`EXPLAIN (COSTS, VERBOSE) UPDATE t SET x = 1`,
		`  /* comment */ EXPLAIN DELETE FROM t`,
	}
	for _, s := range planning {
		if !PlanOnly(s) {
			t.Errorf("PlanOnly(%q) = false, want true — it executes nothing", s)
		}
	}

	// Every one of these RUNS the statement it wraps.
	executing := []string{
		`EXPLAIN ANALYZE DELETE FROM orders`,
		`EXPLAIN ANALYZE INSERT INTO a SELECT * FROM b`,
		`explain analyze update t set x = 1`,
		`EXPLAIN (ANALYZE) DELETE FROM orders`,
		`EXPLAIN (ANALYZE, BUFFERS) DELETE FROM orders`,
		`EXPLAIN (BUFFERS, ANALYZE) DELETE FROM orders`,
		`EXPLAIN (analyze,verbose) TRUNCATE TABLE orders`,
		`EXPLAIN ANALYZE VERBOSE DELETE FROM t`,
	}
	for _, s := range executing {
		if PlanOnly(s) {
			t.Errorf("PlanOnly(%q) = true — this form EXECUTES the statement", s)
		}
	}

	// Not an EXPLAIN at all.
	for _, s := range []string{`SELECT 1`, `DELETE FROM t`, ``, `-- just a comment`} {
		if PlanOnly(s) {
			t.Errorf("PlanOnly(%q) = true, want false", s)
		}
	}
}

// The option list used to be stepped over wholesale. Reading it is what stops
// EXPLAIN from becoming a bypass, so it gets its own test.
func TestPlanOnly_AnalyzeHiddenInTheOptionListStillExecutes(t *testing.T) {
	if PlanOnly(`EXPLAIN (ANALYZE) DROP TABLE orders`) {
		t.Fatal("ANALYZE inside the option list must still count as executing — otherwise EXPLAIN is a way to run anything while being judged a read")
	}
	// And the inner verb is still reported, so the audit row names what was run.
	if got := ParseVerb(`EXPLAIN (ANALYZE) DROP TABLE orders`); got != "DROP" {
		t.Errorf("ParseVerb = %q, want DROP", got)
	}
}

// Oracle wraps differently: EXPLAIN PLAN [SET STATEMENT_ID = 'x'] FOR <stmt>.
func TestParseVerb_UnwrapsOracleExplainPlanFor(t *testing.T) {
	cases := map[string]string{
		`EXPLAIN PLAN FOR SELECT * FROM orders`:                              "SELECT",
		`EXPLAIN PLAN FOR DELETE FROM orders`:                                "DELETE",
		`EXPLAIN PLAN SET STATEMENT_ID = 'q3' FOR UPDATE t SET x = 1`:        "UPDATE",
		// The literal contains the word FOR — cutting there would leave nonsense.
		`EXPLAIN PLAN SET STATEMENT_ID = 'plan for q3' FOR DELETE FROM t`:    "DELETE",
	}
	for sql, want := range cases {
		if got := ParseVerb(sql); got != want {
			t.Errorf("ParseVerb(%q) = %q, want %q", sql, got, want)
		}
		if !PlanOnly(sql) {
			t.Errorf("PlanOnly(%q) = false — EXPLAIN PLAN FOR does not run the statement", sql)
		}
	}
}

// The verb is still the wrapped one either way: the console should say the
// operator explained a DELETE, not that they ran an "EXPLAIN".
func TestParseVerb_ReportsTheWrappedVerb(t *testing.T) {
	for sql, want := range map[string]string{
		`EXPLAIN DELETE FROM t`:                "DELETE",
		`EXPLAIN ANALYZE DELETE FROM t`:        "DELETE",
		`EXPLAIN (FORMAT JSON) INSERT INTO t`:  "INSERT",
	} {
		if got := ParseVerb(sql); got != want {
			t.Errorf("ParseVerb(%q) = %q, want %q", sql, got, want)
		}
	}
}

// Strict mode guards full-table mutations. A plan for one mutates nothing.
func TestNoWhere_APlanIsNotAnUnscopedMutation(t *testing.T) {
	if !NoWhere(`DELETE FROM orders`) {
		t.Fatal("fixture: a bare DELETE is the case strict mode exists for")
	}
	if NoWhere(`EXPLAIN DELETE FROM orders`) {
		t.Error("planning a full-table DELETE deletes nothing — strict mode must not fire")
	}
	// …but EXPLAIN ANALYZE really does perform it, so the guard must still fire.
	if !NoWhere(`EXPLAIN ANALYZE DELETE FROM orders`) {
		t.Error("EXPLAIN ANALYZE DELETE executes the delete — strict mode must still fire")
	}
	if !NoWhere(`EXPLAIN (ANALYZE) DELETE FROM orders`) {
		t.Error("the option-list form executes too — strict mode must still fire")
	}
}
