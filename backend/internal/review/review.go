// Package review implements the 数据库规范审查 (SQL review) engine: a library of
// rules that judge whether a statement conforms to the organisation's standards,
// as opposed to whether it is DANGEROUS.
//
// That distinction is why this is not part of internal/gateway. The risk engine
// answers "may this person run this here", and its verdicts gate execution
// through the capability matrix and the high-risk dictionary. A review rule
// answers "is this change written the way we require" — a CREATE TABLE with no
// primary key is not dangerous, it is wrong, and it should be caught before a
// release reaches production rather than intercepted at the terminal. Mixing the
// two would mean either the dictionary starts holding style rules (and its
// levels stop meaning risk) or the review starts denying commands (and it stops
// being something a developer can run on their own work).
//
// The engine is pure: rules in, statements in, findings out. Nothing here reads
// the database or knows about users, which is what lets one call serve the
// manual check page, a release pipeline's review stage, and the tests.
package review

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"velagateway/internal/gateway"
	"velagateway/pkg/sqlutil"
)

// The dialect codes a rule can be scoped to. They are the FAMILIES the review
// speaks, not the engine labels a connection carries: "TiDB 5.7", "tidb-cluster"
// and "TiDB" are one dialect, and a rule written for it must fire on all three.
//
// TiDB is separate from MySQL even though it speaks the MySQL protocol (see
// gateway.engineFamily, which merges them because the WIRE protocol is what
// picks a driver). Here they differ: TiDB rejects some MySQL DDL outright and
// has its own hot-spot concerns, so a review that treated it as MySQL would pass
// statements the target database refuses.
const (
	DialectMySQL   = "mysql"
	DialectTiDB    = "tidb"
	DialectDWS     = "dws"
	DialectOracle  = "oracle"
	DialectGeneric = "generic" // an engine no dialect-specific rule targets
	DialectAll     = "all"     // a rule that applies to every dialect
)

// Dialects is the display order used by the console's dialect tabs.
var Dialects = []string{DialectMySQL, DialectTiDB, DialectDWS, DialectOracle}

// DialectFor maps a connection's engine label onto a review dialect. Unknown
// engines resolve to generic, which still runs every "all" rule — a review that
// checked nothing because the label was unfamiliar would be worse than one that
// checks only the universal rules.
func DialectFor(engine string) string {
	e := strings.ToLower(strings.TrimSpace(engine))
	switch {
	case e == "":
		return DialectGeneric
	case strings.Contains(e, "tidb"):
		return DialectTiDB
	case strings.Contains(e, "oracle"):
		return DialectOracle
	case strings.Contains(e, "dws"), strings.Contains(e, "gauss"):
		return DialectDWS
	case strings.Contains(e, "mysql"), strings.Contains(e, "mariadb"), strings.Contains(e, "polardb"):
		return DialectMySQL
	}
	return DialectGeneric
}

// Rule is one library entry as the engine sees it (model.SQLReviewRule without
// its storage concerns).
type Rule struct {
	Code     string `json:"code"`
	Name     string `json:"name"`
	Dialect  string `json:"dialect"` // "all", or a comma-separated list of dialect codes
	Category string `json:"category"`
	Level    string `json:"level"` // error|warn|info
	Kind     string `json:"kind"`  // builtin|regex
	Message  string `json:"message"`
	Params   string `json:"params"` // JSON knobs; "" = built-in defaults
	Enabled  bool   `json:"enabled"`
}

// AppliesTo reports whether the rule is in scope for a dialect.
func (r Rule) AppliesTo(dialect string) bool {
	d := strings.TrimSpace(strings.ToLower(r.Dialect))
	if d == "" || d == DialectAll {
		return true
	}
	for _, part := range strings.Split(d, ",") {
		if strings.TrimSpace(part) == dialect {
			return true
		}
	}
	return false
}

// Finding is one rule firing on one statement.
type Finding struct {
	Code     string `json:"code"`
	Name     string `json:"name"`
	Level    string `json:"level"`
	Category string `json:"category"`
	Stmt     int    `json:"stmt"` // 1-based statement number within the script
	Line     int    `json:"line"` // 1-based line the statement starts on
	SQL      string `json:"sql"`  // bounded, credential-masked excerpt
	Message  string `json:"message"`
}

