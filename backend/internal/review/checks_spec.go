package review

// 由公司规范(docs/ 下四份 MySQL / TiDB / Oracle / Huawei DWS 規範)派生出来的检查器。
//
// 和 checks.go 一样,这里的检查器只读 st.masked / st.upper —— 引号里的内容已被抹成
// 空格,所以字符串字面量里的关键字永远不会触发规则。
//
// 一条贯穿本文件的取舍:**查得准才查**。规范里有相当一部分要求(DBA 审核、定期备份、
// 测试环境验证、经 ELB 连接、时区一致、大表归档周期)不是语句里能读出来的东西,它们
// 不在这里,也不该被硬凑成正则 —— 一条老是误报的规则,最后换来的是所有人学会无视
// 整个审查。哪些条目落地成了规则、哪些没有,见 docs/adr/0007-sql-review-from-specs.md。

import (
	"regexp"
	"strings"

	"velagateway/internal/apimsg"
)

// ---------------------------------------------------------------- MySQL

var alterAlgoRe = regexp.MustCompile(`(?i)\bALGORITHM\s*=`)
var alterLockRe = regexp.MustCompile(`(?i)\bLOCK\s*=`)
var noAlgorithmClauseRe = regexp.MustCompile(`(?is)^(?:(?:ADD|DROP)\s+(?:INDEX|KEY)\b|RENAME\b)`)

// MySQL's partition operations, plus TiDB's and MariaDB's own.
var partitionOpRe = regexp.MustCompile(`(?is)^(?:PARTITION\b` +
	`|(?:ADD|DROP|DISCARD|IMPORT|TRUNCATE|COALESCE|REORGANIZE|EXCHANGE|ANALYZE|CHECK|OPTIMIZE|REBUILD|REPAIR)\s+PARTITION\b` +
	`|(?:REMOVE|UPGRADE)\s+PARTITIONING\b` +
	`|(?:FIRST|LAST|COMPACT|MERGE\s+FIRST|SPLIT\s+MAXVALUE)\s+PARTITION\b` +
	`|CONVERT\s+(?:PARTITION\b|TABLE\s+\S+\s+TO\s+PARTITION\b))`)

// chkAlterAlgorithm requires an explicit ALGORITHM (or LOCK) on ALTER TABLE.
//
// The point is not that the default is wrong — it is that the default is
// INVISIBLE. Writing ALGORITHM=INSTANT states the expectation, and the database
// refuses the statement when it cannot honour it; leaving it out lets the same
// DDL silently fall back to a full table copy under a lock.
//
// Plain secondary index changes are exempt: they default to INPLACE and cannot
// fall back that way, and the standard's own index example omits ALGORITHM.
// RENAME clauses are exempt for the same reason: they change names, not rows.
func chkAlterAlgorithm(st *stmt, _ params, _ string) []apimsg.M {
	if !alterTableRe.MatchString(st.masked) {
		return nil
	}
	// Partition operations mostly reject a trailing ALGORITHM. They cannot be
	// comma-mixed with other clauses, so the opening words are enough to tell, and
	// the commas in DROP PARTITION p0, p1 separate names, not clauses.
	if partitionOpRe.MatchString(alterTableBody(st)) {
		return nil
	}
	if alterAlgoRe.MatchString(st.masked) || alterLockRe.MatchString(st.masked) {
		return nil
	}
	if onlyNoAlgorithmClauses(st) {
		return nil
	}
	return []apimsg.M{msgf("ALTER TABLE %s 未指定 ALGORITHM/LOCK —— 默认行为不可见,大表上可能退化为全表拷贝并锁表", identName(alterTableName(st)))}
}

// onlyNoAlgorithmClauses reports whether every clause of an ALTER TABLE adds or
// drops an ordinary index, or renames something. FULLTEXT, SPATIAL, UNIQUE and
// PRIMARY keep the requirement, and so does any statement mixing in another kind
// of change.
func onlyNoAlgorithmClauses(st *stmt) bool {
	clauses := splitTop(alterTableBody(st))
	if len(clauses) == 0 {
		return false
	}
	for _, c := range clauses {
		if !noAlgorithmClauseRe.MatchString(c) {
			return false
		}
	}
	return true
}

