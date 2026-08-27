package review

// 华为 DWS 规范(docs/DWS对象设计和业务开发规范-ABLE.md,RULE 1..62)派生的检查器。
//
// 新版规范把条目编了号并逐条标注【Must】/【Should】,所以这里的每个检查器都对得上
// 一条 RULE —— 出处写在 builtin.go 对应条目的 SpecRef 上,被拦下来的人能回去读原文。
//
// 和 checks_spec.go 同一条取舍:**查得准才查**。规范里的连接管理(RULE 35-37)、
// 锁错峰(RULE 40)、统计信息收集(RULE 41)、跨表字段类型一致(RULE 28)、变更审批与
// 测试环境验证(RULE 57/59)都不是单条语句里能读出来的,它们不在这里。

import (
	"fmt"
	"regexp"
	"strings"
)

// ---------------------------------------------------------------- 对象命名

// objectNames returns the names a statement CREATEs, which is what the naming
// rules judge. It deliberately does not look at names a statement merely
// REFERENCES: a query against a legacy table must not be reported as though the
// author had just named it badly.
var createObjectRe = regexp.MustCompile(`(?is)^\s*CREATE\s+(?:OR\s+REPLACE\s+)?(?:GLOBAL\s+|LOCAL\s+|TEMPORARY\s+|TEMP\s+|UNLOGGED\s+|UNIQUE\s+|MATERIALIZED\s+)*(TABLE|VIEW|INDEX|SEQUENCE|SCHEMA|DATABASE|FUNCTION|PROCEDURE)\s+(?:IF\s+NOT\s+EXISTS\s+)?([^\s(;]+)`)

func createdObject(st *stmt) (kind, name string, ok bool) {
	m := createObjectRe.FindStringSubmatch(st.masked)
	if m == nil {
		return "", "", false
	}
	return strings.ToUpper(m[1]), identName(m[2]), true
}

// createdNames is the object plus, for a CREATE TABLE, its columns — RULE 2/3/5
// speak of "对象名", and a column is one.
func createdNames(st *stmt) []string {
	kind, name, ok := createdObject(st)
	if !ok {
		return nil
	}
	out := []string{name}
	if kind == "TABLE" {
		for _, c := range createOrAlterColumns(st) {
			out = append(out, c.name)
		}
	}
	return out
}

// RULE 2: 对象名只能用小写字母、下划线、数字,起始必须是字母。
func chkDWSNameCharset(st *stmt, p params, _ string) []string {
	pat := p.str("pattern", `^[a-z][a-z0-9_]*$`)
	re, err := regexp.Compile(pat)
	if err != nil {
		return []string{"对象名规则的正则无效: " + pat}
	}
	var out []string
	for _, n := range createdNames(st) {
		if n != "" && !re.MatchString(n) {
			out = append(out, "对象名 "+n+" 不符合 "+pat+"(只允许小写字母、下划线、数字,且以字母开头)")
		}
	}
	return out
}

// RULE 3: 对象名禁止以 pg / gs / mlog / redis 开头 —— 这些前缀是系统对象的地盘。
func chkDWSReservedPrefix(st *stmt, p params, _ string) []string {
	prefixes := p.list("prefixes", []string{"pg", "gs", "mlog", "redis"})
	var out []string
	for _, n := range createdNames(st) {
		low := strings.ToLower(n)
		for _, pre := range prefixes {
			if strings.HasPrefix(low, pre) {
				out = append(out, "对象名 "+n+" 以保留前缀 "+pre+" 开头")
				break
			}
		}
	}
	return out
}

// RULE 5: 对象名不超过 63 字节。
//
// 字节,不是字符 —— 超长会被静默截断,而截断是按字节发生的。一个 21 个汉字的表名在
// "字符数"上远没到 63,却已经越界了。
func chkDWSNameLength(st *stmt, p params, _ string) []string {
	max := p.num("max", 63)
	var out []string
	for _, n := range createdNames(st) {
		if b := len([]byte(n)); b > max {
			out = append(out, fmt.Sprintf("对象名 %s 占 %d 字节,超过 %d —— 超长会被静默截断", n, b, max))
		}
	}
	return out
}

