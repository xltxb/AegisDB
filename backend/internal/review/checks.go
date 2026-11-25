package review

import (
	"regexp"
	"strconv"
	"strings"

	"velagateway/internal/apimsg"
)

// checker judges one statement. It returns one message per violation found (a
// column-level rule can report several in a single CREATE TABLE), and nil when
// the statement conforms or the rule does not apply to it.
//
// Every checker reads st.masked/st.upper — the text with quoted literals blanked
// — so a keyword inside a string value cannot trigger it. The two exceptions
// (leading-wildcard LIKE, plain-text password) need the literal itself and say
// so at their definition.
type checker func(st *stmt, p params, dialect string) []apimsg.M

// builtinChecker resolves a rule code to its implementation. A code with no
// implementation cannot reach here: Builtins below and this registry are checked
// against each other by TestBuiltinRegistryComplete.
func builtinChecker(code string) (checker, bool) {
	c, ok := registry[code]
	return c, ok
}

var registry = map[string]checker{
	"dml.require.where":              chkRequireWhere,
	"dml.require.limit":              chkRequireLimit,
	"dml.insert.require.column":      chkInsertColumns,
	"dml.forbid.select.star":         chkSelectStar,
	"ddl.forbid.drop":                chkForbidDrop,
	"ddl.forbid.truncate":            chkForbidTruncate,
	"ddl.forbid.rename":              chkForbidRename,
	"ddl.require.primary.key":        chkRequirePrimaryKey,
	"ddl.require.table.comment":      chkTableComment,
	"ddl.require.col.comment":        chkColumnComment,
	"ddl.require.col.not.null":       chkColumnNotNull,
	"ddl.addcol.notnull.nodefault":   chkAddColumnNotNullNoDefault,
	"ddl.forbid.column.type":         chkForbidColumnType,
	"ddl.varchar.max.length":         chkVarcharLength,
	"naming.table.pattern":           chkTableNaming,
	"naming.index.prefix":            chkIndexNaming,
	"naming.identifier.length":       chkIdentifierLength,
	"naming.forbid.keyword":          chkReservedWord,
	"index.max.columns":              chkIndexColumns,
	"index.max.per.table":            chkIndexCount,
	"mysql.require.innodb":           chkEngineInnoDB,
	"mysql.require.utf8mb4":          chkCharsetUtf8mb4,
	"tidb.forbid.foreign.key":        chkTiDBForeignKey,
	"tidb.avoid.auto.increment":      chkTiDBAutoIncrement,
	"dws.require.distribute.by":      chkDWSDistributeBy,
	"oracle.prefer.varchar2":         chkOracleVarchar2,
	"oracle.number.precision":        chkOracleNumberPrecision,
	"security.forbid.plain.password": chkPlainPassword,
	"security.forbid.grant.all":      chkGrantAll,
	"security.forbid.grant.public":   chkGrantPublic,
	"perf.forbid.leading.wildcard":   chkLeadingWildcard,
	"perf.forbid.func.on.column":     chkFunctionOnColumn,

	// ---- 由公司规范派生(实现见 checks_spec.go)----
	"mysql.alter.require.algorithm":    chkAlterAlgorithm,
	"oracle.index.require.online":      chkOracleIndexOnline,
	"tidb.require.col.not.null":        chkColumnNotNull,
	"tidb.forbid.timestamp":            chkTiDBTimestamp,
	"tidb.avoid.partition":             chkTiDBPartition,
	"tidb.table.prefix":                chkTableNaming,
	"tidb.table.max.length":            chkTableNameLength,
	"dws.require.col.not.null":         chkColumnNotNull,
	"dws.forbid.select.star":           chkSelectStar,
	"dws.delete.use.truncate":          chkDeleteWholeTable,
	"dws.avoid.column.type":            chkForbidColumnType,
	"dws.forbid.column.type":           chkForbidColumnType,
	"dws.require.precision":            chkDWSPrecision,
	"dws.distribute.key.max":           chkDWSDistributeKeyCount,
	"dws.distribute.key.type":          chkDWSDistributeKeyType,
	"dws.replication.confirm":          chkDWSReplication,
	"dws.partition.max":                chkDWSPartitionCount,
	"dws.view.forbid.orderby":          chkViewOrderBy,
	"dws.view.max.nesting":             chkViewNesting,
	"dws.forbid.not.in.subquery":       chkNotInSubquery,
	"dws.forbid.scalar.subquery":       chkScalarSubquery,
	"dws.subquery.forbid.orderby":      chkSubqueryOrderBy,
	"dws.forbid.volatile.in.subquery":  chkVolatileInSubquery,
	"dws.forbid.with.recursive":        chkWithRecursive,
	"dws.require.schema.qualified":     chkSchemaQualified,
	"dws.max.join.tables":              chkJoinCount,
	codeSQLTag:                         nil,
	"dws.forbid.func.on.column":       chkFunctionOnColumn,

	// ---- 新版 DWS 规范 RULE 1..62(实现见 checks_dws.go)----
	"dws.object.name.charset":            chkDWSNameCharset,
	"dws.object.name.reserved.prefix":    chkDWSReservedPrefix,
	"dws.object.name.max.length":         chkDWSNameLength,
	"dws.temp.table.date.suffix":         chkDWSTempTableSuffix,
	"dws.forbid.create.database":         chkDWSCreateDatabase,
	"dws.database.charset.utf8":          chkDWSDatabaseUTF8,
	"dws.database.dbcompatibility":       chkDWSDbCompatibility,
	"dws.forbid.tablespace":              chkDWSForbidTablespace,
	"dws.forbid.trigger":                 chkDWSForbidTrigger,
	"dws.forbid.udf":                     chkDWSForbidUDF,
	"dws.forbid.unlogged.table":          chkDWSForbidUnlogged,
	"dws.require.hstore.opt":             chkDWSRequireHstoreOpt,
	"dws.distribute.key.subset":          chkDWSDistributeKeySubset,
	"dws.distribute.key.no.uuid.default": chkDWSUUIDDefaultOnDistKey,
	"dws.pk.max.columns":                 chkDWSPKColumns,
	"dws.partition.single.range":         chkDWSPartitionSingleRange,
	"dws.partition.ttl":                  chkDWSPartitionTTL,
	"dws.index.btree.only":               chkDWSIndexMethod,
	"dws.index.max.per.table":            chkIndexCount,
	"dws.varchar.length":                 chkDWSVarcharLength,
	"dws.forbid.returning":               chkDWSForbidReturning,
	"dws.forbid.distinct.on":             chkDWSForbidDistinctOn,
	"dws.forbid.orderby.in.aggregate":    chkDWSAggregateOrderBy,
	"dws.forbid.query.dop":               chkDWSQueryDop,
	"dws.prefer.join.over.exists":        chkDWSPreferJoin,
	"naming.index.max.length":          chkIndexNameLength,
	"security.forbid.sensitive.column": chkSensitiveColumn,
	// ddl.alter.merge is a SCRIPT-level rule (it compares statements to each
	// other), handled by scriptFindings rather than by a per-statement checker.
	codeAlterMerge: nil,
}