// alterTableBody is what follows ALTER TABLE <name>, without the terminator.
func alterTableBody(st *stmt) string {
	head := alterTableRe.FindString(st.masked)
	return strings.TrimRight(strings.TrimSpace(st.masked[len(head):]), "; \t\r\n")
}

// ---------------------------------------------------------------- Oracle

var (
	alterIdxRebldRe = regexp.MustCompile(`(?is)^\s*ALTER\s+INDEX\s+[^\s]+\s+REBUILD\b`)
	onlineRe        = regexp.MustCompile(`(?i)\bONLINE\b`)
)

func chkOracleIndexOnline(st *stmt, _ params, _ string) []apimsg.M {
	if !createIndexRe.MatchString(st.masked) && !alterIdxRebldRe.MatchString(st.masked) {
		return nil
	}
	if onlineRe.MatchString(st.masked) {
		return nil
	}
	return []apimsg.M{msgf("索引操作未加 ONLINE —— 会阻塞该表的 DML")}
}

// ---------------------------------------------------------------- TiDB

var timestampColRe = regexp.MustCompile(`(?i)\bTIMESTAMP\b`)

// chkTiDBTimestamp flags TIMESTAMP columns: they overflow in 2038.
func chkTiDBTimestamp(st *stmt, _ params, _ string) []apimsg.M {
	cols := createOrAlterColumns(st)
	var out []apimsg.M
	for _, c := range cols {
		// CURRENT_TIMESTAMP as a DEFAULT is a value, not the column type.
		typ := columnTypeOf(c.def)
		if timestampColRe.MatchString(typ) {
			out = append(out, msgf("字段 %s 使用 TIMESTAMP,存在 2038 年溢出问题,应改用 DATETIME", c.name))
		}
	}
	return out
}

var partitionByRe = regexp.MustCompile(`(?i)\bPARTITION\s+BY\b`)

func chkTiDBPartition(st *stmt, _ params, _ string) []apimsg.M {
	if !createTableRe.MatchString(st.masked) || !partitionByRe.MatchString(st.masked) {
		return nil
	}
	return []apimsg.M{msgf("TiDB 的分区表功能在当前版本尚不完善,建议改用普通表(TiDB 本身已按 Region 分片)")}
}

// ---------------------------------------------------------------- DWS 建表

var (
	partitionNameRe  = regexp.MustCompile(`(?i)\bPARTITION\s+([A-Za-z_][A-Za-z0-9_$#]*)`)
	distributeByRe   = regexp.MustCompile(`(?is)\bDISTRIBUTE\s+BY\s+(HASH|REPLICATION|ROUNDROBIN)\s*(\(([^)]*)\))?`)
)

// distributeKeys returns the columns of a DISTRIBUTE BY HASH(...) clause.
func distributeKeys(st *stmt) (mode string, keys []string, found bool) {
	m := distributeByRe.FindStringSubmatch(st.masked)
	if m == nil {
		return "", nil, false
	}
	mode = strings.ToUpper(strings.TrimSpace(m[1]))
	for _, k := range strings.Split(m[3], ",") {
		if k = identName(strings.TrimSpace(k)); k != "" {
			keys = append(keys, k)
		}
	}
	return mode, keys, true
}

func chkDWSDistributeKeyCount(st *stmt, p params, _ string) []apimsg.M {
	if !createTableRe.MatchString(st.masked) {
		return nil
	}
	_, keys, ok := distributeKeys(st)
	if !ok {
		return nil
	}
	max := p.num("max", 3)
	if len(keys) <= max {
		return nil
	}
	return []apimsg.M{msgf("分布键有 %d 个字段,超过上限 %d", len(keys), max)}
}

