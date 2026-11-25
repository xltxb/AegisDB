package gateway

// 数据查询里 SELECT 之外的只读写法,以及几种"看着是查询、实际在做别的"的变种(2026-10-29)。
//
//   - MySQL 系的 SHOW:只放行看本库表结构的几种(白名单),FROM / IN 后面的库必须是授权的库。
//     SHOW DATABASES / PROCESSLIST / GRANTS / VARIABLES / STATUS … 会越过"只看本库"的边界
//     (别的库、别人的会话、账号与配置),不放。
//   - DESC:MySQL 系只认 DESC 表 [列];Oracle 只认 DESC [schema.]名字,由执行层翻成数据字典查询。
//   - EXPLAIN / DESC FOR CONNECTION n:看的是**别人会话**正在跑的语句,不放。
//   - Oracle 的数据库链接(t@link):借链接那头的库执行,授权范围与只读事务都管不到。
//   - Oracle 的 XQuery 读表(ora:view、fn:collection("oradb:/…")):表名藏在字符串里,跨库检查看不见。

import (
	"regexp"
	"strings"

	"velagateway/internal/model"
)

// bizObj:[库.]对象。
const bizObj = bizIdent + `(?:\s*\.\s*` + bizIdent + `)?`

// SHOW 白名单。db 组是 FROM / IN 后面的库名(没写就是当前库,也就是授权的库)。
var bizShowRes = []*regexp.Regexp{
	regexp.MustCompile(`(?is)^SHOW\s+CREATE\s+(?:TABLE|VIEW)\s+` + bizObj + `$`),
	regexp.MustCompile(`(?is)^SHOW\s+(?:EXTENDED\s+)?(?:FULL\s+)?(?:COLUMNS|FIELDS)\s+(?:FROM|IN)\s+` + bizObj +
		`(?:\s+(?:FROM|IN)\s+(?P<db>` + bizIdent + `))?(?:\s+(?:LIKE|WHERE)\s.*)?$`),
	regexp.MustCompile(`(?is)^SHOW\s+(?:EXTENDED\s+)?(?:INDEX|INDEXES|KEYS)\s+(?:FROM|IN)\s+` + bizObj +
		`(?:\s+(?:FROM|IN)\s+(?P<db>` + bizIdent + `))?(?:\s+WHERE\s.*)?$`),
	regexp.MustCompile(`(?is)^SHOW\s+(?:EXTENDED\s+)?(?:FULL\s+)?TABLES(?:\s+(?:FROM|IN)\s+(?P<db>` + bizIdent + `))?(?:\s+(?:LIKE|WHERE)\s.*)?$`),
	regexp.MustCompile(`(?is)^SHOW\s+TABLE\s+STATUS(?:\s+(?:FROM|IN)\s+(?P<db>` + bizIdent + `))?(?:\s+(?:LIKE|WHERE)\s.*)?$`),
}

// MySQL 系的 DESC 表 [列名或通配串]。
var bizMySQLDescRe = regexp.MustCompile(`(?is)^DESC(?:RIBE)?\s+` + bizObj + `(?:\s+(?:` + bizIdent + `|'[^']*'))?$`)

// Oracle 的 DESC [schema.]名字。不认 @链接、表达式。
var bizOracleDescRe = regexp.MustCompile(`(?is)^DESC(?:RIBE)?\s+(?P<a>` + bizIdent + `)(?:\s*\.\s*(?P<b>` + bizIdent + `))?$`)

var bizForConnectionRe = regexp.MustCompile(`(?is)^(?:EXPLAIN|DESCRIBE|DESC)\b.*\bFOR\s+CONNECTION\b`)

// t@link、"T"@"LINK"、t @ link。
var bizDBLinkRe = regexp.MustCompile(`(?:[\pL\pN_$#"]|^)\s*@\s*(` + "\"[^\"]+\"|[\\pL_][\\pL\\pN_$#.]*" + `)`)

var bizXQueryTableRe = regexp.MustCompile(`(?i)ora:view|oradb:|fn:collection|fn:doc\b`)

// bizStmtText:去掉注释与末尾分号的语句。
func bizStmtText(st string) string {
	s := strings.TrimSpace(StripComments(st))
	for strings.HasSuffix(s, ";") {
		s = strings.TrimSpace(strings.TrimSuffix(s, ";"))
	}
	return s
}

// bizShowCheck:MySQL 系的一条 SHOW 能否执行。blocked 是"别的库 / 系统库"(小写名 → 拒绝原因)。
func bizShowCheck(st string, blocked map[string]string, grantedDB string) *model.RuleRef {
	s := bizStmtText(st)
	for _, re := range bizShowRes {
		m := re.FindStringSubmatch(s)
		if m == nil {
			continue
		}
		if i := re.SubexpIndex("db"); i > 0 && m[i] != "" {
			db := strings.Trim(m[i], "`\"")
			if code, bad := bizBlockedDB(blocked, db); bad {
				return model.NewRuleRef(code, "name", db, "db", grantedDB)
			}
		}
		return nil
	}
	return model.NewRuleRef(model.BizShowForm)
}

// OracleBizDescribe 认数据查询里 Oracle 的 DESC [schema.]名字,返回数据字典里的 owner(空 = 当前
// schema)与对象名:不加引号的转大写,加了引号的照原样。
func OracleBizDescribe(query string) (owner, name string, ok bool) {
	m := bizOracleDescRe.FindStringSubmatch(bizStmtText(query))
	if m == nil {
		return "", "", false
	}
	a, b := oracleDictName(m[bizOracleDescRe.SubexpIndex("a")]), m[bizOracleDescRe.SubexpIndex("b")]
	if b == "" {
		return "", a, true
	}
	return a, oracleDictName(b), true
}

// oracleDictName:标识符在 Oracle 数据字典里的写法。
func oracleDictName(raw string) string {
	if len(raw) >= 2 && (raw[0] == '"' || raw[0] == '`') {
		return raw[1 : len(raw)-1]
	}
	return strings.ToUpper(raw)
}

// bizOracleDescSQL:DESC 翻成的数据字典查询(列与终端的 DESC 一致)。owner 为空时按当前 schema
// —— 执行层已在会话上切到授权的 schema;不像终端那样不限 owner,那会看到别的 schema 的同名表。
func bizOracleDescSQL(owner string) string {
	cond := `SYS_CONTEXT('USERENV','CURRENT_SCHEMA')`
	if owner != "" {
		cond = `:2`
	}
	return `SELECT COLUMN_NAME, DATA_TYPE, DATA_LENGTH, NULLABLE, DATA_DEFAULT FROM ALL_TAB_COLUMNS` +
		` WHERE TABLE_NAME = :1 AND OWNER = ` + cond + ` ORDER BY COLUMN_ID`
}

// bizVariantCheck:与动词无关的几种变种(FOR CONNECTION、Oracle 数据库链接与 XQuery 读表)。
func bizVariantCheck(fam, st string) *model.RuleRef {
	body := bizStmtText(st)
	masked := blankQuoted(body)
	if bizForConnectionRe.MatchString(masked) {
		return model.NewRuleRef(model.BizOtherSession)
	}
	if fam != familyOracle {
		return nil
	}
	if m := bizDBLinkRe.FindStringSubmatch(blankStringLiterals(body)); m != nil {
		return model.NewRuleRef(model.BizDBLink, "name", strings.Trim(m[1], `"`))
	}
	if m := bizXQueryTableRe.FindString(body); m != "" {
		return model.NewRuleRef(model.BizFunc, "name", "XQuery "+m)
	}
	return nil
}
