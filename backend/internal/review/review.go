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
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"aegisdb/internal/apimsg"
	"aegisdb/internal/gateway"
	"aegisdb/internal/model"
	"aegisdb/pkg/sqlutil"
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
	// MessageRef 是 Message 的机器可读身份(apiMsg),界面按读者语言显示;运维自定义的规则消息原样嵌在里面。
	// 存在发布阶段的审查结果里(JSON),老结果没有它,界面回落到 Message。
	MessageRef *model.RuleRef `json:"messageRef,omitempty"`
}

// Result is the whole review of one script.
type Result struct {
	Dialect    string    `json:"dialect"`
	Statements int       `json:"statements"`
	Errors     int       `json:"errors"`
	Warnings   int       `json:"warnings"`
	Infos      int       `json:"infos"`
	// Findings 只是**示例**:每条规则最多 MaxExamplesPerRule 条(脚本里最前面的那几条)。
	// 命中总数看 Rules / Total / Errors·Warnings·Infos,它们按全部命中算。
	Findings []Finding `json:"findings"`
	// Rules 按规则汇总:每条命中过的规则一行,带命中总数。错误在前、警告其次、提示最后,
	// 同级按首次命中的先后。空 = 没有任何命中(全部通过)。
	Rules []RuleHit `json:"rules"`
	// Total 是全部命中条数(= Errors+Warnings+Infos),可能远大于 len(Findings)。
	Total int `json:"total"`
	// Passed means no error-level finding. It is NOT "no findings": a release
	// stopped by every warning would be a release nobody could ever ship, so the
	// gate is the level an operator deliberately assigned to the rule.
	Passed bool `json:"passed"`
}

// maxExcerpt bounds the statement text carried on a finding. Findings are stored
// on a release stage (MEDIUMTEXT) and rendered in a list; a 6MB migration would
// otherwise put its whole body in there once per finding.
const maxExcerpt = 240

// MaxExamplesPerRule 是每条规则在结果里带的示例条数。
//
// 2026-11-10:一张 50 万条语句、每条都命中的脚本,逐条存下来约 100MB —— 超过 MySQL
// MEDIUMTEXT 的 16MB 时整条阶段更新失败(状态也写不进去,审查一直显示"运行中"),
// 详情接口每 2.5 秒整段回传,浏览器画 50 万行卡死。用户确认不需要逐条留痕,
// 看的人要的是"哪几条规则、各命中多少、长什么样",每条规则 5 条示例(用户拍板)。
// 这样整份结果的大小只跟命中的规则数有关,跟脚本多长无关。
const MaxExamplesPerRule = 5

// RuleHit 是一条规则在这份脚本里的命中汇总。
type RuleHit struct {
	Code     string `json:"code"`
	Name     string `json:"name"`
	Level    string `json:"level"`
	Category string `json:"category"`
	Count    int    `json:"count"` // 命中总数(示例只带前 MaxExamplesPerRule 条)
	// MessageRef/Message:首条命中的提示,汇总行直接显示,不必再去示例里找。
	Message    string         `json:"message"`
	MessageRef *model.RuleRef `json:"messageRef,omitempty"`
}

