package gateway

import (
	"testing"

	"velagateway/internal/model"
)

type fakeStore struct {
	cmds []model.RiskCommand
	caps map[string]string // "cap|env" (lowercased) -> level
}

func (f *fakeStore) CapabilityLevel(_ int64, capability, env string) string {
	if lvl, ok := f.caps[lower(capability)+"|"+lower(env)]; ok {
		return lvl
	}
	return model.LevelAllow
}
func (f *fakeStore) RiskCommands() []model.RiskCommand { return f.cmds }

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
	store := &fakeStore{cmds: []model.RiskCommand{{Command: "DROP", Env: "PROD", Level: model.RiskHigh}}}
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
	store := &fakeStore{cmds: []model.RiskCommand{{Command: "DROP", Env: "PROD", Level: model.RiskHigh}}}
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
	store := &fakeStore{cmds: []model.RiskCommand{{Command: "DROP", Env: "PROD", Level: model.RiskHigh}}}
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