// RULE 4: 临时/中间计算表以 _YYYYMMDD 结尾,并明确清理策略。
var tempTableRe = regexp.MustCompile(`(?is)^\s*CREATE\s+(?:GLOBAL\s+|LOCAL\s+)?(?:TEMPORARY|TEMP)\s+TABLE\b`)
var dateSuffixRe = regexp.MustCompile(`_[0-9]{8}$`)

func chkDWSTempTableSuffix(st *stmt, _ params, _ string) []string {
	if !tempTableRe.MatchString(st.masked) {
		return nil
	}
	_, name, ok := createdObject(st)
	if !ok || dateSuffixRe.MatchString(name) {
		return nil
	}
	return []string{"临时/中间表 " + name + " 未以 _YYYYMMDD 结尾,清理时无从判断它属于哪一天"}
}

// ---------------------------------------------------------------- 数据库设计

var (
	createDatabaseRe = regexp.MustCompile(`(?is)^\s*CREATE\s+DATABASE\b`)
	utf8Re           = regexp.MustCompile(`(?i)ENCODING\s*=?\s*'?\s*UTF-?8`)
	dbCompatRe       = regexp.MustCompile(`(?i)DBCOMPATIBILITY\s*=?\s*'?\s*([A-Za-z0-9_]+)`)
	tablespaceRe     = regexp.MustCompile(`(?i)\bTABLESPACE\b`)
	createTblSpaceRe = regexp.MustCompile(`(?is)^\s*CREATE\s+TABLESPACE\b`)
)

// RULE 7: 一个集群只能有 1 个自定义数据库。
//
// 这条判不了"已经有几个库",所以它判的是那个能观察到的动作:又在建库。集群里已经
// 有一个自定义库是常态,所以每一条 CREATE DATABASE 都值得被人看一眼。
func chkDWSCreateDatabase(st *stmt, _ params, _ string) []string {
	if !createDatabaseRe.MatchString(st.masked) {
		return nil
	}
	return []string{"一个 DWS 集群只允许有一个自定义数据库 —— 确认这不是在建第二个"}
}

// RULE 8: 建库必须指定字符集 UTF8。
//
// 读 st.sql 而不是 st.masked:选项的值写在引号里(ENCODING = 'UTF8'),而掩码正是
// 把引号内容抹成空格的 —— 在掩码文本上找它,永远找不到。语句已由 createDatabaseRe
// 圈定,不存在别处的字符串误触发。
func chkDWSDatabaseUTF8(st *stmt, _ params, _ string) []string {
	if !createDatabaseRe.MatchString(st.masked) || utf8Re.MatchString(st.sql) {
		return nil
	}
	return []string{"建库未指定 ENCODING = 'UTF8'"}
}

// RULE 9: 建库必须指定 dbcompatibility = MySQL。
func chkDWSDbCompatibility(st *stmt, _ params, _ string) []string {
	if !createDatabaseRe.MatchString(st.masked) {
		return nil
	}
	// 同上:值在引号里,只有 st.sql 还留着它。
	m := dbCompatRe.FindStringSubmatch(st.sql)
	if m == nil {
		return []string{"建库未指定 DBCOMPATIBILITY = 'MySQL'"}
	}
	if strings.EqualFold(m[1], "mysql") {
		return nil
	}
	return []string{"建库的 DBCOMPATIBILITY 为 " + m[1] + ",规范要求 MySQL"}
}

// RULE 10: 禁止自定义表空间 —— 建它,或把对象放进去,都算。
func chkDWSForbidTablespace(st *stmt, _ params, _ string) []string {
	if createTblSpaceRe.MatchString(st.masked) {
		return []string{"禁止自定义表空间"}
	}
	if createObjectRe.MatchString(st.masked) && tablespaceRe.MatchString(st.masked) {
		return []string{"对象指定了 TABLESPACE,规范禁止使用自定义表空间"}
	}
	return nil
}

// ---------------------------------------------------------------- 高危/不支持

var (
	createTriggerRe = regexp.MustCompile(`(?is)^\s*CREATE\s+(?:OR\s+REPLACE\s+)?TRIGGER\b`)
	unloggedRe      = regexp.MustCompile(`(?is)^\s*CREATE\s+UNLOGGED\s+TABLE\b`)
	langCJavaRe     = regexp.MustCompile(`(?i)\bLANGUAGE\s+(C|JAVA)\b`)
)