// summarize 把全部命中压成"按规则汇总 + 每条规则前几条示例",并统计各级别条数。
func (res *Result) summarize(all []Finding) {
	res.Findings = make([]Finding, 0, len(all))
	res.Rules = []RuleHit{}
	idx := map[string]int{}
	for _, f := range all {
		switch f.Level {
		case LevelError:
			res.Errors++
		case LevelInfo:
			res.Infos++
		default:
			res.Warnings++
		}
		key := f.Code + "\x00" + f.Level
		i, ok := idx[key]
		if !ok {
			i = len(res.Rules)
			idx[key] = i
			res.Rules = append(res.Rules, RuleHit{
				Code: f.Code, Name: f.Name, Level: f.Level, Category: f.Category,
				Message: f.Message, MessageRef: f.MessageRef,
			})
		}
		res.Rules[i].Count++
		if res.Rules[i].Count <= MaxExamplesPerRule {
			res.Findings = append(res.Findings, f)
		}
	}
	res.Total = len(all)
	rank := func(level string) int {
		switch level {
		case LevelError:
			return 0
		case LevelInfo:
			return 2
		}
		return 1
	}
	sort.SliceStable(res.Rules, func(a, b int) bool { return rank(res.Rules[a].Level) < rank(res.Rules[b].Level) })
}

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
	var broken []Finding
	for _, r := range rules {
		if r.Enabled && r.AppliesTo(dialect) {
			// 编译不过的自定义正则:不参与逐条审查,但在结果里报**一条警告**(不是 error)。
			// 从前它按规则级别(多半是 error)逐条报,一条写坏的规则让所有升级单卡在规范审查上;
			// 6364b74 改成只记日志、静默跳过,规则从"拦住"悄悄变成"放行",写它的人永远不知道它是死的
			// (2026-10-26 回归排查)。警告不挡发布,但每次审查都看得见。
			if r.Kind == "regex" {
				if pat := parseParams(r.Params).str("pattern", ""); pat != "" {
					if _, err := regexp.Compile("(?is)" + pat); err != nil {
						slog.Warn("SQL 规范:自定义规则的正则无法编译,本次未生效", "code", r.Code, "err", err)
						m := msgf("规范规则 %s 的正则无效,本次审查未生效,请管理员修正: %v", r.Code, err)
						broken = append(broken, Finding{
							Code: r.Code, Name: r.Name, Level: LevelWarn, Category: r.Category,
							Message: m.S, MessageRef: m.Ref,
						})
						continue
					}
				}
			}
			active = append(active, r)
		}
	}
	stmts := splitWithLines(sql)
	for _, st := range stmts {
		if !st.inner {
			res.Statements++
		}
	}
	for _, st := range stmts {
		for _, r := range active {
			for _, msg := range fire(r, st, dialect) {
				res.Findings = append(res.Findings, Finding{
					Code: r.Code, Name: r.Name, Level: r.Level, Category: r.Category,
					Stmt: st.index, Line: st.line, SQL: excerpt(st.raw), Message: msg.S, MessageRef: msg.Ref,
				})
			}
		}
	}
	res.Findings = append(res.Findings, scriptFindings(active, stmts, sql)...)
	res.Findings = append(res.Findings, broken...)
	res.summarize(res.Findings)
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
func fire(r Rule, st *stmt, dialect string) []apimsg.M {
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
	msgs := c(st, withDefaults(r.Code, dialect, p), dialect)
	// A rule's configured Message overrides the checker's wording so an operator
	// can phrase the standard in their own terms; the checker's detail is appended
	// because it names the actual column/table, which the phrasing cannot.
	if custom := strings.TrimSpace(r.Message); custom != "" {
		out := make([]apimsg.M, 0, len(msgs))
		for _, m := range msgs {
			if m.S == "" || m.S == custom {
				out = append(out, apimsg.M{S: custom})
			} else {
				out = append(out, apimsg.Sprintf("%s — %s", custom, m))
			}
		}
		return out
	}
	return msgs
}

// regexRule applies an operator-written pattern to the statement.
//
// 编译不过的正则**跳过并记日志**(2026-10 黑盒测试,工单 02 第 6 条)。从前它把自己报成一条
// 发现,而规则级别多半是 error —— 于是一条写坏的规则让**所有**审查都 failed,所有升级单都卡在
// 规范审查上。保存时已经先编译一遍(service.validateRegexRule),能走到这里的只剩旧版本存进来
// 的历史数据,日志里写清是哪一条,让管理员去改。
func regexRule(r Rule, p params, st *stmt) []apimsg.M {
	pat := p.str("pattern", "")
	if pat == "" {
		return nil
	}
	re, err := regexp.Compile("(?is)" + pat)
	if err != nil {
		return nil // Check 已经把它筛掉并记了日志;这里只是兜底
	}
	target := st.sql
	if p.str("scope", "") == "raw" {
		target = st.raw
	}
	hit := re.MatchString(target)
	// forbid (default): a match is a violation. require: the ABSENCE is.
	if p.str("mode", "forbid") == "require" {
		if !hit {
			return []apimsg.M{msgf("语句未匹配要求的模式: %s", pat)}
		}
		return nil
	}
	if hit {
		return []apimsg.M{msgf("语句命中禁止的模式: %s", pat)}
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
	inner  bool
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

// msgf 是一条审查发现的话:中文由同一格式串经 fmt 生成(与改造前一字不差),ref 让界面按读者语言显示
// (apimsg,第三批)。格式串都由 go generate ./internal/apimsg 收进文案表。
func msgf(format string, args ...any) apimsg.M { return apimsg.Sprintf(format, args...) }

// AddFinding 把审查之外得出的一条结论并进结果(示例、按规则汇总、计数、Passed 一并更新)。
// error 级的汇总行排在最前,与 summarize 的顺序一致。
func (res *Result) AddFinding(f Finding) {
	switch f.Level {
	case LevelError:
		res.Errors++
	case LevelInfo:
		res.Infos++
	default:
		res.Warnings++
	}
	res.Total++
	res.Findings = append(res.Findings, f)
	hit := RuleHit{Code: f.Code, Name: f.Name, Level: f.Level, Category: f.Category, Count: 1, Message: f.Message, MessageRef: f.MessageRef}
	if f.Level == LevelError {
		res.Rules = append([]RuleHit{hit}, res.Rules...)
	} else {
		res.Rules = append(res.Rules, hit)
	}
	res.Passed = res.Errors == 0
}

// StatementLine 是第 n 条语句(1 起,与 Finding.Stmt 同一编号)在脚本里起始的行号;找不到回 0。
func StatementLine(sql string, n int) int {
	for _, st := range splitWithLines(sql) {
		if !st.inner && st.index == n {
			return st.line
		}
	}
	return 0
}
