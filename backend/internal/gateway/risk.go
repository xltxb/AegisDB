// Package gateway implements the risk-judgement engine and the proxy executor.
package gateway

import (
	"log/slog"
	"regexp"
	"strings"

	"velagateway/internal/model"
	"velagateway/pkg/sqlutil"
)

// Action results of a verdict.
const (
	ActionAllow   = "allow"
	ActionApprove = "approve"
	ActionDeny    = "deny"
)

// Verdict is the outcome of the three-layer risk evaluation.
//
// Rule is the canonical Chinese sentence — what approvals and the audit chain
// record, and what must not change with whoever happens to be reading. Ref is
// the same statement as a code plus arguments, so a client can render it in the
// operator's language; see model.RuleRef. Rule is always RenderRule(Ref), never
// written independently, or the two would drift and the audit trail would stop
// matching what the operator was shown.
type Verdict struct {
	Action  string // allow|approve|deny
	Risk    string // high|mid|low
	Rule    string
	Ref     *model.RuleRef
	Command string
}

// verdict builds a gated verdict from its rule identity, keeping Rule and Ref in
// step by construction.
func verdict(action, risk, verb string, ref *model.RuleRef) Verdict {
	return Verdict{Action: action, Risk: risk, Rule: model.RenderRule(ref), Ref: ref, Command: verb}
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
	// StrictNoWhere reports whether TIER blocks a DELETE / UPDATE carrying no
	// WHERE. An error means the answer is UNKNOWN — like the other two layers,
	// it must not be read as "not blocked" (ED3).
	StrictNoWhere(tier string) (bool, error)
}

// RiskEngine evaluates commands against the three layers.
type RiskEngine struct {
	store Store
}

func NewRiskEngine(store Store) *RiskEngine {
	return &RiskEngine{store: store}
}

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
func StripComments(sql string) string { return stripCommentsEsc(sql, false) }