func chkDWSDistributeKeyType(st *stmt, p params, _ string) []apimsg.M {
	if !createTableRe.MatchString(st.masked) {
		return nil
	}
	_, keys, ok := distributeKeys(st)
	if !ok || len(keys) == 0 {
		return nil
	}
	allowed := p.list("types", []string{
		"tinyint", "smallint", "integer", "int", "bigint",
		"number", "char", "varchar", "varchar2",
	})
	block, _ := body(st.masked)
	cols, _ := columnsOf(block)
	typeOf := map[string]string{}
	for _, c := range cols {
		typeOf[strings.ToLower(c.name)] = strings.ToLower(columnTypeOf(c.def))
	}
	var out []apimsg.M
	for _, k := range keys {
		typ, known := typeOf[strings.ToLower(k)]
		if !known {
			continue // 分布键引用了本语句里看不到的列,交给数据库自己报
		}
		base := baseTypeName(typ)
		hit := false
		for _, a := range allowed {
			if base == a {
				hit = true
				break
			}
		}
		if !hit {
			out = append(out, msgf("分布键 %s 的类型 %s 不在允许的类型范围内", k, base))
		}
	}
	return out
}

// chkDWSReplication asks for confirmation rather than judging: whether the table
// is under a million rows, and whether it is a fact table, is not in the
// statement. See the rule's note in builtin.go for why it ships as a warning.
func chkDWSReplication(st *stmt, _ params, _ string) []apimsg.M {
	if !createTableRe.MatchString(st.masked) {
		return nil
	}
	mode, _, ok := distributeKeys(st)
	if !ok || mode != "REPLICATION" {
		return nil
	}
	return []apimsg.M{msgf("复制表会在每个 DN 上各存一份:请确认该表在百万行以下,且不是事实表或维表")}
}

// tableOrientation reads ORIENTATION= from the WITH(...) clause; empty when the
// statement does not say (DWS then defaults to row storage).
func tableOrientation(st *stmt) string {
	if m := orientationRe.FindStringSubmatch(st.masked); m != nil {
		return strings.ToLower(m[1])
	}
	return ""
}




func chkDWSPartitionCount(st *stmt, p params, _ string) []apimsg.M {
	if !partitionByRe.MatchString(st.masked) {
		return nil
	}
	max := p.num("max", 1000)
	n := 0
	for _, m := range partitionNameRe.FindAllStringSubmatch(st.masked, -1) {
		if !strings.EqualFold(m[1], "BY") {
			n++
		}
	}
	if n <= max {
		return nil
	}
	return []apimsg.M{msgf("单表定义了 %d 个分区,超过上限 %d —— 过多分区会带来小文件与锁竞争", n, max)}
}


// ---------------------------------------------------------------- DWS 视图

var (
	createViewRe = regexp.MustCompile(`(?is)^\s*CREATE\s+(?:OR\s+REPLACE\s+)?(?:MATERIALIZED\s+)?VIEW\b`)
	orderByRe    = regexp.MustCompile(`(?i)\bORDER\s+BY\b`)
	selectWordRe = regexp.MustCompile(`(?i)\bSELECT\b`)
)

func chkViewOrderBy(st *stmt, _ params, _ string) []apimsg.M {
	if !createViewRe.MatchString(st.masked) || !orderByRe.MatchString(st.masked) {
		return nil
	}
	return []apimsg.M{msgf("视图定义中出现 ORDER BY —— 视图的排序不被保证,却会让优化器无法下推")}
}

// chkViewNesting counts SELECT levels inside a view body. It measures the
// nesting WRITTEN HERE, not nesting through other views (that would need the
// catalog), and says so — a rule that quietly checks less than its name claims
// is worse than one that states its bound.
func chkViewNesting(st *stmt, p params, _ string) []apimsg.M {
	if !createViewRe.MatchString(st.masked) {
		return nil
	}
	max := p.num("max", 4)
	depth, cur := 0, 0
	for i := 0; i < len(st.masked); i++ {
		switch st.masked[i] {
		case '(':
			cur++
			if cur > depth {
				depth = cur
			}
		case ')':
			if cur > 0 {
				cur--
			}
		}
	}
	levels := len(selectWordRe.FindAllString(st.masked, -1))
	if levels <= max {
		return nil
	}
	return []apimsg.M{msgf("视图定义中出现 %d 层 SELECT,超过上限 %d 层(本规则只数本语句内的嵌套,不追踪引用到的其它视图)", levels, max)}
}

