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
	"unicode"
	"unicode/utf8"
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
//	1. **按列名**:回传的列名就是敏感字段名。`SELECT *` 落在这一步,而且它恰恰是
//	   最安全的形态 —— 驱动回传的就是真实列名。
//	2. **按算法**:选择列表第 k 项的表达式里提到了敏感字段,第 k 列就打码。
//	   `SELECT id_card AS x`、`SELECT SUBSTR(id_card,1,6) AS x` 都在这一步兜住。
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
	plan := sensitiveMaskPlan(sql, cols, rules)
	out := make([]int, 0, len(plan))
	for i := range cols {
		if _, ok := plan[i]; ok {
			out = append(out, i)
		}
	}
	return out
}

// sensitiveMaskPlan is SensitiveMaskTargets plus HOW each target is masked:
// column index → style.
//
// 样式跟着**数据来自哪个字段**走,不跟着结果列叫什么走。原先按结果列名查样式,
// `id_card AS x` 查不到、回落成 partial —— 规则写的 full 被降成"留头 3 位、尾 4 位",
// 几个错开起点的 SUBSTR 窗口就能拼回全号;`id_card AS phone` 则套用了 phone 更弱的
// 样式。所以一列的样式取它的列名、以及它表达式里提到的每个敏感字段,其中**最严**的那个。
func sensitiveMaskPlan(sql string, cols []string, rules []SensitiveRule) map[int]string {
	if len(cols) == 0 || len(rules) == 0 {
		return nil
	}
	// 两种字面量理解各算一次、取并集(backslash.go):MySQL 的 \' 能把表名和改名表达式
	// 伪装成字符串,按表限定的规则于是不激活。多打码是本函数认可的方向。
	ix := newSensitiveIndex(rules)
	plan := map[int]string{}
	for _, r := range sqlReadings(sql) {
		for i, st := range sensitiveMaskTargets(r, cols, ix) {
			plan[i] = stricterStyle(plan[i], st)
		}
	}
	return plan
}

// stricterStyle returns whichever of two styles reveals less: full hides the
// value, hash still shows which rows are equal, partial shows head and tail.
func stricterStyle(a, b string) string {
	rank := func(s string) int {
		switch s {
		case "":
			return 0
		case MaskFull:
			return 3
		case MaskHash:
			return 2
		}
		return 1 // partial,以及 MaskValue 按 partial 处理的未知样式
	}
	if rank(b) > rank(a) {
		return b
	}
	return a
}

func sensitiveMaskTargets(sql string, cols []string, ix *sensitiveIndex) map[int]string {
	stripped := StripComments(sql)
	clean := blankQuoted(stripped) // 字面量抹掉:'id_card' 只是一段文本
	// 表名**不能**从 clean 里取:blankQuoted 连带把引号标识符的内容也抹成了空格,于是
	// `SELECT * FROM ` + "`t_user`" + ` 里的表名整个消失,按表限定的规则就不激活 ——
	// 一条本该打码的 id_card 直接原样回传。反引号在 MySQL 侧是 ORM 和图形客户端的默认
	// 写法,双引号在 PG / Oracle 上同样是常态,所以这条漏法根本不需要谁刻意去构造。
	//
	// 取表名只抹**单引号字面量**:那才是"数据可能被当成语法"的来源。留下来的双引号 /
	// 反引号内容至多让一个不存在的表名被当真,而那个方向是多打码 —— 与本函数开头写的
	// "宁可多打"一致。
	tables := referencedTables(blankStringLiterals(stripped))

	// 生效的规则字段先建成查找表,下面两步都是查表,不随规则条数变慢(见 sensitive_index.go)
	active := ix.active(tables)
	if active.empty() {
		return nil
	}

	hit := make(map[int]string, len(cols))
	// 第一步:列名
	for i, c := range cols {
		if name := strings.ToLower(strings.TrimSpace(c)); active.whole[name] {
			hit[i] = ix.styleFor(name)
		}
	}
	// 第二步:选择列表里第 k 项是怎么算出来的。按列名已经命中的也要看 —— 它决定样式:
	// `id_card AS phone` 的数据是 id_card 的。
	//
	// 只在项数与列数一一对应时才做:出现 `*` 或项数对不上时位置会错位,而错位地
	// 打码比不打更糟 —— 它打了不该打的,却漏了该打的。
	//
	// 切分在 clean 上做(引号标识符里的逗号、FROM 不能把列表切错位),比对却要在只抹了
	// 单引号字面量的文本上做:`id_card` / "id_card" 就是那一列,连它一起抹掉,
	// `SELECT "id_card" AS x` 就原值回传了。两种抹法都保长度,所以下标通用。
	idents := blankStringLiterals(stripped)
	spans, ok := selectListSpans(clean)
	mark := func(item string, i int) {
		active.eachMentioned(item, func(col string) {
			hit[i] = stricterStyle(hit[i], ix.styleFor(col))
		})
	}
	switch {
	case ok && len(spans) == len(cols):
		for i, sp := range spans {
			mark(idents[sp[0]:sp[1]], i)
		}
	case ok && len(cols) >= len(spans)-1 && starCount(clean, spans) == 1:
		// 只有一个 `*` / `t.*`:它前面的项从左对位,后面的项从右对位 —— 位置是确定的。
		// 从前项数一对不上就整步跳过,`SELECT *, id_card AS x` 的 x 于是原值回传(2026-10 安全审计 H1)。
		star := starIndex(clean, spans)
		for k, sp := range spans {
			switch {
			case k < star:
				mark(idents[sp[0]:sp[1]], k)
			case k > star:
				mark(idents[sp[0]:sp[1]], len(cols)-(len(spans)-k))
			}
		}
	case ok:
		// 是 SELECT,却对不上位(多个星号):选择列表里提到了敏感列,就整张结果按最严的样式
		// 打码。错位地打会漏掉该打的,而这里宁可多打(H1)。
		//
		// 只在 ok 时这样做。认不出选择列表的语句 —— EXPLAIN、SHOW、CALL —— 返回的不是表里
		// 的数据行:EXPLAIN 子查询里提到了敏感列,执行计划就被整张打成 ******(2026-10-22 生产)。
		// 它们只走上面按列名的那一步,与 H1 之前一样。
		style := ""
		active.eachMentioned(idents[spans[0][0]:spans[len(spans)-1][1]], func(col string) {
			style = stricterStyle(style, ix.styleFor(col))
		})
		if style != "" {
			for i := range cols {
				hit[i] = stricterStyle(hit[i], style)
			}
		}
	}
	return hit
}

