package sqlutil

import "regexp"

// quotedVal matches a single- or double-quoted SQL string literal (with backslash
// escapes) — the form a password/secret literal takes.
const quotedVal = `(?:'(?:[^'\\]|\\.)*'|"(?:[^"\\]|\\.)*")`

var (
	// IDENTIFIED [WITH <plugin>] BY [PASSWORD] '<secret>' — MySQL/MariaDB
	// CREATE USER / ALTER USER / GRANT ... IDENTIFIED BY '...'.
	reIdentifiedBy = regexp.MustCompile(`(?i)(identified\s+(?:with\s+[^\s'"]+\s+)?by\s+(?:password\s+)?)` + quotedVal)
	// PASSWORD('<secret>') — the MySQL PASSWORD() function.
	rePasswordFn = regexp.MustCompile(`(?i)(password\s*\(\s*)` + quotedVal + `(\s*\))`)
	// [ENCRYPTED] PASSWORD [=] '<secret>' — PostgreSQL CREATE/ALTER ROLE, SET PASSWORD.
	rePasswordKV = regexp.MustCompile(`(?i)((?:encrypted\s+)?password\s*=?\s*)` + quotedVal)
	// SET PASSWORD [FOR '<user>'@'<host>'] = '<secret>' — the target user carries its
	// own quotes, so match up to the LAST '=' before the value.
	reSetPassword = regexp.MustCompile(`(?i)(set\s+password\b.*=\s*)` + quotedVal)
)

// RedactSecrets masks password/secret string literals in a SQL command so the
// audit log and approval notifications never store or surface credentials in the
// clear (e.g. CREATE USER ... IDENTIFIED BY '***'). It only rewrites the literal
// after a known credential keyword, leaving the rest of the statement intact.
func RedactSecrets(sql string) string {
	out := reIdentifiedBy.ReplaceAllString(sql, `${1}'***'`)
	out = rePasswordFn.ReplaceAllString(out, `${1}'***'${2}`)
	out = reSetPassword.ReplaceAllString(out, `${1}'***'`)
	out = rePasswordKV.ReplaceAllString(out, `${1}'***'`)
	return out
}
