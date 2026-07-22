// Package sqlutil holds small SQL text helpers shared across the gateway.
package sqlutil

import "strings"

// SplitStatements splits a SQL string into individual statements on top-level
// semicolons. Semicolons inside string literals ('...', "..."), quoted
// identifiers (`...`), and comments (-- , #, /* */) are NOT treated as
// separators, so a value like 'a;b' can't split a statement in two. Comments are
// stripped; each returned statement is trimmed and non-empty.
//
// Quote termination follows the SQL standard: only a doubled quote ('' / "" / ``)
// escapes. Backslash is treated as an ordinary character — this is deliberate.
// Honoring MySQL's backslash escapes would let 'a\'; DROP…' desync against a
// PostgreSQL/NO_BACKSLASH_ESCAPES target and merge a hidden statement into what
// looks like one literal. The standard rule can only ever over-split (safe:
// every fragment is still risk-judged), never merge (which would bypass).
func SplitStatements(sql string) []string {
	var out []string
	var b strings.Builder
	flush := func() {
		if s := strings.TrimSpace(b.String()); s != "" {
			out = append(out, s)
		}
		b.Reset()
	}
	for i := 0; i < len(sql); i++ {
		c := sql[i]
		switch c {
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
		case '#': // MySQL '#' line comment
			for i < len(sql) && sql[i] != '\n' {
				i++
			}
			b.WriteByte(' ')
		case '/': // block comment "/* ... */"
			if i+1 < len(sql) && sql[i+1] == '*' {
				i += 2
				for i+1 < len(sql) && !(sql[i] == '*' && sql[i+1] == '/') {
					i++
				}
				i++ // skip the closing '/'
				b.WriteByte(' ')
			} else {
				b.WriteByte(c)
			}
		case ';':
			flush()
		default:
			b.WriteByte(c)
		}
	}
	flush()
	return out
}