// ---------------------------------------------------------------- DML

var (
	whereRe   = regexp.MustCompile(`(?i)\bWHERE\b`)
	limitRe   = regexp.MustCompile(`(?i)\bLIMIT\b`)
	selStarRe = regexp.MustCompile(`(?i)\bSELECT\s+\*`)
	insertRe  = regexp.MustCompile(`(?is)^\s*(?:INSERT|REPLACE)\s+(?:LOW_PRIORITY\s+|DELAYED\s+|HIGH_PRIORITY\s+|IGNORE\s+|/\*.*?\*/\s*)*INTO\s+([^\s(]+)\s*(.?)`)
	setRe     = regexp.MustCompile(`(?i)\bSET\b`)
)

// chkRequireWhere is the one review rule that overlaps the risk engine's
// strict mode, and deliberately so: strict mode is a global switch an operator
// may have turned off for the terminal, while a release must never carry an
// unscoped mutation regardless of that setting.
func chkRequireWhere(st *stmt, _ params, _ string) []apimsg.M {
	if st.verb != "UPDATE" && st.verb != "DELETE" {
		return nil
	}
	if whereRe.MatchString(st.masked) {
		return nil
	}
	return []apimsg.M{msgf("%s 未带 WHERE 条件,将作用于全表", st.verb)}
}

func chkRequireLimit(st *stmt, _ params, _ string) []apimsg.M {
	if st.verb != "UPDATE" && st.verb != "DELETE" {
		return nil
	}
	if !whereRe.MatchString(st.masked) || limitRe.MatchString(st.masked) {
		return nil // no WHERE at all is the require.where rule's finding, not this one
	}
	return []apimsg.M{msgf("%s 未带 LIMIT,建议分批执行以免长事务锁表", st.verb)}
}

// chkInsertColumns flags `INSERT INTO t VALUES (…)` — a column list is what
// keeps the statement correct after someone adds a column to the table.
func chkInsertColumns(st *stmt, _ params, _ string) []apimsg.M {
	if st.verb != "INSERT" && st.verb != "REPLACE" {
		return nil
	}
	m := insertRe.FindStringSubmatch(st.masked)
	if m == nil {
		return nil
	}
	// MySQL's `INSERT INTO t SET a=1` names its columns too.
	rest := st.masked[len(m[0])-len(m[2]):]
	if strings.HasPrefix(strings.TrimSpace(rest), "(") {
		return nil
	}
	if head := headOf(rest, 40); setRe.MatchString(head) {
		return nil
	}
	return []apimsg.M{msgf("INSERT 未显式指定列名: %s", identName(m[1]))}
}

func chkSelectStar(st *stmt, _ params, _ string) []apimsg.M {
	if !selStarRe.MatchString(st.masked) {
		return nil
	}
	return []apimsg.M{msgf("使用了 SELECT *,建议显式列出所需字段")}
}

// ---------------------------------------------------------------- DDL guards

var (
	dropRe     = regexp.MustCompile(`(?is)^\s*DROP\s+([A-Z]+)\s+(?:IF\s+EXISTS\s+)?([^\s;]+)`)
	truncateRe = regexp.MustCompile(`(?is)^\s*TRUNCATE\s+(?:TABLE\s+)?([^\s;]+)`)
	renameRe   = regexp.MustCompile(`(?is)^\s*RENAME\s+TABLE\s+([^\s;]+)`)
	alterRenRe = regexp.MustCompile(`(?is)^\s*ALTER\s+TABLE\s+([^\s]+)\s+RENAME\b`)
)

