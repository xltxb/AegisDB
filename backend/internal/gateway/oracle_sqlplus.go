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
	// EXEC :n := 表达式。与上面一样按写判(动词是 EXEC),只是换成赋值的形状。
	oraExecAssignRe = regexp.MustCompile(`(?is)^\s*EXEC(?:UTE)?\s+(:[A-Za-z][A-Za-z0-9_$#]*\s*:=\s*[^;]+?)\s*;?\s*$`)

	// DESC[RIBE] [schema.]<表>。
	//
	// 名字被限死在标识符字符集里。它最终会拼进一个字符串字面量,所以这条正则是一道
	// **安全边界**,不只是解析规则 —— 放宽它之前先想清楚。
	// SHOW ERR[ORS] 不带对象(交给 OracleClientCommand 按"已忽略"处理)。
	oraShowErrBareRe = regexp.MustCompile(`(?is)^\s*SHOW\s+ERR(?:O|OR|ORS)?\s*;?\s*$`)
	// SHOW ERR[ORS] <类型> [schema.]<名字>(sqlplus-compat 工单 02)。名字与 DESC 一样限死在
	// 标识符字符集里 —— 它会拼进字符串字面量,这条正则是安全边界。
	oraShowErrRe = regexp.MustCompile(`(?is)^\s*SHOW\s+ERR(?:O|OR|ORS)?\s+` +
		`(PACKAGE\s+BODY|PACKAGE|PROCEDURE|FUNCTION|TRIGGER|VIEW|TYPE\s+BODY|TYPE|JAVA\s+SOURCE|JAVA\s+CLASS|DIMENSION)\s+` +
		`([A-Za-z0-9_$#]+)(?:\.([A-Za-z0-9_$#]+))?\s*;?\s*$`)

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
		// EXEC :n := 表达式 —— 给绑定变量赋值(sqlplus-compat 06)
		if m := oraExecAssignRe.FindStringSubmatch(s); m != nil {
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
	case oraShowErrRe.MatchString(s):
		return oracleShowErrors(oraShowErrRe.FindStringSubmatch(s)), true
	case oraShowParamRe.MatchString(s):
		m := oraShowParamRe.FindStringSubmatch(s)
		name := quoteLit(strings.ToLower(m[1]))
		return `SELECT NAME, VALUE, ISDEFAULT FROM V$PARAMETER WHERE NAME LIKE '%` + name + `%' ORDER BY NAME`, true
	}
	// 其余的 SHOW(SQLCODE、SPOOL、AUTOCOMMIT…)翻译不了:它们问的是
	// **sqlplus 自己的会话状态**,那个状态在网关这边根本不存在。硬造一个答案,
	// 等于让人以为自己在跟一个有历史的会话说话。原样下发,让 Oracle 说实话。
	return s, false
}

// oracleShowErrors 把 SHOW ERRORS <类型> [schema.]<名字> 翻成 ALL_ERRORS 查询,两列与 SQL*Plus
// 一致(LINE/COL、ERROR)。没有错误时终端说 "No errors."(查出零行)。
//
// 不带 schema 时按**当前 schema** 查(SYS_CONTEXT 而不是 USER):终端可能 ALTER SESSION SET
// CURRENT_SCHEMA 过,CREATE 建在当前 schema 下,错误也记在那里。
func oracleShowErrors(m []string) string {
	typ := strings.ToUpper(strings.Join(strings.Fields(m[1]), " "))
	owner, name := "", m[2]
	if m[3] != "" {
		owner, name = m[2], m[3]
	}
	ownerCond := `SYS_CONTEXT('USERENV','CURRENT_SCHEMA')`
	if owner != "" {
		ownerCond = `'` + quoteLit(strings.ToUpper(owner)) + `'`
	}
	return `SELECT TO_CHAR(LINE) || '/' || TO_CHAR(POSITION) AS "LINE/COL", TEXT AS "ERROR"` +
		` FROM ALL_ERRORS WHERE OWNER = ` + ownerCond +
		` AND NAME = '` + quoteLit(strings.ToUpper(name)) + `' AND TYPE = '` + quoteLit(typ) + `'` +
		` ORDER BY SEQUENCE`
}

