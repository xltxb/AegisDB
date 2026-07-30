// Package gateway implements the risk-judgement engine and the proxy executor.
package gateway

import (
	"log/slog"
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
	// CapabilityLevel returns allow|approve|deny for role×capability×env. A row
	// that simply does not exist means allow; a non-nil error means the level is
	// UNKNOWN and must not be confused with it (ED3).
	CapabilityLevel(roleID int64, capability, env string) (string, error)
	// RiskCommands returns the full high-risk dictionary (all envs). An error
	// means the dictionary is unavailable, not that it is empty.
	RiskCommands() ([]model.RiskCommand, error)
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
// allow. Ordinary line (`--`) and block (`/* */`) comments are removed,
// INCLUDING ones nested inside an executable comment (MySQL treats those as
// whitespace within the executed body).
//
// `#` is NOT treated as a comment even though MySQL says it is: PostgreSQL reads
// it as an operator, so skipping to end-of-line would erase a real command from
// the text the dictionary scan and NoWhere examine while the server still ran it
// (ER2). Keeping the text can only make a statement look more dangerous, which
// is the safe direction on either engine.
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
	structure := blankQuoted(StripComments(sql))
	// A CTE can carry the DELETE/UPDATE (`WITH d AS (DELETE ...) SELECT ...`), so
	// gating on the leading verb alone let a full-table mutation past the guard
	// (ER9). Treat such a statement as the mutation it performs.
	if verb == "with" && deleteOrUpdateRe.MatchString(structure) {
		verb = "delete"
	}
	if verb != "delete" && verb != "update" {
		return false
	}
	// Look for WHERE in the statement's STRUCTURE only. A word boundary alone
	// stops `elsewhere` from counting, but not a value that literally contains
	// the word: `UPDATE users SET note='where'` has no WHERE clause at all yet
	// satisfied the check, so strict mode waved through a full-table write (ER8).
	return !whereRe.MatchString(structure)
}

var whereRe = regexp.MustCompile(`(?i)\bwhere\b`)

// blankQuoted replaces the CONTENTS of string literals and quoted identifiers
// with spaces, leaving the delimiters and everything else in place. Keyword
// heuristics run over the result so data can never be mistaken for syntax.
// Lengths are preserved so any positional reporting stays meaningful.
func blankQuoted(sql string) string {
	b := []byte(sql)
	for i := 0; i < len(b); i++ {
		q := b[i]
		if q != '\'' && q != '"' && q != '`' {
			continue
		}
		i++
		for i < len(b) {
			if b[i] == q {
				if i+1 < len(b) && b[i+1] == q { // doubled quote = escaped, stay inside
					b[i], b[i+1] = ' ', ' '
					i += 2
					continue
				}
				break // closing delimiter
			}
			b[i] = ' '
			i++
		}
	}
	return string(b)
}

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
	verb := ParseVerb(sql)
	// A CTE may carry the mutation: `WITH d AS (DELETE ... RETURNING *) SELECT ...`
	// really deletes rows on PostgreSQL. WITH leads, so keying on the first verb
	// alone routed it down the query path and recorded it as a read (ER9). What
	// matters is whether the statement mutates, not which keyword comes first.
	if verb == "WITH" && mutatingRe.MatchString(blankQuoted(StripComments(sql))) {
		return false
	}
	return readVerbs[verb]
}

// mutatingRe finds a data-modifying verb anywhere in a statement's structure
// (quoted text is blanked first so a value can't trigger it).
var mutatingRe = regexp.MustCompile(`(?i)\b(insert|update|delete|merge|replace|truncate|drop|alter|create|grant|revoke)\b`)

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
		// mutating operation, so treat it as read-level rather than gating a
		// harmless no-op behind the write-approval path.
		//
		// This is only safe because callers judge SPLIT statements: every real
		// command starts with a keyword, so the empty verb means there is no
		// command here. Do NOT judge raw terminal input against this mapping —
		// a stray leading separator (";UPDATE …") parses to no verb and would be
		// filed as a read (ER3). Service.Exec normalises via SplitStatements
		// first; the old claim that the dictionary scan backstops this case was
		// wrong, since the seeded dictionary has no UPDATE/INSERT/CREATE entries.
		return "select"
	default:
		return "write"
	}
}