// dropColumnRe catches ALTER TABLE … DROP COLUMN, which the three standards
// forbid in production alongside DROP TABLE/DATABASE. It is a separate shape
// from a leading DROP and would otherwise slip past unnoticed.
var dropColumnRe = regexp.MustCompile(`(?is)\bALTER\s+TABLE\s+([^\s]+)\s+.*?\bDROP\s+(?:COLUMN\s+)?([A-Za-z_` + "`" + `"\[][^\s,;]*)`)

func chkForbidDrop(st *stmt, p params, _ string) []apimsg.M {
	// 关键字用大写形式识别,对象名从原始大小写里取 —— 报错里出现 T_USERS 而库里
	// 叫 t_users,会让人以为是另一张表。
	rawM := dropRe.FindStringSubmatch(st.masked)
	m := dropRe.FindStringSubmatch(st.upper)
	if m == nil {
		if p.boolean("dropColumn", false) {
			if dm := dropColumnRe.FindStringSubmatch(st.masked); dm != nil && !isDropConstraint(st.upper) {
				return []apimsg.M{msgf("ALTER TABLE %s DROP COLUMN %s 会丢弃列上的数据且不可回滚", identName(dm[1]), identName(dm[2]))}
			}
		}
		return nil
	}
	objects := p.list("objects", []string{"table", "database", "schema", "tablespace"})
	obj := strings.ToLower(m[1])
	for _, o := range objects {
		if o == obj {
			name := identName(m[2])
			if rawM != nil {
				name = identName(rawM[2])
			}
			return []apimsg.M{msgf("DROP %s %s 属于不可逆变更,需通过备份/回滚点流程执行", strings.ToUpper(obj), name)}
		}
	}
	return nil
}

func chkForbidTruncate(st *stmt, _ params, _ string) []apimsg.M {
	m := truncateRe.FindStringSubmatch(st.upper)
	if m == nil {
		return nil
	}
	return []apimsg.M{msgf("TRUNCATE %s 会清空整表且不可回滚", identName(m[1]))}
}

func chkForbidRename(st *stmt, _ params, _ string) []apimsg.M {
	if m := renameRe.FindStringSubmatch(st.upper); m != nil {
		return []apimsg.M{msgf("RENAME TABLE %s 会立即改变线上对象名,需与应用发布同步", identName(m[1]))}
	}
	if m := alterRenRe.FindStringSubmatch(st.upper); m != nil {
		return []apimsg.M{msgf("ALTER TABLE %s RENAME 会立即改变线上对象名,需与应用发布同步", identName(m[1]))}
	}
	return nil
}

// ---------------------------------------------------------------- CREATE TABLE

var (
	createTableRe = regexp.MustCompile(`(?is)^\s*CREATE\s+(?:GLOBAL\s+TEMPORARY\s+|LOCAL\s+TEMPORARY\s+|TEMPORARY\s+|UNLOGGED\s+)?TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?([^\s(]+)`)
	alterTableRe  = regexp.MustCompile(`(?is)^\s*ALTER\s+TABLE\s+([^\s]+)`)
	addColumnRe   = regexp.MustCompile(`(?is)\bADD\s+(?:COLUMN\s+)?`)
	createIndexRe = regexp.MustCompile(`(?is)^\s*CREATE\s+(UNIQUE\s+)?INDEX\s+([^\s]+)\s+ON\s+([^\s(]+)\s*\(([^)]*)\)`)
)

func alterTableName(st *stmt) string {
	if m := alterTableRe.FindStringSubmatch(st.masked); m != nil {
		return identName(m[1])
	}
	return ""
}

// body returns the parenthesised definition block of a CREATE TABLE, and the
// text that follows it (where MySQL puts ENGINE/CHARSET/COMMENT and DWS puts
// DISTRIBUTE BY). Empty when the statement has no block — CREATE TABLE … AS
// SELECT, which every column-level rule must skip rather than report as a table
// with no columns.
func body(sql string) (block, tail string) {
	start := strings.Index(sql, "(")
	if start < 0 {
		return "", ""
	}
	depth := 0
	for i := start; i < len(sql); i++ {
		switch sql[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return sql[start+1 : i], sql[i+1:]
			}
		}
	}
	return "", ""
}

// splitTop splits a definition block on top-level commas.
func splitTop(s string) []string {
	var out []string
	depth, last := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, strings.TrimSpace(s[last:i]))
				last = i + 1
			}
		}
	}
	if tail := strings.TrimSpace(s[last:]); tail != "" {
		out = append(out, tail)
	}
	return out
}

// constraintHeads are the words that open a table CONSTRAINT rather than a
// column definition, so column rules do not report "the column PRIMARY has no
// comment".
var constraintHeads = map[string]bool{
	"PRIMARY": true, "UNIQUE": true, "KEY": true, "INDEX": true, "FULLTEXT": true,
	"SPATIAL": true, "CONSTRAINT": true, "FOREIGN": true, "CHECK": true, "PERIOD": true,
}

type colDef struct {
	name string // as written, unquoted
	def  string // the whole definition item
	up   string // upper-cased definition
}

// columnsOf returns the column definitions and the constraint/index items of a
// CREATE TABLE block, separated.
func columnsOf(block string) (cols []colDef, constraints []string) {
	for _, item := range splitTop(block) {
		if item == "" {
			continue
		}
		head := strings.ToUpper(firstToken(item))
		if constraintHeads[head] {
			constraints = append(constraints, item)
			continue
		}
		cols = append(cols, colDef{name: identName(firstToken(item)), def: item, up: strings.ToUpper(item)})
	}
	return cols, constraints
}

