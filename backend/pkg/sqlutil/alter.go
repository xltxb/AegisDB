package sqlutil

// ALTER TABLE 到底在改什么 —— 用于收单时的字段与约束检查。
//
// ## 和 refs.go 同一条规矩
//
// 这里也不是解析器。它只认几种**日常会写**的形状,认不出来就说认不出(Recognised=false),
// 绝不猜。理由同样是认错的方向:一句"字段 c 已存在"会让人去改脚本、或者以为环境不对;
// 一句"这条 ALTER 的形状没能识别"只是让人自己看一眼。
//
// ## 认哪几种
//
//	ADD [COLUMN] c TYPE [NOT NULL] [DEFAULT …]     c 必须**不存在**;NOT NULL 且无默认值要探数据
//	ADD (c TYPE …)                                 Oracle 的括号写法,同上
//	DROP [COLUMN] c                                c 必须存在
//	MODIFY [COLUMN] c TYPE [NOT NULL]              c 必须存在;改成 NOT NULL 要探数据
//	CHANGE [COLUMN] old new TYPE                   old 必须存在,new 必须不存在
//	ALTER [COLUMN] c SET NOT NULL                  c 必须存在;要探数据
//	ADD [CONSTRAINT x] UNIQUE [KEY|INDEX] [i] (c…) 要探数据(有没有重复值)
//
// 外键、CHECK、分区、改类型的宽窄,一律不认 —— 它们要么形状太多,要么"会不会违反"
// 没法用一条便宜的探测回答。不认就在结果里说没检查,而不是默默放过。

import "strings"

// AlterKind 是一次改动的种类。
type AlterKind string

const (
	AlterAddColumn    AlterKind = "add-column"
	AlterDropColumn   AlterKind = "drop-column"
	AlterModifyColumn AlterKind = "modify-column"
	AlterChangeColumn AlterKind = "change-column"
	AlterSetNotNull   AlterKind = "set-not-null"
	AlterAddUnique    AlterKind = "add-unique"
)

// AlterOp 是 ALTER TABLE 里的一条改动。
type AlterOp struct {
	Kind AlterKind
	// Column 是被动的那一列(CHANGE 时是旧名)。
	Column string
	// NewColumn 只有 CHANGE 有:改完之后的名字。
	NewColumn string
	// Columns 只有 AddUnique 有:约束涉及的列。
	Columns []string
	// NotNull 说明这次改动会让该列变成非空 —— 那是要探数据的信号。
	NotNull bool
	// HasDefault 说明加列时带了默认值。带了就不会因为表里有数据而失败。
	HasDefault bool
}

// AlterOps 拆开一条 ALTER TABLE。
//
// Recognised 为 false 表示**整条**没认出来 —— 调用方此时不该下任何结论。只要有一个
// 子句没认出来就整条不认:一条 ALTER 里的子句是一起成败的,报一半结论会让人以为
// 另一半没问题。
func AlterOps(sql string) (table Ref, ops []AlterOp, recognised bool) {
	toks := tokenize(maskLiterals(sql))
	if len(toks) < 3 || up(toks[0]) != "ALTER" || up(toks[1]) != "TABLE" {
		return Ref{}, nil, false
	}
	i := 2
	// PostgreSQL 的 ALTER TABLE [IF EXISTS] [ONLY] t
	for i < len(toks) && (up(toks[i]) == "IF" || up(toks[i]) == "EXISTS" || up(toks[i]) == "ONLY") {
		i++
	}
	t, ok := refAt(toks, i)
	if !ok {
		return Ref{}, nil, false
	}
	i++

	for _, clause := range splitClauses(toks[i:]) {
		op, ok := parseAlterClause(clause)
		if !ok {
			return t, nil, false
		}
		ops = append(ops, op)
	}
	if len(ops) == 0 {
		return t, nil, false
	}
	return t, ops, true
}

