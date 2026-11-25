package sqlutil

// 一条语句引用了哪些对象 —— 用于发布收单时的对象检查。
//
// ## 这不是解析器,而且刻意不是
//
// 本仓库的立场写在 ADR 0008 里:**网关不做完整 SQL 解析,也不该做**。一个半吊子的
// 解析器会在每个引擎的方言上出新的错。这里只做一件很窄的事:认出几个关键字后面
// 跟着的那个名字。
//
// ## 认不出来要说"认不出",不能说"不存在"
//
// 这是整个文件最要紧的一条。对象检查的产出会摆在发布单上给人看,而:
//
//   - 一句"表 t_order 不存在"会让人去建表,或者以为发布环境配错了;
//   - 一句"这条语句的引用对象没能识别"只是让人自己看一眼。
//
// 前者在认错的时候会让人做出错误的动作,后者不会。所以凡是拿不准的形状 —— 子查询、
// CTE、动态 SQL、带函数的 FROM —— 一律标成未识别,而不是猜一个名字出来。
//
// ## 三类引用分开
//
//   Requires  这条语句要求它**已经存在**
//   Creates   这条语句会创建它
//   Drops     这条语句会删掉它
//
// 分开是因为同一批脚本里 `CREATE TABLE t; INSERT INTO t …` 是完全正常的写法:第二条
// 的 t 在提交那一刻确实不存在。不区分的话,每一份"建表 + 灌数据"的脚本都会报一堆假的
// "对象不存在"。

import "strings"

// Ref 是一个被引用到的对象名。Schema 为空表示语句里没写限定名 ——
// 那不等于"在默认 schema 下",只是"没写"。解析成哪个 schema 是调用方的事。
type Ref struct {
	Schema string
	Name   string
}

// Refs 是一条语句的对象引用。
//
// Recognised 为 false 表示这条语句的形状没被认出来 —— 此时另外三个切片里的东西
// 可能不完整,调用方必须把它当成"不知道",而不是"没有引用任何对象"。
type Refs struct {
	Requires   []Ref
	Creates    []Ref
	Drops      []Ref
	Recognised bool
}

// ObjectRefs 认出一条语句引用的对象。
//
// 它接受的是**单条**语句(SplitStatements 的产物),注释已经被剥掉。
func ObjectRefs(sql string) Refs {
	toks := tokenize(maskLiterals(sql))
	if len(toks) == 0 {
		return Refs{}
	}
	switch up(toks[0]) {
	case "INSERT":
		// INSERT INTO t / INSERT t(不带 INTO 的 MySQL 写法)
		return requiresAfter(toks, []string{"INTO"}, 1)
	case "UPDATE":
		return requiresAt(toks, 1)
	case "DELETE":
		return requiresAfter(toks, []string{"FROM"}, 1)
	case "TRUNCATE":
		return requiresAfter(toks, []string{"TABLE"}, 1)
	case "ALTER":
		if len(toks) > 1 && up(toks[1]) == "TABLE" {
			return requiresAt(toks, 2)
		}
		// ALTER INDEX / ALTER SESSION / ALTER USER … 不在这里判。
		return Refs{}
	case "DROP":
		return dropRefs(toks)
	case "CREATE":
		return createRefs(toks)
	case "SELECT", "WITH", "TABLE", "VALUES":
		// SELECT 的引用来自 FROM / JOIN。WITH(CTE)里的名字不是表,所以
		// 带 CTE 的语句一律不认 —— 见 fromJoinRefs。
		if up(toks[0]) == "WITH" {
			return Refs{}
		}
		return fromJoinRefs(toks)
	}
	return Refs{}
}

// requiresAt 把第 i 个词当作对象名。
func requiresAt(toks []string, i int) Refs {
	r, ok := refAt(toks, i)
	if !ok {
		return Refs{}
	}
	return Refs{Requires: []Ref{r}, Recognised: true}
}

