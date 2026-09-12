package gateway

// 敏感字段脱敏 —— 在**结果离开网关之前**把它打码。
//
// 为什么在这里而不是前端:前端拿到的必须已经是打码后的数据。一个"前端负责打码"的
// 设计,等于把敏感数据完整地发到了浏览器,再请浏览器不要显示 —— 抓个包、开个
// DevTools 就绕过了,而且它已经躺在浏览器缓存和任何中间代理的日志里了。
//
// 落点选在 RealRun 与 RealQueryEach:整个网关只有这两处会把行读出来(终端、异步
// 执行、发布代执行都走 RealRun,导出走 RealQueryEach)。放在这里,新加的调用路径
// 也天然被覆盖 —— 不会有人"忘了加脱敏"。

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

// 打码方式。
const (
	MaskPartial = "partial" // 留头留尾,中间抹掉 —— 够核对,不够泄露
	MaskFull    = "full"    // 整体抹掉
	MaskHash    = "hash"    // 同值同码:能核对"是不是同一个人",看不出是谁
)

// SensitiveRule 是一条"这张表的这个字段是敏感的"。
//
// Table 为 "*" 表示所有表 —— 有些字段(口令、密钥)在哪张表上都不该被看到。
type SensitiveRule struct {
	Table  string
	Column string
	Style  string // 空 = MaskPartial
}

// SensitiveMaskTargets 返回结果集中需要打码的列下标。
//
// 匹配分两步,第二步是为了兜住别名:
//
//  1. **按列名**:回传的列名就是敏感字段名。`SELECT *` 落在这一步,而且它恰恰是
//     最安全的形态 —— 驱动回传的就是真实列名。
//  2. **按算法**:选择列表第 k 项的表达式里提到了敏感字段,第 k 列就打码。
//     `SELECT id_card AS x`、`SELECT SUBSTR(id_card,1,6) AS x` 都在这一步兜住。
//
// 只看**选择列表**,不看整条语句:`SELECT count(1) FROM t WHERE id_card = ?` 的输出
// 是个计数,不是敏感数据,把它打码只会让人觉得这道闸没道理。
//
// 方向上宁可多打:多打一列是看不到本可以看的数据,漏打一列是敏感数据直接回传。
//
// # 它的上限
//
// 这道闸防的是**顺手看到**,不是防外泄。一个铁了心要拿数据的人总能构造出映射不
// 回去的表达式(经由子查询再套一层别名是最简单的一种)。真正的边界是**不给这张表
// 的访问权限** —— 标签与角色才是那道墙,这里只是让日常查询不至于把身份证号刷在
// 屏幕上。把它当成外泄防线来依赖,是这套机制唯一的误用方式。
func SensitiveMaskTargets(sql string, cols []string, rules []SensitiveRule) []int {
	if len(cols) == 0 || len(rules) == 0 {
		return nil
	}
	clean := blankQuoted(StripComments(sql)) // 字面量抹掉:'id_card' 只是一段文本
	tables := referencedTables(clean)

	active := make([]SensitiveRule, 0, len(rules))
	for _, r := range rules {
		t := strings.ToLower(strings.TrimSpace(r.Table))
		if t == "" || t == "*" || tables[t] {
			active = append(active, r)
		}
	}
	if len(active) == 0 {
		return nil
	}

	hit := make(map[int]bool, len(cols))
	// 第一步:列名
	for i, c := range cols {
		lc := strings.ToLower(strings.TrimSpace(c))
		for _, r := range active {
			if lc == strings.ToLower(strings.TrimSpace(r.Column)) {
				hit[i] = true
				break
			}
		}
	}
	// 第二步:选择列表里第 k 项是怎么算出来的。
	//
	// 只在项数与列数一一对应时才做:出现 `*` 或项数对不上时位置会错位,而错位地
	// 打码比不打更糟 —— 它打了不该打的,却漏了该打的。
	// 每一个 UNION 分支都要看。
	//
	// 结果集的列名来自**第一个**分支:`SELECT phone AS c FROM a UNION ALL SELECT
	// id_card FROM t_user` 回来的列叫 c,按列名匹配不上,而只读第一个分支的话,第二个
	// 分支里那个 id_card 从头到尾没有被看见 —— 身份证号原样刷在屏幕上,那一列的名字
	// 还是个人畜无害的 c。
	//
	// 按位置合并:第 k 列对应每个分支的第 k 项,任一分支命中就打第 k 列。UNION 本来就
	// 要求各分支列数一致、第 k 列是同一个东西,所以这个对应关系是 SQL 自己保证的。
	for _, branch := range unionBranches(clean) {
		items, ok := selectListItems(branch)
		// 只在项数与列数一一对应时才做:出现 `*` 或项数对不上时位置会错位,而错位地
		// 打码比不打更糟 —— 它打了不该打的,却漏了该打的。
		if !ok || len(items) != len(cols) {
			continue
		}
		for i, item := range items {
			if hit[i] {
				continue
			}
			for _, r := range active {
				if mentionsColumn(item, r.Column) {
					hit[i] = true
					break
				}
			}
		}
	}
	out := make([]int, 0, len(hit))
	for i := range cols {
		if hit[i] {
			out = append(out, i)
		}
	}
	return out
}