// isStarItem:选择列表的这一项是 `*` 或 `t.*`(引号标识符在 clean 里已抹成空格,形如 `"   ".*`)。
func isStarItem(item string) bool {
	item = strings.TrimSpace(item)
	return item == "*" || strings.HasSuffix(item, ".*")
}

func starCount(clean string, spans [][2]int) int {
	n := 0
	for _, sp := range spans {
		if isStarItem(clean[sp[0]:sp[1]]) {
			n++
		}
	}
	return n
}

func starIndex(clean string, spans [][2]int) int {
	for k, sp := range spans {
		if isStarItem(clean[sp[0]:sp[1]]) {
			return k
		}
	}
	return -1
}

// nestedShapeRe 认的是"选择列表里的名字可能已经映射不回原列"的那几种句式:
// 派生表 / 子查询、UNION、CTE。它们的共同点是**外层看到的列名由内层决定**,而这里
// 的打码是拿选择列表的文本去比对的。
//
// 派生表能站的位置不只是 FROM / JOIN 后面紧跟一个括号:多套几层括号
// (`FROM ((SELECT …))`)、逗号联表(`FROM a, (SELECT …) s`)、LATERAL 都是它(F4)。
// 逗号也会命中选择列表里的标量子查询 —— 那一项的文本里本来就写着敏感字段,多送一次
// 审批是认可的方向。WHERE 里的 `IN (SELECT …)` 不在此列:它不产出列。
var nestedShapeRe = regexp.MustCompile(`(?is)\bUNION\b|\bWITH\b|(?:\bFROM|\bJOIN|\bLATERAL|,)\s*(?:\(\s*)+SELECT\b`)

// MaskMayBeEvaded 报告这条查询是否处在"打码可能兜不住"的形态上 ——
// 既碰了带敏感规则的表,又用了派生表 / UNION / CTE 这类会重命名列的句式。
//
// 为什么需要它:打码是按**选择列表的文本**比对的,而
//
//	SELECT x FROM (SELECT id_card AS x FROM t_user) s
//
// 的外层选择列表里只有一个 `x`,既不是敏感列名,表达式里也没提到它 —— 两步都不命中,
// 于是原值原样回传(M-10)。终端上这只是"顺手看到",能接受;**导出不一样**:它产出的
// 是一份可以带走的 CSV,而且正是"敏感导出要审批"这条控制要拦的东西。
//
// 所以这里不试图去解析 SQL 把列血缘算准 —— 那要一个真正的解析器,而算错的方向是漏。
// 这里只回答一个保守得多的问题:这条查询**有没有可能**绕过打码?有,就让它去走审批。
// 代价是一些本来干净的查询也要审批一次;而反过来的代价是身份证号成批地离开网关。
func MaskMayBeEvaded(sql string, rules []SensitiveRule) bool {
	return MaskEvasionColumn(sql, rules) != ""
}