// splitClauses 按**顶层**逗号切开子句。括号里的逗号是列表,不是子句分隔
// (`ADD UNIQUE (a, b)` 是一条,不是两条)。
func splitClauses(toks []string) [][]string {
	out := [][]string{}
	depth, start := 0, 0
	for i, t := range toks {
		switch t {
		case "(":
			depth++
		case ")":
			depth--
		case ",":
			if depth == 0 {
				out = append(out, toks[start:i])
				start = i + 1
			}
		}
	}
	if start < len(toks) {
		out = append(out, toks[start:])
	}
	return out
}

func parseAlterClause(c []string) (AlterOp, bool) {
	if len(c) == 0 {
		return AlterOp{}, false
	}
	switch up(c[0]) {
	case "ADD":
		return parseAdd(c)
	case "DROP":
		return parseDrop(c)
	case "MODIFY":
		return parseModify(c)
	case "CHANGE":
		return parseChange(c)
	case "ALTER":
		return parseAlterColumn(c)
	}
	return AlterOp{}, false
}

func parseAdd(c []string) (AlterOp, bool) {
	i := 1
	// ADD CONSTRAINT x UNIQUE (…) / ADD UNIQUE KEY uk (…) / ADD UNIQUE (…)
	if i < len(c) && up(c[i]) == "CONSTRAINT" {
		i += 2 // 跳过约束名
	}
	if i < len(c) && up(c[i]) == "UNIQUE" {
		cols, ok := parenList(c[i:])
		if !ok {
			return AlterOp{}, false
		}
		return AlterOp{Kind: AlterAddUnique, Columns: cols}, true
	}
	// 其余只认加列。索引 / 外键 / CHECK 一律不认(见文件顶部)。
	if i < len(c) && (up(c[i]) == "KEY" || up(c[i]) == "INDEX" || up(c[i]) == "FOREIGN" ||
		up(c[i]) == "CHECK" || up(c[i]) == "PRIMARY" || up(c[i]) == "FULLTEXT" || up(c[i]) == "SPATIAL") {
		return AlterOp{}, false
	}
	if i < len(c) && up(c[i]) == "COLUMN" {
		i++
	}
	if i < len(c) && up(c[i]) == "IF" { // ADD COLUMN IF NOT EXISTS
		return AlterOp{}, false // 幂等写法不会因为已存在而失败,这里不下结论
	}
	// Oracle:ADD (c TYPE …) —— 括号里可能有多列,那时不认(一条 op 只描述一列)。
	if i < len(c) && c[i] == "(" {
		inner := c[i+1:]
		if len(inner) == 0 {
			return AlterOp{}, false
		}
		if len(splitClauses(trimTrailingParen(inner))) != 1 {
			return AlterOp{}, false
		}
		return columnDef(trimTrailingParen(inner))
	}
	return columnDef(c[i:])
}

// columnDef 从 `c TYPE [NOT NULL] [DEFAULT …]` 里读出列名与两个信号。
func columnDef(c []string) (AlterOp, bool) {
	if len(c) == 0 {
		return AlterOp{}, false
	}
	name := unquote(c[0])
	if name == "" || !isIdentStart(name[0]) {
		return AlterOp{}, false
	}
	return AlterOp{
		Kind: AlterAddColumn, Column: name,
		NotNull: hasNotNull(c), HasDefault: hasWord(c, "DEFAULT"),
	}, true
}

func parseDrop(c []string) (AlterOp, bool) {
	i := 1
	if i < len(c) && up(c[i]) == "COLUMN" {
		i++
	}
	if i < len(c) && up(c[i]) == "IF" {
		return AlterOp{}, false // DROP COLUMN IF EXISTS:不存在也不报错
	}
	if i >= len(c) {
		return AlterOp{}, false
	}
	name := unquote(c[i])
	if name == "" || !isIdentStart(name[0]) {
		return AlterOp{}, false
	}
	// DROP CONSTRAINT / INDEX / KEY / PRIMARY KEY 不是删列。
	switch strings.ToUpper(name) {
	case "CONSTRAINT", "INDEX", "KEY", "PRIMARY", "FOREIGN", "CHECK", "PARTITION":
		return AlterOp{}, false
	}
	return AlterOp{Kind: AlterDropColumn, Column: name}, true
}

