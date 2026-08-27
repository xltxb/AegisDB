package review

// Builtin describes one shipped rule: what it checks, which dialects it applies
// to, the level it fires at out of the box, and WHERE IN THE COMPANY'S WRITTEN
// STANDARD it comes from.
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
	Spec     string // 规范分级:critical|mandatory|recommended;空 = 规范未覆盖,平台内置
	SpecRef  string // 出处,如 "DWS §5.3 分布鍵";空 = 平台内置
	Params   string // default knobs as JSON; "" means the checker's own defaults
}

// 规范分级 —— 四份规范(MySQL / TiDB / Oracle / Huawei DWS)共用的三级词汇。
//
// 它和 Level 是两件事,刻意分开存:
//
//   - Spec 说的是**规范怎么定性这条要求**。它是引文,不是配置 —— 运维把某条规则降
//     成告警,不等于把公司规范改了,而一个只有 Level 的库会让这两件事看起来一样。
//   - Level 说的是**这条发现在本平台值多少钱**:error 拦住发布,warn 记录并放行,
//     info 只是建议。
//
// 种子按 SpecLevel 映射:高危/强制 → error(规范原文都是"DBA 强制退回 / 需修改后
// 方可上线"),建议 → info。少数几条查得出但查不准的,种子里刻意压低一档并在该条
// 上写明原因 —— 一条老是误报的拦截规则,最后换来的是所有人学会无视整个审查。
const (
	SpecCritical    = "critical"    // 【高危】数据错误 / 不可恢复 / 集群不稳定
	SpecMandatory   = "mandatory"   // 【強制】必须遵守,违反需修改后方可上线
	SpecRecommended = "recommended" // 【建議】最佳实践,例外需在评审说明
	SpecNone        = ""            // 规范未覆盖,平台内置的防护
)

// SpecLevels is the console's display order for the classification filter.
var SpecLevels = []string{SpecCritical, SpecMandatory, SpecRecommended}