// ---------------------------------------------------------------- DWS SQL 写法

var (
	notInSubqRe   = regexp.MustCompile(`(?is)\bNOT\s+IN\s*\(\s*SELECT\b`)
	withRecurseRe = regexp.MustCompile(`(?is)\bWITH\s+RECURSIVE\b`)
	deleteFromRe  = regexp.MustCompile(`(?is)^\s*DELETE\s+(?:FROM\s+)?([^\s;]+)`)
	joinRe        = regexp.MustCompile(`(?i)\bJOIN\b`)
	leadCommentRe = regexp.MustCompile(`(?s)^\s*(/\*.*?\*/|--[^\n]*)`)
)

func chkNotInSubquery(st *stmt, _ params, _ string) []apimsg.M {
	if !notInSubqRe.MatchString(st.masked) {
		return nil
	}
	return []apimsg.M{msgf("NOT IN (子查询) 在有 NULL 时结果不符直觉且无法下推,应改写为 NOT EXISTS")}
}

func chkWithRecursive(st *stmt, _ params, _ string) []apimsg.M {
	if !withRecurseRe.MatchString(st.masked) {
		return nil
	}
	return []apimsg.M{msgf("WITH RECURSIVE 在分布式执行下无法下推,会退化为单点计算")}
}


// chkDeleteWholeTable flags an unconditional DELETE. It overlaps
// dml.require.where deliberately: that rule says the statement is UNSCOPED, this
// one says what to write instead on DWS, where a full-table DELETE leaves dead
// tuples the cluster then has to vacuum.
func chkDeleteWholeTable(st *stmt, _ params, _ string) []apimsg.M {
	m := deleteFromRe.FindStringSubmatch(st.upper)
	if m == nil || whereRe.MatchString(st.upper) {
		return nil
	}
	return []apimsg.M{msgf("无条件 DELETE %s 会留下大量死元组,全表删除应使用 TRUNCATE", identName(m[1]))}
}

func chkJoinCount(st *stmt, p params, _ string) []apimsg.M {
	max := p.num("max", 16)
	n := len(joinRe.FindAllString(st.masked, -1))
	if n == 0 {
		return nil
	}
	// n 个 JOIN 关键字 = n+1 张表参与。
	tables := n + 1
	if tables <= max {
		return nil
	}
	return []apimsg.M{msgf("单条 SQL 参与 JOIN 的表约 %d 张,超过上限 %d —— 优化器难以生成稳定计划", tables, max)}
}

// sqlTagFinding requires a leading comment identifying the system/module/job, so
// a slow query found in the cluster can be traced back to whoever runs it.
//
// It is a SCRIPT-level rule, not a per-statement one, and not by choice: the
// splitter strips comments, so by the time a statement reaches a checker the tag
// is gone. The original script is the only place it still exists.
//
// It fires once per script rather than once per statement — a 200-statement ETL
// file would otherwise produce 200 copies of the same sentence.
func sqlTagFinding(r Rule, stmts []*stmt, script string) []Finding {
	first := (*stmt)(nil)
	for _, st := range stmts {
		switch st.verb {
		case "SELECT", "INSERT", "UPDATE", "DELETE", "MERGE":
			first = st
		}
		if first != nil {
			break
		}
	}
	if first == nil || leadCommentRe.MatchString(script) {
		return nil
	}
	msg := msgf("脚本开头缺少标识注释,应形如 /* sys_mod_job_step */ —— 否则集群里查到的慢 SQL 无从追溯来源")
	if custom := strings.TrimSpace(r.Message); custom != "" {
		msg = apimsg.Sprintf("%s — %s", custom, msg)
	}
	return []Finding{{
		Code: r.Code, Name: r.Name, Level: r.Level, Category: r.Category,
		Stmt: first.index, Line: first.line, SQL: excerpt(first.raw), Message: msg.S, MessageRef: msg.Ref,
	}}
}