// identName strips quoting (backticks, double quotes, brackets) and any schema
// qualifier, leaving the bare object name.
func identName(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "`\"[]();")
	if i := strings.LastIndex(s, "."); i >= 0 {
		s = s[i+1:]
	}
	return strings.Trim(s, "`\"[]")
}

func headOf(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func chkRequirePrimaryKey(st *stmt, _ params, _ string) []apimsg.M {
	m := createTableRe.FindStringSubmatch(st.masked)
	if m == nil {
		return nil
	}
	block, _ := body(st.masked)
	if strings.TrimSpace(block) == "" {
		return nil // CREATE TABLE … AS SELECT: no column block to judge
	}
	if strings.Contains(strings.ToUpper(block), "PRIMARY KEY") {
		return nil
	}
	return []apimsg.M{msgf("表 %s 未定义主键", identName(m[1]))}
}

var tableCommentRe = regexp.MustCompile(`(?is)\bCOMMENT\s*=?\s*['"]`)

func chkTableComment(st *stmt, _ params, _ string) []apimsg.M {
	m := createTableRe.FindStringSubmatch(st.masked)
	if m == nil {
		return nil
	}
	block, tail := body(st.masked)
	if strings.TrimSpace(block) == "" {
		return nil
	}
	if tableCommentRe.MatchString(tail) {
		return nil
	}
	return []apimsg.M{msgf("表 %s 缺少表注释 (COMMENT)", identName(m[1]))}
}

func chkColumnComment(st *stmt, _ params, _ string) []apimsg.M {
	var out []apimsg.M
	if createTableRe.MatchString(st.masked) {
		block, _ := body(st.masked)
		if strings.TrimSpace(block) == "" {
			return nil
		}
		cols, _ := columnsOf(block)
		for _, c := range cols {
			if !strings.Contains(c.up, "COMMENT") {
				out = append(out, msgf("列 %s 缺少字段注释 (COMMENT)", c.name))
			}
		}
		return out
	}
	// ALTER TABLE … ADD COLUMN x INT  → the same requirement applies.
	for _, c := range addedColumns(st) {
		if !strings.Contains(c.up, "COMMENT") {
			out = append(out, msgf("新增列 %s 缺少字段注释 (COMMENT)", c.name))
		}
	}
	return out
}

// addedColumns returns the column definitions an ALTER TABLE adds.
func addedColumns(st *stmt) []colDef {
	if !alterTableRe.MatchString(st.masked) {
		return nil
	}
	var out []colDef
	rest := st.masked
	for {
		loc := addColumnRe.FindStringIndex(rest)
		if loc == nil {
			return out
		}
		after := rest[loc[1]:]
		rest = after
		// One ADD may carry a parenthesised list of columns; otherwise the
		// definition runs to the next top-level comma.
		seg := after
		if strings.HasPrefix(strings.TrimSpace(after), "(") {
			block, _ := body(after)
			for _, item := range splitTop(block) {
				out = append(out, colDef{name: identName(firstToken(item)), def: item, up: strings.ToUpper(item)})
			}
			continue
		}
		if parts := splitTop(seg); len(parts) > 0 {
			seg = parts[0]
		}
		seg = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(seg), ";"))
		if seg == "" {
			continue
		}
		head := strings.ToUpper(firstToken(seg))
		if constraintHeads[head] {
			continue // ADD INDEX / ADD CONSTRAINT is not a column
		}
		out = append(out, colDef{name: identName(firstToken(seg)), def: seg, up: strings.ToUpper(seg)})
	}
}

func chkColumnNotNull(st *stmt, _ params, _ string) []apimsg.M {
	var out []apimsg.M
	cols := addedColumns(st)
	if createTableRe.MatchString(st.masked) {
		block, _ := body(st.masked)
		if strings.TrimSpace(block) == "" {
			return nil
		}
		cols, _ = columnsOf(block)
	}
	for _, c := range cols {
		if strings.Contains(c.up, "NOT NULL") || strings.Contains(c.up, "PRIMARY KEY") {
			continue
		}
		if isTemporalType(columnTypeOf(skipQuotedName(c.def))) {
			continue // 时间类型字段不强制 NOT NULL(TiDB/MySQL/Oracle/DWS 规范一致)
		}
		out = append(out, msgf("列 %s 未声明 NOT NULL", c.name))
	}
	return out
}

// temporalTypes are the date/time base type names across MySQL/TiDB, Oracle and
// DWS(GaussDB/PostgreSQL). Precision and time-zone suffixes are stripped before
// the lookup: TIMESTAMP(6) WITH LOCAL TIME ZONE → timestamp, INTERVAL DAY(2) TO
// SECOND → interval.
var temporalTypes = map[string]bool{
	"date": true, "datetime": true, "timestamp": true, "time": true, "year": true,
	"interval": true, "timestamptz": true, "timetz": true, "smalldatetime": true,
	"abstime": true, "reltime": true, "tinterval": true,
}

// skipQuotedName rewrites a definition that opens with a quoted column name
// ("CREATED_AT" TIMESTAMP(6) …, as Oracle/DWS tools export DDL) to a placeholder
// name, so columnTypeOf finds the type. In the masked text a double-quoted name
// is blanked to `"   "`, which Fields would split into two bogus tokens.
func skipQuotedName(def string) string {
	d := strings.TrimSpace(def)
	if d == "" || (d[0] != '"' && d[0] != '`') {
		return def
	}
	if end := strings.IndexByte(d[1:], d[0]); end >= 0 {
		return "c " + d[end+2:]
	}
	return def
}