// matchCommand finds the first dictionary command (for env) appearing in the SQL.
func (e *RiskEngine) matchCommand(sql, env string) (string, string, error) {
	names := make([]string, 0)
	levelByName := map[string]string{}
	cmds, err := e.store.RiskCommands()
	if err != nil {
		return "", "", err
	}
	for _, rc := range cmds {
		if !strings.EqualFold(rc.Env, env) { // env match is case-insensitive
			continue
		}
		names = append(names, regexp.QuoteMeta(rc.Command))
		levelByName[strings.ToUpper(rc.Command)] = rc.Level
	}
	if len(names) == 0 {
		return "", model.RiskOff, nil
	}
	re := regexp.MustCompile(`(?i)\b(` + strings.Join(names, "|") + `)\b`)
	m := re.FindString(StripComments(sql))
	if m == "" {
		return "", model.RiskOff, nil
	}
	name := strings.ToUpper(m)
	return name, levelByName[name], nil
}

// ScanStatement judges a single statement by the dictionary (for env) + strict
// mode only — used by SQL script scanning. Returns (command, risk, noWhere)
// where risk is high|mid|safe.
func (e *RiskEngine) ScanStatement(env, sql string) (string, string, bool) {
	matched, lvl, err := e.matchCommand(sql, env)
	if err != nil {
		// The dictionary is unreadable; report the statement as high risk rather
		// than clearing it (ED3). A script scan that silently downgrades every
		// statement to "safe" during an outage is worse than a noisy one.
		return ParseVerb(sql), model.RiskHigh, NoWhere(sql)
	}
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

// capabilityLevelUnion returns the most permissive capability level across the
// user's roles (allow ≺ approve ≺ deny). No rows / unknown role default to allow
// via the store, so a single permissive role is enough to grant the capability.
func (e *RiskEngine) capabilityLevelUnion(roleIDs []int64, cap, env string) (string, error) {
	best := model.LevelDeny
	rank := map[string]int{model.LevelAllow: 0, model.LevelApprove: 1, model.LevelDeny: 2}
	if len(roleIDs) == 0 {
		return model.LevelAllow, nil
	}
	for _, id := range roleIDs {
		lvl, err := e.store.CapabilityLevel(id, cap, env)
		if err != nil {
			return "", err // unknown level — the caller must not guess (ED3)
		}
		if rank[lvl] < rank[best] {
			best = lvl
		}
	}
	return best, nil
}

// Evaluate runs the three-layer judgement (menu guard is enforced by middleware):
//
//	① capability matrix (role × capability × env)
//	② high-risk dictionary (command × env)
//	③ strict mode (DELETE/UPDATE without WHERE)
//
// The strictest level wins.
func (e *RiskEngine) Evaluate(roleID int64, env, sql string) Verdict {
	return e.EvaluateRoles([]int64{roleID}, env, sql)
}

// EvaluateRoles is Evaluate for a user holding multiple roles: the capability
// level (layer ①) is the MOST permissive across all the user's roles, so roles
// compose as a union. Layers ② (risk dictionary) and ③ (strict mode) are
// role-independent and unchanged.
func (e *RiskEngine) EvaluateRoles(roleIDs []int64, env, sql string) Verdict {
	verb := ParseVerb(sql)
	cap := MapVerbToCapability(verb)
	capLevel, err := e.capabilityLevelUnion(roleIDs, cap, env)
	if err != nil {
		return unavailableVerdict(verb, err)
	}
	if capLevel == model.LevelDeny {
		return Verdict{Action: ActionDeny, Risk: model.RiskHigh, Rule: "能力矩阵 · 该环境禁止此操作", Command: verb}
	}

	matched, lvl, err := e.matchCommand(sql, env)
	if err != nil {
		return unavailableVerdict(verb, err)
	}
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

// unavailableVerdict is returned when a gate layer cannot be read at all. Both
// layers live in the database, so a transient outage would otherwise silently
// disable them together — a missing capability row reads as allow and an empty
// dictionary reads as off, so DROP TABLE on PROD would sail through (ED3). If
// the gate cannot be consulted the command is refused, not waved past.
func unavailableVerdict(verb string, err error) Verdict {
	slog.Error("risk evaluation unavailable — denying command", "verb", verb, "err", err)
	return Verdict{Action: ActionDeny, Risk: model.RiskHigh, Rule: "风险控制暂时不可用 · 已按最严处理", Command: verb}
}

// deleteOrUpdateRe finds a row-mutating DML verb anywhere in a statement's
// structure — used to recognise a CTE that carries the mutation.
var deleteOrUpdateRe = regexp.MustCompile(`(?i)\b(delete|update)\b`)