// MaskEvasionColumn 返回可能被嵌套结构带出打码的那个敏感字段名;没有则为空串。
//
// 比"有嵌套就拦"多问一句:**SQL 文本里提没提到这个字段**。绕过打码的唯一办法是把敏感
// 字段改个名(id_card AS x、SUBSTR(id_card,…) AS x),而改名这件事在文本里一定会写出
// id_card 这个词。一条从头到尾没提 id_card 的 CTE,输出里不可能凭空冒出一列 id_card
// 的值 —— 哪怕它 SELECT *,结果集的列名也是真名,打码按列名照样兜住。
//
// 这一句把"所有 WITH 都进审批"收窄成"提到了敏感字段的 WITH 才进审批"。规则写成 *
// 不限表的部署(现网就是),之前是每条 CTE 都要审 —— 一道逢查询就要审批的闸,两天之内
// 就会被要求关掉。
//
// 仍然保守的地方:提到了敏感字段、但输出列名其实没改(WITH u AS (SELECT id_card FROM t)
// SELECT id_card FROM u)也会进审批。分辨这个要真正的列血缘,而算错的方向是漏。
func MaskEvasionColumn(sql string, rules []SensitiveRule) string {
	if len(rules) == 0 {
		return ""
	}
	for _, r := range sqlReadings(sql) { // 两种字面量理解都看,见 backslash.go
		if c := maskEvasionColumn(r, rules); c != "" {
			return c
		}
	}
	return ""
}

func maskEvasionColumn(sql string, rules []SensitiveRule) string {
	if !nestedShapeRe.MatchString(blankQuoted(StripComments(sql))) {
		return "" // 形态是平的:选择列表里的名字就是表上的名字
	}
	// 文本里根本没提到敏感字段,改名无从谈起(见 sensitive_error.go)
	return sensitiveMentioned(sql, rules)
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

// referencedTables collects every identifier the statement mentions, lower-cased —
// a superset of its table names. A rule scoped to `t_user` activates whenever
// `t_user` appears as a whole identifier anywhere (`dwd.t_user`, "T_USER", …).
//
// 从前只认 FROM / JOIN 后面紧跟的那一个名字,于是逗号联表(FROM a, t_user)、
// STRAIGHT_JOIN、PG 的 FROM ONLY、派生表后面再逗号接的表,统统认不出来 —— 按表限定的
// 规则不激活,原值直接回传。表引用的合法写法太多,按句式去认总会漏;而把所有标识符都
// 当作"可能是表名",代价只是某个别名或列名恰好与规则表同名时多打一次码,正是"宁可多打"
// 的方向。调用方负责先抹掉单引号字面量:'t_user' 只是一段文本。
func referencedTables(sql string) map[string]bool {
	out := map[string]bool{}
	eachIdent(strings.ToLower(sql), func(w string) bool {
		out[w] = true
		return true
	})
	return out
}

var selectHeadRe = regexp.MustCompile(`(?is)^\s*(?:WITH\b.*?\)\s*)?SELECT\s+(?:ALL\s+|DISTINCT\s+)?`)
var fromWordRe = regexp.MustCompile(`(?is)\bFROM\b`)

// selectListSpans returns the top-level items of the SELECT list as byte offsets
// into sql, so the same items can be read out of another, equally long rendering
// of the statement (see sensitiveMaskTargets).
//
// ok is false when there is no SELECT list to read. A `*` item is returned like
// any other; the caller decides how to line the items up with the result columns.
//
// 从前列表里出现任何 `*` 就返回 ok=false —— 连 count(*)、乘号都算。理由是"有星号时驱动回传
// 真列名,按列名就够了",但星号旁边另写一项改名的敏感列(`SELECT *, id_card AS x`、
// `SELECT count(*) n, id_card x`)就整个漏了(2026-10 安全审计 H1)。
func selectListSpans(sql string) (spans [][2]int, ok bool) {
	head := selectHeadRe.FindStringIndex(sql)
	if head == nil {
		return nil, false
	}
	base := head[1]
	rest := sql[base:]
	// 选择列表到第一个顶层 FROM 为止。
	end := len(rest)
	for _, loc := range fromWordRe.FindAllStringIndex(rest, -1) {
		if parenDepth(rest[:loc[0]]) == 0 {
			end = loc[0]
			break
		}
	}
	list := rest[:end]
	start := 0
	for _, it := range splitTopLevelCommas(list) {
		lo, hi := start, start+len(it)
		start = hi + 1 // 跳过逗号
		for lo < hi && isSpaceByte(list[lo]) {
			lo++
		}
		for hi > lo && isSpaceByte(list[hi-1]) {
			hi--
		}
		if lo < hi {
			spans = append(spans, [2]int{base + lo, base + hi})
		}
	}
	return spans, len(spans) > 0
}

func isSpaceByte(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v'
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
	// 词边界自己写,不用 \b:Go 正则的 \b **只认 ASCII**。在"身份证"两侧,一边是空格、
	// 一边是汉字,谁都不是 ASCII 词字符,于是根本没有边界 —— \b身份证\b 永远匹配不上,
	// 中文字段名的规则整个失效:既不打码,也不进审批。规则字段名是什么文字,不该决定
	// 它管不管用。这里把"任何字母/数字/下划线/$/#"都当作词字符($ 与 # 是 Oracle、DWS
	// 合法的标识符字符,与 referencedTables 的口径一致)。
	//
	// 不用正则:从前每调用一次就现场编译一个带 Unicode 字符类的正则,而它的调用次数是
	// "选择列表项数 × 敏感规则数" —— 一条 175 列的 SELECT 配几十条规则,就是上万次编译,
	// 生产上一条只返回 0 行的查询在网关里卡了 8 秒(打码前要先算出哪几列要打码,0 行也躲不开)。
	// 改成在小写化的文本里找,再按同一口径判断两侧是不是词字符。
	li, lc := strings.ToLower(item), strings.ToLower(col)
	for from := 0; from <= len(li); {
		j := strings.Index(li[from:], lc)
		if j < 0 {
			return false
		}
		start := from + j
		end := start + len(lc)
		if !identRuneBefore(li, start) && !identRuneAt(li, end) {
			return true
		}
		from = start + 1
	}
	return false
}

// isIdentRune:与 mentionsColumn 原先的正则 [\pL\pN_$#] 同一口径。
func isIdentRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsNumber(r) || r == '_' || r == '$' || r == '#'
}