// isTemporalType reports whether a column type (as columnTypeOf returns it) is a
// date/time type. Arrays (timestamp[]) are not.
func isTemporalType(typ string) bool {
	return temporalTypes[strings.Trim(baseTypeName(typ), "`\"")]
}

// chkAddColumnNotNullNoDefault catches the classic online-DDL failure: adding a
// NOT NULL column with no DEFAULT to a table that already has rows. The database
// has no value to write into the existing rows, so the statement either fails
// outright or rewrites the whole table under a lock.
func chkAddColumnNotNullNoDefault(st *stmt, _ params, _ string) []apimsg.M {
	var out []apimsg.M
	for _, c := range addedColumns(st) {
		if strings.Contains(c.up, "NOT NULL") && !strings.Contains(c.up, "DEFAULT") {
			out = append(out, msgf("新增列 %s 声明 NOT NULL 但未给 DEFAULT,存量数据表上会失败或长时间锁表", c.name))
		}
	}
	return out
}

var typeWordRe = regexp.MustCompile(`(?i)^\s*[^\s]+\s+([A-Za-z_][A-Za-z0-9_]*)`)

func chkForbidColumnType(st *stmt, p params, dialect string) []apimsg.M {
	// 默认值见 params_spec.go(fire 会先把它合进 p);这里的字面量只是兜底。
	// Oracle 不再默认禁 VARCHAR:那是 oracle.prefer.varchar2 的事,两条各报一次只是噪音。
	def := []string{"float", "double", "real", "blob", "longblob"}
	if dialect == DialectOracle {
		def = append(def, "long")
	}
	banned := p.list("types", def)
	cols := addedColumns(st)
	if createTableRe.MatchString(st.masked) {
		block, _ := body(st.masked)
		if strings.TrimSpace(block) == "" {
			return nil
		}
		cols, _ = columnsOf(block)
	}
	var out []apimsg.M
	for _, c := range cols {
		m := typeWordRe.FindStringSubmatch(c.def)
		if m == nil {
			continue
		}
		t := strings.ToLower(m[1])
		for _, b := range banned {
			if t == b {
				out = append(out, msgf("列 %s 使用了不推荐的类型 %s", c.name, strings.ToUpper(t)))
				break
			}
		}
	}
	return out
}

var varcharRe = regexp.MustCompile(`(?i)\bVARCHAR2?\s*\(\s*(\d+)`)

func chkVarcharLength(st *stmt, p params, _ string) []apimsg.M {
	max := p.num("max", 4000)
	var out []apimsg.M
	for _, m := range varcharRe.FindAllStringSubmatch(st.masked, -1) {
		n, err := strconv.Atoi(m[1])
		if err == nil && n > max {
			out = append(out, msgf("VARCHAR(%d) 超过约定上限 %d,超长文本建议独立表存储", n, max))
		}
	}
	return out
}

// ---------------------------------------------------------------- naming

func chkTableNaming(st *stmt, p params, dialect string) []apimsg.M {
	m := createTableRe.FindStringSubmatch(st.masked)
	if m == nil {
		return nil
	}
	def := `^[a-z][a-z0-9_]*$`
	if dialect == DialectOracle {
		// Oracle folds unquoted identifiers to upper case, so a lower-case pattern
		// would flag every conforming table on it.
		def = `^[A-Za-z][A-Za-z0-9_$#]*$`
	}
	pat := p.str("pattern", def)
	re, err := regexp.Compile(pat)
	if err != nil {
		return []apimsg.M{msgf("表名规则的正则无效: %s", pat)}
	}
	name := identName(m[1])
	if re.MatchString(name) {
		return nil
	}
	return []apimsg.M{msgf("表名 %s 不符合命名规范 %s", name, pat)}
}

var (
	keyDefRe = regexp.MustCompile(`(?is)^(UNIQUE\s+)?(?:KEY|INDEX)\s+([^\s(]+)?`)
	// 具名主键约束。MySQL 会无视这个名字(主键索引永远叫 PRIMARY),但 TiDB / DWS 会保留它,
	// 而三份规范都把"主键索引名以 pk_ 开头"写成了【強制】。没起名字的主键不报 —— 那不是
	// 写错了名字,是没有名字。
	pkConstraintRe = regexp.MustCompile(`(?is)\bCONSTRAINT\s+([^\s(]+)\s+PRIMARY\s+KEY\b`)
)