// bareName strips quoting and any schema/alias qualifier, so a rule written for
// `t_user` matches `dwd.t_user` and `"T_USER"` alike.
func bareName(s string) string {
	const quotes = "`\"[]"
	s = strings.Trim(s, quotes)
	if i := strings.LastIndexByte(s, '.'); i >= 0 {
		s = s[i+1:]
	}
	return strings.Trim(s, quotes)
}

var sensitiveTableRefRe = regexp.MustCompile(`(?is)\b(?:FROM|JOIN|INTO|UPDATE)\s+([A-Za-z_"` + "`" + `][A-Za-z0-9_$#."` + "`" + `]*)`)

// referencedTables collects the table names a statement mentions, lower-cased and
// stripped of any schema qualifier — a rule written for `t_user` must match
// `dwd.t_user` and `u.t_user` alike.
func referencedTables(sql string) map[string]bool {
	out := map[string]bool{}
	for _, m := range sensitiveTableRefRe.FindAllStringSubmatch(sql, -1) {
		name := bareName(strings.TrimSpace(m[1]))
		if name != "" {
			out[strings.ToLower(name)] = true
		}
	}
	return out
}

var selectHeadRe = regexp.MustCompile(`(?is)^\s*(?:WITH\b.*?\)\s*)?SELECT\s+(?:ALL\s+|DISTINCT\s+)?`)
var fromWordRe = regexp.MustCompile(`(?is)\bFROM\b`)

// selectListItems returns the top-level items of the SELECT list.
//
// ok is false when the shape cannot be read positionally — no SELECT, no FROM, or
// a `*` anywhere in the list. `*` is not a failure of this function so much as a
// case that does not need it: with a star the driver hands back the real column
// names, which the name pass already matches.
func selectListItems(sql string) (items []string, ok bool) {
	head := selectHeadRe.FindStringIndex(sql)
	if head == nil {
		return nil, false
	}
	rest := sql[head[1]:]
	// 选择列表到第一个顶层 FROM 为止。
	end := len(rest)
	depth := 0
	for _, loc := range fromWordRe.FindAllStringIndex(rest, -1) {
		depth = parenDepth(rest[:loc[0]])
		if depth == 0 {
			end = loc[0]
			break
		}
	}
	list := rest[:end]
	if strings.Contains(list, "*") {
		return nil, false
	}
	for _, it := range splitTopLevelCommas(list) {
		if s := strings.TrimSpace(it); s != "" {
			items = append(items, s)
		}
	}
	return items, len(items) > 0
}

func parenDepth(s string) int {
	d := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			d++
		case ')':
			if d > 0 {
				d--
			}
		}
	}
	return d
}

func splitTopLevelCommas(s string) []string {
	var out []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	return append(out, s[start:])
}

// mentionsColumn reports whether a select-list item computes from the named
// column. Word boundaries matter: `id` must not match `id_card`.
func mentionsColumn(item, column string) bool {
	col := strings.TrimSpace(column)
	if col == "" {
		return false
	}
	re, err := regexp.Compile(`(?i)\b` + regexp.QuoteMeta(col) + `\b`)
	if err != nil {
		return false
	}
	return re.MatchString(item)
}