func chkDWSForbidTrigger(st *stmt, _ params, _ string) []string {
	if !createTriggerRe.MatchString(st.masked) {
		return nil
	}
	return []string{"DWS 不支持触发器"}
}

func chkDWSForbidUnlogged(st *stmt, _ params, _ string) []string {
	if !unloggedRe.MatchString(st.masked) {
		return nil
	}
	return []string{"DWS 不支持 unlogged 表"}
}

func chkDWSForbidUDF(st *stmt, _ params, _ string) []string {
	if !langCJavaRe.MatchString(st.masked) {
		return nil
	}
	return []string{"DWS 不支持自定义外部函数(C / Java UDF)"}
}

// ---------------------------------------------------------------- 表结构

var (
	hstoreOptRe  = regexp.MustCompile(`(?i)ENABLE_HSTORE_OPT\s*=\s*TRUE`)
	colVersionRe = regexp.MustCompile(`(?i)COLVERSION\s*=\s*([0-9.]+)`)
)

// RULE 11: 表结构一律采用列存 hstore_opt 3.0 表。
//
// 三样缺一不可:orientation=column、enable_hstore_opt=true、colversion=3.0。逐项
// 报,而不是笼统说一句"不合规" —— 少写了哪一个,人得能直接看出来。
func chkDWSRequireHstoreOpt(st *stmt, _ params, _ string) []string {
	if !createTableRe.MatchString(st.masked) {
		return nil
	}
	// CREATE TABLE … AS SELECT 继承来源表的存储形态,不在这条的适用范围。
	if block, _ := body(st.masked); block == "" {
		return nil
	}
	var missing []string
	if tableOrientation(st) != "column" {
		missing = append(missing, "orientation=column")
	}
	if !hstoreOptRe.MatchString(st.masked) {
		missing = append(missing, "enable_hstore_opt=true")
	}
	if m := colVersionRe.FindStringSubmatch(st.masked); m == nil {
		missing = append(missing, "colversion=3.0")
	} else if strings.TrimSpace(m[1]) != "3.0" {
		missing = append(missing, "colversion=3.0(当前为 "+m[1]+")")
	}
	if len(missing) == 0 {
		return nil
	}
	return []string{"建表未采用列存 hstore_opt 3.0,缺少 WITH (" + strings.Join(missing, ", ") + ")"}
}

// RULE 14: 分布键字段集必须是唯一键、主键约束的子集。
//
// 只在本语句同时声明了主键或唯一键时才判 —— 表没声明任何键时,规范这一条无从谈起,
// 硬报会变成"每张无主键表都多一条噪音",而"建表必须有主键"是另一条规则的事。
func chkDWSDistributeKeySubset(st *stmt, _ params, _ string) []string {
	if !createTableRe.MatchString(st.masked) {
		return nil
	}
	mode, keys, ok := distributeKeys(st)
	if !ok || mode != "HASH" || len(keys) == 0 {
		return nil
	}
	block, _ := body(st.masked)
	keyCols := uniqueKeyColumns(block)
	if len(keyCols) == 0 {
		return nil
	}
	var out []string
	for _, k := range keys {
		if !keyCols[strings.ToLower(k)] {
			out = append(out, "分布键 "+k+" 不属于该表的主键/唯一键")
		}
	}
	return out
}

var pkInlineRe = regexp.MustCompile(`(?is)\b(PRIMARY\s+KEY|UNIQUE)\s*\(([^)]*)\)`)

// uniqueKeyColumns collects every column named by a PRIMARY KEY / UNIQUE
// constraint, inline column markers included.
func uniqueKeyColumns(block string) map[string]bool {
	out := map[string]bool{}
	for _, m := range pkInlineRe.FindAllStringSubmatch(block, -1) {
		for _, c := range strings.Split(m[2], ",") {
			if n := identName(strings.TrimSpace(c)); n != "" {
				out[strings.ToLower(n)] = true
			}
		}
	}
	cols, _ := columnsOf(block)
	for _, c := range cols {
		up := strings.ToUpper(c.def)
		if strings.Contains(up, "PRIMARY KEY") || strings.Contains(up, "UNIQUE") {
			out[strings.ToLower(c.name)] = true
		}
	}
	return out
}