func parseModify(c []string) (AlterOp, bool) {
	i := 1
	if i < len(c) && up(c[i]) == "COLUMN" {
		i++
	}
	// Oracle:MODIFY (c NOT NULL)
	if i < len(c) && c[i] == "(" {
		inner := trimTrailingParen(c[i+1:])
		if len(splitClauses(inner)) != 1 || len(inner) == 0 {
			return AlterOp{}, false
		}
		c, i = inner, 0
	}
	if i >= len(c) {
		return AlterOp{}, false
	}
	name := unquote(c[i])
	if name == "" || !isIdentStart(name[0]) {
		return AlterOp{}, false
	}
	return AlterOp{Kind: AlterModifyColumn, Column: name, NotNull: hasNotNull(c), HasDefault: hasWord(c, "DEFAULT")}, true
}

func parseChange(c []string) (AlterOp, bool) {
	i := 1
	if i < len(c) && up(c[i]) == "COLUMN" {
		i++
	}
	if i+1 >= len(c) {
		return AlterOp{}, false
	}
	old, nw := unquote(c[i]), unquote(c[i+1])
	if old == "" || nw == "" || !isIdentStart(old[0]) || !isIdentStart(nw[0]) {
		return AlterOp{}, false
	}
	return AlterOp{Kind: AlterChangeColumn, Column: old, NewColumn: nw, NotNull: hasNotNull(c)}, true
}

// parseAlterColumn 认 PostgreSQL 的 ALTER [COLUMN] c SET NOT NULL。
// 其余(SET DEFAULT / TYPE / DROP NOT NULL)不认:它们要么不会因数据失败,
// 要么"会不会失败"没法用一条便宜的探测回答。
func parseAlterColumn(c []string) (AlterOp, bool) {
	i := 1
	if i < len(c) && up(c[i]) == "COLUMN" {
		i++
	}
	if i >= len(c) {
		return AlterOp{}, false
	}
	name := unquote(c[i])
	if name == "" || !isIdentStart(name[0]) {
		return AlterOp{}, false
	}
	if hasWord(c[i:], "SET") && hasNotNull(c[i:]) {
		return AlterOp{Kind: AlterSetNotNull, Column: name, NotNull: true}, true
	}
	return AlterOp{}, false
}

// parenList 读出 `UNIQUE [KEY|INDEX] [name] (a, b)` 里的列名。
func parenList(c []string) ([]string, bool) {
	open := -1
	for i, t := range c {
		if t == "(" {
			open = i
			break
		}
	}
	if open < 0 {
		return nil, false
	}
	out := []string{}
	for _, part := range splitClauses(trimTrailingParen(c[open+1:])) {
		if len(part) == 0 {
			return nil, false
		}
		n := unquote(part[0])
		if n == "" || !isIdentStart(n[0]) {
			return nil, false
		}
		out = append(out, n)
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

// trimTrailingParen 去掉与开括号配对的那个收括号之后的部分。
func trimTrailingParen(c []string) []string {
	depth := 0
	for i, t := range c {
		switch t {
		case "(":
			depth++
		case ")":
			if depth == 0 {
				return c[:i]
			}
			depth--
		}
	}
	return c
}

func hasNotNull(c []string) bool {
	for i := 0; i+1 < len(c); i++ {
		if up(c[i]) == "NOT" && up(c[i+1]) == "NULL" {
			return true
		}
	}
	return false
}

func hasWord(c []string, w string) bool {
	for _, t := range c {
		if up(t) == w {
			return true
		}
	}
	return false
}
