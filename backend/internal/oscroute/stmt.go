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
	Table string
	Alter string
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
		if strings.Contains(clause, ",") && !insideParens(clause) {
			return IndexDDL{}, false
		}
		return IndexDDL{Table: cleanIdent(m[1]), Alter: normalizeSpace(clause)}, true
	}
	if m := reCreateIndex.FindStringSubmatch(s); m != nil {
		kind := strings.ToUpper(strings.TrimSpace(m[1]))
		if kind != "" {
			kind += " "
		}
		return IndexDDL{
			Table: cleanIdent(m[3]),
			Alter: normalizeSpace("ADD " + kind + "INDEX " + cleanIdent(m[2]) + " " + m[4]),
		}, true
	}
	if m := reDropIndex.FindStringSubmatch(s); m != nil {
		return IndexDDL{Table: cleanIdent(m[2]), Alter: "DROP INDEX " + cleanIdent(m[1])}, true
	}
	return IndexDDL{}, false
}

// insideParens 报告这个子句里的逗号是不是全都在括号内 —— `ADD INDEX i (a, b)` 是
// 一个子句,`ADD INDEX i (a), ADD INDEX j (b)` 是两个。
func insideParens(clause string) bool {
	depth := 0
	for _, r := range clause {
		switch r {
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

// cleanIdent 去掉反引号/双引号与库名前缀。
//
// 库名前缀必须剥掉:OSC 的 StartRequest 分开收 schema 与 table,schema 来自发布单
// 的目标库。带着前缀会拼出 `app`.`app.t_order` 这样的名字。
func cleanIdent(s string) string {
	s = strings.TrimSpace(s)
	s = strings.NewReplacer("`", "", `"`, "").Replace(s)
	if i := strings.LastIndex(s, "."); i >= 0 {
		s = s[i+1:]
	}
	return strings.TrimSpace(s)
}

func normalizeSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
