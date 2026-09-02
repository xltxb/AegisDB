package gateway

// SQL*Plus 的客户端命令,在网关这边翻译成等价的 SQL。
//
// 起因:`SHOW USER` 在 sqlplus 里跑得好好的,在平台上报 ORA-00900。两边都没错 ——
// **SHOW 不是 SQL**,它是 SQL*Plus 自己的命令,由客户端解释掉,从来不会发到服务端。
// 网关走的是驱动,原样发过去,Oracle 说"这不是一条 SQL 语句",完全正确。
//
// DESC[RIBE] 是同一回事,而且人敲得比 SHOW 还勤。
//
// 这和之前 MySQL 的 DELIMITER 属于同一类问题:人以为自己在跟数据库说话,其实那句话
// 是说给客户端听的。解决办法也一样 —— 由网关承担客户端那一半职责。
//
// 翻译的位置很讲究:**必须在判定之后、下发之前**。
//
// 高危字典和规范审查匹配的是**文本里的那个词**。如果先翻译再判定,一条写着
// "SHOW PARAMETER" 的字典规则就再也匹配不到任何东西 —— 那是一个绕过。所以判定看到
// 的永远是用户输入的原文,只有交给驱动的那一份被改写。
//
// 覆盖面刻意窄:只翻译语义明确、等价 SQL 没有歧义的几条。翻不了的原样下发,让
// Oracle 自己报错 —— 一条报 ORA-00900 的语句,比一条被平台猜着改写成别的东西然后
// 悄悄执行了的语句要好得多。

import (
	"regexp"
	"strings"
)

var (
	// SHOW USER —— 当前登录的是谁。
	oraShowUserRe = regexp.MustCompile(`(?is)^\s*SHOW\s+USER\s*;?\s*$`)
	// SHOW CON_NAME —— 多租户下当前容器。
	oraShowConNameRe = regexp.MustCompile(`(?is)^\s*SHOW\s+CON_NAME\s*;?\s*$`)
	// SHOW RELEASE / SHOW VERSION —— 版本。
	oraShowRelRe = regexp.MustCompile(`(?is)^\s*SHOW\s+(?:REL(?:EASE)?|VERSION)\s*;?\s*$`)
	// SHOW PARAMETER <名字片段> —— sqlplus 是按子串模糊匹配的,这里保持一致。
	oraShowParamRe = regexp.MustCompile(`(?is)^\s*SHOW\s+PARAM(?:ETER)?S?\s+([A-Za-z0-9_$#]+)\s*;?\s*$`)

	// EXEC[UTE] <过程> —— SQL*Plus 对 BEGIN <过程>; END; 的简写,服务端不认得。
	//
	// 这里只认"看起来像一次调用"的形状(标识符,可带 schema 前缀,可带参数表)。
	// 参数原样搬进 BEGIN…END,因为那本来就是一段 PL/SQL —— 但正因如此,调用它
	// 需要的权限一点不能少:判定看到的是原文 EXEC,而未知动词一律按 write 判,
	// 这与今天 CALL 的待遇一致(存储过程能干任何事,所以按写处理)。
	oraExecRe = regexp.MustCompile(`(?is)^\s*EXEC(?:UTE)?\s+([A-Za-z0-9_$#.]+(?:\s*\([^;]*\))?)\s*;?\s*$`)

	// DESC[RIBE] [schema.]<表>。
	//
	// 名字被限死在标识符字符集里。它最终会拼进一个字符串字面量,所以这条正则是一道
	// **安全边界**,不只是解析规则 —— 放宽它之前先想清楚。
	oraDescRe = regexp.MustCompile(`(?is)^\s*DESC(?:RIBE)?\s+([A-Za-z0-9_$#]+)(?:\.([A-Za-z0-9_$#]+))?\s*;?\s*$`)
)

