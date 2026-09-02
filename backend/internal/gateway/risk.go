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
	// CapabilityLevel returns allow|approve|deny for role×capability×TIER. A row
	// that simply does not exist means allow; a non-nil error means the level is
	// UNKNOWN and must not be confused with it (ED3).
	CapabilityLevel(roleID int64, capability, tier string) (string, error)
	// RiskCommands returns the full high-risk dictionary (all tiers). An error
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
// leading comments/whitespace. A leading `EXPLAIN [ANALYZE|VERBOSE|(options)]`
// or Oracle `EXPLAIN PLAN FOR` prefix is unwrapped so the WRAPPED verb is judged,
// which is what the statement is really about.
//
// Whether that wrapped verb is actually performed is a separate question, and
// the answer is not the same for every EXPLAIN — see PlanOnly.
func ParseVerb(sql string) string {
	verb, _ := parseVerbExplain(sql)
	return verb
}

// PlanOnly reports whether a statement only produces a query plan and executes
// nothing.
//
// `EXPLAIN <stmt>` plans; `EXPLAIN ANALYZE <stmt>` RUNS the statement and reports
// what actually happened — in PostgreSQL that means `EXPLAIN ANALYZE DELETE …`
// really deletes. So the two cannot be judged alike: the first is a read whatever
// it wraps, the second is exactly as dangerous as its inner verb.
//
// ANALYZE also hides inside the option list — `EXPLAIN (ANALYZE) DELETE …` and
// `EXPLAIN (ANALYZE, BUFFERS) DELETE …` both execute. Missing that form would
// turn EXPLAIN into a way to run any statement while being judged a read, so the
// option list is inspected rather than skipped over.
func PlanOnly(sql string) bool {
	_, planOnly := parseVerbExplain(sql)
	return planOnly
}

// parseVerbExplain returns the effective verb and whether the statement is a
// plan-only EXPLAIN. Both answers come from one walk of the prefix so they can
// never disagree about what was found.
func parseVerbExplain(sql string) (verb string, planOnly bool) {
	s := strings.TrimSpace(StripComments(sql))
	verb = firstWord(s)
	// A WITH statement is governed by what it DOES, not by its leading keyword:
	// the everyday CTE (`WITH t AS (SELECT …) SELECT …`) is a read, while a CTE
	// carrying a mutation (`WITH d AS (DELETE … RETURNING …) SELECT …` — really
	// deletes, ER9) is exactly its mutation. Leaving the verb as WITH filed every
	// CTE into the default (write) capability, which refused read-only CTE
	// exports and mis-labelled CTE reads in the terminal.
	if strings.EqualFold(verb, "WITH") {
		return cteEffectiveVerb(s), false
	}
	if !strings.EqualFold(verb, "EXPLAIN") {
		return strings.ToUpper(verb), false
	}

	planOnly = true // an EXPLAIN plans, unless an executing option turns up below
	rest := strings.TrimSpace(s[len(verb):])
	for {
		w := firstWord(rest)
		up := strings.ToUpper(w)
		// ANALYSE is ANALYZE's British spelling, which PostgreSQL and GaussDB both
		// accept; PERFORMANCE is DWS/GaussDB's own bare option
		// (EXPLAIN { [ANALYZE|ANALYSE] [VERBOSE] | PERFORMANCE } <stmt>). All three
		// RUN the wrapped statement — missing any of them turns EXPLAIN into a way
		// to execute anything while being judged a plan.
		if up == "ANALYZE" || up == "ANALYSE" || up == "PERFORMANCE" {
			planOnly = false
			rest = strings.TrimSpace(rest[len(w):])
			continue
		}
		if up == "VERBOSE" {
			rest = strings.TrimSpace(rest[len(w):])
			continue
		}
		break
	}
	if strings.HasPrefix(rest, "(") { // EXPLAIN (ANALYZE, BUFFERS, …) <stmt>
		if i := strings.Index(rest, ")"); i >= 0 {
			// Read the options rather than stepping over them: ANALYZE in here
			// executes the statement just as the bare keyword does.
			if explainAnalyzeOptRe.MatchString(rest[:i+1]) {
				planOnly = false
			}
			rest = strings.TrimSpace(rest[i+1:])
		}
	}
	// Oracle: EXPLAIN PLAN FOR <stmt> — and EXPLAIN PLAN SET STATEMENT_ID = 'x'
	// [INTO t] FOR <stmt>. Everything up to the FOR is Oracle's own preamble; the
	// statement being explained follows it and is never executed.
	if strings.EqualFold(firstWord(rest), "PLAN") {
		// Search the STRUCTURE: SET STATEMENT_ID = 'plan for q3' carries the word
		// inside a literal, and cutting there would leave a nonsense verb.
		// blankQuoted preserves length, so the index maps straight back.
		if i := explainPlanForRe.FindStringIndex(blankQuoted(rest)); i != nil {
			rest = strings.TrimSpace(rest[i[1]:])
		}
	}
	if inner := firstWord(rest); inner != "" {
		verb = inner
	}
	return strings.ToUpper(verb), planOnly
}