// requiresAfter 跳过可选的关键字(INTO / TABLE …)之后取对象名。
func requiresAfter(toks []string, optional []string, i int) Refs {
	for i < len(toks) && contains(optional, up(toks[i])) {
		i++
	}
	// MySQL 的 INSERT 允许 LOW_PRIORITY / DELAYED / IGNORE 等修饰词。
	for i < len(toks) && isInsertModifier(up(toks[i])) {
		i++
	}
	for i < len(toks) && contains(optional, up(toks[i])) {
		i++
	}
	return requiresAt(toks, i)
}

func isInsertModifier(t string) bool {
	switch t {
	case "LOW_PRIORITY", "DELAYED", "HIGH_PRIORITY", "IGNORE", "ONLY", "OVERWRITE":
		return true
	}
	return false
}

func dropRefs(toks []string) Refs {
	if len(toks) < 2 {
		return Refs{}
	}
	i := 1
	kind := up(toks[i])
	if kind != "TABLE" && kind != "VIEW" {
		return Refs{} // DROP INDEX / SEQUENCE / USER … 不在这里判
	}
	i++
	i = skipIfExists(toks, i)
	r, ok := refAt(toks, i)
	if !ok {
		return Refs{}
	}
	// IF EXISTS 的 DROP 不要求对象存在 —— 那正是它的意思。
	if hadIfExists(toks) {
		return Refs{Drops: []Ref{r}, Recognised: true}
	}
	return Refs{Requires: []Ref{r}, Drops: []Ref{r}, Recognised: true}
}

func createRefs(toks []string) Refs {
	i := 1
	// CREATE OR REPLACE VIEW / CREATE UNIQUE INDEX / CREATE GLOBAL TEMPORARY TABLE
	for i < len(toks) && isCreateModifier(up(toks[i])) {
		i++
	}
	if i >= len(toks) {
		return Refs{}
	}
	switch up(toks[i]) {
	case "TABLE":
		i = skipIfNotExists(toks, i+1)
		r, ok := refAt(toks, i)
		if !ok {
			return Refs{}
		}
		// CREATE TABLE … AS SELECT 还引用了源表,而源表必须已经存在。
		out := Refs{Creates: []Ref{r}, Recognised: true}
		if src := fromJoinRefs(toks); src.Recognised {
			out.Requires = src.Requires
		}
		return out
	case "VIEW":
		r, ok := refAt(toks, i+1)
		if !ok {
			return Refs{}
		}
		out := Refs{Creates: []Ref{r}, Recognised: true}
		if src := fromJoinRefs(toks); src.Recognised {
			out.Requires = src.Requires
		}
		return out
	case "INDEX":
		// CREATE INDEX idx ON t (…) —— 要求 t 存在,索引名本身不查。
		for j := i; j < len(toks); j++ {
			if up(toks[j]) == "ON" {
				return requiresAt(toks, j+1)
			}
		}
		return Refs{}
	}
	return Refs{}
}

func isCreateModifier(t string) bool {
	switch t {
	case "OR", "REPLACE", "UNIQUE", "GLOBAL", "LOCAL", "TEMPORARY", "TEMP", "MATERIALIZED", "UNLOGGED":
		return true
	}
	return false
}

// fromJoinRefs 取 FROM / JOIN 后面的表名。
//
// 拿不准就整条不认:
//   - FROM ( …          子查询,后面那个名字是个别名不是表;
//   - 语句里有 SELECT 之外的括号嵌套时,这个扫描本来就不可靠。
//
// 宁可整条说"认不出",也不要报一个猜出来的名字 —— 见文件顶部。
func fromJoinRefs(toks []string) Refs {
	out := Refs{}
	seen := map[string]bool{}
	found := false
	for i := 0; i < len(toks); i++ {
		t := up(toks[i])
		if t != "FROM" && t != "JOIN" {
			continue
		}
		if i+1 >= len(toks) {
			return Refs{}
		}
		next := toks[i+1]
		if next == "(" {
			return Refs{} // 子查询 / 表值函数:不猜
		}
		r, ok := refAt(toks, i+1)
		if !ok {
			return Refs{}
		}
		// FROM t AS x 里的 t 才是表;但 FROM f(…) 这种函数调用要排除。
		if i+2 < len(toks) && toks[i+2] == "(" {
			return Refs{}
		}
		key := r.Schema + "." + r.Name
		if !seen[key] {
			seen[key] = true
			out.Requires = append(out.Requires, r)
		}
		found = true
	}
	out.Recognised = found
	return out
}

