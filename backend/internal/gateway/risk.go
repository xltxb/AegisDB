// Package gateway implements the risk-judgement engine and the proxy executor.
package gateway

import (
	"regexp"
	"strings"
	"sync/atomic"

	"velagateway/internal/model"
)

// Action results of a verdict.
const (
	ActionAllow   = "allow"
	ActionApprove = "approve"
	ActionDeny    = "deny"
)

// Verdict is the outcome of the three-layer risk evaluation.
type Verdict struct {
	Action  string // allow|approve|deny
	Risk    string // high|mid|low
	Rule    string
	Command string
}

// RequiresApproval reports whether the command must go through approval.
func (v Verdict) RequiresApproval() bool { return v.Action == ActionApprove }

// Store supplies the engine with capability-matrix and risk-dictionary data.
type Store interface {
	// CapabilityLevel returns allow|approve|deny for role×capability×env (default allow).
	CapabilityLevel(roleID int64, capability, env string) string
	// RiskCommands returns the full high-risk dictionary (all envs).
	RiskCommands() []model.RiskCommand
}

// RiskEngine evaluates commands against the three layers + strict mode.
type RiskEngine struct {
	store  Store
	strict atomic.Bool // toggled at runtime from the settings API; read on every eval
}

func NewRiskEngine(store Store, strict bool) *RiskEngine {
	e := &RiskEngine{store: store}
	e.strict.Store(strict)
	return e
}

func (e *RiskEngine) SetStrict(v bool) { e.strict.Store(v) }

var verbRe = regexp.MustCompile(`(?i)^\s*([a-z_]+)`)

