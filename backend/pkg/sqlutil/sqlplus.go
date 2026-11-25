package sqlutil

import (
	"regexp"
	"sort"
	"strings"
)

// SQL*Plus 的 SET 行。
//
// SQL*Plus 的客户端命令以**换行**结束,不需要分号:脚本里写的是
//
//	SET SERVEROUTPUT ON
//	BEGIN … END;
//	/
//
// 按分号切,SET 会和下一行的 BEGIN 粘成一条,被当成 SET 吞掉,剩下的 END 发给 Oracle 就是
// ORA-00900。所以在语句边界、行首遇到 SQL*Plus 的 SET 且这一行没有分号时,这一行自己就是
// 一条语句(与 DELIMITER 指令同一个做法)。
//
// 这个函数不知道引擎,所以只认 SQL*Plus 的**选项名白名单**:MySQL / PostgreSQL 的 SET 目标
// (@变量、NAMES、search_path、time_zone …)都不在里面,它们多行书写的写法(SET @sql = CONCAT(…
// 换行 …);)照旧按分号切。与 MySQL / PG 变量重名的选项(AUTOCOMMIT、TIME)不在白名单里:
// 带分号时它们照常是一条语句,只是不做"按行结束"。
//
// 白名单是 SQL*Plus 的**全部** SET 选项,每个从最短缩写到全称都认(2026-10-28:从前只列了几个
// 写法,`SET HEAD OFF` 不在里面,和下一行粘成一条,执行器把整段当 SET 忽略 —— 下一条语句被悄悄
// 跳过)。
var sqlPlusSetLineRe = regexp.MustCompile(`(?i)^SET\s+(` + sqlPlusSetOptionAlt() + `)\b`)

// sqlPlusSetOptions 是 SQL*Plus 的 SET 选项:全称与最短缩写(SQL*Plus 手册里 SET 一节方括号前的部分)。
var sqlPlusSetOptions = []struct{ full, min string }{
	{"APPINFO", "APPI"}, {"ARRAYSIZE", "ARRAY"}, {"AUTOPRINT", "AUTOP"}, {"AUTORECOVERY", "AUTORECOVERY"},
	{"AUTOTRACE", "AUTOT"}, {"BLOCKTERMINATOR", "BLO"}, {"CMDSEP", "CMDS"}, {"COLINVISIBLE", "COLINVI"},
	{"COLSEP", "COLSEP"}, {"CONCAT", "CON"}, {"COPYCOMMIT", "COPYC"}, {"COPYTYPECHECK", "COPYTYPECHECK"},
	{"DEFINE", "DEF"}, {"DESCRIBE", "DESCRIBE"}, {"ECHO", "ECHO"}, {"EDITFILE", "EDITF"}, {"EMBEDDED", "EMB"},
	{"ERRORDETAILS", "ERRORDETAILS"}, {"ERRORLOGGING", "ERRORL"}, {"ESCAPE", "ESC"}, {"ESCCHAR", "ESCCHAR"},
	{"EXITCOMMIT", "EXITC"}, {"FEEDBACK", "FEED"}, {"FLAGGER", "FLAGGER"}, {"FLUSH", "FLU"}, {"HEADING", "HEA"},
	{"HEADSEP", "HEADS"}, {"INSTANCE", "INSTANCE"}, {"LINESIZE", "LIN"}, {"LOBOFFSET", "LOBOF"},
	{"LOBPREFETCH", "LOBPREFETCH"}, {"LOGSOURCE", "LOGSOURCE"}, {"LONG", "LONG"}, {"LONGCHUNKSIZE", "LONGC"},
	{"MARKUP", "MARK"}, {"NEWPAGE", "NEWP"}, {"NULL", "NULL"}, {"NUMFORMAT", "NUMF"}, {"NUMWIDTH", "NUM"},
	{"PAGESIZE", "PAGES"}, {"PAUSE", "PAU"}, {"RECSEP", "RECSEP"}, {"RECSEPCHAR", "RECSEPCHAR"},
	{"ROWLIMIT", "ROWLIMIT"}, {"ROWPREFETCH", "ROWPREFETCH"}, {"SCAN", "SCAN"}, {"SERVEROUTPUT", "SERVEROUT"},
	{"SHIFTINOUT", "SHIFT"}, {"SHOWMODE", "SHOW"}, {"SQLBLANKLINES", "SQLBL"}, {"SQLCASE", "SQLC"},
	{"SQLCONTINUE", "SQLCO"}, {"SQLNUMBER", "SQLN"}, {"SQLPLUSCOMPATIBILITY", "SQLPLUSCOMPAT"},
	{"SQLPREFIX", "SQLPRE"}, {"SQLPROMPT", "SQLP"}, {"SQLTERMINATOR", "SQLT"}, {"STATEMENTCACHE", "STATEMENTC"},
	{"SUFFIX", "SUF"}, {"TAB", "TAB"}, {"TERMOUT", "TERM"}, {"TIMING", "TIMI"}, {"TRIMOUT", "TRIM"},
	{"TRIMSPOOL", "TRIMS"}, {"UNDERLINE", "UND"}, {"VERIFY", "VER"}, {"WRAP", "WRA"}, {"XQUERY", "XQUERY"},
	// 不在这里:AUTOCOMMIT / AUTO(MySQL 的 SET autocommit)、TIME / TI(PostgreSQL 的 SET TIME ZONE)
}