// refAt 把第 i 个词(可能带 schema 限定)读成一个对象名。
func refAt(toks []string, i int) (Ref, bool) {
	if i < 0 || i >= len(toks) {
		return Ref{}, false
	}
	name := unquote(toks[i])
	if name == "" || !isIdentStart(name[0]) {
		return Ref{}, false
	}
	// a.b / a.b.c —— 取最后两段:限定名的最后一段是对象,倒数第二段是 schema。
	parts := strings.Split(name, ".")
	for _, p := range parts {
		if p == "" {
			return Ref{}, false // 形如 a..b,不认
		}
	}
	switch len(parts) {
	case 1:
		return Ref{Name: parts[0]}, true
	default:
		return Ref{Schema: parts[len(parts)-2], Name: parts[len(parts)-1]}, true
	}
}

func skipIfExists(toks []string, i int) int {
	if i+1 < len(toks) && up(toks[i]) == "IF" && up(toks[i+1]) == "EXISTS" {
		return i + 2
	}
	return i
}

func skipIfNotExists(toks []string, i int) int {
	if i+2 < len(toks) && up(toks[i]) == "IF" && up(toks[i+1]) == "NOT" && up(toks[i+2]) == "EXISTS" {
		return i + 3
	}
	return i
}

func hadIfExists(toks []string) bool {
	for i := 0; i+1 < len(toks) && i < 6; i++ {
		if up(toks[i]) == "IF" && up(toks[i+1]) == "EXISTS" {
			return true
		}
	}
	return false
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func up(s string) string { return strings.ToUpper(s) }

// maskLiterals 把字符串字面量的内容换成空格,免得 'DROP TABLE x' 这样的值被当成语句。
// 引号本身留着,所以词的边界不变。
func maskLiterals(s string) string {
	out := []byte(s)
	for i := 0; i < len(out); i++ {
		c := out[i]
		if c != '\'' && c != '"' {
			continue
		}
		// 双引号在 PG/Oracle 里是标识符引用,不能当字面量抹掉 —— 只抹单引号。
		if c == '"' {
			for j := i + 1; j < len(out); j++ {
				if out[j] == '"' {
					i = j
					break
				}
			}
			continue
		}
		for j := i + 1; j < len(out); j++ {
			if out[j] == '\'' {
				if j+1 < len(out) && out[j+1] == '\'' { // 双写转义
					out[j], out[j+1] = ' ', ' '
					j++
					continue
				}
				i = j
				break
			}
			out[j] = ' '
		}
	}
	return string(out)
}

// tokenize 切成词。括号和逗号自成一个词(FROM ( 要认得出),其余按空白与标点切。
// 反引号 / 双引号 / 方括号引起来的标识符整体算一个词,里面的点不切。
func tokenize(s string) []string {
	out := []string{}
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '`' || c == '"' || c == '[':
			end := byte('`')
			if c == '"' {
				end = '"'
			} else if c == '[' {
				end = ']'
			}
			cur.WriteByte(c)
			for i++; i < len(s); i++ {
				cur.WriteByte(s[i])
				if s[i] == end {
					break
				}
			}
		case c == '(' || c == ')' || c == ',' || c == ';':
			flush()
			out = append(out, string(c))
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			flush()
		default:
			cur.WriteByte(c)
		}
	}
	flush()
	return out
}

// unquote 去掉标识符两侧的引号,`a`.`b` → a.b。
func unquote(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '`', '"', '[', ']':
			continue
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c >= 0x80
}