func chkIndexNaming(st *stmt, p params, _ string) []apimsg.M {
	var out []apimsg.M
	if createTableRe.MatchString(st.masked) || alterTableRe.MatchString(st.masked) {
		pk := p.str("pkPrefix", "pk_")
		for _, m := range pkConstraintRe.FindAllStringSubmatch(st.masked, -1) {
			if name := identName(m[1]); !strings.HasPrefix(strings.ToLower(name), strings.ToLower(pk)) {
				out = append(out, msgf("主键约束 %s 未使用约定前缀 %s", name, pk))
			}
		}
	}
	uniq := p.str("uniquePrefix", "uk_")
	idx := p.str("indexPrefix", "idx_")
	check := func(name string, unique bool) apimsg.M {
		if name == "" {
			return apimsg.M{}
		}
		want := idx
		if unique {
			want = uniq
		}
		if strings.HasPrefix(strings.ToLower(name), strings.ToLower(want)) {
			return apimsg.M{}
		}
		return msgf("索引 %s 未使用约定前缀 %s", name, want)
	}
	if m := createIndexRe.FindStringSubmatch(st.masked); m != nil {
		if msg := check(identName(m[2]), strings.TrimSpace(m[1]) != ""); msg.S != "" {
			out = append(out, msg)
		}
		return out
	}
	items := indexItems(st)
	for _, it := range items {
		if m := keyDefRe.FindStringSubmatch(strings.ToUpper(it)); m != nil {
			// Re-read the name from the original case; the regex ran on the upper
			// form only to recognise the keywords.
			raw := keyDefRe.FindStringSubmatch(it)
			name := ""
			if raw != nil && len(raw) > 2 {
				name = identName(raw[2])
			}
			if msg := check(name, strings.TrimSpace(m[1]) != ""); msg.S != "" {
				out = append(out, msg)
			}
		}
	}
	return out
}

// indexItems returns the index/key definitions of a CREATE TABLE block or an
// ALTER TABLE … ADD INDEX.
func indexItems(st *stmt) []string {
	var out []string
	if createTableRe.MatchString(st.masked) {
		block, _ := body(st.masked)
		_, cons := columnsOf(block)
		for _, c := range cons {
			up := strings.ToUpper(c)
			if strings.HasPrefix(up, "KEY") || strings.HasPrefix(up, "INDEX") || strings.HasPrefix(up, "UNIQUE") {
				out = append(out, c)
			}
		}
		return out
	}
	if !alterTableRe.MatchString(st.masked) {
		return nil
	}
	rest := st.masked
	for {
		loc := addColumnRe.FindStringIndex(rest)
		if loc == nil {
			return out
		}
		seg := rest[loc[1]:]
		rest = seg
		if parts := splitTop(seg); len(parts) > 0 {
			seg = parts[0]
		}
		up := strings.ToUpper(strings.TrimSpace(seg))
		if strings.HasPrefix(up, "KEY") || strings.HasPrefix(up, "INDEX") || strings.HasPrefix(up, "UNIQUE") {
			out = append(out, strings.TrimSpace(seg))
		}
	}
}

// chkTableNameLength 只量表名。tidb.table.max.length 原先借用 chkIdentifierLength,于是列名、
// 索引名超过 32 也被报成"表名不超过 32 个字符"的 error —— TiDB 规范只限了表名。
func chkTableNameLength(st *stmt, p params, _ string) []apimsg.M {
	m := createTableRe.FindStringSubmatch(st.masked)
	if m == nil {
		return nil
	}
	max := p.num("max", 32)
	if n := identName(m[1]); len([]rune(n)) > max {
		return []apimsg.M{msgf("表名 %s 长度 %d 超过上限 %d", n, len([]rune(n)), max)}
	}
	return nil
}

func chkIdentifierLength(st *stmt, p params, dialect string) []apimsg.M {
	def := 64
	if dialect == DialectOracle {
		def = 30 // pre-12.2 Oracle; configurable for 12.2+ (128)
	}
	max := p.num("max", def)
	var names []string
	if m := createTableRe.FindStringSubmatch(st.masked); m != nil {
		names = append(names, identName(m[1]))
		block, _ := body(st.masked)
		cols, _ := columnsOf(block)
		for _, c := range cols {
			names = append(names, c.name)
		}
	}
	if m := createIndexRe.FindStringSubmatch(st.masked); m != nil {
		names = append(names, identName(m[2]))
	}
	for _, c := range addedColumns(st) {
		names = append(names, c.name)
	}
	var out []apimsg.M
	for _, n := range names {
		if n != "" && len([]rune(n)) > max {
			out = append(out, msgf("标识符 %s 长度 %d 超过上限 %d", n, len([]rune(n)), max))
		}
	}
	return out
}

// defaultReserved is a deliberately small list: words that are reserved in at
// least one of the four supported engines AND are common table/column names.
// A full reserved-word list would flag half of every schema and get the rule
// switched off, which protects nothing.
var defaultReserved = []string{
	"order", "group", "desc", "asc", "key", "index", "table", "column", "user",
	"comment", "level", "size", "range", "rows", "session", "resource", "number", "date",
}

func chkReservedWord(st *stmt, p params, _ string) []apimsg.M {
	words := map[string]bool{}
	for _, w := range p.list("words", defaultReserved) {
		words[w] = true
	}
	var names []string
	if m := createTableRe.FindStringSubmatch(st.masked); m != nil {
		names = append(names, identName(m[1]))
		block, _ := body(st.masked)
		cols, _ := columnsOf(block)
		for _, c := range cols {
			names = append(names, c.name)
		}
	}
	for _, c := range addedColumns(st) {
		names = append(names, c.name)
	}
	var out []apimsg.M
	for _, n := range names {
		if n != "" && words[strings.ToLower(n)] {
			out = append(out, msgf("标识符 %s 是保留字,使用时必须加引号,建议改名", n))
		}
	}
	return out
}

// ---------------------------------------------------------------- indexes