var (
	// An executing option anywhere in an EXPLAIN option list.
	//
	// Both spellings of ANALYZE/ANALYSE count; PERFORMANCE is listed too even
	// though GaussDB only documents it as a BARE option — if some version does
	// accept it in the list, treating it as executing is the safe direction.
	//
	// Deliberately blunt: `EXPLAIN (ANALYZE FALSE) …` does not execute, and this
	// still treats it as if it did. The two ways of being wrong are not
	// comparable — over-gating costs an approval on a statement that would have
	// been safe, while under-gating runs a DELETE that was judged a read. There
	// is no lookahead in RE2 to express "ANALYZE not followed by FALSE" anyway,
	// and a cleverer rule here would be one more thing to get subtly wrong.
	explainAnalyzeOptRe = regexp.MustCompile(`(?i)\b(analy[sz]e|performance)\b`)
	// The FOR that separates Oracle's EXPLAIN PLAN preamble from the statement.
	explainPlanForRe = regexp.MustCompile(`(?i)\bfor\b`)
)

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
	// A plan-only EXPLAIN mutates nothing, so there is no unscoped mutation to
	// guard against — `EXPLAIN DELETE FROM t` deletes no rows.
	if PlanOnly(sql) {
		return false
	}
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
// readVerbs are the statements that RETURN ROWS and change nothing.
//
// TABLE / VALUES / FETCH are full query statements in their own right, not
// fragments: `TABLE t` is PostgreSQL/DWS (and MySQL 8.0.19+) shorthand for
// `SELECT * FROM t`, `VALUES (1),(2)` is a standalone result set, and FETCH
// pulls the next batch from an open cursor. Leaving them out routed them down
// the write path, where nothing collects a result set — the statement ran and
// the console showed nothing.
var readVerbs = map[string]bool{
	"SELECT": true, "SHOW": true, "DESC": true, "DESCRIBE": true, "WITH": true, "EXPLAIN": true,
	"TABLE": true, "VALUES": true, "FETCH": true, "HELP": true,
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

// cteMainVerbs are the keywords a WITH statement's main clause can begin with.
var cteMainVerbs = map[string]bool{
	"SELECT": true, "INSERT": true, "UPDATE": true, "DELETE": true,
	"MERGE": true, "REPLACE": true, "VALUES": true, "TABLE": true,
}

// cteEffectiveVerb resolves the verb that governs a WITH statement (s is
// already comment-stripped and trimmed).
//
// Mutation wins outright: if a mutating verb appears ANYWHERE in the blanked
// text — main clause or inside a CTE body — that verb governs, so
// `WITH d AS (DELETE …) SELECT …` is judged a DELETE, never a read (ER9's
// stance, now expressed in the verb itself). Otherwise the statement is a read
// shaped by its main clause: scan at paren depth 0 past the CTE definitions
// (their bodies sit inside parentheses) for the first main-clause keyword.
// Nothing recognisable falls back to WITH, which no capability maps to a read.
func cteEffectiveVerb(s string) string {
	blanked := blankQuoted(s)
	if m := mutatingRe.FindString(blanked); m != "" {
		return strings.ToUpper(m)
	}
	depth := 0
	for i := 0; i < len(blanked); i++ {
		switch blanked[i] {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 && isWordStart(blanked, i) {
				j := i
				for j < len(blanked) && isWordChar(blanked[j]) {
					j++
				}
				if w := strings.ToUpper(blanked[i:j]); cteMainVerbs[w] {
					return w
				}
				i = j - 1
			}
		}
	}
	return "WITH"
}

// isWordStart reports whether position i begins a word (letter preceded by a
// non-word byte or the start of the string).
func isWordStart(s string, i int) bool {
	if !isWordChar(s[i]) {
		return false
	}
	return i == 0 || !isWordChar(s[i-1])
}

func isWordChar(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// KnownVerb reports whether the classifier actually recognises this verb, as
// opposed to filing it under the default (write) bucket because nothing else
// matched.
//
// The distinction matters at exactly one place: deciding whether to collect a
// result set. Every engine has keywords of its own — DWS's `EXPLAIN PERFORMANCE`,
// SQLite's `PRAGMA`, MySQL's `CALL` returning a result set — and a verb table can
// only ever be behind. Where the table has an answer, use it; where it does not,
// ASK THE DATABASE instead of guessing (see RealRun). Judgement still classifies
// everything, because judging is the product; but "does this return rows" is a
// question the database can answer for itself.
func KnownVerb(verb string) bool {
	switch strings.ToUpper(strings.TrimSpace(verb)) {
	case "SELECT", "SHOW", "DESC", "DESCRIBE", "EXPLAIN", "USE",
		"TABLE", "VALUES", "FETCH", "HELP",
		"INSERT", "UPDATE", "DELETE", "REPLACE", "MERGE",
		"DROP", "ALTER", "TRUNCATE", "RENAME", "CREATE",
		"GRANT", "REVOKE", "WITH":
		return true
	}
	return false
}

// MapVerbToCapability maps a SQL verb to a capability-matrix dimension
// (case-insensitive).
func MapVerbToCapability(verb string) string {
	switch strings.ToUpper(strings.TrimSpace(verb)) {
	case "SELECT", "SHOW", "DESC", "DESCRIBE", "EXPLAIN", "USE",
		// 独立的查询语句,只是关键字不叫 SELECT —— 见 readVerbs 的说明。
		"TABLE", "VALUES", "FETCH", "HELP",
		// 事务控制。它们自己不改任何数据,只是决定前面那些改动作不作数 —— 而
		// 那些改动各自已经按自己的动词判过了。要求 write 才能 COMMIT,挡住的是
		// "把一组 SELECT 包进事务里"这种正当用法,挡不住任何东西。
		//
		// 刻意不含 BEGIN 与 START:
		//   BEGIN 在 Oracle 里是匿名 PL/SQL 块的开头,块里可以 DELETE —— 判成
		//         select 就是把整块代码放行了。
		//   START 在 MySQL 里还有 START REPLICA / START SLAVE,是复制管理。
		// 两个词都得看后面跟着什么才知道是哪一件事,而这里只拿得到动词本身。
		"COMMIT", "ROLLBACK", "SAVEPOINT":
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

// matchCommand finds the first dictionary command (for tier) appearing in the SQL.
//
// `tier` is a control-tier code (model.EnvTier.Code), NOT the connection's
// environment. The dictionary is keyed by tier, so callers must resolve
// connection → environment → tier first; passing an environment code straight in
// finds no rows and returns RiskOff, which reads as "nothing dangerous here".
func (e *RiskEngine) matchCommand(sql, tier string) (string, string, error) {
	names := make([]string, 0)
	levelByName := map[string]string{}
	cmds, err := e.store.RiskCommands()
	if err != nil {
		return "", "", err
	}
	for _, rc := range cmds {
		if !strings.EqualFold(rc.TierCode, tier) { // match is case-insensitive
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

// ScanStatement judges a single statement by the dictionary (for tier) + strict
// mode only — used by SQL script scanning. Returns (command, risk, noWhere)
// where risk is high|mid|safe.
//
// `tier` is the scan baseline tier (model.EnvTier.ScanBaseline). It must be a
// tier that actually exists: an empty or unknown code matches no dictionary rows
// and reports every statement as safe, without erroring. Callers resolve it via
// Repo.ScanBaselineTier and refuse to scan when that fails.
func (e *RiskEngine) ScanStatement(tier, sql string) (string, string, bool) {
	// Same reasoning as EvaluateFor: a plan-only EXPLAIN executes nothing, so a
	// script line that merely asks for a plan is not what makes the script risky.
	if PlanOnly(sql) {
		return ParseVerb(sql), "safe", false
	}
	matched, lvl, err := e.matchCommand(sql, tier)
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
func (e *RiskEngine) capabilityLevelUnion(roleIDs []int64, cap, tier string) (string, error) {
	best := model.LevelDeny
	rank := map[string]int{model.LevelAllow: 0, model.LevelApprove: 1, model.LevelDeny: 2}
	if len(roleIDs) == 0 {
		return model.LevelAllow, nil
	}
	for _, id := range roleIDs {
		lvl, err := e.store.CapabilityLevel(id, cap, tier)
		if err != nil {
			return "", err // unknown level — the caller must not guess (ED3)
		}
		if rank[lvl] < rank[best] {
			best = lvl
		}
	}
	return best, nil
}

// CapabilityFor answers one capability-matrix cell for a set of roles, using the
// same union rule the judgement layers use (allow ≺ approve ≺ deny).
//
// It exists for dimensions that are NOT read off a statement. A release ticket
// is not SQL — nothing to parse a verb from — but "may this role raise one on
// this tier" is the same matrix question, and answering it in the service layer
// would be a second implementation of the union, free to disagree with this one
// about what a missing row or a multi-role user means.
func (e *RiskEngine) CapabilityFor(roleIDs []int64, cap, tier string) (string, error) {
	return e.capabilityLevelUnion(roleIDs, cap, tier)
}

// Evaluate runs the three-layer judgement (menu guard is enforced by middleware):
//
//	① capability matrix (role × capability × tier)
//	② high-risk dictionary (command × tier)
//	③ strict mode (DELETE/UPDATE without WHERE)
//
// The strictest level wins.
//
// Both rule layers are keyed by CONTROL TIER, not by the connection's
// environment — see matchCommand on why the distinction is load bearing.
func (e *RiskEngine) Evaluate(roleID int64, tier, sql string) Verdict {
	return e.EvaluateRoles([]int64{roleID}, tier, sql)
}

// EvaluateRoles is Evaluate for a user holding multiple roles: the capability
// level (layer ①) is the MOST permissive across all the user's roles, so roles
// compose as a union. Layers ② (risk dictionary) and ③ (strict mode) are
// role-independent and unchanged.
func (e *RiskEngine) EvaluateRoles(roleIDs []int64, tier, sql string) Verdict {
	return e.EvaluateFor(roleIDs, "", tier, sql)
}

// EvaluateFor is EvaluateRoles for a command written in a specific engine's
// language. The three policy layers are identical; only the reading of the
// command differs, and that is delegated to the engine's dialect (see
// dialect.go). An empty engine keeps the SQL dialect, so existing callers and
// behaviour are unchanged.
func (e *RiskEngine) EvaluateFor(roleIDs []int64, engine, tier, sql string) Verdict {
	d := DialectFor(engine)
	verb := d.Verb(sql)
	cap := d.Capability(verb)

	// A plan-only EXPLAIN performs none of what it wraps, so it is judged as the
	// read it is — at every layer, not just this one. Asking the planner how a
	// DELETE would run is how someone decides whether to propose it at all, and
	// gating that behind the approval the DELETE itself needs means the plan can
	// only be seen after the decision it was meant to inform.
	//
	// The layers below are skipped for the same reason and not as a shortcut: the
	// dictionary matches the word DELETE in the text, and strict mode looks for a
	// missing WHERE — both are statements about what a command WILL DO, and this
	// one does nothing. EXPLAIN ANALYZE is not covered by any of this; PlanOnly
	// is false for it and it falls through to the ordinary path below.
	// 会话级设置(`SET search_path …` / Oracle `ALTER SESSION SET …`)既不读也不写
	// 数据,只配置这条连接。它和 plan-only 的 EXPLAIN 是同一种东西,所以在这里用
	// 同样的方式短路 —— 包括跳过下面的字典与严格模式,理由也一模一样:
	//
	// 字典匹配的是**文本里的那个词**。运维为了拦 `ALTER TABLE` 在字典里写下
	// "ALTER",不会是想连 `ALTER SESSION SET NLS_DATE_FORMAT` 一起拦掉;字典是
	// 单词粒度的,靠它命中会话设置是碰巧,不是本意。而在 Oracle 上,切 schema 正是
	// 读数据的前置步骤 —— 拦掉它,只读用户就什么都干不了了。
	//
	// 只用 select 一道闸:能查数据的人,自然可以把自己的会话设好。放宽严格到会话
	// 边界为止(SET GLOBAL / SET ROLE / ALTER SYSTEM 都不算,见 session_scope.go)。
	if SessionScoped(sql) {
		capLevel, err := e.capabilityLevelUnion(roleIDs, "select", tier)
		if err != nil {
			return unavailableVerdict(verb, err)
		}
		if capLevel == model.LevelDeny {
			return Verdict{Action: ActionDeny, Risk: model.RiskHigh, Rule: "能力矩阵 · 该环境禁止此操作", Command: verb}
		}
		if capLevel == model.LevelApprove {
			return Verdict{Action: ActionApprove, Risk: model.RiskMid, Rule: "能力矩阵 · 需审批", Command: verb}
		}
		return Verdict{Action: ActionAllow, Risk: model.RiskLow, Command: verb}
	}

	if PlanOnly(sql) {
		// A plan is gated by BOTH `select` and `explain`, whichever is stricter.
		//
		// `explain` is its own dimension because reading a plan and reading data
		// are different permissions — a plan discloses schema and row-count
		// statistics for a statement the role may be forbidden to run — so an
		// estate that must control it can, without revoking SELECT.
		//
		// But the read gate has to keep applying, or introducing the dimension
		// would have LOOSENED things: `explain` seeds `allow` everywhere, so a role
		// deliberately denied SELECT on this tier would have gained the ability to
		// read plans of the very tables it may not read. Separating a permission
		// must not hand out what the original one refused.
		readLevel, err := e.capabilityLevelUnion(roleIDs, "select", tier)
		if err != nil {
			return unavailableVerdict(verb, err)
		}
		planLevel, err := e.capabilityLevelUnion(roleIDs, "explain", tier)
		if err != nil {
			return unavailableVerdict(verb, err)
		}
		capLevel := stricterLevel(readLevel, planLevel)
		if capLevel == model.LevelDeny {
			return Verdict{Action: ActionDeny, Risk: model.RiskHigh, Rule: "能力矩阵 · 该环境禁止此操作", Command: verb}
		}
		if capLevel == model.LevelApprove {
			return Verdict{Action: ActionApprove, Risk: model.RiskMid, Rule: "能力矩阵 · 需审批", Command: verb}
		}
		return Verdict{Action: ActionAllow, Risk: model.RiskLow, Command: verb}
	}

	capLevel, err := e.capabilityLevelUnion(roleIDs, cap, tier)
	if err != nil {
		return unavailableVerdict(verb, err)
	}
	if capLevel == model.LevelDeny {
		return Verdict{Action: ActionDeny, Risk: model.RiskHigh, Rule: "能力矩阵 · 该环境禁止此操作", Command: verb}
	}

	matched, lvl, err := e.matchCommand(sql, tier)
	if err != nil {
		return unavailableVerdict(verb, err)
	}
	if matched != "" {
		verb = matched
	}
	rule := ""
	if e.strict.Load() && d.UnscopedMutation(sql) {
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

// Unavailable is unavailableVerdict for callers outside this package that fail
// BEFORE they can evaluate — specifically, when a connection's environment does
// not resolve to a control tier. Without a tier there is no key to look rules up
// by, and both layers read a missing row as permission granted, so the command
// must be refused rather than judged against nothing (ED3). The verb is parsed
// here only so the refusal names the command it blocked.
func Unavailable(engine, sql string, err error) Verdict {
	return unavailableVerdict(DialectFor(engine).Verb(sql), err)
}

// deleteOrUpdateRe finds a row-mutating DML verb anywhere in a statement's
// structure — used to recognise a CTE that carries the mutation.
var deleteOrUpdateRe = regexp.MustCompile(`(?i)\b(delete|update)\b`)

// stricterLevel returns whichever capability level gates more (allow ≺ approve ≺
// deny). Used where two dimensions both apply and neither may be talked over by
// the other — see the plan-only branch of EvaluateFor.
func stricterLevel(a, b string) string {
	rank := map[string]int{model.LevelAllow: 0, model.LevelApprove: 1, model.LevelDeny: 2}
	if rank[b] > rank[a] {
		return b
	}
	return a
}