// OracleSQLPlus rewrites a SQL*Plus client command into equivalent SQL.
//
// 第二个返回值是"翻译了没有"。没翻译就原样下发。
func OracleSQLPlus(sql string) (string, bool) {
	s := strings.TrimSpace(sql)
	// 绝大多数语句在这里就走了,不必跑那几条正则。
	switch strings.ToUpper(firstWordOf(s)) {
	case "SHOW":
		return oracleShow(s)
	case "DESC", "DESCRIBE":
		return oracleDescribe(s)
	case "EXEC", "EXECUTE":
		if m := oraExecRe.FindStringSubmatch(s); m != nil {
			return "BEGIN " + strings.TrimSpace(m[1]) + "; END;", true
		}
	}
	return sql, false
}

func oracleShow(s string) (string, bool) {
	switch {
	case oraShowUserRe.MatchString(s):
		// sqlplus 打印的是 USER is "SCOTT";这里回一个结果集,因为平台的终端本来就
		// 用表格展示 —— 让它长得像一次查询,比伪造一行 sqlplus 的文字输出更诚实。
		return `SELECT USER AS "USER" FROM DUAL`, true
	case oraShowConNameRe.MatchString(s):
		return `SELECT SYS_CONTEXT('USERENV','CON_NAME') AS "CON_NAME" FROM DUAL`, true
	case oraShowRelRe.MatchString(s):
		return `SELECT BANNER AS "RELEASE" FROM V$VERSION WHERE ROWNUM = 1`, true
	case oraShowParamRe.MatchString(s):
		m := oraShowParamRe.FindStringSubmatch(s)
		name := quoteLit(strings.ToLower(m[1]))
		return `SELECT NAME, VALUE, ISDEFAULT FROM V$PARAMETER WHERE NAME LIKE '%` + name + `%' ORDER BY NAME`, true
	}
	// 其余的 SHOW(SQLCODE、ERRORS、SPOOL、AUTOCOMMIT…)翻译不了:它们问的是
	// **sqlplus 自己的会话状态**,那个状态在网关这边根本不存在。硬造一个答案,
	// 等于让人以为自己在跟一个有历史的会话说话。原样下发,让 Oracle 说实话。
	return s, false
}

// oracleDescribe turns DESC[RIBE] <表> into a query on the data dictionary.
//
// 用 ALL_TAB_COLUMNS 而不是 USER_TAB_COLUMNS:人经常 desc 别的 schema 下的表,
// 而 USER_ 只看得见自己的。ALL_ 覆盖的是"这个账号有权限看到的",正是 sqlplus 的行为。
func oracleDescribe(s string) (string, bool) {
	m := oraDescRe.FindStringSubmatch(s)
	if m == nil {
		return s, false // desc 的是表达式或带引号的怪名字 —— 不猜,原样下发
	}
	owner, table := "", m[1]
	if m[2] != "" {
		owner, table = m[1], m[2]
	}
	// Oracle 把未加引号的标识符按大写存进数据字典,所以这里要转大写 —— 敲
	// `desc t_user` 的人期望看到 T_USER 的列,而不是"查无此表"。
	q := `SELECT COLUMN_NAME, DATA_TYPE, DATA_LENGTH, NULLABLE, DATA_DEFAULT` +
		` FROM ALL_TAB_COLUMNS WHERE TABLE_NAME = '` + quoteLit(strings.ToUpper(table)) + `'`
	if owner != "" {
		q += ` AND OWNER = '` + quoteLit(strings.ToUpper(owner)) + `'`
	}
	return q + ` ORDER BY COLUMN_ID`, true
}

// quoteLit escapes a single quote for an SQL string literal.
//
// 正则已经不允许引号进来,所以这一步是多余的 —— 留着是因为"正则限住了"这条保证会
// 随着正则被改而失效,而这一行不会。
func quoteLit(s string) string { return strings.ReplaceAll(s, "'", "''") }

func firstWordOf(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, " \t\r\n"); i > 0 {
		return s[:i]
	}
	return s
}
