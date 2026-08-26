package sqlutil

import (
	"regexp"
	"strings"
)

// PL/SQL 块的整块识别 —— SplitStatements 里唯一一处 MERGE。
//
// SplitStatements 的总纲是"过切安全、合并危险":判定器看到的文本必须永远不比
// 服务端执行的文本更少。这里合并却不违背它,因为被合并的恰好是 Oracle 自己作为
// 一条语句执行的那个单元 —— 判定的文本与执行的文本仍然逐字相同。包体里的每一个
// 分号都属于包体,把它们当成语句分隔符,得到的是六段谁也执行不了的残片,而且每段
// 都被单独判定,判定结果对不上真正会发生的事。
//
// 门禁不因整块而放松:高危字典是对整条语句文本做正则扫描(risk.matchCommand),
// 块里的 DROP 照样命中;能力矩阵按首动词判,CREATE 落在 ddl 维度上,正是部署一个
// 包应该要的权限。
//
// 合并只在终结符明确时发生(见 takePLSQLBlock),所以一个块永远吞不掉它后面的语句。

var (
	// A stored-program header. TYPE is included: object type bodies carry the
	// same internal semicolons.
	plsqlHeadRe = regexp.MustCompile(`(?is)^\s*CREATE\s+(?:OR\s+REPLACE\s+)?(?:(?:NON)?EDITIONABLE\s+)?(?:PACKAGE|PROCEDURE|FUNCTION|TRIGGER|TYPE)\b`)
	// An anonymous block. DECLARE is unambiguous; BEGIN is NOT — in MySQL it
	// opens a transaction ("BEGIN; UPDATE …; COMMIT;"), so a bare BEGIN counts as
	// a block only when a '/' terminator proves the script is PL/SQL.
	plsqlDeclareRe = regexp.MustCompile(`(?is)^\s*DECLARE\b`)
	plsqlBeginRe   = regexp.MustCompile(`(?is)^\s*BEGIN\b`)
	// The tail of a complete block: END; or END <name>;
	plsqlTailRe = regexp.MustCompile(`(?is)\bEND\s*(?:[A-Za-z_][A-Za-z0-9_$#]*\s*)?;\s*$`)
)

// plsqlBlockKind reports what s starts with: "" (not a block), "block" (a stored
// program or DECLARE — may end at EOF) or "begin" (needs an explicit '/').
func plsqlBlockKind(s string) string {
	switch {
	case plsqlHeadRe.MatchString(s), plsqlDeclareRe.MatchString(s):
		return "block"
	case plsqlBeginRe.MatchString(s):
		return "begin"
	}
	return ""
}

// takePLSQLBlock returns the block starting at i, the offset just past it, and
// whether a block was recognised at all.
//
// Two terminators, both unambiguous:
//
//   - '/' alone on a line — the SQL*Plus convention every Oracle deployment
//     script uses. Whatever follows it stays a separate statement.
//   - end of input — accepted ONLY when the block is the entire input (atStart)
//     and the text ends on END; . With nothing trailing it, there is nothing a
//     merge could swallow, and this is what makes pasting one package body work.
//
// Anything else — a body with no terminator followed by further statements — is
// left to ordinary semicolon splitting. Over-splitting a script Oracle would
// have rejected anyway beats merging a statement nobody judged.
func takePLSQLBlock(sql string, i int, atStart bool) (body string, next int, ok bool) {
	kind := plsqlBlockKind(sql[i:])
	if kind == "" {
		return "", 0, false
	}
	for pos := i; pos < len(sql); {
		lineEnd := strings.IndexByte(sql[pos:], '\n')
		abs := len(sql)
		if lineEnd >= 0 {
			abs = pos + lineEnd
		}
		if strings.TrimSpace(sql[pos:abs]) == "/" {
			b := strings.TrimSpace(sql[i:pos])
			if b == "" {
				return "", 0, false
			}
			if abs >= len(sql) {
				return b, len(sql), true
			}
			return b, abs + 1, true // skip the newline that ends the '/' line
		}
		if lineEnd < 0 {
			break
		}
		pos = abs + 1
	}
	// No '/' anywhere: only a whole-input block may end at EOF.
	if kind == "block" && atStart && plsqlTailRe.MatchString(sql[i:]) {
		if b := strings.TrimSpace(sql[i:]); b != "" {
			return b, len(sql), true
		}
	}
	return "", 0, false
}