// MaskValue applies one masking style to one cell.
//
// 空值保持空:把 NULL 打成 *** 会让人以为那里本来有数据,而"这一格是空的"本身
// 通常不是秘密。
func MaskValue(v, style string) string {
	if v == "" {
		return ""
	}
	switch style {
	case MaskFull:
		return "******"
	case MaskHash:
		sum := sha256.Sum256([]byte(v))
		return "#" + hex.EncodeToString(sum[:])[:10]
	}
	// partial:留头留尾。短值整体抹掉 —— 一个 4 位数留头留尾等于没打。
	r := []rune(v)
	if len(r) <= 8 {
		return strings.Repeat("*", len(r))
	}
	return string(r[:3]) + strings.Repeat("*", len(r)-7) + string(r[len(r)-4:])
}

// MaskRows masks the given column indices in place. It is the single place rows
// are rewritten, so a new result path only has to call this one function.
func MaskRows(data [][]string, targets []int, styleOf map[int]string) {
	if len(targets) == 0 {
		return
	}
	for _, row := range data {
		for _, i := range targets {
			if i < len(row) {
				row[i] = MaskValue(row[i], styleOf[i])
			}
		}
	}
}

// SensitiveRulesProvider supplies the current rule set. It is a hook rather than
// a parameter because the two places rows are read (RealRun, RealQueryEach) are
// package functions reached from half a dozen callers; threading the rules
// through all of them would mean each new caller could forget to.
//
// Unset (the zero value) means no masking — which is the right default for the
// simulated executor and the tests, and safe because it can only ever be widened
// by configuration, never narrowed by a missing wire-up.
var SensitiveRulesProvider func() []SensitiveRule

func currentSensitiveRules() []SensitiveRule {
	if SensitiveRulesProvider == nil {
		return nil
	}
	return SensitiveRulesProvider()
}

// maskResultSet masks in place and returns the names of the columns it touched.
//
// 返回列名是为了让界面能标"该列已脱敏" —— 数据本身早已是打码后的,前端不做任何
// 脱敏工作,这一点是这个设计的全部意义。
func maskResultSet(sql string, cols []string, data [][]string) []string {
	rules := currentSensitiveRules()
	targets := SensitiveMaskTargets(sql, cols, rules)
	if len(targets) == 0 {
		return nil
	}
	styleOf := make(map[int]string, len(targets))
	names := make([]string, 0, len(targets))
	for _, i := range targets {
		styleOf[i] = styleFor(cols[i], rules)
		names = append(names, cols[i])
	}
	MaskRows(data, targets, styleOf)
	return names
}

// styleFor picks the masking style configured for a column; the first rule whose
// column matches wins, and anything unconfigured falls back to partial.
func styleFor(col string, rules []SensitiveRule) string {
	lc := strings.ToLower(strings.TrimSpace(col))
	for _, r := range rules {
		if strings.ToLower(strings.TrimSpace(r.Column)) == lc && r.Style != "" {
			return r.Style
		}
	}
	return MaskPartial
}

// maskStream builds a per-row masker for the streaming path (exports). The
// column set is known once, at header time, so the targets are computed once and
// every row goes through the same rewrite.
func maskStream(sql string, cols []string) (apply func([]string), masked []string) {
	rules := currentSensitiveRules()
	targets := SensitiveMaskTargets(sql, cols, rules)
	if len(targets) == 0 {
		return func([]string) {}, nil
	}
	styleOf := make(map[int]string, len(targets))
	names := make([]string, 0, len(targets))
	for _, i := range targets {
		styleOf[i] = styleFor(cols[i], rules)
		names = append(names, cols[i])
	}
	return func(row []string) {
		for _, i := range targets {
			if i < len(row) {
				row[i] = MaskValue(row[i], styleOf[i])
			}
		}
	}, names
}

// setOpRe finds a set operator that joins two query branches.
var setOpRe = regexp.MustCompile(`(?i)\b(UNION\s+ALL|UNION|INTERSECT|EXCEPT|MINUS)\b`)

// unionBranches 把一条语句按**顶层**的集合运算符切成各个查询分支。
//
// 只切顶层:子查询里的 UNION(`WHERE id IN (SELECT … UNION SELECT …)`)不是这条语句的
// 分支,它的列不出现在结果集里,按位置去对会整个错位。
//
// 没有集合运算符时返回整条语句本身 —— 调用方因此不必分两种写法。
func unionBranches(sql string) []string {
	var out []string
	last := 0
	for _, loc := range setOpRe.FindAllStringIndex(sql, -1) {
		if parenDepth(sql[:loc[0]]) != 0 {
			continue // 子查询里的,不是这条语句的分支
		}
		out = append(out, sql[last:loc[0]])
		last = loc[1]
	}
	out = append(out, sql[last:])
	return out
}
