// Package oscroute 回答一个问题:这条 DDL 该不该改走 OSC(ADR 0011)。
//
// 它是纯函数层,不碰数据库 —— 与 osc 包把"采集事实"和"判定"分开是同一个理由:
// 这里的每条规则都能让一次变更走上另一条路,它必须能被单独测透,而不必搭一套流水线。
package oscroute

import (
	"regexp"
	"strings"
)

// IndexDDL 是一条被认下来的纯索引变更。
//
// Alter 是**交给 osc.StartRequest 的子句**,不是原句:CREATE INDEX / DROP INDEX
// 会被翻译成等价的 ALTER 形式,因为 OSC 收的是子句。
type IndexDDL struct {
	// Schema 是语句里带的库名前缀(剥之前的那个),没带前缀时是空字符串。
	//
	// 留着它是因为 Decide 要拿它和发布单的目标库比:cleanIdent 剥前缀是给 OSC 的
	// StartRequest 准备的(它分开收 schema 与 table,schema 来自发布单目标库),
	// 但"剥掉"不等于"这个前缀不重要"——一条 `app_archive.t_order` 若被当成目标库
	// `app` 里的 `t_order`,OSC 会在错误的库里认出一张同名表、加错索引,而语句真正
	// 想改的那张表一个字没动。这是错认,比"认不出、照常直发"贵得多。
	Schema string
	Table  string
	Alter  string
}

// identPat 匹配 `db`.`t` / db.t / t 三种形态。
const identPat = "[` \"\\w.$-]+?"

var (
	// ALTER TABLE t ADD [UNIQUE|FULLTEXT|SPATIAL] INDEX|KEY ... / DROP INDEX|KEY ...
	reAlterIndex = regexp.MustCompile(`(?is)^\s*ALTER\s+TABLE\s+(` + identPat + `)\s+` +
		`((?:ADD\s+(?:UNIQUE\s+|FULLTEXT\s+|SPATIAL\s+)?(?:INDEX|KEY)\s+.+)|(?:DROP\s+(?:INDEX|KEY)\s+\S+))\s*$`)
	// CREATE [UNIQUE|FULLTEXT|SPATIAL] INDEX name ON t (cols)
	reCreateIndex = regexp.MustCompile(`(?is)^\s*CREATE\s+(UNIQUE\s+|FULLTEXT\s+|SPATIAL\s+)?INDEX\s+(\S+)\s+ON\s+(` +
		identPat + `)\s*(\(.+\))\s*$`)
	// DROP INDEX name ON t
	reDropIndex = regexp.MustCompile(`(?is)^\s*DROP\s+INDEX\s+(\S+)\s+ON\s+(` + identPat + `)\s*$`)
)

// ParseIndexDDL 认出一条纯粹的索引变更,并给出交给 OSC 的 alter 子句。
//
// **宁可少认,不可错认。** 认不出来的后果是这条语句照常直发(原生 DDL 加索引本来
// 就是在线的);而错认的后果是把一条 OSC 做不了的变更交给它,那次发布会在 Preflight
// 那里失败 —— 人看到的是一张失败的发布单,而不是"这条语句不该走这条路"。
func ParseIndexDDL(sql string) (IndexDDL, bool) {
	s := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(sql), ";"))
	if s == "" {
		return IndexDDL{}, false
	}
	// 逗号分隔的多子句 ALTER 一律不认。ADD PRIMARY KEY 同理:它重建聚簇索引,
	// 不是加一个二级索引。两者都超出 ADR 0011 给这套东西划的范围。
	if m := reAlterIndex.FindStringSubmatch(s); m != nil {
		clause := strings.TrimSpace(m[2])
		if !isSingleIndexClause(clause) {
			return IndexDDL{}, false
		}
		schema, table := splitIdent(m[1])
		return IndexDDL{Schema: schema, Table: table, Alter: normalizeSpace(clause)}, true
	}
	if m := reCreateIndex.FindStringSubmatch(s); m != nil {
		kind := strings.ToUpper(strings.TrimSpace(m[1]))
		if kind != "" {
			kind += " "
		}
		schema, table := splitIdent(m[3])
		return IndexDDL{
			Schema: schema, Table: table,
			Alter: normalizeSpace("ADD " + kind + "INDEX " + stripQuotes(m[2]) + " " + m[4]),
		}, true
	}
	if m := reDropIndex.FindStringSubmatch(s); m != nil {
		schema, table := splitIdent(m[2])
		return IndexDDL{Schema: schema, Table: table, Alter: "DROP INDEX " + stripQuotes(m[1])}, true
	}
	return IndexDDL{}, false
}