// sqlPlusSetOptionAlt 把每个选项从最短缩写到全称的所有写法拼成正则的分支,长的在前。
func sqlPlusSetOptionAlt() string {
	seen := map[string]bool{}
	var alts []string
	for _, o := range sqlPlusSetOptions {
		for n := len(o.full); n >= len(o.min); n-- {
			if w := o.full[:n]; !seen[w] {
				seen[w] = true
				alts = append(alts, w)
			}
		}
	}
	sort.Slice(alts, func(i, j int) bool { return len(alts[i]) > len(alts[j]) })
	return strings.Join(alts, "|")
}

// SQL*Plus 的其余行命令(2026-10 sqlplus-compat 工单 01/03):同样以换行结束、不写分号。
//
// 只收**任何 SQL 方言都不会拿来开头**的词,和 SET 白名单同一个思路:MySQL / PostgreSQL 的
// 语句没有以 PROMPT、REM、DEFINE、COLUMN … 开头的,所以这里不必知道引擎。缩写按 SQL*Plus
// 的最短写法列出(COL、DEF、PRO …),必须是整词。COPY 不在里面 —— 它是 PostgreSQL 的语句。
var sqlPlusLineCmdRe = regexp.MustCompile(`(?i)^(?:PROMPT|PROMP|PROM|PRO|REMARK|REMAR|REMA|REM|` +
	`DEFINE|DEFIN|DEFI|DEF|UNDEFINE|UNDEFIN|UNDEFI|UNDEF|ACCEPT|ACCEP|ACCE|ACC|` +
	`COLUMN|COLUM|COLU|COL|CLEAR|CLEA|CL|BREAK|BREA|BRE|COMPUTE|COMPUT|COMPU|COMP|` +
	`TTITLE|TTITL|TTIT|TTI|BTITLE|BTITL|BTIT|BTI|REPHEADER|REPHEADE|REPHEAD|REPHEA|REPHE|REPH|` +
	`REPFOOTER|REPFOOTE|REPFOOT|REPFOO|REPFO|REPF|PAUSE|PAUS|PAU|TIMING|TIMIN|TIMI|` +
	// 第二批(sqlplus-compat 07/09):脚本里的 WHENEVER / SPOOL / EXIT 同样一行一条。
	// @file 也是(以 @ 开头的语句在 MySQL / PostgreSQL 里不存在)。START 不在里面 —— 它是
	// MySQL / PostgreSQL 的 START TRANSACTION。
	`WHENEVER|SPOOL|SPOO|SPO|EXIT|QUIT|` +
	// 绑定变量(sqlplus-compat 06)
	`VARIABLE|VARIABL|VARIAB|VARIA|VARI|VAR|PRINT|PRIN|PRI)(?:\s|$)|^@`)

// sqlPlusTextLineRe:PROMPT / REMARK 后面是**原样的文字**,里面的分号、引号都只是字符
// (`REM don't` 的单引号若当成字符串开头,会一路吞到下一个单引号)。整行就是一条。
var sqlPlusTextLineRe = regexp.MustCompile(`(?i)^(?:PROMPT|PROMP|PROM|PRO|REMARK|REMAR|REMA|REM)(?:\s|$)`)

// sqlPlusShowErrRe:SHOW ERR[ORS] 在 SQL*Plus 脚本里常常紧跟在 CREATE … / 之后、不写分号。
// MySQL 也有 SHOW ERRORS,但它写分号时不受影响(行里有分号就不接手)。
var sqlPlusShowErrRe = regexp.MustCompile(`(?i)^SHOW\s+ERR(?:O|OR|ORS)?(?:\s|$)`)

// takeSQLPlusSetLine 在 i(语句边界、行首)识别一行 SQL*Plus 行命令(SET 及上面那些),返回这一行
// (去掉首尾空白)与下一行的起点。行里有分号时不接手,交给正常的分号切分 —— PROMPT / REMARK
// 例外,整行都是它的文字。
func takeSQLPlusSetLine(sql string, i int) (stmt string, next int, ok bool) {
	rest := sql[i:]
	end := strings.IndexByte(rest, '\n')
	line := rest
	next = len(sql)
	if end >= 0 {
		line = rest[:end]
		next = i + end + 1
	}
	line = strings.TrimSpace(line)
	if sqlPlusTextLineRe.MatchString(line) {
		return line, next, true
	}
	if strings.Contains(line, ";") {
		return "", 0, false
	}
	if !sqlPlusSetLineRe.MatchString(line) && !sqlPlusLineCmdRe.MatchString(line) && !sqlPlusShowErrRe.MatchString(line) {
		return "", 0, false
	}
	return line, next, true
}