func identRuneBefore(s string, i int) bool {
	if i <= 0 {
		return false
	}
	r, _ := utf8.DecodeLastRuneInString(s[:i])
	return isIdentRune(r)
}

func identRuneAt(s string, i int) bool {
	if i >= len(s) {
		return false
	}
	r, _ := utf8.DecodeRuneInString(s[i:])
	return isIdentRune(r)
}

// MaskValue applies one masking style to one cell.
//
// maskSalt 是 MaskHash 的部署盐,bootstrap 期由 SetMaskSalt 灌入(派生自本地密钥)。
// 空盐会退化成无盐 SHA-256 —— 那是 L-11 描述的可离线还原的状态,所以设不上时
// SetMaskSalt 会拒绝把它清空。
var maskSalt []byte

// SetMaskSalt 设置脱敏盐。调用发生在 bootstrap,早于任何请求。
func SetMaskSalt(b []byte) {
	if len(b) == 0 {
		return // 宁可保留上一次的盐,也不要静默退回无盐
	}
	maskSalt = append([]byte{}, b...)
}

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
		// **加盐**。无盐的 SHA-256 对低熵值等于没打码:手机号一共 11 位、身份证号的
		// 结构是公开的,一台笔记本几分钟就能把全空间跑完再反查回去(L-11)。盐是每套
		// 部署一份、来自本地密钥,链外的人没有它就跑不了这张彩虹表。
		//
		// 同一套部署里同一个值仍然得到同一个哈希 —— 这正是 hash 这种打码方式存在的
		// 理由:看不到原值,但能看出"这两行是同一个人"。换部署之后对不上,那是对的:
		// 两套部署之间本来就不该能靠打码值做关联。
		sum := sha256.Sum256(append(append([]byte{}, maskSalt...), v...))
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
	targets, styleOf, names := maskPlanFor(sql, cols)
	if len(targets) == 0 {
		return nil
	}
	MaskRows(data, targets, styleOf)
	return names
}

// maskStream builds a per-row masker for the streaming path (exports). The
// column set is known once, at header time, so the targets are computed once and
// every row goes through the same rewrite.
func maskStream(sql string, cols []string) (apply func([]string), masked []string) {
	targets, styleOf, names := maskPlanFor(sql, cols)
	if len(targets) == 0 {
		return func([]string) {}, nil
	}
	return func(row []string) {
		for _, i := range targets {
			if i < len(row) {
				row[i] = MaskValue(row[i], styleOf[i])
			}
		}
	}, names
}

// maskPlanFor resolves the current rules into the columns to mask, in column
// order, with each one's style (see sensitiveMaskPlan) and name.
func maskPlanFor(sql string, cols []string) (targets []int, styleOf map[int]string, names []string) {
	styleOf = sensitiveMaskPlan(sql, cols, currentSensitiveRules())
	for i := range cols {
		if _, ok := styleOf[i]; ok {
			targets = append(targets, i)
			names = append(names, cols[i])
		}
	}
	return targets, styleOf, names
}
