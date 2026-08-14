package sqlutil

import (
	"regexp"
	"strings"
)

// Credential masking for command text that is stored, displayed, or sent
// somewhere. See RedactSecrets.
//
// The gateway drives four engine families (see internal/gateway/realdb.go:
// mysql, postgres, oracle, sqlite), and each spells "here is the password"
// differently. Every form below is one of them; the tests name the engine each
// belongs to, because a rule with no engine behind it is a rule nobody can check.

// quotedVal matches a single- or double-quoted SQL string literal (with backslash
// escapes) — the form a password/secret literal takes on most engines.
const quotedVal = `(?:'(?:[^'\\]|\\.)*'|"(?:[^"\\]|\\.)*")`

// bareVal matches an UNQUOTED credential. Oracle takes the password as a bare
// token — `ALTER USER u IDENTIFIED BY NewPass1` is ordinary, not a corner case —
// so a rule that only understands quoted literals misses Oracle almost entirely.
//
// It runs to the next separator rather than to a legal-identifier character
// class: a password that is illegal unquoted still has to be masked if someone
// types it, and masking a character too many costs nothing while masking one too
// few is the whole problem.
const bareVal = `[^\s;'"()]+`

// secretVal is either form.
const secretVal = `(?:` + quotedVal + `|` + bareVal + `)`

// authPlugin matches the plugin in IDENTIFIED WITH/VIA <plugin>. MySQL accepts it
// bare or quoted and its documentation shows the quoted form, so both appear in
// pasted SQL; matching only the bare one let the password through in the clear.
const authPlugin = `(?:` + quotedVal + `|[^\s'"]+)`

var (
	// The IDENTIFIED clause, in every shape the supported engines use:
	//
	//   MySQL/TiDB/PolarDB  IDENTIFIED BY '<pw>'
	//                       IDENTIFIED WITH <plugin> BY '<pw>' | AS '<hash>'
	//                       IDENTIFIED BY PASSWORD '<hash>'
	//   MariaDB             IDENTIFIED VIA <plugin> USING '<pw>'
	//   Oracle              IDENTIFIED BY <pw>                    (unquoted)
	//                       IDENTIFIED BY <new> REPLACE <old>     (both secrets)
	//                       IDENTIFIED BY VALUES '<hash>'
	//   GaussDB/openGauss   IDENTIFIED BY '<new>' REPLACE '<old>'
	//
	// REPLACE carries the CURRENT password — the one already in use everywhere
	// else. It is handled inside this clause rather than as its own rule because
	// REPLACE is also an ordinary function and statement (REPLACE INTO,
	// REPLACE(col,…)); only its position after IDENTIFIED BY makes it a secret.
	//
	// Group 2 is the value, groups 3+4 the REPLACE separator and old value.
	reIdentifiedClause = regexp.MustCompile(`(?i)\bidentified\s+(?:(?:with|via)\s+` + authPlugin + `\s+)?(?:by|as|using)\s+(?:(values|password)\s+)?(` + secretVal + `)(?:(\s+replace\s+)(` + secretVal + `))?`)

	// PASSWORD('<secret>') — MySQL's PASSWORD() function, also MariaDB's
	// IDENTIFIED VIA <plugin> USING PASSWORD('<pw>').
	rePasswordFn = regexp.MustCompile(`(?i)(\bpassword\s*\(\s*)` + quotedVal + `(\s*\))`)

	// SET PASSWORD [FOR '<user>'@'<host>'] = '<secret>' — the target user carries
	// its own quotes, so match up to the LAST '=' before the value.
	reSetPassword = regexp.MustCompile(`(?i)(set\s+password\b.*=\s*)` + quotedVal)

	// [UN|ENCRYPTED] PASSWORD [=] '<secret>' — PostgreSQL/GaussDB CREATE|ALTER
	// ROLE/USER, and the WITH PASSWORD = '<pw>' spelling.
	//
	// \b matters more than it looks. Without it `password` also matched the TAIL of
	// `mysql_native_password`, so on IDENTIFIED WITH 'mysql_native_password' BY
	// '<pw>' this rule fired on `password' BY '` and produced `password'***'` —
	// output that CONTAINED a mask and read as redacted while the real password sat
	// untouched beside it. A mask in the wrong place is worse than no mask: it is
	// what stops anyone looking twice.
	rePasswordKV = regexp.MustCompile(`(?i)((?:(?:un)?encrypted\s+)?\bpassword\s*=?\s*)` + quotedVal)
)

// identifiedKeywords are words that can follow IDENTIFIED BY without being the
// secret. Masking them would corrupt the statement while protecting nothing:
// `IDENTIFIED BY RANDOM PASSWORD` (MySQL 8) asks the server to generate one, and
// `IDENTIFIED EXTERNALLY`/`GLOBALLY` delegate authentication entirely. `password`
// is here for MariaDB's `USING PASSWORD('<pw>')`, where the literal sits inside
// the function call and rePasswordFn masks it.
var identifiedKeywords = map[string]bool{
	"random": true, "externally": true, "globally": true, "none": true, "password": true,
}

// RedactSecrets masks password/secret string literals in a SQL command so the
// audit log, approval notifications and anything sent to an external approval
// service never carry credentials in the clear (e.g. CREATE USER … IDENTIFIED BY
// '***'). It only rewrites the value after a known credential keyword, leaving
// the rest of the statement intact — the reviewer still has to be able to see
// which account is being created, on which host, with which plugin.
//
// It is idempotent: several paths redact text that was already redacted.
func RedactSecrets(sql string) string {
	out := redactIdentifiedClauses(sql)
	out = rePasswordFn.ReplaceAllString(out, `${1}'***'${2}`)
	out = reSetPassword.ReplaceAllString(out, `${1}'***'`)
	out = rePasswordKV.ReplaceAllString(out, `${1}'***'`)
	return out
}

// redactIdentifiedClauses masks the value (and any REPLACE value) of every
// IDENTIFIED clause, leaving the keyword, the plugin name and the rest alone.
//
// Written as a submatch walk rather than a ReplaceAllString template because the
// decision is conditional: a match whose value is a keyword must be left exactly
// as it was, which a replacement template cannot express.
func redactIdentifiedClauses(sql string) string {
	ms := reIdentifiedClause.FindAllStringSubmatchIndex(sql, -1)
	if ms == nil {
		return sql
	}
	var b strings.Builder
	last := 0
	for _, m := range ms {
		// m[4]:m[5] = the value; m[8]:m[9] = the REPLACE value (-1 when absent).
		valStart, valEnd := m[4], m[5]
		if valStart < 0 {
			continue
		}
		if identifiedKeywords[strings.ToLower(strings.Trim(sql[valStart:valEnd], `'"`))] {
			continue // not the secret — see identifiedKeywords
		}
		b.WriteString(sql[last:valStart])
		b.WriteString(`'***'`)
		last = valEnd
		if oldStart, oldEnd := m[8], m[9]; oldStart >= 0 {
			b.WriteString(sql[last:oldStart]) // the " REPLACE " separator, verbatim
			b.WriteString(`'***'`)
			last = oldEnd
		}
	}
	b.WriteString(sql[last:])
	return b.String()
}