// chkSubqueryOrderBy flags ORDER BY inside a parenthesised subquery. The outer
// ORDER BY of the statement itself is fine and must not be reported, so the
// check walks the parens and only looks at depth > 0.
func chkSubqueryOrderBy(st *stmt, _ params, _ string) []apimsg.M {
	if !orderByRe.MatchString(st.masked) {
		return nil
	}
	for _, loc := range orderByRe.FindAllStringIndex(st.masked, -1) {
		if parenDepthAt(st.masked, loc[0]) > 0 {
			return []apimsg.M{msgf("子查询内出现 ORDER BY —— 排序不被保证,只会白白多一次排序开销")}
		}
	}
	return nil
}

// chkScalarSubquery flags a subquery in the SELECT list or on either side of a
// comparison — the shapes that force a per-row execution.
func chkScalarSubquery(st *stmt, _ params, _ string) []apimsg.M {
	if st.verb != "SELECT" && st.verb != "UPDATE" && st.verb != "DELETE" {
		return nil
	}
	var out []apimsg.M
	if scalarSelectListRe.MatchString(st.masked) {
		out = append(out, msgf("SELECT 列表中出现子查询(标量子查询),应改为 LEFT JOIN + 聚合"))
	}
	if scalarCompareRe.MatchString(st.masked) {
		out = append(out, msgf("比较运算的一侧是子查询(标量子查询),应改为 LEFT JOIN + 聚合"))
	}
	return out
}

var (
	scalarSelectListRe = regexp.MustCompile(`(?is)\bSELECT\b[^;]*?,\s*\(\s*SELECT\b`)
	scalarCompareRe    = regexp.MustCompile(`(?is)(=|<>|!=|>=|<=|>|<)\s*\(\s*SELECT\b`)
)

func chkVolatileInSubquery(st *stmt, p params, _ string) []apimsg.M {
	spans := subquerySpans(st.masked)
	if len(spans) == 0 {
		return nil
	}
	fns := p.list("functions", []string{"nextval", "uuid_generate_v1", "uuid_generate_v4", "random", "currval", "lastval"})
	var out []apimsg.M
	for _, fn := range fns {
		re, err := regexp.Compile(`(?i)\b` + regexp.QuoteMeta(fn) + `\s*\(`)
		if err != nil {
			continue
		}
		for _, loc := range re.FindAllStringIndex(st.masked, -1) {
			if inSubquery(spans, loc[0]) {
				out = append(out, msgf("子查询中调用了不稳定函数 %s() —— 会导致计划无法下推", fn))
				break
			}
		}
	}
	return out
}


// chkSchemaQualified requires schema.table in FROM / JOIN / INTO / UPDATE.
func chkSchemaQualified(st *stmt, _ params, _ string) []apimsg.M {
	var out []apimsg.M
	seen := map[string]bool{}
	for _, m := range tableRefRe.FindAllStringSubmatch(st.masked, -1) {
		ref := strings.TrimSpace(m[2])
		if ref == "" || strings.HasPrefix(ref, "(") {
			continue // 子查询 / 派生表
		}
		name := strings.Trim(ref, `"`)
		if strings.Contains(name, ".") || seen[strings.ToLower(name)] {
			continue
		}
		// 关键字开头的不是表名(如 FROM DUAL、JOIN LATERAL)。
		if isSQLKeyword(strings.ToUpper(name)) {
			continue
		}
		seen[strings.ToLower(name)] = true
		out = append(out, msgf("对象 %s 未带 schema 限定,应写作 schema.%s", name, name))
	}
	return out
}

// 标识符字符类用 Unicode 类,不用 A-Za-z:中文表名整个抓不到,这条规则就静静地跳过它 ——
// 一张叫「用户表」的表不带 schema 也不会被指出来。与 gateway 包 sensitiveTableRefRe 同一类问题。
var tableRefRe = regexp.MustCompile(`(?is)\b(FROM|JOIN|INTO|UPDATE)\s+([\pL_"][\pL\pN_$#."]*)`)

