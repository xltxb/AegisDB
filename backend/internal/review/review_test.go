package review

import (
	"strings"
	"testing"
)

// allRules turns the shipped catalog into enabled runtime rules, which is the
// state a fresh install runs in.
func allRules() []Rule {
	out := make([]Rule, 0, len(Builtins))
	for _, b := range Builtins {
		out = append(out, Rule{
			Code: b.Code, Name: b.Name, Dialect: b.Dialect, Category: b.Category,
			Level: b.Level, Kind: "builtin", Params: b.Params, Enabled: true,
		})
	}
	return out
}

func codes(r Result) map[string]bool {
	m := map[string]bool{}
	for _, f := range r.Findings {
		m[f.Code] = true
	}
	return m
}

// TestBuiltinRegistryComplete is the guard that keeps the catalog and the
// implementations in step. A catalog entry with no checker is a rule that is
// listed in the console, looks enabled, and silently checks nothing — the exact
// failure this package exists to prevent elsewhere.
func TestBuiltinRegistryComplete(t *testing.T) {
	for _, b := range Builtins {
		if _, ok := registry[b.Code]; !ok {
			t.Errorf("catalog rule %s has no checker", b.Code)
		}
	}
	seen := map[string]bool{}
	for _, b := range Builtins {
		if seen[b.Code] {
			t.Errorf("duplicate catalog code %s", b.Code)
		}
		seen[b.Code] = true
	}
	for code := range registry {
		if !seen[code] {
			t.Errorf("checker %s is registered but not in the catalog", code)
		}
	}
}

func TestRequireWhereAndLimit(t *testing.T) {
	r := Check(DialectMySQL, "DELETE FROM tbl_order;", allRules())
	if !codes(r)["dml.require.where"] {
		t.Fatal("DELETE without WHERE should be flagged")
	}
	if r.Passed {
		t.Fatal("an error-level finding must fail the review")
	}
	// A WHERE inside a string literal is not a WHERE clause.
	r = Check(DialectMySQL, "DELETE FROM tbl_order WHERE id = 1 LIMIT 100;", allRules())
	if codes(r)["dml.require.where"] || codes(r)["dml.require.limit"] {
		t.Fatalf("scoped DELETE should pass, got %+v", r.Findings)
	}
	r = Check(DialectMySQL, "UPDATE t SET note = 'delete from t' WHERE id=1 LIMIT 1;", allRules())
	if codes(r)["dml.require.where"] {
		t.Fatal("literal content must not be parsed as SQL")
	}
}

func TestCreateTableRules(t *testing.T) {
	sql := "CREATE TABLE Orders (id INT, amount FLOAT) ENGINE=MyISAM DEFAULT CHARSET=utf8;"
	got := codes(Check(DialectMySQL, sql, allRules()))
	for _, want := range []string{
		"ddl.require.primary.key",   // no PK
		"ddl.require.table.comment", // no table comment
		"ddl.require.col.comment",   // columns have no comment
		"ddl.forbid.column.type",    // FLOAT
		"naming.table.pattern",      // upper-case table name
		"mysql.require.innodb",      // MyISAM
		"mysql.require.utf8mb4",     // utf8
	} {
		if !got[want] {
			t.Errorf("expected rule %s to fire on %q", want, sql)
		}
	}
	// A conforming table trips none of the structural rules.
	ok := "CREATE TABLE tbl_order (\n" +
		"  id BIGINT NOT NULL AUTO_INCREMENT COMMENT '主键',\n" +
		"  amount DECIMAL(12,2) NOT NULL DEFAULT 0 COMMENT '金额',\n" +
		"  PRIMARY KEY (id),\n" +
		"  KEY idx_amount (amount)\n" +
		") ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='订单';"
	got = codes(Check(DialectMySQL, ok, allRules()))
	for bad := range got {
		if strings.HasPrefix(bad, "ddl.") || strings.HasPrefix(bad, "mysql.") || strings.HasPrefix(bad, "naming.") {
			t.Errorf("conforming table should not trip %s", bad)
		}
	}
}

func TestAddColumnNotNullWithoutDefault(t *testing.T) {
	got := codes(Check(DialectMySQL, "ALTER TABLE tbl_order ADD COLUMN memo VARCHAR(64) NOT NULL COMMENT '备注';", allRules()))
	if !got["ddl.addcol.notnull.nodefault"] {
		t.Fatal("NOT NULL without DEFAULT should be flagged")
	}
	got = codes(Check(DialectMySQL, "ALTER TABLE tbl_order ADD COLUMN memo VARCHAR(64) NOT NULL DEFAULT '' COMMENT '备注';", allRules()))
	if got["ddl.addcol.notnull.nodefault"] {
		t.Fatal("a DEFAULT satisfies the rule")
	}
}

