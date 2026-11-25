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
	//
	// MySQL 的存储程序也在这里(2026-10):CREATE [DEFINER = 用户] PROCEDURE / FUNCTION /
	// TRIGGER / EVENT,体里同样满是分号。mysqldump 导出的都带 DEFINER。
	plsqlHeadRe = regexp.MustCompile(`(?is)^\s*CREATE\s+(?:OR\s+REPLACE\s+)?(?:(?:NON)?EDITIONABLE\s+)?(?:DEFINER\s*=\s*\S+\s+)?(?:PACKAGE|PROCEDURE|FUNCTION|TRIGGER|TYPE|EVENT)\b`)
	// An anonymous block. DECLARE is unambiguous; BEGIN is NOT — in MySQL it
	// opens a transaction ("BEGIN; UPDATE …; COMMIT;"), so a bare BEGIN counts as
	// a block only when a '/' terminator proves the script is PL/SQL.
	plsqlDeclareRe = regexp.MustCompile(`(?is)^\s*DECLARE\b`)
	plsqlBeginRe   = regexp.MustCompile(`(?is)^\s*BEGIN\b`)
	// The tail of a complete block: END; or END <name>;
	plsqlTailRe = regexp.MustCompile(`(?is)\bEND\s*(?:[A-Za-z_][A-Za-z0-9_$#]*\s*)?;\s*$`)
	// 同上,但分号可有可无。只用于"整段输入就是这一个存储程序"的情形:MySQL 脚本按
	// DELIMITER $$ 拆出来的一条以 END 结尾(分隔符被去掉了,没有分号),这条再经过一次
	// 分句(终端批量执行、审批执行都会)时不能被拆碎 —— 2026-10 生产:建分区的脚本里
	// 第一个 CREATE FUNCTION 被拆成 13 段,第一段就报 1064。
	plsqlTailOptSemiRe = regexp.MustCompile(`(?is)\bEND\s*(?:[A-Za-z_][A-Za-z0-9_$#]*\s*)?;?\s*$`)
)

// IsPLSQLBlock reports whether s begins a PL/SQL block (stored program, DECLARE
// or anonymous BEGIN).
//
// SplitStatements hands such a block back WHOLE, because that is the unit Oracle
// executes. Anything that JUDGES the statement rather than running it has the
// opposite need — a DROP inside a package body is still a DROP — and must look
// inside. This tells it when to.
func IsPLSQLBlock(s string) bool { return plsqlBlockKind(s) != "" }

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
		if isSlashLine(sql[pos:abs]) {
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
	// 没有 '/' 时整块交回(下面三种)的前提:这个存储程序**确实有块体**。单语句的 MySQL 触发器 / 事件 /
	// 函数(CREATE TRIGGER … SET NEW.c = NOW(); CREATE EVENT … DO DELETE …;)在第一个分号就结束了,
	// 后面是另一条语句 —— 151d04a 起只要全文以 END; / END 结尾(常见的是 CASE … END;)就被整段并成一条,
	// 下发即 1064,后面那条也不再单独判定(2026-10-26 讨论 12)。
	if kind == "block" && !hasBlockBody(sql[i:]) {
		return "", 0, false
	}
	// No '/' anywhere: only a whole-input block may end at EOF.
	if atStart && plsqlTailRe.MatchString(sql[i:]) && (kind == "block" || beginIsAnonymousBlock(sql[i:])) {
		if b := strings.TrimSpace(sql[i:]); b != "" {
			return b, len(sql), true
		}
	}
	// 整段输入是一个存储程序 / DECLARE 块、以不带分号的 END 结尾:同样整块(见 plsqlTailOptSemiRe)。
	// 整段就这一条,后面没有东西可吞。裸 BEGIN 不在此列 —— MySQL 里那是开事务。
	if atStart && kind == "block" && plsqlTailOptSemiRe.MatchString(sql[i:]) {
		if b := strings.TrimSpace(sql[i:]); b != "" {
			return b, len(sql), true
		}
	}
	// A STORED program running to end of input on END; is taken whole even when it
	// is not the first statement — a package spec ended by '/' and then a body that
	// lost its own '/' is the usual shape. Splitting it is not the safe direction
	// here: Oracle does not reject the first fragment `CREATE OR REPLACE PACKAGE
	// BODY … AS <first declaration>`, it REPLACES the existing body with it and then
	// reports compilation errors, so the fragments destroy a working object.
	if !atStart && plsqlHeadRe.MatchString(sql[i:]) && plsqlTailRe.MatchString(sql[i:]) {
		if b := strings.TrimSpace(sql[i:]); b != "" {
			return b, len(sql), true
		}
	}
	return "", 0, false
}

