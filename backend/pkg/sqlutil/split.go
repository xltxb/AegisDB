// Package sqlutil holds small SQL text helpers shared across the gateway.
package sqlutil

import "strings"

// SplitStatements splits a SQL string into individual statements on top-level
// semicolons. Semicolons inside string literals ('...', "...", E'...', $tag$…$tag$),
// quoted identifiers (`...`), and comments (--, /* */) are NOT treated as
// separators, so a value like 'a;b' can't split a statement in two. Comments are
// stripped; each returned statement is trimmed and non-empty.
//
// THE GOVERNING RULE: this splitter feeds the risk judgement while the ORIGINAL
// text is what reaches the server, so the two lexers must never disagree in the
// direction that MERGES statements. Over-splitting is safe (every fragment is
// still judged, so the worst case is an over-block); merging is a bypass — the
// judge sees one benign statement while the server runs a stacked one. Every
// rule below is chosen for that asymmetry:
//
//   - Quote termination follows the SQL standard: only a doubled quote ('' / ""
//     / ``) escapes; backslash is an ordinary character. Honoring MySQL's
//     backslash escapes would desync against a PostgreSQL/NO_BACKSLASH_ESCAPES
//     target and merge a hidden statement into what looks like one literal.
//   - PostgreSQL dollar quotes and E'...' escape strings ARE honored, because
//     NOT honoring them merges: a quote inside $$'$$ (or the trailing '' of
//     E'\'') would otherwise open a literal that swallows the separator (ER1).
//   - '#' is deliberately NOT a comment even though MySQL treats it as one: in
//     PostgreSQL it is an operator, so skipping to end-of-line would delete a
//     real stacked statement from the judged text while the server still ran it
//     (ER2). Splitting there over-splits on MySQL, which is the safe direction.
//   - MySQL's DELIMITER directive IS honoured. It is a CLIENT directive — the
//     server rejects it — and it exists precisely so a routine body's semicolons
//     stop being separators. Ignoring it split every stored-procedure script mid
//     body into fragments the server rejects one by one, which is why such a
//     script simply could not be run from the console. The directive line is
//     consumed rather than emitted, exactly as the mysql client does with it.
//
//     It does NOT weaken judgement: what a custom delimiter produces is a LONGER
//     statement, and every layer that matters reads the whole text of one —
//     the high-risk dictionary scans it, and the review looks inside anything
//     still carrying semicolons (see review.innerStatements).
func SplitStatements(sql string) []string {
	judged, _ := splitStatements(sql)
	return judged
}

// SplitStatementsExec 与 SplitStatements 按同一处边界拆开同一份脚本,返回的是**下发给服务器**
// 的文本:可执行注释(`/*!ver … */`、TiDB 的 `/*T![feature] … */`)原样保留标记,其余与判定
// 文本逐字节相同。两者条数与顺序一一对应 —— 判第 i 条、执行第 i 条。
//
// 为什么要两份:判定要看见注释体(服务器会执行它),所以判定文本去掉标记、留下注释体;可下发
// 时去掉标记就改了语义 —— `/*T![auto_rand] AUTO_RANDOM(5) */` 被剥成普通注释,TiDB 建出的表
// 丢了 AUTO_RANDOM(2026-10 生产);`/*!99999 … */` 去掉标记则不论版本都会执行。
//
// 可执行注释里的分号照旧是分隔符(判定要逐条看);下发时每一段各自补齐标记 ——
// `/*!50000 SET @a=1; SET @b=2 */` 下发成 `/*!50000 SET @a=1 */` 与 `/*!50000 SET @b=2 */`。
func SplitStatementsExec(sql string) []string {
	_, exec := splitStatements(sql)
	return exec
}

// ExecCommentOpener 报告 sql[i:] 是否以可执行注释的开头标记起头,返回标记长度(0 = 不是):
//   - `/*!` 加可选的版本数字(MySQL / MariaDB / TiDB:`/*!50100 … */`)
//   - `/*M!` 加可选的版本数字(MariaDB:`/*M!100100 … */`)
//   - `/*T!` 加可选的特性列表(TiDB:`/*T![auto_rand] … */`、`/*T![clustered_index] … */`)
//
// 服务器会执行这几种注释的内容:判定时必须看见注释体,下发时必须原样保留标记。特性列表只认
// 字母、数字、下划线与逗号 —— 方括号里出现别的字符就不当列表,免得把注释体吞进"标记"里、
// 躲过判定。
func ExecCommentOpener(sql string, i int) int {
	if i+2 >= len(sql) || sql[i] != '/' || sql[i+1] != '*' {
		return 0
	}
	j := i + 2
	switch {
	case sql[j] == '!':
		j++
		for j < len(sql) && sql[j] >= '0' && sql[j] <= '9' {
			j++
		}
	case sql[j] == 'M' && j+1 < len(sql) && sql[j+1] == '!':
		j += 2
		for j < len(sql) && sql[j] >= '0' && sql[j] <= '9' {
			j++
		}
	case sql[j] == 'T' && j+1 < len(sql) && sql[j+1] == '!':
		j += 2
		if j < len(sql) && sql[j] == '[' {
			k := j + 1
			for k < len(sql) && (identByte(sql[k]) && sql[k] != '$' || sql[k] == ',') {
				k++
			}
			if k < len(sql) && sql[k] == ']' {
				j = k + 1
			}
		}
	default:
		return 0
	}
	return j - i
}