// StripComments neutralizes SQL comments before the risk heuristics run.
//
// MySQL *executable* comments `/*!ver ... */` are actually run by the server, so
// their body is KEPT (only the `/*!ver` and matching `*/` markers are dropped) —
// deleting the whole thing would hide a real DROP and let it fall through to
// allow. Ordinary line (`--`, `#`) and block (`/* */`) comments are removed,
// INCLUDING ones nested inside an executable comment (MySQL treats those as
// whitespace within the executed body).
//
// It is a single left-to-right scan so it never loses non-comment text. The
// earlier regex version unwrapped `/*! */` with a non-greedy match that stopped
// at the first inner `*/`, then a block-comment pass swallowed the real verb —
// e.g. `/*!40000 /* c */ DROP TABLE y */` collapsed to "" (allow) while MySQL ran
// DROP TABLE y (A3). Quoted literals/identifiers are copied verbatim; stripping
// inside a string only ever makes a statement look more dangerous (safe
// direction), and the dictionary scan backs the heuristic regardless.
func StripComments(sql string) string {
	var b strings.Builder
	n := len(sql)
	execDepth := 0 // >0 while inside /*!ver ... */: keep the body, drop the markers
	for i := 0; i < n; i++ {
		c := sql[i]
		switch {
		case c == '\'' || c == '"' || c == '`': // quoted literal / identifier — copy verbatim
			q := c
			b.WriteByte(c)
			i++
			for i < n {
				d := sql[i]
				b.WriteByte(d)
				if d == q {
					if i+1 < n && sql[i+1] == q { // doubled quote = escaped quote
						i++
						b.WriteByte(sql[i])
					} else {
						break // closing quote
					}
				}
				i++
			}
		case c == '-' && i+1 < n && sql[i+1] == '-': // "--" line comment
			for i < n && sql[i] != '\n' {
				i++
			}
			b.WriteByte(' ')
		case c == '#': // MySQL "#" line comment
			for i < n && sql[i] != '\n' {
				i++
			}
			b.WriteByte(' ')
		case c == '/' && i+1 < n && sql[i+1] == '*':
			if i+2 < n && sql[i+2] == '!' { // executable comment opener: drop "/*!<digits>", keep body
				i += 3
				for i < n && sql[i] >= '0' && sql[i] <= '9' {
					i++
				}
				i-- // the loop's i++ lands on the first body byte
				execDepth++
				b.WriteByte(' ')
			} else { // plain block comment: remove up to the matching "*/"
				j := i + 2
				for j+1 < n && !(sql[j] == '*' && sql[j+1] == '/') {
					j++
				}
				i = j + 1 // skip past "*/"
				b.WriteByte(' ')
			}
		case c == '*' && i+1 < n && sql[i+1] == '/' && execDepth > 0: // executable-comment closer
			execDepth--
			i++ // skip '/'
			b.WriteByte(' ')
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// ParseVerb returns the effective leading SQL keyword in upper-case, ignoring
// leading comments/whitespace. A leading PostgreSQL `EXPLAIN [ANALYZE|VERBOSE|
// (options)]` prefix is unwrapped so the real verb is judged — EXPLAIN ANALYZE
// actually executes the wrapped statement.
func ParseVerb(sql string) string {
	s := strings.TrimSpace(StripComments(sql))
	verb := firstWord(s)
	if strings.EqualFold(verb, "EXPLAIN") {
		rest := strings.TrimSpace(s[len(verb):])
		for {
			w := firstWord(rest)
			if up := strings.ToUpper(w); up == "ANALYZE" || up == "VERBOSE" {
				rest = strings.TrimSpace(rest[len(w):])
				continue
			}
			break
		}
		if strings.HasPrefix(rest, "(") { // EXPLAIN (ANALYZE, ...) option list
			if i := strings.Index(rest, ")"); i >= 0 {
				rest = strings.TrimSpace(rest[i+1:])
			}
		}
		if inner := firstWord(rest); inner != "" {
			verb = inner
		}
	}
	return strings.ToUpper(verb)
}

// firstWord returns the leading [a-z_]+ token (case-insensitive) or "".
func firstWord(s string) string {
	m := verbRe.FindStringSubmatch(s)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// NoWhere reports a DELETE/UPDATE statement that lacks a WHERE clause. Comments
// are stripped first so a `/* where */` decoy can't mask a full-table mutation.
// WHERE is matched as a whole word, so an identifier like `elsewhere`/`nowhere`
// no longer masquerades as a WHERE clause (B7).
func NoWhere(sql string) bool {
	verb := strings.ToLower(ParseVerb(sql))
	if verb != "delete" && verb != "update" {
		return false
	}
	return !whereRe.MatchString(StripComments(sql))
}

var whereRe = regexp.MustCompile(`(?i)\bwhere\b`)

// readVerbs are the non-mutating leading verbs. Bare EXPLAIN only plans (no
// execution), so it counts as a read; EXPLAIN ANALYZE is handled by IsRead via
// the wrapped verb.
var readVerbs = map[string]bool{
	"SELECT": true, "SHOW": true, "DESC": true, "DESCRIBE": true, "WITH": true, "EXPLAIN": true,
}

// IsRead reports whether sql is a non-mutating statement, so callers can route it
// to a query path and classify its audit result. It keys on the effective
// executed verb (ParseVerb unwraps EXPLAIN [ANALYZE] to the wrapped verb), so
// `EXPLAIN ANALYZE DELETE ...` — which really runs the DELETE on PostgreSQL — is
// correctly treated as a write rather than a read (B1). `EXPLAIN <DML>` without
// ANALYZE resolves to the DML verb and is conservatively treated as a write,
// which is the safe direction for audit classification.
func IsRead(sql string) bool {
	return readVerbs[ParseVerb(sql)]
}

// MapVerbToCapability maps a SQL verb to a capability-matrix dimension
// (case-insensitive).
func MapVerbToCapability(verb string) string {
	switch strings.ToUpper(strings.TrimSpace(verb)) {
	case "SELECT", "SHOW", "DESC", "DESCRIBE", "EXPLAIN", "USE":
		return "select"
	case "INSERT", "UPDATE", "DELETE", "REPLACE", "MERGE":
		return "write"
	case "DROP", "ALTER", "TRUNCATE", "RENAME", "CREATE":
		return "ddl"
	case "GRANT", "REVOKE":
		return "grant"
	case "":
		// No leading SQL keyword (blank line, bare number, comment-only): not a
		// mutating operation, so treat it as read-level rather than gating it
		// behind the write-approval path. Any embedded high-risk command is still
		// caught by the dictionary scan in Evaluate (e.g. "1; DROP TABLE x").
		return "select"
	default:
		return "write"
	}
}

// matchCommand finds the first dictionary command (for env) appearing in the SQL.
func (e *RiskEngine) matchCommand(sql, env string) (string, string) {
	names := make([]string, 0)
	levelByName := map[string]string{}
	for _, rc := range e.store.RiskCommands() {
		if !strings.EqualFold(rc.Env, env) { // env match is case-insensitive
			continue
		}
		names = append(names, regexp.QuoteMeta(rc.Command))
		levelByName[strings.ToUpper(rc.Command)] = rc.Level
	}
	if len(names) == 0 {
		return "", model.RiskOff
	}
	re := regexp.MustCompile(`(?i)\b(` + strings.Join(names, "|") + `)\b`)
	m := re.FindString(StripComments(sql))
	if m == "" {
		return "", model.RiskOff
	}
	name := strings.ToUpper(m)
	return name, levelByName[name]
}

// ScanStatement judges a single statement by the dictionary (for env) + strict
// mode only — used by SQL script scanning. Returns (command, risk, noWhere)
// where risk is high|mid|safe.
func (e *RiskEngine) ScanStatement(env, sql string) (string, string, bool) {
	matched, lvl := e.matchCommand(sql, env)
	verb := matched
	if verb == "" {
		verb = ParseVerb(sql)
	}
	noWhere := NoWhere(sql)
	switch {
	case lvl == model.RiskHigh:
		return verb, "high", noWhere
	case e.strict.Load() && noWhere:
		return verb, "high", noWhere
	case lvl == model.RiskMid:
		return verb, "mid", noWhere
	default:
		return verb, "safe", noWhere
	}
}

// Evaluate runs the three-layer judgement (menu guard is enforced by middleware):
//
//	① capability matrix (role × capability × env)
//	② high-risk dictionary (command × env)
//	③ strict mode (DELETE/UPDATE without WHERE)
//
// The strictest level wins.
func (e *RiskEngine) Evaluate(roleID int64, env, sql string) Verdict {
	verb := ParseVerb(sql)
	cap := MapVerbToCapability(verb)
	capLevel := e.store.CapabilityLevel(roleID, cap, env)
	if capLevel == model.LevelDeny {
		return Verdict{Action: ActionDeny, Risk: model.RiskHigh, Rule: "能力矩阵 · 该环境禁止此操作", Command: verb}
	}

	matched, lvl := e.matchCommand(sql, env)
	if matched != "" {
		verb = matched
	}
	rule := ""
	if e.strict.Load() && NoWhere(sql) {
		lvl = model.RiskHigh
		rule = "严格模式 · 无 WHERE 的 DELETE / UPDATE"
	}

	switch lvl {
	case model.RiskHigh:
		if rule == "" {
			rule = "高危命令字典 · PROD 禁止直接执行"
		}
		return Verdict{Action: ActionApprove, Risk: model.RiskHigh, Rule: rule, Command: verb}
	case model.RiskMid:
		return Verdict{Action: ActionApprove, Risk: model.RiskMid, Rule: "高危命令字典 · 需审批", Command: verb}
	default:
		if capLevel == model.LevelApprove {
			return Verdict{Action: ActionApprove, Risk: model.RiskMid, Rule: "能力矩阵 · 需审批", Command: verb}
		}
		return Verdict{Action: ActionAllow, Risk: model.RiskLow, Command: verb}
	}
}
