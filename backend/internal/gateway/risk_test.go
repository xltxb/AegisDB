package gateway

import (
	"errors"
	"testing"

	"velagateway/internal/model"
)

type fakeStore struct {
	cmds []model.RiskCommand
	caps map[string]string // "cap|env" (lowercased) -> level
}

func (f *fakeStore) CapabilityLevel(_ int64, capability, env string) (string, error) {
	if lvl, ok := f.caps[lower(capability)+"|"+lower(env)]; ok {
		return lvl, nil
	}
	return model.LevelAllow, nil
}
func (f *fakeStore) RiskCommands() ([]model.RiskCommand, error) { return f.cmds, nil }

func lower(s string) string { // tiny local helper to keep the fake honest
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

func TestMapVerbToCapability_CaseInsensitive(t *testing.T) {
	cases := map[string]string{
		"drop": "ddl", "DROP": "ddl", "Drop": "ddl",
		"select": "select", "SELECT": "select",
		"insert": "write", "Grant": "grant",
	}
	for verb, want := range cases {
		if got := MapVerbToCapability(verb); got != want {
			t.Errorf("MapVerbToCapability(%q) = %q, want %q", verb, got, want)
		}
	}
}

// The dictionary rule stored with UPPER-case env + command must match a
// lower-case connection env and any-case SQL. Regression for env being compared
// with '!=' (case-sensitive) which silently skipped the rule.
func TestEvaluate_DictionaryMatchCaseInsensitive(t *testing.T) {
	store := &fakeStore{cmds: []model.RiskCommand{{Command: "DROP", TierCode: "PROD", Level: model.RiskHigh}}}
	e := NewRiskEngine(store, false)

	for _, sql := range []string{"drop table x", "DROP TABLE x", "DrOp TaBlE x"} {
		v := e.Evaluate(1, "prod", sql) // conn env lower-case
		if v.Action != ActionApprove || v.Risk != model.RiskHigh {
			t.Errorf("Evaluate(%q) = %+v; want approve/high", sql, v)
		}
	}
	// no rule for dev → allowed
	if v := e.Evaluate(1, "dev", "drop table x"); v.Action != ActionAllow {
		t.Errorf("DROP on dev (no rule) should allow, got %+v", v)
	}
}

// A high-risk command hidden inside a MySQL executable comment /*! ... */ is run
// by the server, so the engine must NOT treat it as a harmless read. Regression
// for StripComments deleting the comment body and letting DROP fall to allow.
func TestEvaluate_ExecutableCommentNotBypassed(t *testing.T) {
	store := &fakeStore{cmds: []model.RiskCommand{{Command: "DROP", TierCode: "PROD", Level: model.RiskHigh}}}
	e := NewRiskEngine(store, false)

	for _, sql := range []string{
		"/*!32302 DROP TABLE users */",
		"/*! DROP TABLE users */",
		"/*!40000 drop table users */",
	} {
		v := e.Evaluate(1, "prod", sql)
		if v.Action == ActionAllow {
			t.Errorf("Evaluate(%q) = %+v; executable-comment DROP must not be allowed", sql, v)
		}
	}
}

// PostgreSQL EXPLAIN ANALYZE actually runs the wrapped statement, so a wrapped
// DROP/DELETE must be judged on its real verb, not treated as a read. Regression
// for ParseVerb returning EXPLAIN → mapped to select → allow.
func TestEvaluate_ExplainAnalyzeUsesRealVerb(t *testing.T) {
	store := &fakeStore{cmds: []model.RiskCommand{{Command: "DROP", TierCode: "PROD", Level: model.RiskHigh}}}
	e := NewRiskEngine(store, true) // strict mode on (catches no-WHERE DML too)

	// dictionary-matched DROP behind EXPLAIN ANALYZE
	if v := e.Evaluate(1, "prod", "EXPLAIN ANALYZE DROP TABLE users"); v.Action == ActionAllow {
		t.Errorf("EXPLAIN ANALYZE DROP must not be allowed, got %+v", v)
	}
	// strict-mode no-WHERE DELETE behind EXPLAIN (ANALYZE)
	if v := e.Evaluate(1, "prod", "EXPLAIN (ANALYZE) DELETE FROM users"); v.Action == ActionAllow {
		t.Errorf("EXPLAIN (ANALYZE) DELETE without WHERE must not be allowed, got %+v", v)
	}
	// plain read EXPLAIN SELECT stays allowed
	if v := e.Evaluate(1, "prod", "EXPLAIN SELECT * FROM users"); v.Action != ActionAllow {
		t.Errorf("EXPLAIN SELECT should stay allowed, got %+v", v)
	}
}

// ED3: the store cannot report "no rule configured" and "the query failed" with
// the same value. Both layers of the gate read from the database, so when the
// database is briefly unreachable — connection killed, pool exhausted, lock
// timeout — treating the error as "nothing configured" turns the capability
// matrix and the risk dictionary off simultaneously and lets DROP TABLE through
// on PROD. A gate that cannot be consulted has to refuse.
type brokenStore struct{ err error }

func (b *brokenStore) CapabilityLevel(int64, string, string) (string, error) {
	return "", b.err
}
func (b *brokenStore) RiskCommands() ([]model.RiskCommand, error) { return nil, b.err }

func TestEvaluate_FailsClosedWhenStoreErrors(t *testing.T) {
	e := NewRiskEngine(&brokenStore{err: errors.New("driver: bad connection")}, false)

	v := e.Evaluate(1, "prod", "DROP TABLE orders")
	if v.Action == ActionAllow {
		t.Errorf("risk lookup failed but the command was allowed: %+v", v)
	}
}

// ER8: strict mode's last line of defence is "a DELETE/UPDATE with no WHERE is a
// full-table write". The check looks for the word `where` anywhere in the
// statement, so a value that merely CONTAINS it satisfies the check — an earlier
// round fixed the identifier case (`elsewhere`) with a word boundary, but a
// string literal is the other half of the same hole and defeats it exactly.
func TestNoWhere_NotFooledByWhereInsideALiteral(t *testing.T) {
	fullTable := []string{
		`UPDATE users SET note='where'`,
		`UPDATE users SET note = "where"`,
		`DELETE FROM users -- where`,
		`UPDATE users SET note='... where id=1 ...'`,
	}
	for _, sql := range fullTable {
		if !NoWhere(sql) {
			t.Errorf("NoWhere(%q) = false; this is a full-table write and strict mode must catch it", sql)
		}
	}
	// A real WHERE clause must still register.
	for _, sql := range []string{
		`UPDATE users SET note='x' WHERE id=1`,
		`DELETE FROM users WHERE id=1`,
		`UPDATE users SET note='where' WHERE id=1`,
	} {
		if NoWhere(sql) {
			t.Errorf("NoWhere(%q) = true; the statement has a real WHERE clause", sql)
		}
	}
}

// ER9: PostgreSQL lets a CTE carry the mutation — `WITH d AS (DELETE ... RETURNING *)
// SELECT * FROM d` really deletes rows. The leading verb is WITH, which sits in
// the read set, so such a statement was routed down the query path, recorded as
// a read, and skipped strict mode's full-table guard entirely. What decides is
// whether the statement mutates, not which keyword happens to come first.
func TestDataModifyingCTE_IsAWriteAndStrictModeSeesIt(t *testing.T) {
	mutating := []string{
		`WITH d AS (DELETE FROM users RETURNING *) SELECT * FROM d`,
		`WITH u AS (UPDATE users SET tier='x' RETURNING id) SELECT * FROM u`,
		`WITH i AS (INSERT INTO t SELECT * FROM s RETURNING *) SELECT count(*) FROM i`,
	}
	for _, sql := range mutating {
		if IsRead(sql) {
			t.Errorf("IsRead(%q) = true; this statement mutates data", sql)
		}
	}
	// A no-WHERE mutation inside a CTE is still a full-table write.
	if !NoWhere(`WITH d AS (DELETE FROM users RETURNING *) SELECT * FROM d`) {
		t.Error("a CTE deleting every row must be reported as a full-table write")
	}
	if NoWhere(`WITH d AS (DELETE FROM users WHERE id=1 RETURNING *) SELECT * FROM d`) {
		t.Error("the CTE's DELETE has a WHERE clause")
	}
	// A read-only CTE stays a read.
	if !IsRead(`WITH d AS (SELECT * FROM users) SELECT * FROM d`) {
		t.Error("a read-only CTE must stay a read")
	}
}