// ---------------------------------------------------------------- 命名 / 安全



func chkSensitiveColumn(st *stmt, p params, _ string) []apimsg.M {
	pats := p.list("patterns", []string{`id_?card`, `identity_?no`, `passport_?no`, `bank_?card`, `card_?no`, `cvv`, `credit_?card`, `social_?security`, `ssn`})
	res := make([]*regexp.Regexp, 0, len(pats))
	for _, p := range pats {
		if re, err := regexp.Compile(`(?i)` + p); err == nil {
			res = append(res, re)
		}
	}
	var out []apimsg.M
	for _, c := range createOrAlterColumns(st) {
		for _, re := range res {
			if re.MatchString(c.name) {
				out = append(out, msgf("字段 %s 命中敏感信息命名 —— 规范禁止在本平台的库中存储敏感字段", c.name))
				break
			}
		}
	}
	return out
}


// ---------------------------------------------------------------- DWS 精度

var (
	numericTypeRe = regexp.MustCompile(`(?i)^(NUMERIC|DECIMAL|VARCHAR|VARCHAR2|NVARCHAR|NVARCHAR2|CHAR)\b`)
	hasPrecision  = regexp.MustCompile(`\(\s*[0-9]`)
)

// chkDWSPrecision 的适用类型来自参数。新版规范(RULE 25)只管 NUMERIC/DECIMAL,
// VARCHAR 的长度挪到了 RULE 29 由 chkDWSVarcharLength 管 —— 两条规则各管各的,
// 否则同一个字段会被报两次,而人只会觉得这审查在重复啰嗦。
func chkDWSPrecision(st *stmt, p params, _ string) []apimsg.M {
	types := p.list("types", []string{"numeric", "decimal", "varchar", "varchar2", "nvarchar", "nvarchar2", "char"})
	var out []apimsg.M
	for _, c := range createOrAlterColumns(st) {
		typ := strings.TrimSpace(columnTypeOf(c.def))
		base := baseTypeName(strings.ToLower(typ))
		want := false
		for _, t := range types {
			if base == t {
				want = true
				break
			}
		}
		if !want {
			continue
		}
		if hasPrecision.MatchString(typ) {
			continue
		}
		out = append(out, msgf("字段 %s 的类型 %s 未指定精度/长度", c.name, base))
	}
	return out
}

// chkIndexNameLength caps the index name. The ceiling is a governance choice
// (32 in the MySQL/TiDB standards), not the engine's limit — a name the database
// accepts can still be too long to read in a slow-query log.
func chkIndexNameLength(st *stmt, p params, _ string) []apimsg.M {
	max := p.num("max", 32)
	var out []apimsg.M
	for _, name := range indexNames(st) {
		if n := len([]rune(name)); n > max {
			out = append(out, msgf("索引名 %s 有 %d 个字符,超过上限 %d", name, n, max))
		}
	}
	return out
}

// indexNames collects the index names a statement creates, from either
// CREATE INDEX or the KEY/UNIQUE items of a CREATE TABLE / ALTER TABLE.
func indexNames(st *stmt) []string {
	var out []string
	if m := createIndexRe.FindStringSubmatch(st.masked); m != nil {
		if n := identName(m[2]); n != "" {
			out = append(out, n)
		}
		return out
	}
	for _, it := range indexItems(st) {
		if raw := keyDefRe.FindStringSubmatch(it); raw != nil && len(raw) > 2 {
			if n := identName(raw[2]); n != "" {
				out = append(out, n)
			}
		}
	}
	return out
}