// isSingleIndexClause 报告这个 ADD/DROP 子句是不是**一个**子句,而不是逗号拼起来的
// 好几个。
//
// 单双引号(字符串字面量)一律不认,不是"跳过它扫描" —— 那需要一个完整的引号+转义
// 状态机(转义引号、反斜杠……),而 ParseIndexDDL 的注释已经说清楚不引入 SQL parser。
// 字符串字面量里可以裸着放一个没有配对的 `(`(比如 COMMENT 里的 'see (spec'),
// 那样的裸括号会让下面的括号计数永久偏移,把真正分隔两个子句的顶层逗号误判成
// "在括号里"—— 这正是这条函数存在的理由被打穿的地方。宁可把带字符串字面量的索引
// 变更整体降级成"认不出来、照常直发"(原生加索引本来就是在线的),也不去猜引号
// 什么时候结束。
//
// 反引号不在此列,单独处理:它是标识符定界符,不是字符串字面量,`ADD INDEX i (`col`)`
// 是最常见的写法,拒掉它等于挡住大多数真实的 DDL。反引号成对出现、不支持转义,
// 配对规则简单可靠,扫描时把它包住的区间整段跳过(不数括号、不数逗号)就够了 ——
// 不要把这套"跳过"逻辑也套到单双引号上,那正是要避免的引号状态机。
func isSingleIndexClause(clause string) bool {
	depth := 0
	inBacktick := false
	for _, r := range clause {
		if inBacktick {
			if r == '`' {
				inBacktick = false
			}
			continue
		}
		switch r {
		case '`':
			inBacktick = true
		case '\'', '"':
			return false
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				return false
			}
		}
	}
	return true
}

// stripQuotes 去掉反引号/双引号,不碰前缀。
//
// 只用于**索引名**:索引名里的 "." 是名字的一部分(比如 `idx.v2`),不是库名前缀,
// 和表名的剥离规则不能共用 cleanIdent 那把剪刀 —— 共用的话 CREATE INDEX 会建出一个
// 改了名的索引,DROP INDEX 会找错对象,而且都不报错。
func stripQuotes(s string) string {
	s = strings.TrimSpace(s)
	s = strings.NewReplacer("`", "", `"`, "").Replace(s)
	return strings.TrimSpace(s)
}

// splitIdent 去掉反引号/双引号,并把库名前缀从表名里分离出来,两半都返回。
// 只用于**表名**(索引名走 stripQuotes,见该函数注释)。
//
// 库名前缀必须从 Table 里剥掉:OSC 的 StartRequest 分开收 schema 与 table,schema
// 来自发布单的目标库。带着前缀会拼出 `app`.`app.t_order` 这样的名字。但剥掉不等于
// 丢弃 —— Decide 要拿这个前缀去和发布单的目标库比对(I1),两个库都有同名表时,
// 不比对的后果是在错误的库里加错索引,而语句真正想改的那张表一个字没动。
func splitIdent(s string) (schema, table string) {
	s = stripQuotes(s)
	if i := strings.LastIndex(s, "."); i >= 0 {
		return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+1:])
	}
	return "", strings.TrimSpace(s)
}

func normalizeSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