// stripCommentsEsc is StripComments told how the target engine reads a `\` inside
// a string literal. See backslashEscapes for why that is not one answer.
func stripCommentsEsc(sql string, backslash bool) string {
	var b strings.Builder
	n := len(sql)
	execDepth := 0 // >0 while inside /*!ver ... */: keep the body, drop the markers
	for i := 0; i < n; i++ {
		c := sql[i]
		switch {
		case c == '\'' || c == '"' || c == '`': // quoted literal / identifier — copy verbatim
			q := c
			esc := backslash && q != '`' // 反引号括的是标识符,里面的反斜杠哪个引擎都不转义
			b.WriteByte(c)
			i++
			for i < n {
				d := sql[i]
				b.WriteByte(d)
				if esc && d == '\\' && i+1 < n { // `\x` 整对都是内容 —— 包括 `\'`
					i++
					b.WriteByte(sql[i])
					i++
					continue
				}
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
func NoWhere(sql string) bool { return noWhereIn("", sql) }

// backslashEscapes 说一族引擎把字符串字面量里的 `\'` 读成什么。
//
// 这不是一个可以随便挑一边的细节:同一串字节在两族引擎里是两条不同的语句。
//
//	UPDATE t SET a='x\' WHERE 1=1 --'
//
// MySQL 默认 sql_mode 下 `\'` 是转义的引号,字符串一直延伸到末尾那个引号 ——
// 整条语句没有 WHERE,是一次整表更新。PostgreSQL(standard_conforming_strings)、
// Oracle、SQLite 的标准字符串里反斜杠只是一个普通字符,字符串在第二个引号处就
// 结束了,后面的 `WHERE 1=1` 是真的子句。
//
// known=false 表示这个标签解析不出引擎族(自建、改过名、CSV 导入的连接都会这样)。
func backslashEscapes(engine string) (esc, known bool) {
	switch engineFamily(engine) {
	case familyMySQL:
		return true, true
	case familyPostgres, familyOracle, familySQLite:
		return false, true
	}
	return false, false
}

// noWhereIn 是 NoWhere 加上目标引擎 —— 无 WHERE 拦截是唯一必须按引擎读字符串的
// 地方,理由是它找的是"**缺**了一段结构"。
//
// 别处的启发式(字典扫描、动词解析)找的都是"**有**某段结构",把字面量当结构只会
// 让语句看起来更危险 —— 误报,安全方向。无 WHERE 反过来:把 WHERE 从字面量里读成
// 结构,拦截整层就被跳过了,是漏判。所以这一处不能沿用"一律不认反斜杠"。
//
// 引擎认不出来时两种读法都试,任一读出无 WHERE 就按无 WHERE 判:一个认不出的标签
// 背后可能就是 MySQL,而这一层在那种情况下宁可多拦。
func noWhereIn(engine, sql string) bool {
	if esc, known := backslashEscapes(engine); known {
		return noWhere(sql, esc)
	}
	return noWhere(sql, false) || noWhere(sql, true)
}

func noWhere(sql string, backslash bool) bool {
	// A plan-only EXPLAIN mutates nothing, so there is no unscoped mutation to
	// guard against — `EXPLAIN DELETE FROM t` deletes no rows.
	if PlanOnly(sql) {
		return false
	}
	verb := strings.ToLower(ParseVerb(sql))
	structure := blankQuotedEsc(stripCommentsEsc(sql, backslash), backslash)
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
func blankQuoted(sql string) string { return blankQuotedEsc(sql, false) }

// blankQuotedEsc is blankQuoted told how the target engine reads a `\` inside a
// string literal (see backslashEscapes). The masking itself lives in sqlutil —
// the review package needs the same thing with different quoting rules, and two
// copies of it had already drifted apart on exactly this question.
func blankQuotedEsc(sql string, backslash bool) string {
	// 反引号要当引号:MySQL 的 `drop` 是一个标识符,不是那个动词。
	return sqlutil.MaskLiterals(sql, sqlutil.LiteralMask{Backtick: true, Backslash: backslash})
}

// quotedContents is blankQuoted's mirror: it returns only what was INSIDE the
// string literals and quoted identifiers, joined by spaces. Used to look into
// the payload of dynamic SQL — see dynamicExecRe.
func quotedContents(sql string) string {
	var out []byte
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
					out = append(out, q)
					i += 2
					continue
				}
				break // closing delimiter
			}
			out = append(out, b[i])
			i++
		}
		out = append(out, ' ')
	}
	return string(out)
}

// dynamicExecRe marks the constructs that RUN a string as SQL: Oracle
// `EXECUTE IMMEDIATE '…'`, SQL Server `sp_executesql N'…'` / `EXEC('…')`,
// MySQL `PREPARE s FROM '…'`, PL/pgSQL `EXECUTE '…'`.
//
// Matched against the BLANKED structure, so the word "execute" sitting inside
// somebody's data does not turn that row into dynamic SQL.
//
// Deliberately loose: over-matching only causes the payload to be scanned too,
// which is the safe direction. Missing a dialect's form is the direction that
// costs coverage, so a bare EXECUTE counts as well.
var dynamicExecRe = regexp.MustCompile(`(?i)\b(execute|exec|sp_executesql|prepare)\b`)

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
	// 扫的是**抹掉字面量之后**的结构,不是原文。
	//
	// 这曾是全项目唯一一处还在读字符串内容的关键词启发式:无 WHERE 判断、CTE 里的
	// 写操作识别、EXPLAIN 解析、敏感字段识别,五处早就先抹字面量了。字典漏了这一步,
	// 于是一份只有 INSERT 的菜单初始化脚本,因为权限串写作 'system:menu:delete',
	// 被判成高危 DELETE 并整脚本送审。数据里的词不是语法。
	clean := StripComments(sql)
	structure := blankQuoted(clean)
	m := re.FindString(structure)

	// 但字符串里的关键词有一种情况确实会执行:动态 SQL。抹掉字面量会连
	// `EXECUTE IMMEDIATE 'DROP TABLE t'` 里那个真的要跑的 DROP 一起抹掉。所以只要
	// 结构里出现动态执行的构造,就把所有字面量的内容也拿来扫一遍。
	//
	// 从前这类语句是**碰巧**被覆盖的(字典扫原文,顺带扫到了引号里)。现在是明确的
	// 规则:命中时能说清这是动态 SQL 的载荷,而不是某个字段的值。
	if m == "" && dynamicExecRe.MatchString(structure) {
		m = re.FindString(quotedContents(clean))
	}
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
func (e *RiskEngine) ScanStatement(engine, tier, sql string) (string, string, bool) {
	// 按**目标实例的引擎**读这条语句 —— 反斜杠在 MySQL 的字符串里是转义,在标准
	// 字符串里不是,同一串字节因此是两条不同的语句(见 backslashEscapes)。
	//
	// 不带引擎调 NoWhere 会走"两种读法取最严",那在判定层是安全余量,在**报告**层
	// 却是假警报:一份合法的 PostgreSQL 脚本被报成整表更新,在严格分层上升成 high。
	// 而扫描器手里就攥着目标连接,引擎就在上面。
	d := DialectFor(engine)
	// Same reasoning as EvaluateFor: a plan-only EXPLAIN executes nothing, so a
	// script line that merely asks for a plan is not what makes the script risky.
	if PlanOnly(sql) {
		return d.Verb(sql), "safe", false
	}
	matched, lvl, err := e.matchCommand(sql, tier)
	if err != nil {
		// The dictionary is unreadable; report the statement as high risk rather
		// than clearing it (ED3). A script scan that silently downgrades every
		// statement to "safe" during an outage is worse than a noisy one.
		return d.Verb(sql), model.RiskHigh, d.UnscopedMutation(sql)
	}
	verb := matched
	if verb == "" {
		verb = d.Verb(sql)
	}
	noWhere := d.UnscopedMutation(sql)
	// The baseline tier's own setting governs the scan, exactly as its dictionary
	// does. An unreadable flag is treated as ON: the scan already reports a
	// statement high when the dictionary cannot be read, and clearing one here
	// would be the same silent downgrade in a different layer (ED3).
	strict, serr := e.store.StrictNoWhere(tier)
	if serr != nil {
		strict = true
	}
	switch {
	case lvl == model.RiskHigh:
		return verb, "high", noWhere
	case strict && noWhere:
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
//
// NO ROLES AT ALL is the opposite case, and it means DENY.
//
// It used to return allow, on the reasoning that the state was unreachable — a
// user always had a primary role, and the API refuses to save an empty role set.
// Disabling an account now strips its roles (Repo.ClearUserRoles), so the state
// is reachable, and "allow" would have meant a stripped account holds every
// capability the moment anything let it past the status gate. Permission comes
// FROM a role; with none there is nothing to derive it from.
func (e *RiskEngine) capabilityLevelUnion(roleIDs []int64, cap, tier string) (string, error) {
	best := model.LevelDeny
	rank := map[string]int{model.LevelAllow: 0, model.LevelApprove: 1, model.LevelDeny: 2}
	if len(roleIDs) == 0 {
		return model.LevelDeny, nil
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
			return verdict(ActionDeny, model.RiskHigh, verb, model.NewRuleRef(model.RuleCapDeny))
		}
		if capLevel == model.LevelApprove {
			return verdict(ActionApprove, model.RiskMid, verb, model.NewRuleRef(model.RuleCapApprove))
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
			return verdict(ActionDeny, model.RiskHigh, verb, model.NewRuleRef(model.RuleCapDeny))
		}
		if capLevel == model.LevelApprove {
			return verdict(ActionApprove, model.RiskMid, verb, model.NewRuleRef(model.RuleCapApprove))
		}
		return Verdict{Action: ActionAllow, Risk: model.RiskLow, Command: verb}
	}

	capLevel, err := e.capabilityLevelUnion(roleIDs, cap, tier)
	if err != nil {
		return unavailableVerdict(verb, err)
	}
	if capLevel == model.LevelDeny {
		return verdict(ActionDeny, model.RiskHigh, verb, model.NewRuleRef(model.RuleCapDeny))
	}

	matched, lvl, err := e.matchCommand(sql, tier)
	if err != nil {
		return unavailableVerdict(verb, err)
	}
	if matched != "" {
		verb = matched
	}
	var ref *model.RuleRef
	// Layer ③, keyed by the same tier as the two layers above it. A tier that
	// switches this off is saying "a full-table write is ordinary here" — which is
	// what dev means by setting its whole dictionary to `off`.
	strict, serr := e.store.StrictNoWhere(tier)
	if serr != nil {
		return unavailableVerdict(verb, serr) // gate unreadable → refuse, never wave through (ED3)
	}
	if strict && d.UnscopedMutation(sql) {
		lvl = model.RiskHigh
		ref = model.NewRuleRef(model.RuleStrictNoWhere)
	}

	switch lvl {
	case model.RiskHigh:
		if ref == nil {
			// 名字里那个分层要是**这次判定用的**分层。它一直硬写着 PROD,于是 UAT 上
			// 被字典拦下的人读到的是"PROD 禁止直接执行" —— 一条对不上自己所在环境的
			// 理由,只会让人怀疑是网关判错了库。
			ref = model.NewRuleRef(model.RuleDictDeny, "tier", strings.ToUpper(tier))
		}
		return verdict(ActionApprove, model.RiskHigh, verb, ref)
	case model.RiskMid:
		return verdict(ActionApprove, model.RiskMid, verb, model.NewRuleRef(model.RuleDictApprove))
	default:
		if capLevel == model.LevelApprove {
			return verdict(ActionApprove, model.RiskMid, verb, model.NewRuleRef(model.RuleCapApprove))
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
	return verdict(ActionDeny, model.RiskHigh, verb, model.NewRuleRef(model.RuleUnavailable))
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