// isDropConstraint tells an index/constraint drop apart from a column drop, so
// the DROP guard does not report "ALTER TABLE t DROP CONSTRAINT fk_x" as data
// loss — it removes a rule, not a column's values.
func isDropConstraint(upper string) bool {
	for _, w := range []string{"DROP CONSTRAINT", "DROP INDEX", "DROP KEY", "DROP PRIMARY KEY",
		"DROP FOREIGN KEY", "DROP CHECK", "DROP PARTITION", "DROP DEFAULT"} {
		if strings.Contains(upper, w) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------- 共用小工具

// createOrAlterColumns returns the columns a CREATE TABLE defines or an ALTER
// TABLE adds, so a column-level rule covers both without each checker repeating
// the branch.
func createOrAlterColumns(st *stmt) []colDef {
	if createTableRe.MatchString(st.masked) {
		block, _ := body(st.masked)
		cols, _ := columnsOf(block)
		return cols
	}
	if alterTableRe.MatchString(st.masked) {
		return addedColumns(st)
	}
	return nil
}

// columnTypeOf reads the type out of a column definition ("id BIGINT NOT NULL"
// → "BIGINT"). It keeps the parenthesised precision, which chkDWSPrecision needs.
func columnTypeOf(def string) string {
	f := strings.Fields(strings.TrimSpace(def))
	if len(f) < 2 {
		return ""
	}
	typ := f[1]
	// "DOUBLE PRECISION" / "TIMESTAMP WITH TIME ZONE" — keep the second word when
	// it is part of the type rather than a constraint.
	if len(f) > 2 {
		switch strings.ToUpper(f[2]) {
		case "PRECISION", "VARYING":
			typ += " " + f[2]
		}
	}
	// A precision split by a space ("NUMERIC (10,2)") still belongs to the type.
	if !strings.Contains(typ, "(") && len(f) > 2 && strings.HasPrefix(f[2], "(") {
		typ += f[2]
	}
	return typ
}

// baseTypeName drops the precision: "varchar(64)" → "varchar".
func baseTypeName(typ string) string {
	if i := strings.IndexByte(typ, '('); i >= 0 {
		typ = typ[:i]
	}
	return strings.ToLower(strings.TrimSpace(typ))
}

// subquerySpans returns the [start,end) ranges of parenthesised SELECTs.
//
// "inside parentheses" is NOT the same as "inside a subquery": an INSERT's
// VALUES(...) list is parenthesised too, and nextval() there is the ordinary way
// to take a sequence value. Keying on `( … SELECT` instead is what keeps this
// rule off correct code.
func subquerySpans(s string) [][2]int {
	var spans [][2]int
	for i := 0; i < len(s); i++ {
		if s[i] != '(' {
			continue
		}
		// 括号后的第一个词是不是 SELECT(允许 WITH 开头的子查询)。
		j := i + 1
		for j < len(s) && (s[j] == ' ' || s[j] == '\n' || s[j] == '\t' || s[j] == '\r') {
			j++
		}
		head := strings.ToUpper(headOf(s[j:], 6))
		if head != "SELECT" && strings.ToUpper(headOf(s[j:], 4)) != "WITH" {
			continue
		}
		depth, end := 1, -1
		for k := i + 1; k < len(s); k++ {
			switch s[k] {
			case '(':
				depth++
			case ')':
				depth--
				if depth == 0 {
					end = k
				}
			}
			if end >= 0 {
				break
			}
		}
		if end < 0 {
			end = len(s)
		}
		spans = append(spans, [2]int{i, end})
	}
	return spans
}

func inSubquery(spans [][2]int, off int) bool {
	for _, sp := range spans {
		if off > sp[0] && off < sp[1] {
			return true
		}
	}
	return false
}

// parenDepthAt reports how many unclosed '(' precede offset i.
func parenDepthAt(s string, i int) int {
	depth := 0
	for j := 0; j < i && j < len(s); j++ {
		switch s[j] {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		}
	}
	return depth
}

// isSQLKeyword recognises the words that can follow FROM/JOIN without being a
// table name, so chkSchemaQualified does not demand a schema for DUAL.
func isSQLKeyword(w string) bool {
	switch w {
	case "DUAL", "LATERAL", "SELECT", "ONLY", "TABLE", "VALUES", "UNNEST":
		return true
	}
	return false
}