func chkIndexColumns(st *stmt, p params, _ string) []apimsg.M {
	max := p.num("max", 5)
	count := func(cols string) int {
		n := 0
		for _, c := range splitTop(cols) {
			if strings.TrimSpace(c) != "" {
				n++
			}
		}
		return n
	}
	var out []apimsg.M
	if m := createIndexRe.FindStringSubmatch(st.masked); m != nil {
		if n := count(m[4]); n > max {
			out = append(out, msgf("索引 %s 含 %d 个字段,超过上限 %d", identName(m[2]), n, max))
		}
		return out
	}
	for _, it := range indexItems(st) {
		block, _ := body(it)
		if block == "" {
			continue
		}
		if n := count(block); n > max {
			out = append(out, msgf("索引定义含 %d 个字段,超过上限 %d: %s", n, max, strings.TrimSpace(headOf(it, 60))))
		}
	}
	return out
}

func chkIndexCount(st *stmt, p params, _ string) []apimsg.M {
	if !createTableRe.MatchString(st.masked) {
		return nil
	}
	max := p.num("max", 5)
	n := len(indexItems(st))
	if n <= max {
		return nil
	}
	return []apimsg.M{msgf("单表索引 %d 个,超过上限 %d,过多索引会拖慢写入", n, max)}
}

// ---------------------------------------------------------------- MySQL / TiDB

var (
	engineRe  = regexp.MustCompile(`(?i)\bENGINE\s*=\s*([A-Za-z0-9_]+)`)
	charsetRe = regexp.MustCompile(`(?i)\b(?:DEFAULT\s+)?(?:CHARSET|CHARACTER\s+SET)\s*=?\s*([A-Za-z0-9_]+)`)
)

func chkEngineInnoDB(st *stmt, p params, _ string) []apimsg.M {
	m := createTableRe.FindStringSubmatch(st.masked)
	if m == nil {
		return nil
	}
	block, tail := body(st.masked)
	if strings.TrimSpace(block) == "" {
		return nil
	}
	want := strings.ToLower(p.str("engine", "innodb"))
	em := engineRe.FindStringSubmatch(tail)
	if em == nil {
		return []apimsg.M{msgf("建表未显式指定 ENGINE,建议 ENGINE=InnoDB")}
	}
	if strings.ToLower(em[1]) == want {
		return nil
	}
	return []apimsg.M{msgf("存储引擎 %s 不符合规范,应为 %s", em[1], want)}
}

func chkCharsetUtf8mb4(st *stmt, p params, _ string) []apimsg.M {
	m := createTableRe.FindStringSubmatch(st.masked)
	if m == nil {
		return nil
	}
	block, tail := body(st.masked)
	if strings.TrimSpace(block) == "" {
		return nil
	}
	want := strings.ToLower(p.str("charset", "utf8mb4"))
	cm := charsetRe.FindStringSubmatch(tail)
	if cm == nil {
		return []apimsg.M{msgf("建表未指定字符集,建议 DEFAULT CHARSET=%s", want)}
	}
	if strings.ToLower(cm[1]) == want {
		return nil
	}
	return []apimsg.M{msgf("字符集 %s 不符合规范,应为 %s", cm[1], want)}
}

var foreignKeyRe = regexp.MustCompile(`(?i)\bFOREIGN\s+KEY\b`)

func chkTiDBForeignKey(st *stmt, _ params, _ string) []apimsg.M {
	if !createTableRe.MatchString(st.masked) && !alterTableRe.MatchString(st.masked) {
		return nil
	}
	if !foreignKeyRe.MatchString(st.masked) {
		return nil
	}
	return []apimsg.M{msgf("TiDB 不建议使用外键约束,请在应用层保证参照完整性")}
}

var autoIncRe = regexp.MustCompile(`(?i)\bAUTO_INCREMENT\b`)

func chkTiDBAutoIncrement(st *stmt, _ params, _ string) []apimsg.M {
	if !createTableRe.MatchString(st.masked) || !autoIncRe.MatchString(st.masked) {
		return nil
	}
	return []apimsg.M{msgf("TiDB 上自增主键易形成写入热点,建议 AUTO_RANDOM 或使用业务分布式 ID")}
}

// ---------------------------------------------------------------- DWS

var (
	distributeRe  = regexp.MustCompile(`(?i)\bDISTRIBUTE\s+BY\b`)
	orientationRe = regexp.MustCompile(`(?i)\bORIENTATION\s*=\s*([A-Za-z]+)`)
)

func chkDWSDistributeBy(st *stmt, _ params, _ string) []apimsg.M {
	m := createTableRe.FindStringSubmatch(st.masked)
	if m == nil {
		return nil
	}
	if distributeRe.MatchString(st.masked) {
		return nil
	}
	return []apimsg.M{msgf("表 %s 未指定 DISTRIBUTE BY,DWS 将按默认策略分布,易造成数据倾斜", identName(m[1]))}
}


// ---------------------------------------------------------------- Oracle

var (
	oraVarcharRe = regexp.MustCompile(`(?i)\bVARCHAR\s*\(`)
	oraNumberRe  = regexp.MustCompile(`(?i)\bNUMBER\b\s*(\()?`)
)

func chkOracleVarchar2(st *stmt, _ params, _ string) []apimsg.M {
	// VARCHAR2 also contains "VARCHAR", so match only the form followed by "(".
	if !oraVarcharRe.MatchString(st.masked) {
		return nil
	}
	return []apimsg.M{msgf("Oracle 中应使用 VARCHAR2 而非 VARCHAR(VARCHAR 的语义未来可能变化)")}
}