// splitStatements 是两份输出的共同实现:j 是判定文本,x 是下发文本。除了可执行注释的标记,
// 两者写入的每个字节都相同。
func splitStatements(sql string) (judged, exec []string) {
	var out, outX []string
	var b, x strings.Builder
	both := func(c byte) { b.WriteByte(c); x.WriteByte(c) }
	bothStr := func(s string) { b.WriteString(s); x.WriteString(s) }
	var opens []string // 当前所在的可执行注释的开头标记(由外到内)
	delim := ";"       // 当前语句分隔符;DELIMITER 指令会改它
	flush := func() {
		if s := strings.TrimSpace(b.String()); s != "" {
			xs := strings.TrimSpace(x.String())
			for range opens { // 段落停在可执行注释里面:下发文本补齐收尾标记
				xs += " */"
			}
			out = append(out, s)
			outX = append(outX, xs)
		}
		b.Reset()
		x.Reset()
		if len(opens) > 0 { // 下一段仍在注释里:下发文本重新带上开头标记
			x.WriteString(strings.Join(opens, " ") + " ")
		}
	}
	emit := func(s string) { // 整块取出的单元(PL/SQL 块、SQL*Plus SET 行):两份相同
		out = append(out, s)
		outX = append(outX, s)
		b.Reset()
		x.Reset()
	}
	for i := 0; i < len(sql); i++ {
		// At a statement boundary a PL/SQL block is taken WHOLE: every semicolon
		// inside a package body belongs to the body, not to the script. This is
		// the one merge this function performs — see plsql.go for why it does not
		// violate the rule above (the merged unit is exactly the unit the server
		// executes, and the dictionary still scans all of it).
		//
		// 只在默认分隔符下合并:写了 DELIMITER $$ 的脚本已经亲手标出了每条语句的边界,
		// 按它拆就是对的。这时再套整块规则,一个 CREATE PROCEDURE 可能一路吞到文件末尾
		// (末尾恰好是 END; 时),把后面的语句都并进来。
		if delim == ";" && strings.TrimSpace(b.String()) == "" && plsqlBlockKind(sql[i:]) != "" {
			atStart := len(out) == 0 && strings.TrimSpace(sql[:i]) == ""
			if body, next, ok := takePLSQLBlock(sql, i, atStart); ok {
				emit(body)
				i = next - 1 // the loop's i++ lands on the next byte
				continue
			}
		}
		// DELIMITER 只在语句边界、且位于行首时生效 —— 它是一行指令,不是表达式。
		// 这两个条件一起,保证 SQL 文本里出现 "delimiter" 这个词(列名、字符串)
		// 不会被误当成指令。
		if strings.TrimSpace(b.String()) == "" && atLineStart(sql, i) {
			// SQL*Plus 的 SET 行以换行结束(见 sqlplus.go)。它**是**一条语句(发下去由执行器
			// 按 SQL*Plus 命令处理),不像 DELIMITER 那样被吞掉。
			if st, next, ok := takeSQLPlusSetLine(sql, i); ok {
				emit(st)
				i = next - 1
				continue
			}
			if d, next, ok := takeDelimiterDirective(sql, i); ok {
				delim = d
				b.Reset() // 指令本身不下发给服务器
				x.Reset()
				i = next - 1
				continue
			}
		}
		// 非默认分隔符时,分号不再是分隔符 —— 那正是 DELIMITER 存在的理由。
		if delim != ";" && strings.HasPrefix(sql[i:], delim) {
			flush()
			i += len(delim) - 1
			continue
		}
		c := sql[i]
		switch c {
		case '$': // PostgreSQL dollar-quoted string $tag$ ... $tag$ — copy verbatim
			tag, ok := dollarTag(sql, i)
			// Inside an identifier (`a$x$`) '$' is an identifier byte, not an
			// opener; reading it as one would swallow the separator and merge.
			if i > 0 && identByte(sql[i-1]) {
				ok = false
			}
			if !ok { // ordinary '$' (identifier char, $1 placeholder, …)
				both(c)
				break
			}
			bothStr(tag)
			i += len(tag)
			for i < len(sql) && !strings.HasPrefix(sql[i:], tag) {
				both(sql[i])
				i++
			}
			if i < len(sql) { // closing tag
				bothStr(tag)
				i += len(tag)
			}
			i-- // the loop's i++ lands on the next byte
		case 'E', 'e': // PostgreSQL escape string E'...' — backslash escapes the next byte
			if i+1 >= len(sql) || sql[i+1] != '\'' || (i > 0 && identByte(sql[i-1])) {
				both(c) // ordinary identifier byte
				break
			}
			both(c)
			i++
			both(sql[i]) // opening quote
			i++
			for i < len(sql) {
				d := sql[i]
				both(d)
				if d == '\\' && i+1 < len(sql) { // escaped byte — cannot close the literal
					i++
					both(sql[i])
					i++
					continue
				}
				if d == '\'' {
					if i+1 < len(sql) && sql[i+1] == '\'' { // doubled quote
						i++
						both(sql[i])
					} else {
						break // closing quote
					}
				}
				i++
			}
		case '\'', '"', '`': // quoted literal / identifier — copy verbatim
			q := c
			both(c)
			i++
			for i < len(sql) {
				d := sql[i]
				both(d)
				if d == q {
					if i+1 < len(sql) && sql[i+1] == q { // doubled quote = escaped quote
						i++
						both(sql[i])
					} else {
						break // closing quote
					}
				}
				i++
			}
		case '-': // line comment "--" to end of line
			if i+1 < len(sql) && sql[i+1] == '-' {
				for i < len(sql) && sql[i] != '\n' {
					i++
				}
				both(' ')
			} else {
				both(c)
			}
		case '/':
			if i+1 < len(sql) && sql[i+1] == '*' {
				if n := ExecCommentOpener(sql, i); n > 0 {
					// Executable comment (MySQL "/*!<digits>", TiDB "/*T![feature]"):
					// the server RUNS the body, so the judged text drops only the
					// marker and keeps the body (EX2); the text sent to the server
					// keeps the marker verbatim. Its ';' still separates statements.
					marker := sql[i : i+n]
					opens = append(opens, marker)
					b.WriteByte(' ')
					x.WriteString(marker)
					i += n - 1 // the loop's i++ lands on the first body byte
					break
				}
				i += 2 // plain block comment: remove up to the matching "*/"
				for i+1 < len(sql) && !(sql[i] == '*' && sql[i+1] == '/') {
					i++
				}
				i++ // skip the closing '/'
				both(' ')
			} else {
				both(c)
			}
		case '*': // closer of an executable comment whose body we kept
			if len(opens) > 0 && i+1 < len(sql) && sql[i+1] == '/' {
				opens = opens[:len(opens)-1]
				i++
				b.WriteByte(' ')
				x.WriteString("*/")
			} else {
				both(c)
			}
		case ';':
			if delim != ";" {
				both(c) // 自定义分隔符生效时,分号属于语句体
				break
			}
			flush()
		default:
			both(c)
		}
	}
	flush()
	return out, outX
}

