package review

// Builtin describes one shipped rule: what it checks, which dialects it applies
// to, and the level it fires at out of the box.
//
// This table is the SEED, not the runtime configuration. Every entry becomes a
// row in tbl_sql_review_rule on first boot, and from then on the row is what the
// engine reads: an operator who lowers a rule to `warn`, disables it, or tightens
// its pattern must not have that decision reverted by the next upgrade. New
// releases add rows for codes that do not exist yet and leave the rest alone
// (see repository.SeedSQLReviewRules).
type Builtin struct {
	Code     string
	Name     string
	Dialect  string // "all" or a comma-separated list
	Category string
	Level    string
	Params   string // default knobs as JSON; "" means the checker's own defaults
}

// Rule categories, in display order.
const (
	CatDML       = "dml"
	CatDDL       = "ddl"
	CatStructure = "structure"
	CatNaming    = "naming"
	CatIndex     = "index"
	CatSecurity  = "security"
	CatPerf      = "perf"
)

// Categories is the console's grouping order.
var Categories = []string{CatDML, CatDDL, CatStructure, CatNaming, CatIndex, CatSecurity, CatPerf}

// Builtins is the shipped rule library. Order here is the seeded sort order, so
// related rules stay together in the console.
var Builtins = []Builtin{
	// ---- DML ----
	{"dml.require.where", "UPDATE/DELETE 必须带 WHERE", DialectAll, CatDML, LevelError, ""},
	{"dml.require.limit", "UPDATE/DELETE 建议带 LIMIT 分批", "mysql,tidb", CatDML, LevelWarn, ""},
	{"dml.insert.require.column", "INSERT 必须显式指定列名", DialectAll, CatDML, LevelWarn, ""},
	{"dml.forbid.select.star", "禁止 SELECT *", DialectAll, CatDML, LevelInfo, ""},

	// ---- DDL 高影响动作 ----
	{"ddl.forbid.drop", "禁止 DROP 表/库", DialectAll, CatDDL, LevelError, `{"objects":["table","database","schema","tablespace"]}`},
	{"ddl.forbid.truncate", "禁止 TRUNCATE", DialectAll, CatDDL, LevelError, ""},
	{"ddl.forbid.rename", "禁止直接 RENAME 线上对象", DialectAll, CatDDL, LevelWarn, ""},
	{"ddl.alter.merge", "同表多条 ALTER 建议合并", DialectAll, CatDDL, LevelInfo, `{"max":1}`},

	// ---- 表结构 ----
	{"ddl.require.primary.key", "建表必须有主键", DialectAll, CatStructure, LevelError, ""},
	{"ddl.require.table.comment", "建表必须写表注释", "mysql,tidb", CatStructure, LevelWarn, ""},
	{"ddl.require.col.comment", "字段必须写注释", "mysql,tidb", CatStructure, LevelWarn, ""},
	{"ddl.require.col.not.null", "字段建议 NOT NULL", DialectAll, CatStructure, LevelInfo, ""},
	{"ddl.addcol.notnull.nodefault", "新增 NOT NULL 列必须给 DEFAULT", DialectAll, CatStructure, LevelError, ""},
	{"ddl.forbid.column.type", "禁用的字段类型", DialectAll, CatStructure, LevelWarn, ""},
	{"ddl.varchar.max.length", "VARCHAR 长度上限", DialectAll, CatStructure, LevelWarn, `{"max":4000}`},

	// ---- 命名 ----
	{"naming.table.pattern", "表名命名规范", DialectAll, CatNaming, LevelWarn, ""},
	{"naming.index.prefix", "索引名前缀规范", DialectAll, CatNaming, LevelWarn, `{"indexPrefix":"idx_","uniquePrefix":"uk_"}`},
	{"naming.identifier.length", "标识符长度上限", DialectAll, CatNaming, LevelWarn, ""},
	{"naming.forbid.keyword", "禁止用保留字命名", DialectAll, CatNaming, LevelWarn, ""},

	// ---- 索引 ----
	{"index.max.columns", "单个索引字段数上限", DialectAll, CatIndex, LevelWarn, `{"max":5}`},
	{"index.max.per.table", "单表索引数量上限", DialectAll, CatIndex, LevelInfo, `{"max":5}`},

	// ---- MySQL / TiDB ----
	{"mysql.require.innodb", "存储引擎必须为 InnoDB", DialectMySQL, CatStructure, LevelWarn, `{"engine":"innodb"}`},
	{"mysql.require.utf8mb4", "字符集必须为 utf8mb4", "mysql,tidb", CatStructure, LevelWarn, `{"charset":"utf8mb4"}`},
	{"tidb.forbid.foreign.key", "TiDB 禁止外键约束", DialectTiDB, CatStructure, LevelError, ""},
	{"tidb.avoid.auto.increment", "TiDB 慎用自增主键(写热点)", DialectTiDB, CatStructure, LevelInfo, ""},

	// ---- DWS (GaussDB) ----
	{"dws.require.distribute.by", "DWS 建表必须指定 DISTRIBUTE BY", DialectDWS, CatStructure, LevelError, ""},
	{"dws.prefer.column.store", "DWS 分析型表建议列存", DialectDWS, CatStructure, LevelInfo, `{"orientation":"column"}`},

	// ---- Oracle ----
	{"oracle.prefer.varchar2", "Oracle 应使用 VARCHAR2", DialectOracle, CatStructure, LevelWarn, ""},
	{"oracle.number.precision", "NUMBER 应指定精度", DialectOracle, CatStructure, LevelInfo, ""},

	// ---- 安全 ----
	{"security.forbid.plain.password", "禁止语句中出现明文口令", DialectAll, CatSecurity, LevelError, ""},
	{"security.forbid.grant.all", "禁止 GRANT ALL", DialectAll, CatSecurity, LevelError, ""},
	{"security.forbid.grant.public", "禁止向 PUBLIC 授权", "oracle,dws", CatSecurity, LevelError, ""},

	// ---- 性能 ----
	{"perf.forbid.leading.wildcard", "禁止前置通配符 LIKE", DialectAll, CatPerf, LevelWarn, ""},
	{"perf.forbid.func.on.column", "WHERE 中禁止对列使用函数", DialectAll, CatPerf, LevelWarn, ""},
}
