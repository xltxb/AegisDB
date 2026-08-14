package sqlutil

import "regexp"

// quotedVal matches a single- or double-quoted SQL string literal (with backslash
// escapes) — the form a password/secret literal takes.
const quotedVal = `(?:'(?:[^'\\]|\\.)*'|"(?:[^"\\]|\\.)*")`

// authPlugin matches the plugin name in IDENTIFIED WITH <plugin>. MySQL accepts
// it bare OR quoted, and the quoted form is what the reference documentation
// shows — so it is common. Matching only the bare form is what let
// `IDENTIFIED WITH 'mysql_native_password' BY '<secret>'` through with the
// password in the clear.
const authPlugin = `(?:` + quotedVal + `|[^\s'"]+)`

var (
	// IDENTIFIED [WITH <plugin>] BY|AS [PASSWORD] '<secret>' — MySQL/MariaDB
	// CREATE USER / ALTER USER / GRANT ... IDENTIFIED BY '...'.
	//
	// `AS` is included because IDENTIFIED WITH <plugin> AS '<hash>' carries the
	// stored password hash. A hash is not a cleartext password but it is still a
	// credential: it is exactly what an attacker replays or cracks offline, and it
	// is what gets copied between servers to clone an account.
	reIdentifiedBy = regexp.MustCompile(`(?i)(identified\s+(?:with\s+` + authPlugin + `\s+)?(?:by|as)\s+(?:password\s+)?)` + quotedVal)
	// PASSWORD('<secret>') — the MySQL PASSWORD() function.
	rePasswordFn = regexp.MustCompile(`(?i)(\bpassword\s*\(\s*)` + quotedVal + `(\s*\))`)
	// [ENCRYPTED] PASSWORD [=] '<secret>' — PostgreSQL CREATE/ALTER ROLE, SET PASSWORD.
	//
	// \b matters more than it looks. Without it `password` also matched the TAIL of
	// `mysql_native_password`, so on
	//   IDENTIFIED WITH 'mysql_native_password' BY '<secret>'
	// this rule matched `password' BY '` and rewrote it to `password'***'` —
	// producing output that CONTAINED a `***` and read as redacted while the real
	// password sat untouched right after it. A mask in the wrong place is worse
	// than no mask: it is the thing that stops anyone looking twice.
	rePasswordKV = regexp.MustCompile(`(?i)((?:encrypted\s+)?\bpassword\s*=?\s*)` + quotedVal)
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