// oracleDescribe turns DESC[RIBE] <表> into a query on the data dictionary.
//
// 用 ALL_TAB_COLUMNS 而不是 USER_TAB_COLUMNS:人经常 desc 别的 schema 下的表,
// 而 USER_ 只看得见自己的。ALL_ 覆盖的是"这个账号有权限看到的",正是 sqlplus 的行为。
//
// 但 ALL_ 看得见的同名表可能不止一张:从前不带 schema 时只按 TABLE_NAME 过滤,
// 同名表在两个 schema 各有一份,每列就出两遍。现在先把名字解析成**一个**属主
// (oracleDescTarget),再取它的列。
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
	return `SELECT C.COLUMN_NAME, C.DATA_TYPE, C.DATA_LENGTH, C.NULLABLE, C.DATA_DEFAULT` +
		` FROM ALL_TAB_COLUMNS C, (` + oracleDescTarget(strings.ToUpper(owner), strings.ToUpper(table)) + `) T` +
		` WHERE C.OWNER = T.OWNER AND C.TABLE_NAME = T.TABLE_NAME ORDER BY C.COLUMN_ID`, true
}

// oracleDescTarget 把 DESC 的名字解析成**一个** (OWNER, TABLE_NAME),顺序同 sqlplus:
// 当前 schema 的表/视图 → 当前 schema 的私有同义词 → 公共同义词。当前 schema 用
// SYS_CONTEXT 而不是 USER:终端标签切过 schema(ALTER SESSION SET CURRENT_SCHEMA)。
//
// 最后一档是 sqlplus 没有的:都找不到、而整个实例只有**一个**看得见的属主有这张表时取它
// —— 只读账号靠授权读业务库、没建同义词,从前 desc 能看到列,修重复不该把它变成查无此表。
// 两个以上属主就不猜。带 schema 时只查那个 schema(表,或它的私有同义词)。
//
// 前端 metaCommand.ts 的 oracleDescTarget 是同一份解析,改一处要改两处。
func oracleDescTarget(owner, table string) string {
	own := `SYS_CONTEXT('USERENV','CURRENT_SCHEMA')`
	if owner != "" {
		own = `'` + quoteLit(owner) + `'`
	}
	name := `'` + quoteLit(table) + `'`
	branches := []string{
		`SELECT OWNER, TABLE_NAME, 1 AS PRI FROM ALL_TAB_COLUMNS WHERE OWNER = ` + own + ` AND TABLE_NAME = ` + name + ` AND ROWNUM = 1`,
		`SELECT TABLE_OWNER, TABLE_NAME, 2 FROM ALL_SYNONYMS WHERE OWNER = ` + own + ` AND SYNONYM_NAME = ` + name + ` AND DB_LINK IS NULL`,
	}
	if owner == "" {
		branches = append(branches,
			`SELECT TABLE_OWNER, TABLE_NAME, 3 FROM ALL_SYNONYMS WHERE OWNER = 'PUBLIC' AND SYNONYM_NAME = `+name+` AND DB_LINK IS NULL`,
			`SELECT MIN(OWNER), TABLE_NAME, 4 FROM ALL_TAB_COLUMNS WHERE TABLE_NAME = `+name+` GROUP BY TABLE_NAME HAVING COUNT(DISTINCT OWNER) = 1`)
	}
	// ORDER BY 不能直接挂在 UNION ALL 上:老版本 Oracle 在内联视图里认不出复合查询的别名
	// (现场报 ORA-00904: "PRI",23ai 不报)。先包成一层视图,再按它的列排 —— 经典的 top-N 写法。
	return `SELECT OWNER, TABLE_NAME FROM (SELECT OWNER, TABLE_NAME FROM (` + strings.Join(branches, ` UNION ALL `) + `) ORDER BY PRI) WHERE ROWNUM = 1`
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
