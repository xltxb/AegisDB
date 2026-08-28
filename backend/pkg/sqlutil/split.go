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
	var out []string
	var b strings.Builder
	execDepth := 0 // >0 while inside /*!ver ... */: keep the body, drop the markers
	delim := ";"  // 当前语句分隔符;DELIMITER 指令会改它
	flush := func() {
		if s := strings.TrimSpace(b.String()); s != "" {
			out = append(out, s)
		}
		b.Reset()
	}
	for i := 0; i < len(sql); i++ {
		// At a statement boundary a PL/SQL block is taken WHOLE: every semicolon
		// inside a package body belongs to the body, not to the script. This is
		// the one merge this function performs — see plsql.go for why it does not
		// violate the rule above (the merged unit is exactly the unit the server
		// executes, and the dictionary still scans all of it).
		if strings.TrimSpace(b.String()) == "" && plsqlBlockKind(sql[i:]) != "" {
			atStart := len(out) == 0 && strings.TrimSpace(sql[:i]) == ""
			if body, next, ok := takePLSQLBlock(sql, i, atStart); ok {
				out = append(out, body)
				b.Reset()
				i = next - 1 // the loop's i++ lands on the next byte
				continue
			}
		}
		// DELIMITER 只在语句边界、且位于行首时生效 —— 它是一行指令,不是表达式。
		// 这两个条件一起,保证 SQL 文本里出现 "delimiter" 这个词(列名、字符串)
		// 不会被误当成指令。
		if strings.TrimSpace(b.String()) == "" && atLineStart(sql, i) {
			if d, next, ok := takeDelimiterDirective(sql, i); ok {
				delim = d
				b.Reset() // 指令本身不下发给服务器
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
			if !ok { // ordinary '$' (identifier char, $1 placeholder, …)
				b.WriteByte(c)
				break
			}
			b.WriteString(tag)
			i += len(tag)
			for i < len(sql) && !strings.HasPrefix(sql[i:], tag) {
				b.WriteByte(sql[i])
				i++
			}
			if i < len(sql) { // closing tag
				b.WriteString(tag)
				i += len(tag)
			}
			i-- // the loop's i++ lands on the next byte
		case 'E', 'e': // PostgreSQL escape string E'...' — backslash escapes the next byte
			if i+1 >= len(sql) || sql[i+1] != '\'' || (i > 0 && identByte(sql[i-1])) {
				b.WriteByte(c) // ordinary identifier byte
				break
			}
			b.WriteByte(c)
			i++
			b.WriteByte(sql[i]) // opening quote
			i++
			for i < len(sql) {
				d := sql[i]
				b.WriteByte(d)
				if d == '\\' && i+1 < len(sql) { // escaped byte — cannot close the literal
					i++
					b.WriteByte(sql[i])
					i++
					continue
				}
				if d == '\'' {
					if i+1 < len(sql) && sql[i+1] == '\'' { // doubled quote
						i++
						b.WriteByte(sql[i])
					} else {
						break // closing quote
					}
				}
				i++
			}
		case '\'', '"', '`': // quoted literal / identifier — copy verbatim
			q := c
			b.WriteByte(c)
			i++
			for i < len(sql) {
				d := sql[i]
				b.WriteByte(d)
				if d == q {
					if i+1 < len(sql) && sql[i+1] == q { // doubled quote = escaped quote
						i++
						b.WriteByte(sql[i])
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
				b.WriteByte(' ')
			} else {
				b.WriteByte(c)
			}
		case '/':
			if i+1 < len(sql) && sql[i+1] == '*' {
				if i+2 < len(sql) && sql[i+2] == '!' {
					// MySQL executable comment: the server RUNS the body, so drop
					// only the "/*!<digits>" marker and keep the body in the text
					// that gets judged (EX2). Its ';' still separates statements.
					i += 3
					for i < len(sql) && sql[i] >= '0' && sql[i] <= '9' {
						i++
					}
					execDepth++
					i-- // the loop's i++ lands on the first body byte
					b.WriteByte(' ')
					break
				}
				i += 2 // plain block comment: remove up to the matching "*/"
				for i+1 < len(sql) && !(sql[i] == '*' && sql[i+1] == '/') {
					i++
				}
				i++ // skip the closing '/'
				b.WriteByte(' ')
			} else {
				b.WriteByte(c)
			}
		case '*': // closer of an executable comment whose body we kept
			if execDepth > 0 && i+1 < len(sql) && sql[i+1] == '/' {
				execDepth--
				i++
				b.WriteByte(' ')
			} else {
				b.WriteByte(c)
			}
		case ';':
			if delim != ";" {
				b.WriteByte(c) // 自定义分隔符生效时,分号属于语句体
				break
			}
			flush()
		default:
			b.WriteByte(c)
		}
	}
	flush()
	return out
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
