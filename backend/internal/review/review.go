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
	if e == "" {
		return DialectGeneric
	}
	// 大类由 gateway.EngineFamily 定 —— 和网关判定挑协议用的是同一张表。
	//
	// 这里原本抄了一遍子串级联,而且抄漏了 postgre 那一支:PolarDB for PostgreSQL 因为
	// 标签里含 polardb 被套上了 **MySQL 规范**。那些「VARCHAR 长度」「表必须有主键
	// 自增」的条目拿去审一份 PG 脚本,报出来的没有一条是真的 —— 而人对审查结果的信任
	// 是一次性的:报过一次没道理的,下一次真的那条也不会有人看。
	switch gateway.EngineFamily(e) {
	case gateway.FamilyOracle:
		return DialectOracle
	case gateway.FamilyMySQL:
		// TiDB 有自己那份规范(docs/TIDB規範.md),从 MySQL 家族里单拎出来。
		if strings.Contains(e, "tidb") {
			return DialectTiDB
		}
		return DialectMySQL
	case gateway.FamilyPostgres:
		// DWS / GaussDB 同样有自己那份规范;剩下的 PG 走 Generic —— 我们没有为通用
		// PostgreSQL 写过规范库,套别人的不如不套。
		if strings.Contains(e, "dws") || strings.Contains(e, "gauss") {
			return DialectDWS
		}
		return DialectGeneric
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
	prepared := make([]preparedRule, 0, len(rules))
	for _, r := range rules {
		if r.Enabled && r.AppliesTo(dialect) {
			active = append(active, r)
			prepared = append(prepared, prepare(r))
		}
	}
	stmts := splitWithLines(sql)
	for _, st := range stmts {
		if !st.inner {
			res.Statements++
		}
	}
	for _, st := range stmts {
		for _, pr := range prepared {
			r := pr.rule
			for _, msg := range fire(pr, st, dialect) {
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

// preparedRule is a rule with its per-rule work already done.
//
// 解参数、编译运维写的正则,这两件事只跟规则有关,跟当前扫到第几条语句无关。它们
// 从前在 (语句 × 规则) 的双重循环里各做一遍 —— 一份 200 条语句、30 条规则的迁移
// 脚本就是 6000 次 JSON 解析加 6000 次正则编译。
//
// 编译错误随身带着,而不是在准备阶段就报掉:一条正则写错的规则要**每条语句报一次**,
// 和从前一样。写规则的人只有从报告里看见它,才会知道它是错的 —— 一条永不触发的
// 规则和一条永远通过的规则,从外面看一模一样。
type preparedRule struct {
	rule   Rule
	params params
	re     *regexp.Regexp
	reErr  error
	check  checker
}

func prepare(r Rule) preparedRule {
	pr := preparedRule{rule: r, params: parseParams(r.Params)}
	if r.Kind == "regex" {
		if pat := pr.params.str("pattern", ""); pat != "" {
			pr.re, pr.reErr = regexp.Compile("(?is)" + pat)
		}
		return pr
	}
	// A nil checker is a SCRIPT-level rule (scriptFindings owns it), not a
	// missing implementation — see the registry.
	if c, ok := builtinChecker(r.Code); ok {
		pr.check = c
	}
	return pr
}

// fire runs one rule against one statement and returns its messages (usually
// none or one). A regex rule matches its pattern; a builtin rule runs the
// checker registered under its code.
func fire(pr preparedRule, st *stmt, dialect string) []string {
	r := pr.rule
	if r.Kind == "regex" {
		return regexRule(pr, st)
	}
	if pr.check == nil {
		return nil
	}
	msgs := pr.check(st, pr.params, dialect)
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
func regexRule(pr preparedRule, st *stmt) []string {
	p := pr.params
	pat := p.str("pattern", "")
	if pat == "" {
		return nil
	}
	if pr.reErr != nil {
		return []string{fmt.Sprintf("规则 %s 的正则无效: %v", pr.rule.Code, pr.reErr)}
	}
	// 匹配的是拆分器交回来的语句文本。
	//
	// 这里从前还有个 `scope: "raw"` 的开关,说是拿"原文"来匹配 —— 而它什么都不做:
	// SplitStatements 在拆的时候就把注释全抹了(连语句中间的也抹),st.raw 与 st.sql
	// 只差首尾空白。于是一条"每条 DDL 必须带 -- ticket: 注释"的 require 规则永远
	// 匹配不上,运维怎么写都让它一直报,而看不出是旋钮坏了。没有种子规则、文档或
	// 界面用过它,所以拆掉这个假承诺 —— 留着只是个陷阱。真要按注释审查,那是另一件
	// 事:得让拆分器交回原文区间,而那条拆分规则是判定的地基,不能顺手改。
	hit := pr.re.MatchString(st.sql)
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
	// inner:这条是从 PL/SQL 块体里拆出来的,不是脚本里独立的一条。
	// 规则照跑,但不计入"这段脚本有几条语句" —— 那个数字是给人看的。
	inner bool
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
		// PL/SQL 块要再看进去一层:块整体交回来是给执行用的,而审查的检查器都锚在
		// 语句开头 —— 不拆开,包体里的 DROP 就一条规则都触发不了。内部语句沿用块
		// 的序号与行号,报出来指向的是这个块,而不是一个凭空多出来的语句。
		for _, inner := range innerStatements(clean) {
			ic := gateway.StripComments(inner)
			im := maskLiterals(ic)
			out = append(out, &stmt{
				index: i + 1, line: line, raw: inner, sql: strings.TrimSpace(ic),
				masked: im, upper: strings.ToUpper(im), verb: gateway.ParseVerb(ic),
				inner: true,
			})
		}
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
//
// 反引号**不**当引号:这里的规则正要看见那些标识符(列名前缀、表名规范),抹掉就一条
// 也触发不了。反斜杠按 MySQL 读 —— 规范库里的语句以 MySQL/DWS 为主,而这一层没有
// 连接可问引擎。
func maskLiterals(s string) string {
	return sqlutil.MaskLiterals(s, sqlutil.LiteralMask{Backslash: true})
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