// Result is the whole review of one script.
type Result struct {
	Dialect    string    `json:"dialect"`
	Statements int       `json:"statements"`
	Errors     int       `json:"errors"`
	Warnings   int       `json:"warnings"`
	Infos      int       `json:"infos"`
	Findings   []Finding `json:"findings"`
	// Passed means no error-level finding. It is NOT "no findings": a release
	// stopped by every warning would be a release nobody could ever ship, so the
	// gate is the level an operator deliberately assigned to the rule.
	Passed bool `json:"passed"`
}

// maxExcerpt bounds the statement text carried on a finding. Findings are stored
// on a release stage (MEDIUMTEXT) and rendered in a list; a 6MB migration would
// otherwise put its whole body in there once per finding.
const maxExcerpt = 240

// Check runs every enabled, in-scope rule over each statement of sql.
//
// Statements are split by the same splitter the gateway uses, so what the review
// judges is what the executor would run. Reviewing the raw text instead would
// miss a rule in the tail of a batch and report positions nobody can map back.
func Check(dialect, sql string, rules []Rule) Result {
	if dialect == "" {
		dialect = DialectGeneric
	}
	res := Result{Dialect: dialect, Findings: []Finding{}}
	active := make([]Rule, 0, len(rules))
	for _, r := range rules {
		if r.Enabled && r.AppliesTo(dialect) {
			active = append(active, r)
		}
	}
	stmts := splitWithLines(sql)
	res.Statements = len(stmts)
	for _, st := range stmts {
		for _, r := range active {
			for _, msg := range fire(r, st, dialect) {
				res.Findings = append(res.Findings, Finding{
					Code: r.Code, Name: r.Name, Level: r.Level, Category: r.Category,
					Stmt: st.index, Line: st.line, SQL: excerpt(st.raw), Message: msg,
				})
			}
		}
	}
	res.Findings = append(res.Findings, scriptFindings(active, stmts, sql)...)
	for _, f := range res.Findings {
		switch f.Level {
		case LevelError:
			res.Errors++
		case LevelInfo:
			res.Infos++
		default:
			res.Warnings++
		}
	}
	res.Passed = res.Errors == 0
	return res
}

// Levels a finding can carry (mirrors model.Review*).
const (
	LevelError = "error"
	LevelWarn  = "warn"
	LevelInfo  = "info"
)

// fire runs one rule against one statement and returns its messages (usually
// none or one). A regex rule matches its pattern; a builtin rule runs the
// checker registered under its code.
func fire(r Rule, st *stmt, dialect string) []string {
	p := parseParams(r.Params)
	if r.Kind == "regex" {
		return regexRule(r, p, st)
	}
	// A nil checker is a SCRIPT-level rule (scriptFindings owns it), not a
	// missing implementation — see the registry.
	c, ok := builtinChecker(r.Code)
	if !ok || c == nil {
		return nil
	}
	msgs := c(st, p, dialect)
	// A rule's configured Message overrides the checker's wording so an operator
	// can phrase the standard in their own terms; the checker's detail is appended
	// because it names the actual column/table, which the phrasing cannot.
	if custom := strings.TrimSpace(r.Message); custom != "" {
		out := make([]string, 0, len(msgs))
		for _, m := range msgs {
			if m == "" || m == custom {
				out = append(out, custom)
			} else {
				out = append(out, custom+" — "+m)
			}
		}
		return out
	}
	return msgs
}

// regexRule applies an operator-written pattern to the statement. An invalid
// pattern reports ITSELF as a finding rather than being skipped: a rule that
// silently never fires is indistinguishable from one that always passes, and the
// person who wrote it would never learn it is dead.
func regexRule(r Rule, p params, st *stmt) []string {
	pat := p.str("pattern", "")
	if pat == "" {
		return nil
	}
	re, err := regexp.Compile("(?is)" + pat)
	if err != nil {
		return []string{fmt.Sprintf("规则 %s 的正则无效: %v", r.Code, err)}
	}
	target := st.sql
	if p.str("scope", "") == "raw" {
		target = st.raw
	}
	hit := re.MatchString(target)
	// forbid (default): a match is a violation. require: the ABSENCE is.
	if p.str("mode", "forbid") == "require" {
		if !hit {
			return []string{"语句未匹配要求的模式: " + pat}
		}
		return nil
	}
	if hit {
		return []string{"语句命中禁止的模式: " + pat}
	}
	return nil
}