// LevelForSpec is the seeding default: what a given classification costs unless
// the entry deliberately says otherwise.
func LevelForSpec(spec string) string {
	switch spec {
	case SpecCritical, SpecMandatory:
		return LevelError
	case SpecRecommended:
		return LevelInfo
	}
	return LevelWarn
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

// Spec document labels, used in SpecRef so a finding can be traced back to the
// paragraph that demands it.
const (
	docMySQL  = "MySQL規範"
	docTiDB   = "TIDB規範"
	docOracle = "Oracle規範"
	docDWS    = "DWS对象设计和业务开发规范"
)

// Builtins is the shipped rule library. Order here is the seeded sort order, so
// related rules stay together in the console.
//
// Rules carrying a SpecRef were derived from the four standards under docs/.
// Rules with an empty Spec predate them and cover ground the standards do not
// write down (SELECT * on MySQL, index-count ceilings, …); they are kept because
// deleting a live guard is not something a document's silence should decide, and
// they are marked so nobody mistakes them for quotations.
var Builtins = []Builtin{
	// ------------------------------------------------------------------ DML
	{"dml.require.where", "UPDATE/DELETE 必须带 WHERE", DialectAll, CatDML, LevelError,
		SpecMandatory, docDWS + " RULE 46", ""},
	{"dws.delete.use.truncate", "全表删除应使用 TRUNCATE 而非无条件 DELETE", DialectDWS, CatDML, LevelError,
		SpecMandatory, docDWS + " RULE 46", ""},
	{"dml.require.limit", "UPDATE/DELETE 建议带 LIMIT 分批", "mysql,tidb", CatDML, LevelWarn,
		SpecNone, "", ""},
	{"dml.insert.require.column", "INSERT 必须显式指定列名", DialectAll, CatDML, LevelWarn,
		SpecNone, "", ""},
	{"dml.forbid.select.star", "禁止 SELECT *", "mysql,tidb,oracle,generic", CatDML, LevelInfo,
		SpecNone, "", ""},
	{"dws.forbid.select.star", "严格禁止 SELECT *,须显式列出字段", DialectDWS, CatDML, LevelError,
		SpecMandatory, docDWS + " RULE 50", ""},
	{"dws.forbid.returning", "禁用 RETURNING(无法下推)", DialectDWS, CatDML, LevelError,
		SpecMandatory, docDWS + " RULE 42 不下推寫法", ""},
	{"dws.forbid.distinct.on", "禁用 DISTINCT ON(无法下推)", DialectDWS, CatDML, LevelError,
		SpecMandatory, docDWS + " RULE 42 不下推寫法", ""},
	{"dws.forbid.orderby.in.aggregate", "聚集函数中禁用 ORDER BY(无法下推)", DialectDWS, CatDML, LevelError,
		SpecMandatory, docDWS + " RULE 42 不下推寫法", ""},
	{"dws.prefer.join.over.exists", "优先使用 JOIN 替代 EXISTS / IN", DialectDWS, CatDML, LevelInfo,
		SpecRecommended, docDWS + " RULE 47", ""},

	// ------------------------------------------------------- DDL 高影响动作
	{"ddl.forbid.drop", "禁止 DROP 表/库/列", DialectAll, CatDDL, LevelError,
		SpecMandatory, docMySQL + " §十一 / " + docTiDB + " §十 / " + docDWS + " RULE 58",
		`{"objects":["table","database","schema","tablespace"],"dropColumn":true}`},
	{"ddl.forbid.truncate", "禁止 TRUNCATE", "mysql,tidb,oracle,generic", CatDDL, LevelError,
		SpecNone, "", ""},
	{"ddl.forbid.rename", "禁止直接 RENAME 线上对象", DialectAll, CatDDL, LevelWarn,
		SpecNone, "", ""},
	{"ddl.alter.merge", "同表多条 ALTER 建议合并", DialectAll, CatDDL, LevelInfo,
		SpecNone, "", `{"max":1}`},
	{"mysql.alter.require.algorithm", "ALTER TABLE 必须显式指定 ALGORITHM/LOCK", DialectMySQL, CatDDL, LevelError,
		SpecMandatory, docMySQL + " §五 Column Online DDL", ""},
	{"oracle.index.require.online", "索引创建/重建建议加 ONLINE", DialectOracle, CatDDL, LevelInfo,
		SpecRecommended, docOracle + " §十 Online DDL", ""},
	{"dws.forbid.create.database", "一个集群只允许一个自定义库,禁止再建库", DialectDWS, CatDDL, LevelError,
		SpecMandatory, docDWS + " RULE 7", ""},
	{"dws.database.charset.utf8", "建库必须指定字符集 UTF8", DialectDWS, CatDDL, LevelError,
		SpecMandatory, docDWS + " RULE 8", ""},
	{"dws.database.dbcompatibility", "建库必须指定 dbcompatibility = MySQL", DialectDWS, CatDDL, LevelError,
		SpecMandatory, docDWS + " RULE 9", ""},
	{"dws.forbid.tablespace", "禁止自定义表空间", DialectDWS, CatDDL, LevelError,
		SpecMandatory, docDWS + " RULE 10", ""},
	{"dws.forbid.trigger", "DWS 不支持触发器", DialectDWS, CatDDL, LevelError,
		SpecCritical, docDWS + " DWS特性與限制", ""},
	{"dws.forbid.udf", "DWS 不支持自定义外部函数(C/Java UDF)", DialectDWS, CatDDL, LevelError,
		SpecCritical, docDWS + " DWS特性與限制", ""},
	{"dws.forbid.unlogged.table", "DWS 不支持 unlogged 表", DialectDWS, CatDDL, LevelError,
		SpecCritical, docDWS + " DWS特性與限制", ""},

	// -------------------------------------------------------------- 表结构
	{"ddl.require.primary.key", "建表必须有主键", DialectAll, CatStructure, LevelError,
		SpecNone, "", ""},
	{"ddl.require.table.comment", "建表必须写表注释", "mysql,tidb,dws", CatStructure, LevelError,
		SpecMandatory, docDWS + " 對象命名規範·註釋", ""},
	{"ddl.require.col.comment", "字段必须写注释", "mysql,tidb", CatStructure, LevelWarn,
		SpecNone, "", ""},
	{"ddl.require.col.not.null", "字段建议 NOT NULL", "mysql,oracle,generic", CatStructure, LevelInfo,
		SpecNone, "", ""},
	{"tidb.require.col.not.null", "所有字段必须 NOT NULL(除非业务明确需要为空)", DialectTiDB, CatStructure, LevelError,
		SpecMandatory, docTiDB + " §五 基本要求", ""},
	{"dws.require.col.not.null", "明确不存在 NULL 值的字段必须加 NOT NULL", DialectDWS, CatStructure, LevelWarn,
		// 规范是【強制】,但"明确不允许为空"是业务判断,语句本身读不出来 —— 这条只能
		// 提醒,拦不了。压成 warn 是为了不让一条必然误报的规则去拦发布。
		SpecMandatory, docDWS + " RULE 24", ""},
	{"ddl.addcol.notnull.nodefault", "新增 NOT NULL 列必须给 DEFAULT", DialectAll, CatStructure, LevelError,
		SpecMandatory, docMySQL + " §五 Column NOT NULL", ""},
	{"ddl.forbid.column.type", "禁用的字段类型", DialectAll, CatStructure, LevelError,
		SpecCritical, docDWS + " DWS特性與限制", ""},
	{"dws.avoid.column.type", "不推荐的字段类型", DialectDWS, CatStructure, LevelInfo,
		SpecRecommended, docDWS + " RULE 23",
		`{"types":["blob","raw","real","float4","double precision","float8","float","smallserial","serial","bigserial","name","json","hll","money","point","lseg","box","path","polygon","circle","inet","cidr","macaddr","tsvector","tsquery"]}`},
	{"ddl.varchar.max.length", "VARCHAR 长度上限", DialectAll, CatStructure, LevelWarn,
		SpecNone, "", `{"max":4000}`},
	{"dws.require.precision", "NUMERIC/DECIMAL 必须指定精度", DialectDWS, CatStructure, LevelError,
		SpecMandatory, docDWS + " RULE 25", `{"types":["numeric","decimal"]}`},
	{"dws.varchar.length", "VARCHAR 必须指定长度,且不超过 6000", DialectDWS, CatStructure, LevelInfo,
		SpecRecommended, docDWS + " RULE 29", `{"max":6000}`},
	{"tidb.forbid.timestamp", "禁用 TIMESTAMP,须用 DATETIME(2038 溢出)", DialectTiDB, CatStructure, LevelError,
		SpecMandatory, docTiDB + " §五 時間欄位與 2038 問題", ""},
	{"tidb.avoid.auto.increment", "主键不得使用 AUTO_INCREMENT,应用 AUTO_RANDOM", DialectTiDB, CatStructure, LevelError,
		SpecMandatory, docTiDB + " §五 主鍵規範", ""},
	{"tidb.forbid.foreign.key", "TiDB 禁止外键约束", DialectTiDB, CatStructure, LevelError,
		SpecNone, "", ""},
	{"tidb.avoid.partition", "TiDB 慎用分区表(8.5 版功能尚不完善)", DialectTiDB, CatStructure, LevelInfo,
		SpecRecommended, docTiDB + " §十一 分庫分表,分區表", ""},
	{"mysql.require.innodb", "存储引擎必须为 InnoDB", DialectMySQL, CatStructure, LevelWarn,
		SpecNone, "", `{"engine":"innodb"}`},
	{"mysql.require.utf8mb4", "字符集必须为 utf8mb4", "mysql,tidb", CatStructure, LevelError,
		SpecMandatory, docMySQL + " §四 建庫範例 / " + docTiDB + " §四 建庫範例", `{"charset":"utf8mb4"}`},
	{"oracle.prefer.varchar2", "Oracle 应使用 VARCHAR2", DialectOracle, CatStructure, LevelWarn,
		SpecNone, "", ""},
	{"oracle.number.precision", "NUMBER 应指定精度", DialectOracle, CatStructure, LevelInfo,
		SpecNone, "", ""},

	// ------------------------------------------------------------ DWS 建表
	{"dws.require.distribute.by", "建表必须指定 DISTRIBUTE BY", DialectDWS, CatStructure, LevelError,
		SpecMandatory, docDWS + " RULE 13", ""},
	{"dws.distribute.key.max", "分布键字段数不超过 3 个", DialectDWS, CatStructure, LevelError,
		SpecMandatory, docDWS + " RULE 13", `{"max":3}`},
	{"dws.distribute.key.type", "分布键类型受限", DialectDWS, CatStructure, LevelError,
		SpecMandatory, docDWS + " RULE 16",
		`{"types":["tinyint","smallint","integer","int","bigint","number","char","varchar","varchar2"]}`},
	{"dws.distribute.key.subset", "分布键必须是主键/唯一键的子集", DialectDWS, CatStructure, LevelError,
		SpecMandatory, docDWS + " RULE 14", ""},
	{"dws.distribute.key.no.uuid.default", "禁止用 UUID 系统函数做分布列默认值", DialectDWS, CatStructure, LevelError,
		SpecMandatory, docDWS + " RULE 17",
		`{"functions":["uuid_generate_v1","uuid_generate_v4","uuid","sys_guid","gen_random_uuid"]}`},
	{"dws.pk.max.columns", "主键不超过 5 个字段", DialectDWS, CatStructure, LevelInfo,
		SpecRecommended, docDWS + " RULE 15", `{"max":5}`},
	{"dws.replication.confirm", "复制表须确认为百万行以下的非事实表", DialectDWS, CatStructure, LevelWarn,
		// 规范是【強制】禁止百万以上的表/维表/事实表设为复制表,但行数与表的角色都不
		// 在语句里 —— 只能在看到 REPLICATION 时要求确认,不能替人判断。
		SpecMandatory, docDWS + " RULE 12", ""},
	{"dws.require.hstore.opt", "建表一律采用列存 hstore_opt 3.0 表", DialectDWS, CatStructure, LevelError,
		SpecMandatory, docDWS + " RULE 11", ""},
	{"dws.partition.single.range", "分区键只能一个字段,且须用 RANGE 分区", DialectDWS, CatStructure, LevelError,
		SpecMandatory, docDWS + " RULE 19", ""},
	{"dws.partition.max", "单表分区数不超过 1000 个", DialectDWS, CatStructure, LevelError,
		SpecMandatory, docDWS + " RULE 20", `{"max":1000}`},
	{"dws.partition.ttl", "分区表须指定 ttl 或有淘汰历史分区的安排", DialectDWS, CatStructure, LevelWarn,
		// 规范是【Must】,但"有没有 drop partition 的安排"是调度层的事,建表语句里
		// 读不出来 —— 只能在没写 ttl 时提醒,不能断言违规。
		SpecMandatory, docDWS + " RULE 21", ""},
	{"dws.view.forbid.orderby", "视图定义中禁止 ORDER BY", DialectDWS, CatStructure, LevelError,
		SpecMandatory, docDWS + " RULE 34", ""},
	{"dws.view.max.nesting", "视图嵌套层数不超过 4 层", DialectDWS, CatStructure, LevelError,
		SpecMandatory, docDWS + " RULE 33", `{"max":4}`},

	// -------------------------------------------------------------- 命名
	{"naming.table.pattern", "表名命名规范", DialectAll, CatNaming, LevelWarn,
		SpecNone, "", ""},
	{"tidb.table.prefix", "表名须以 t_ 为前缀,小写字母与下划线", DialectTiDB, CatNaming, LevelError,
		SpecMandatory, docTiDB + " §五 基本要求", `{"pattern":"^t_[a-z0-9_]*$"}`},
	{"naming.index.prefix", "索引名前缀规范(pk_/uk_/idx_)", "mysql,tidb,dws", CatNaming, LevelError,
		SpecMandatory, docMySQL + " §七 / " + docTiDB + " §七 / " + docDWS + " 對象命名規範·索引",
		`{"pkPrefix":"pk_","uniquePrefix":"uk_","indexPrefix":"idx_"}`},
	{"naming.index.max.length", "索引名不超过 32 个字符", "mysql,tidb", CatNaming, LevelError,
		SpecMandatory, docMySQL + " §七 索引設計規範", `{"max":32}`},
	{"naming.identifier.length", "标识符长度上限", DialectAll, CatNaming, LevelWarn,
		SpecNone, "", ""},
	{"tidb.table.max.length", "表名不超过 32 个字符", DialectTiDB, CatNaming, LevelError,
		SpecMandatory, docTiDB + " §五 基本要求", `{"max":32}`},
	{"naming.forbid.keyword", "禁止用保留字命名", DialectAll, CatNaming, LevelWarn,
		SpecNone, "", ""},
	{"dws.object.name.charset", "对象名只能用小写字母/下划线/数字,且以字母开头", DialectDWS, CatNaming, LevelError,
		SpecMandatory, docDWS + " RULE 2", `{"pattern":"^[a-z][a-z0-9_]*$"}`},
	{"dws.object.name.reserved.prefix", "对象名禁止以 pg / gs / mlog / redis 开头", DialectDWS, CatNaming, LevelError,
		SpecMandatory, docDWS + " RULE 3", `{"prefixes":["pg","gs","mlog","redis"]}`},
	{"dws.object.name.max.length", "对象名不超过 63 字节", DialectDWS, CatNaming, LevelError,
		SpecMandatory, docDWS + " RULE 5", `{"max":63}`},
	{"dws.temp.table.date.suffix", "临时/中间表以 _YYYYMMDD 结尾并明确清理策略", DialectDWS, CatNaming, LevelInfo,
		SpecRecommended, docDWS + " RULE 4", ""},

	// -------------------------------------------------------------- 索引
	{"index.max.columns", "单个索引字段数上限", DialectAll, CatIndex, LevelWarn,
		SpecNone, "", `{"max":5}`},
	{"index.max.per.table", "单表索引数量上限", "mysql,tidb,oracle,generic", CatIndex, LevelInfo,
		SpecNone, "", `{"max":5}`},
	{"dws.index.max.per.table", "单表除主键外索引不超过 3 个", DialectDWS, CatIndex, LevelInfo,
		SpecRecommended, docDWS + " RULE 31", `{"max":3}`},
	{"dws.index.btree.only", "只能使用 Btree/CBtree 索引,禁止 psort 等", DialectDWS, CatIndex, LevelError,
		SpecMandatory, docDWS + " RULE 30", `{"methods":["btree","cbtree"]}`},

	// -------------------------------------------------------------- 安全
	{"security.forbid.plain.password", "禁止语句中出现明文口令", DialectAll, CatSecurity, LevelError,
		SpecNone, "", ""},
	{"security.forbid.grant.all", "禁止 GRANT ALL", DialectAll, CatSecurity, LevelError,
		SpecNone, "", ""},
	{"security.forbid.grant.public", "禁止向 PUBLIC 授权", "oracle,dws", CatSecurity, LevelError,
		SpecNone, "", ""},
	// 新版规范删掉了"禁止存储敏感字段"这一条。规则保留(删掉一道在跑的防护,不该由
	// 一份文档的沉默来决定),但退回"平台内置" —— 免得有人拿它当规范原文去引用。
	{"security.forbid.sensitive.column", "禁止存储敏感字段", DialectAll, CatSecurity, LevelError,
		SpecNone, "",
		`{"patterns":["id_?card","identity_?no","passport_?no","bank_?card","card_?no","cvv","credit_?card","social_?security","ssn"]}`},

	// -------------------------------------------------------------- 性能
	{"perf.forbid.leading.wildcard", "禁止前置通配符 LIKE", DialectAll, CatPerf, LevelWarn,
		SpecNone, "", ""},
	{"perf.forbid.func.on.column", "WHERE 中禁止对列使用函数", "mysql,tidb,oracle,generic", CatPerf, LevelWarn,
		SpecNone, "", ""},
	{"dws.forbid.func.on.column", "索引列/分区列做过滤时禁止表达式或类型转换", DialectDWS, CatPerf, LevelError,
		SpecMandatory, docDWS + " RULE 38 / RULE 39", ""},
	{"dws.forbid.not.in.subquery", "禁用 NOT IN (子查询),改写为 NOT EXISTS", DialectDWS, CatPerf, LevelError,
		SpecMandatory, docDWS + " RULE 43", ""},
	{"dws.forbid.scalar.subquery", "禁用标量子查询,改为 LEFT JOIN + 聚合", DialectDWS, CatPerf, LevelError,
		SpecMandatory, docDWS + " RULE 44", ""},
	{"dws.subquery.forbid.orderby", "子查询内禁止 ORDER BY", DialectDWS, CatPerf, LevelError,
		SpecMandatory, docDWS + " RULE 45", ""},
	{"dws.forbid.volatile.in.subquery", "子查询中禁用不稳定函数(nextval/uuid 等)", DialectDWS, CatPerf, LevelError,
		SpecMandatory, docDWS + " RULE 42 不下推寫法",
		`{"functions":["nextval","uuid_generate_v1","uuid","sys_guid","gen_random_uuid","random","clock_timestamp"]}`},
	{"dws.forbid.with.recursive", "禁用 WITH RECURSIVE", DialectDWS, CatPerf, LevelError,
		SpecMandatory, docDWS + " RULE 42 不下推寫法", ""},
	{"dws.require.schema.qualified", "所有对象引用应带 schema", DialectDWS, CatPerf, LevelInfo,
		SpecRecommended, docDWS + " RULE 51", ""},
	{"dws.max.join.tables", "单条 SQL 关联的表不超过 8 张(B端)", DialectDWS, CatPerf, LevelInfo,
		SpecRecommended, docDWS + " RULE 52", `{"max":8}`},
	{"dws.require.sql.tag", "SQL 开头建议加注释标识系统/模块/任务", DialectDWS, CatPerf, LevelInfo,
		SpecRecommended, docDWS + " RULE 56", ""},
	{"dws.forbid.query.dop", "禁止在应用中显式开启并行参数 query_dop", DialectDWS, CatPerf, LevelError,
		SpecMandatory, docDWS + " RULE 54", ""},
}