// RULE 15: 主键约束不超过 5 个字段。
func chkDWSPKColumns(st *stmt, p params, _ string) []string {
	if !createTableRe.MatchString(st.masked) {
		return nil
	}
	max := p.num("max", 5)
	block, _ := body(st.masked)
	for _, m := range pkInlineRe.FindAllStringSubmatch(block, -1) {
		if !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(m[1])), "PRIMARY") {
			continue
		}
		n := len(strings.Split(m[2], ","))
		if n > max {
			return []string{fmt.Sprintf("主键有 %d 个字段,超过上限 %d", n, max)}
		}
	}
	return nil
}

// RULE 17: 禁止用 UUID 系统函数做分布列的默认值。
func chkDWSUUIDDefaultOnDistKey(st *stmt, p params, _ string) []string {
	if !createTableRe.MatchString(st.masked) {
		return nil
	}
	_, keys, ok := distributeKeys(st)
	if !ok || len(keys) == 0 {
		return nil
	}
	fns := p.list("functions", []string{"uuid_generate_v1", "uuid_generate_v4", "uuid", "sys_guid", "gen_random_uuid"})
	isKey := map[string]bool{}
	for _, k := range keys {
		isKey[strings.ToLower(k)] = true
	}
	var out []string
	for _, c := range createOrAlterColumns(st) {
		if !isKey[strings.ToLower(c.name)] {
			continue
		}
		low := strings.ToLower(c.def)
		if !strings.Contains(low, "default") {
			continue
		}
		for _, fn := range fns {
			if strings.Contains(low, fn+"(") {
				out = append(out, "分布列 "+c.name+" 用 "+fn+"() 做默认值 —— 批量写入时会成为资源瓶颈")
				break
			}
		}
	}
	return out
}

// ---------------------------------------------------------------- 分区

var partitionByKindRe = regexp.MustCompile(`(?is)\bPARTITION\s+BY\s+([A-Z]+)\s*\(([^)]*)\)`)

// RULE 19: 分区键只能一个字段,且必须是 RANGE 分区。
func chkDWSPartitionSingleRange(st *stmt, _ params, _ string) []string {
	m := partitionByKindRe.FindStringSubmatch(st.masked)
	if m == nil {
		return nil
	}
	var out []string
	if kind := strings.ToUpper(strings.TrimSpace(m[1])); kind != "RANGE" {
		out = append(out, "使用了 "+kind+" 分区,规范要求 RANGE 分区")
	}
	if n := len(strings.Split(m[2], ",")); n > 1 {
		out = append(out, fmt.Sprintf("分区键有 %d 个字段,规范要求只能一个", n))
	}
	return out
}

var ttlRe = regexp.MustCompile(`(?i)\bTTL\b`)

// RULE 21: 分区表须指定 ttl,或有及时淘汰历史分区的安排。
//
// 后半句(有没有 drop partition 的调度)不在语句里,所以这条只能在没写 ttl 时提醒。
// 它在 builtin.go 里被刻意压成 warn,原因同上。
func chkDWSPartitionTTL(st *stmt, _ params, _ string) []string {
	if !createTableRe.MatchString(st.masked) || !partitionByRe.MatchString(st.masked) {
		return nil
	}
	if ttlRe.MatchString(st.masked) {
		return nil
	}
	return []string{"分区表未指定 ttl —— 请确认另有及时 drop partition 的安排,否则历史分区会无限累积"}
}

// ---------------------------------------------------------------- 索引

var usingMethodRe = regexp.MustCompile(`(?i)\bUSING\s+([A-Za-z_]+)`)

// RULE 30: 只能使用 Btree/CBtree,禁止 psort 等。
var anyCreateIndexRe = regexp.MustCompile(`(?is)^\s*CREATE\s+(?:UNIQUE\s+)?INDEX\b`)

func chkDWSIndexMethod(st *stmt, p params, _ string) []string {
	// 用宽松的形状认 CREATE INDEX:USING 会夹在表名和列表之间,
	// "ON 表(列)"那个形状匹配不到带 USING 的语句,而带 USING 的正是这条要查的。
	if !anyCreateIndexRe.MatchString(st.masked) && !alterTableRe.MatchString(st.masked) {
		return nil
	}
	m := usingMethodRe.FindStringSubmatch(st.masked)
	if m == nil {
		return nil // 未指定 = 走默认,默认就是 btree
	}
	allowed := p.list("methods", []string{"btree", "cbtree"})
	method := strings.ToLower(m[1])
	for _, a := range allowed {
		if method == a {
			return nil
		}
	}
	return []string{"索引使用了 " + strings.ToUpper(method) + " 方式,规范只允许 " + strings.ToUpper(strings.Join(allowed, "/"))}
}