// blockOpenerRe:存储程序头之后、第一个分号之前出现这些词,说明后面是块体(Oracle 的 IS / AS、
// 匿名部分的 DECLARE、MySQL / Oracle 的 BEGIN)。
var blockOpenerRe = regexp.MustCompile(`(?is)\b(?:BEGIN|DECLARE|IS|AS)\b`)

// hasBlockBody 报告存储程序 s 在它的第一个分号之前是否已经打开了块体。没有的话,第一个分号就是
// 这条语句的结尾。字符串、注释里的分号不算。
func hasBlockBody(s string) bool {
	end := len(s)
	for j := 0; j < len(s); j++ {
		switch c := s[j]; {
		case c == '\'' || c == '"' || c == '`':
			for j++; j < len(s) && s[j] != c; j++ {
				if s[j] == '\\' && c == '\'' {
					j++
				}
			}
		case c == '-' && j+1 < len(s) && s[j+1] == '-':
			for j < len(s) && s[j] != '\n' {
				j++
			}
		case c == '/' && j+1 < len(s) && s[j+1] == '*':
			if k := strings.Index(s[j+2:], "*/"); k >= 0 {
				j += k + 3
			} else {
				j = len(s)
			}
		case c == ';':
			end = j
			j = len(s)
		}
	}
	return blockOpenerRe.MatchString(s[:end])
}

// isSlashLine reports whether a line is the SQL*Plus block terminator: '/' alone,
// or '/;' — in the gateway terminal a statement is submitted on ';', so a script
// pasted there naturally ends in '/;'. Not recognising it left the last block
// unterminated and split on its inner semicolons (see takePLSQLBlock).
func isSlashLine(line string) bool {
	l := strings.TrimSpace(line)
	return l == "/" || l == "/;"
}

// beginIsAnonymousBlock resolves the one ambiguity in the BEGIN keyword without
// knowing the engine.
//
// MySQL/PostgreSQL open a transaction with `BEGIN;` or `BEGIN WORK;` — BEGIN is
// the WHOLE statement, so a separator follows it immediately. Oracle's anonymous
// block is `BEGIN <statements> END;` — the keyword is followed by the body.
// The caller has already checked that the text ends in END; and is the entire
// input, so together the two say: this is a PL/SQL block, not a transaction.
//
// Why it is worth resolving: `BEGIN … END;` with no trailing '/' is what a DBA
// actually pastes into a terminal, and splitting it on the body's semicolons
// produced fragments Oracle rejects one by one — the block simply could not be
// run from the console.
//
// What it costs: strict mode's "unscoped mutation" check reads the statement's
// leading verb, so a DELETE inside a merged block no longer trips it. That trade
// is not new — it already applied to DECLARE and CREATE PROCEDURE blocks — and
// the layer that matters still holds: the high-risk dictionary scans the whole
// block text, so a DROP or DELETE inside one is caught exactly as before.
func beginIsAnonymousBlock(s string) bool {
	m := plsqlBeginRe.FindString(s)
	if m == "" {
		return false
	}
	rest := strings.TrimSpace(s[len(m):])
	// `BEGIN;` / `BEGIN WORK;` / `BEGIN TRANSACTION;` — a transaction opener.
	if rest == "" || strings.HasPrefix(rest, ";") {
		return false
	}
	switch strings.ToUpper(firstWordOf(rest)) {
	case "WORK", "TRANSACTION", "ISOLATION", "DEFERRED", "IMMEDIATE", "EXCLUSIVE":
		return false
	}
	return true
}

func firstWordOf(s string) string {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == ';' {
			return s[:i]
		}
	}
	return s
}
