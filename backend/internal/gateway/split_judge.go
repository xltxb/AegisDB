package gateway

import (
	"strings"

	"aegisdb/pkg/sqlutil"
)

// SplitForJudge splits a command into the statements to judge, reading `--` the
// way the TARGET engine does.
//
// The shared splitter follows standard SQL, where `--` always starts a comment.
// MySQL (and everything speaking its protocol) only does so when whitespace or a
// control character follows: `SELECT 1--1 INTO OUTFILE '/tmp/p'` is "1 minus -1"
// to the server. The splitter drops `--1 INTO OUTFILE …` as a comment, the judge
// sees `SELECT 1`, and a single statement is sent to the server as the ORIGINAL
// text — so whatever hides after `--x` (INTO OUTFILE, load_file(…)) runs unjudged.
//
// On MySQL-family engines every `--` not followed by whitespace is therefore
// rewritten to `- -` before splitting: the server reads both spellings as two
// minus signs, so the meaning is unchanged, but the splitter no longer sees a
// comment. Only the judged text changes; what is executed and audited is still
// the caller's original.
//
// Other engines are left alone ON PURPOSE. On PostgreSQL `--` is always a
// comment, and treating `--'` as code there would let that quote open a literal
// that swallows the next separator — merging a stacked statement into this one,
// the one direction the splitter must never err in (see SplitStatements).
//
// `#` 行注释(MySQL 系)拆分器同样不认,开头的 `#` 注释由 ParseVerb 跳过(见 skipLeadingHash)。
func SplitForJudge(engine, sql string) []string {
	if engineFamily(engine) == familyMySQL {
		sql = mysqlDashDash(sql)
	}
	return sqlutil.SplitStatements(sql)
}

// mysqlDashDash rewrites every `--` that MySQL does NOT treat as a comment opener
// into `- -`. It runs over the whole text, literals and comments included: inside
// a literal it only alters data the judge never interprets, and inside a comment
// it changes nothing that matters.
func mysqlDashDash(sql string) string {
	if !strings.Contains(sql, "--") {
		return sql
	}
	var b strings.Builder
	b.Grow(len(sql) + 8)
	for i := 0; i < len(sql); i++ {
		if sql[i] == '-' && i+1 < len(sql) && sql[i+1] == '-' && i+2 < len(sql) && sql[i+2] > ' ' {
			b.WriteString("- ") // the second '-' is handled by the next iteration
			continue
		}
		b.WriteByte(sql[i])
	}
	return b.String()
}