// ---------------------------------------------------------------- SQL 写法

var (
	returningRe   = regexp.MustCompile(`(?i)\bRETURNING\b`)
	distinctOnRe  = regexp.MustCompile(`(?i)\bDISTINCT\s+ON\s*\(`)
	aggOrderByRe  = regexp.MustCompile(`(?is)\b(SUM|COUNT|AVG|MAX|MIN|ARRAY_AGG|STRING_AGG|LISTAGG)\s*\([^()]*\bORDER\s+BY\b`)
	queryDopRe    = regexp.MustCompile(`(?i)\bQUERY_DOP\b`)
	existsOrInSub = regexp.MustCompile(`(?is)\b(EXISTS|IN)\s*\(\s*SELECT\b`)
)

// RULE 42: RETURNING 不支持下推。
func chkDWSForbidReturning(st *stmt, _ params, _ string) []string {
	if !returningRe.MatchString(st.masked) {
		return nil
	}
	return []string{"RETURNING 不支持下推,会退化为单点执行"}
}

// RULE 42: DISTINCT ON 不支持下推。
func chkDWSForbidDistinctOn(st *stmt, _ params, _ string) []string {
	if !distinctOnRe.MatchString(st.masked) {
		return nil
	}
	return []string{"DISTINCT ON 不支持下推,应改写为窗口函数或聚合"}
}

// RULE 42: 聚集函数中使用 ORDER BY 不支持下推。
func chkDWSAggregateOrderBy(st *stmt, _ params, _ string) []string {
	if !aggOrderByRe.MatchString(st.masked) {
		return nil
	}
	return []string{"聚集函数内使用了 ORDER BY,不支持下推"}
}

// RULE 54: 禁止在应用中显式开启并行参数。
func chkDWSQueryDop(st *stmt, _ params, _ string) []string {
	if !queryDopRe.MatchString(st.masked) {
		return nil
	}
	return []string{"显式设置 query_dop 会让单条 SQL 在短时间内吃掉过多资源 —— DWS 本身已按 DN 并行"}
}

// RULE 47【Should】优先使用 JOIN 替代 EXISTS / IN。
//
// NOT EXISTS 不在此列:RULE 43 正是要求把 NOT IN 改写成它,这条再反过来劝人别用,
// 两条规则就会把人夹在中间。
func chkDWSPreferJoin(st *stmt, _ params, _ string) []string {
	for _, loc := range existsOrInSub.FindAllStringIndex(st.masked, -1) {
		head := st.upper[max0(loc[0]-4):loc[0]]
		if strings.Contains(head, "NOT") {
			continue
		}
		return []string{"用 EXISTS / IN 接子查询,优化器较难找到稳定计划,建议改写为 JOIN"}
	}
	return nil
}

func max0(i int) int {
	if i < 0 {
		return 0
	}
	return i
}

// ---------------------------------------------------------------- 类型与长度

var varcharTypeRe = regexp.MustCompile(`(?i)^N?VARCHAR2?\b`)

// RULE 29【Should】VARCHAR 必须指定长度,且不超过 6000。
func chkDWSVarcharLength(st *stmt, p params, _ string) []string {
	max := p.num("max", 6000)
	var out []string
	for _, c := range createOrAlterColumns(st) {
		typ := strings.TrimSpace(columnTypeOf(c.def))
		if !varcharTypeRe.MatchString(typ) {
			continue
		}
		n, ok := typeLength(typ)
		if !ok {
			out = append(out, "字段 "+c.name+" 的 "+baseTypeName(strings.ToLower(typ))+" 未指定长度")
			continue
		}
		if n > max {
			out = append(out, fmt.Sprintf("字段 %s 的长度 %d 超过上限 %d", c.name, n, max))
		}
	}
	return out
}

var lenRe = regexp.MustCompile(`\(\s*([0-9]+)`)

func typeLength(typ string) (int, bool) {
	m := lenRe.FindStringSubmatch(typ)
	if m == nil {
		return 0, false
	}
	n := 0
	for _, ch := range m[1] {
		n = n*10 + int(ch-'0')
	}
	return n, true
}