// atLineStart reports whether i sits at the beginning of a line (only blanks
// before it). A DELIMITER directive is a whole line; requiring that is what
// keeps a column or literal called "delimiter" from being read as one.
func atLineStart(sql string, i int) bool {
	for j := i - 1; j >= 0; j-- {
		switch sql[j] {
		case '\n':
			return true
		case ' ', '\t', '\r':
			continue
		default:
			return false
		}
	}
	return true
}

// takeDelimiterDirective recognises a "DELIMITER <token>" line and returns the
// new delimiter plus the offset just past the line.
//
// The directive is CONSUMED, not emitted: it is a client-side instruction and
// the server rejects it. Emitting it as a statement is what made every stored
// procedure script fail on its first line.
func takeDelimiterDirective(sql string, i int) (delim string, next int, ok bool) {
	const kw = "DELIMITER"
	if len(sql)-i < len(kw)+1 || !strings.EqualFold(sql[i:i+len(kw)], kw) {
		return "", 0, false
	}
	rest := sql[i+len(kw):]
	if rest == "" || (rest[0] != ' ' && rest[0] != '\t') {
		return "", 0, false // "DELIMITERS" 这种以它开头的标识符不算
	}
	lineEnd := strings.IndexByte(rest, '\n')
	line := rest
	next = len(sql)
	if lineEnd >= 0 {
		line = rest[:lineEnd]
		next = i + len(kw) + lineEnd + 1
	}
	d := strings.TrimSpace(line)
	if d == "" {
		return "", 0, false
	}
	// 分隔符是行上的第一个 token;后面若还有字符(注释等)一并忽略。
	if sp := strings.IndexAny(d, " \t"); sp >= 0 {
		d = d[:sp]
	}
	return d, next, true
}

// dollarTag reports whether a PostgreSQL dollar-quote opener starts at sql[i]
// and returns it including both '$' delimiters (`$$`, `$tag$`). Per PostgreSQL,
// the optional tag is an identifier starting with a letter or underscore, so a
// positional parameter like `$1` is NOT an opener.
func dollarTag(sql string, i int) (string, bool) {
	if i >= len(sql) || sql[i] != '$' {
		return "", false
	}
	j := i + 1
	for j < len(sql) && sql[j] != '$' {
		c := sql[j]
		isAlpha := c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
		isDigit := c >= '0' && c <= '9'
		if !isAlpha && !(isDigit && j > i+1) { // digits allowed only after a leading letter/underscore
			return "", false
		}
		j++
	}
	if j >= len(sql) {
		return "", false // no closing '$' — not an opener
	}
	return sql[i : j+1], true
}

// identByte reports whether c can appear inside a SQL identifier, so a leading
// `E` is only read as an escape-string marker when it stands alone (e.g. `E'x'`,
// not the tail of a column named `VALUE'`).
func identByte(c byte) bool {
	return c == '_' || c == '$' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}
