package sqlutil

import "strings"

// 方言开关。两件事只对一家成立,开错人就出事,所以它们不能是全局行为。
type dialectRules struct {
	// nestedBlockComments:`/* a /* b */ c */` 整段都是注释。**只有 PostgreSQL**。
	//
	// 一刀切会开一个绕过判定的口子:MySQL 读 `/* /* */ DROP TABLE t; */` 时,注释到
	// 第一个 */ 就结束,DROP TABLE t 是一条**真语句**。若按嵌套拆,整段成了注释,判定层
	// 看不见那个 DROP,而服务端照样执行它。
	nestedBlockComments bool
	// backslashG:`\G` 既是语句分隔符,也要从语句里剥掉。**只有 MySQL 客户端**。
	// 它不是 SQL,原样发给驱动是语法错。
	backslashG bool
}

// rulesFor 把引擎标签映射成方言开关。
//
// 未知标签一律拿最保守的那一套(两个开关都关):与 MySQL/Oracle 的注释规则一致,
// 也就是"不把任何东西额外当成注释" —— 判定层因此只会多看见,不会少看见。
func rulesFor(engine string) dialectRules {
	e := strings.ToLower(strings.TrimSpace(engine))
	switch {
	case strings.Contains(e, "postgres") || strings.Contains(e, "pg") ||
		strings.Contains(e, "gauss") || strings.Contains(e, "dws") ||
		strings.Contains(e, "opengauss") || strings.Contains(e, "greenplum"):
		return dialectRules{nestedBlockComments: true}
	case strings.Contains(e, "mysql") || strings.Contains(e, "mariadb") ||
		strings.Contains(e, "tidb") || strings.Contains(e, "polardb") ||
		strings.Contains(e, "oceanbase"):
		return dialectRules{backslashG: true}
	}
	return dialectRules{}
}

// SplitStatementsFor 按引擎方言拆分。engine 为空或不认识时,行为与 SplitStatements 完全一致。
//
// 之所以是独立入口而不是给 SplitStatements 加参数:那个函数有二十来个调用点,其中
// 多数(迁移、脚本扫描、审计脱敏)本来就没有"目标引擎"这个概念 —— 给它们编一个出来,
// 只会让"这里该传什么"变成一个每次都要重新想的问题。
func SplitStatementsFor(engine, sql string) []string {
	r := rulesFor(engine)
	if r == (dialectRules{}) {
		return SplitStatements(sql)
	}
	return splitWith(r, sql)
}