func chkOracleNumberPrecision(st *stmt, _ params, _ string) []apimsg.M {
	for _, m := range oraNumberRe.FindAllStringSubmatch(st.masked, -1) {
		if m[1] == "" {
			return []apimsg.M{msgf("NUMBER 未指定精度,建议写明 NUMBER(p[,s]) 以避免存储与比较开销")}
		}
	}
	return nil
}

// ---------------------------------------------------------------- security

// chkPlainPassword reads the MASKED text on purpose: the literal's content is
// already blanked there, so the rule can prove a password was written inline
// without the finding itself carrying it.
var (
	identifiedByRe = regexp.MustCompile(`(?i)\bIDENTIFIED\s+BY\s+["']`)
	passwordEqRe   = regexp.MustCompile(`(?i)\bPASSWORD\s*=\s*["']`)
	grantAllRe     = regexp.MustCompile(`(?i)\bGRANT\s+ALL\b`)
	grantPublicRe  = regexp.MustCompile(`(?i)\bTO\s+PUBLIC\b`)
)

func chkPlainPassword(st *stmt, _ params, _ string) []apimsg.M {
	if identifiedByRe.MatchString(st.masked) || passwordEqRe.MatchString(st.masked) {
		return []apimsg.M{msgf("语句包含明文口令,应改用口令管理流程下发")}
	}
	return nil
}

func chkGrantAll(st *stmt, _ params, _ string) []apimsg.M {
	if st.verb != "GRANT" || !grantAllRe.MatchString(st.masked) {
		return nil
	}
	return []apimsg.M{msgf("GRANT ALL 授权范围过大,应按最小权限逐项授予")}
}

func chkGrantPublic(st *stmt, _ params, _ string) []apimsg.M {
	if st.verb != "GRANT" || !grantPublicRe.MatchString(st.masked) {
		return nil
	}
	return []apimsg.M{msgf("向 PUBLIC 授权会让所有数据库用户获得该权限")}
}

// ---------------------------------------------------------------- performance

// chkLeadingWildcard needs the LITERAL, so it reads st.sql rather than the
// masked form: the whole point is the '%' at the start of the pattern.
var leadingWildcardRe = regexp.MustCompile(`(?i)\bLIKE\s+(?:N)?['"]%`)

func chkLeadingWildcard(st *stmt, _ params, _ string) []apimsg.M {
	if !leadingWildcardRe.MatchString(st.sql) {
		return nil
	}
	return []apimsg.M{msgf("LIKE 以 %% 开头,无法命中索引,数据量大时会全表扫描")}
}

var funcOnColRe = regexp.MustCompile(`(?i)\b(DATE|SUBSTR|SUBSTRING|LEFT|RIGHT|UPPER|LOWER|TO_CHAR|TRUNC|CAST|CONVERT|IFNULL|NVL|YEAR|MONTH|DATE_FORMAT)\s*\(\s*[A-Za-z_][A-Za-z0-9_.]*[^()]*\)\s*(=|>|<|>=|<=|<>|!=|\bLIKE\b|\bIN\b)`)

func chkFunctionOnColumn(st *stmt, _ params, _ string) []apimsg.M {
	idx := whereRe.FindStringIndex(st.masked)
	if idx == nil {
		return nil
	}
	m := funcOnColRe.FindStringSubmatch(st.masked[idx[1]:])
	if m == nil {
		return nil
	}
	return []apimsg.M{msgf("WHERE 条件对列使用了函数 %s(),索引将失效", strings.ToUpper(m[1]))}
}

// ---------------------------------------------------------------- script level

const (
	codeAlterMerge = "ddl.alter.merge"
	// codeSQLTag 只能在脚本这一级判:分句器会把语句前的注释剥掉,所以到了 stmt
	// 手里那句标识注释已经不存在了。规则要的是"这段 SQL 开头有没有标明来源",
	// 原文正好是唯一还留着它的地方。
	codeSQLTag = "dws.require.sql.tag"
)

// scriptFindings holds the rules that compare statements to EACH OTHER, which a
// per-statement checker structurally cannot do.
func scriptFindings(active []Rule, stmts []*stmt, script string) []Finding {
	var out []Finding
	for _, r := range active {
		if r.Code == codeSQLTag {
			out = append(out, sqlTagFinding(r, stmts, script)...)
			continue
		}
		if r.Code != codeAlterMerge {
			continue
		}
		p := withDefaults(r.Code, "", parseParams(r.Params))
		max := p.num("max", 1)
		counts := map[string]int{}
		for _, st := range stmts {
			if t := alterTableName(st); t != "" {
				counts[strings.ToLower(t)]++
			}
		}
		seen := map[string]bool{}
		for _, st := range stmts {
			t := strings.ToLower(alterTableName(st))
			if t == "" || seen[t] {
				continue
			}
			seen[t] = true
			if n := counts[t]; n > max {
				msg := msgf("同一张表 %s 有 %d 条 ALTER 语句,建议合并为一条以避免多次表重建", t, n)
				if custom := strings.TrimSpace(r.Message); custom != "" {
					msg = apimsg.Sprintf("%s — %s", custom, msg)
				}
				out = append(out, Finding{
					Code: r.Code, Name: r.Name, Level: r.Level, Category: r.Category,
					Stmt: st.index, Line: st.line, SQL: excerpt(st.raw), Message: msg.S, MessageRef: msg.Ref,
				})
			}
		}
	}
	return out
}