func TestDialectScoping(t *testing.T) {
	// TiDB rejects the foreign key; MySQL does not carry that rule at all.
	fk := "CREATE TABLE tbl_a (id BIGINT NOT NULL COMMENT 'x', pid BIGINT NOT NULL COMMENT 'y', PRIMARY KEY(id), FOREIGN KEY (pid) REFERENCES tbl_b(id)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='a';"
	if !codes(Check(DialectTiDB, fk, allRules()))["tidb.forbid.foreign.key"] {
		t.Error("TiDB should reject FOREIGN KEY")
	}
	if codes(Check(DialectMySQL, fk, allRules()))["tidb.forbid.foreign.key"] {
		t.Error("a TiDB-only rule must not fire on MySQL")
	}
	// DWS needs a distribution key; Oracle wants VARCHAR2.
	if !codes(Check(DialectDWS, "CREATE TABLE t1 (id INT NOT NULL, PRIMARY KEY(id));", allRules()))["dws.require.distribute.by"] {
		t.Error("DWS should require DISTRIBUTE BY")
	}
	if codes(Check(DialectMySQL, "CREATE TABLE t1 (id INT NOT NULL, PRIMARY KEY(id));", allRules()))["dws.require.distribute.by"] {
		t.Error("a DWS-only rule must not fire on MySQL")
	}
	if !codes(Check(DialectOracle, "CREATE TABLE T1 (ID NUMBER(10) NOT NULL, NAME VARCHAR(20), PRIMARY KEY(ID));", allRules()))["oracle.prefer.varchar2"] {
		t.Error("Oracle should prefer VARCHAR2")
	}
}

func TestSecurityAndPerf(t *testing.T) {
	got := codes(Check(DialectMySQL, "CREATE USER 'app'@'%' IDENTIFIED BY 'S3cr3t!';", allRules()))
	if !got["security.forbid.plain.password"] {
		t.Error("inline password should be flagged")
	}
	for _, f := range Check(DialectMySQL, "CREATE USER 'app'@'%' IDENTIFIED BY 'S3cr3t!';", allRules()).Findings {
		if strings.Contains(f.SQL, "S3cr3t") || strings.Contains(f.Message, "S3cr3t") {
			t.Error("a finding must never carry the password it is reporting")
		}
	}
	got = codes(Check(DialectMySQL, "SELECT id FROM tbl_order WHERE name LIKE '%vela' AND DATE(created_at) = '2026-08-01';", allRules()))
	if !got["perf.forbid.leading.wildcard"] {
		t.Error("leading wildcard should be flagged")
	}
	if !got["perf.forbid.func.on.column"] {
		t.Error("function on a column should be flagged")
	}
}

func TestScriptLevelAlterMerge(t *testing.T) {
	sql := "ALTER TABLE tbl_order ADD COLUMN a INT NOT NULL DEFAULT 0 COMMENT 'a';\n" +
		"ALTER TABLE tbl_order ADD COLUMN b INT NOT NULL DEFAULT 0 COMMENT 'b';"
	r := Check(DialectMySQL, sql, allRules())
	if !codes(r)["ddl.alter.merge"] {
		t.Fatal("two ALTERs on one table should suggest merging")
	}
	if r.Statements != 2 {
		t.Fatalf("expected 2 statements, got %d", r.Statements)
	}
}

func TestDisabledAndLevelOverride(t *testing.T) {
	rules := allRules()
	for i := range rules {
		if rules[i].Code == "dml.require.where" {
			rules[i].Enabled = false
		}
	}
	r := Check(DialectMySQL, "DELETE FROM tbl_order;", rules)
	if codes(r)["dml.require.where"] {
		t.Fatal("a disabled rule must not fire")
	}
	// Lowering the level lets the same statement pass the gate while still being
	// reported — the distinction the pipeline's failOn setting rides on.
	rules = allRules()
	for i := range rules {
		if rules[i].Code == "dml.require.where" {
			rules[i].Level = LevelWarn
		}
	}
	r = Check(DialectMySQL, "DELETE FROM tbl_order;", rules)
	if !r.Passed || r.Warnings == 0 {
		t.Fatalf("warn-level finding should pass the gate: %+v", r)
	}
}

func TestRegexRule(t *testing.T) {
	rules := []Rule{{
		Code: "custom.no.hint", Name: "禁止 SQL_NO_CACHE", Dialect: DialectAll, Category: CatPerf,
		Level: LevelWarn, Kind: "regex", Params: `{"pattern":"SQL_NO_CACHE"}`, Enabled: true,
	}}
	if len(Check(DialectMySQL, "SELECT SQL_NO_CACHE * FROM t;", rules).Findings) != 1 {
		t.Fatal("regex rule should fire")
	}
	if len(Check(DialectMySQL, "SELECT 1;", rules).Findings) != 0 {
		t.Fatal("regex rule should not fire on a clean statement")
	}
	// An invalid pattern reports itself rather than passing silently.
	rules[0].Params = `{"pattern":"("}`
	if len(Check(DialectMySQL, "SELECT 1;", rules).Findings) != 1 {
		t.Fatal("an invalid pattern must surface as a finding")
	}
}

func TestDialectFor(t *testing.T) {
	cases := map[string]string{
		"MySQL 8.0": DialectMySQL, "TiDB 5.7": DialectTiDB, "tidb-cluster": DialectTiDB,
		"GaussDB(DWS)": DialectDWS, "Oracle 19c": DialectOracle, "redis": DialectGeneric, "": DialectGeneric,
	}
	for engine, want := range cases {
		if got := DialectFor(engine); got != want {
			t.Errorf("DialectFor(%q) = %s, want %s", engine, got, want)
		}
	}
}