func excerpt(s string) string {
	s = strings.TrimSpace(sqlutil.RedactSecrets(s))
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= maxExcerpt {
		return s
	}
	return string(r[:maxExcerpt]) + "…"
}

// ---------------------------------------------------------------- statements

// stmt is one statement plus what the checkers need in order to read it without
// re-parsing: comments stripped, string literals blanked (so a WHERE inside a
// quoted value is never mistaken for a clause) and the leading verb resolved.
type stmt struct {
	index  int    // 1-based position in the script
	line   int    // 1-based line the statement starts on
	raw    string // as written, for the excerpt
	sql    string // comments stripped
	masked string // sql with the CONTENT of quoted literals replaced by spaces
	upper  string // upper-cased masked
	verb   string // SELECT / INSERT / CREATE / …
}

func splitWithLines(sql string) []*stmt {
	parts := sqlutil.SplitStatements(sql)
	out := make([]*stmt, 0, len(parts))
	search := 0
	for i, p := range parts {
		line := 1
		// Locate the statement in the original text to number its line. The
		// splitter normalises whitespace, so match on the first token rather than
		// on the whole statement, which would rarely be found verbatim.
		if head := firstToken(p); head != "" {
			if idx := indexFrom(sql, head, search); idx >= 0 {
				line = 1 + strings.Count(sql[:idx], "\n")
				search = idx + len(head)
			}
		}
		clean := gateway.StripComments(p)
		masked := maskLiterals(clean)
		out = append(out, &stmt{
			index: i + 1, line: line, raw: p, sql: strings.TrimSpace(clean),
			masked: masked, upper: strings.ToUpper(masked), verb: gateway.ParseVerb(clean),
		})
	}
	return out
}

func firstToken(s string) string {
	f := strings.Fields(strings.TrimSpace(s))
	if len(f) == 0 {
		return ""
	}
	return f[0]
}

func indexFrom(hay, needle string, from int) int {
	if from >= len(hay) {
		return -1
	}
	i := strings.Index(hay[from:], needle)
	if i < 0 {
		return -1
	}
	return from + i
}

// maskLiterals blanks the CONTENT of quoted strings while preserving length and
// the quote characters, so `WHERE note = 'delete from x'` can never be read as a
// DELETE, and offsets computed on the masked text still line up with the source.
func maskLiterals(s string) string {
	b := []byte(s)
	var quote byte
	for i := 0; i < len(b); i++ {
		c := b[i]
		if quote != 0 {
			if c == '\\' && i+1 < len(b) {
				b[i] = ' '
				b[i+1] = ' '
				i++
				continue
			}
			if c == quote {
				quote = 0
				continue
			}
			if c != '\n' {
				b[i] = ' '
			}
			continue
		}
		if c == '\'' || c == '"' {
			quote = c
		}
	}
	return string(b)
}

// ---------------------------------------------------------------- params

type params map[string]any

func parseParams(s string) params {
	p := params{}
	if strings.TrimSpace(s) == "" {
		return p
	}
	_ = json.Unmarshal([]byte(s), &p)
	return p
}

func (p params) str(key, def string) string {
	if v, ok := p[key]; ok {
		if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
			return s
		}
	}
	return def
}

// boolean reads a flag knob. An absent key is the default, never false —
// "not configured" and "configured off" must not collapse into one another.
func (p params) boolean(key string, def bool) bool {
	v, ok := p[key]
	if !ok {
		return def
	}
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return strings.EqualFold(strings.TrimSpace(t), "true")
	case float64:
		return t != 0
	}
	return def
}

func (p params) num(key string, def int) int {
	if v, ok := p[key]; ok {
		switch n := v.(type) {
		case float64:
			return int(n)
		case string:
			if i, err := strconv.Atoi(strings.TrimSpace(n)); err == nil {
				return i
			}
		}
	}
	return def
}

func (p params) list(key string, def []string) []string {
	v, ok := p[key]
	if !ok {
		return def
	}
	switch t := v.(type) {
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s, ok := e.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.ToLower(strings.TrimSpace(s)))
			}
		}
		if len(out) == 0 {
			return def
		}
		return out
	case string:
		out := []string{}
		for _, s := range strings.Split(t, ",") {
			if s = strings.ToLower(strings.TrimSpace(s)); s != "" {
				out = append(out, s)
			}
		}
		if len(out) == 0 {
			return def
		}
		return out
	}
	return def
}
