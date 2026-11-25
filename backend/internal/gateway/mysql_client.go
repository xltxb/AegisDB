package gateway

// mysql 客户端的显示指令。
//
// `SELECT * FROM t\G` —— 结尾那个 `\G` 是 **mysql 客户端**的语句终结符,意思是
// "执行,并且按竖排显示"。客户端把它吃掉、只把前面那段 SQL 发给服务端。网关走驱动,
// 原样发过去,MySQL 报语法错误。
//
// 和 Oracle 的 SHOW、psql 的反斜杠是同一类问题,但它更好办:`\G` 只是**显示方式**,
// 剥掉之后语句本身完全有效,不需要翻译成别的东西。平台的终端本来就用表格展示,
// 竖排与否在这里没有意义。
//
// 同样在判定之后剥:判定看到的是原文,`SELECT * FROM t\G` 的动词照样是 SELECT,
// 字典照样扫得到整段文本。

import (
	"regexp"
	"strings"
)

// 结尾的 \G 或 \g,允许后面跟一个分号和空白。
//
// 只认**结尾**,是为了不碰字符串字面量里的内容:一条以 \G 结尾的语句,它的最后一个
// 字符是 G;而 `SELECT 'a\G' FROM t` 的最后一个字符是引号或 t,落不进这条正则。
var mysqlVerticalRe = regexp.MustCompile(`(?s)\\[Gg]\s*;?\s*$`)

// clientCommandSQL translates a client-side command into equivalent SQL for the
// engine family, or returns the statement untouched.
//
// 只在**下发前**调用。每个家族的细节在各自的文件里(oracle_sqlplus.go /
// postgres_psql.go / 本文件),这里只负责按家族分派。
func clientCommandSQL(engine, sql string) string {
	switch engineFamily(engine) {
	case familyOracle:
		if out, ok := OracleSQLPlus(sql); ok {
			return out
		}
	case familyPostgres:
		if out, ok := PostgresPsqlMeta(sql); ok {
			return out
		}
	case familyMySQL:
		if out, ok := MySQLStripVertical(sql); ok {
			return out
		}
	}
	return sql
}

// MySQLStripVertical removes a trailing \G / \g terminator.
//
// 第二个返回值是"剥了没有"。
func MySQLStripVertical(sql string) (string, bool) {
	if !strings.Contains(sql, `\`) {
		return sql, false // 绝大多数语句在这里就走了,不必跑正则
	}
	out := mysqlVerticalRe.ReplaceAllString(sql, "")
	if out == sql {
		return sql, false
	}
	// 剥完只剩空白说明整条就是一个 \G —— 那不是一条语句,原样退回去让服务端说话。
	if strings.TrimSpace(out) == "" {
		return sql, false
	}
	return strings.TrimRight(out, " \t\r\n"), true
}
