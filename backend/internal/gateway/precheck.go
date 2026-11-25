package gateway

// 发布收单时的语法校验:**让数据库自己说**。
//
// ## 为什么不是自己解析
//
// ADR 0008 的立场:网关不做完整 SQL 解析,也不该做 —— 一个半吊子的解析器会在每个引擎
// 的方言上出新的错,而"这条语句语法对不对"恰恰是**唯一不必靠平台判断**的问题:把它交给
// 目标库,它自己会说,而且说的是那台库、那个版本的真实答案。
//
// ## 只对 DML,而且只用 EXPLAIN
//
// EXPLAIN(不带 ANALYZE)只生成计划,不执行语句 —— 它同时校验了语法**和**引用的对象,
// 因为生成计划就必须解析出表和列。
//
//	SELECT / INSERT / UPDATE / DELETE   EXPLAIN,由目标库定夺
//	其它(DDL、存储过程、会话设置 …)      不检查,并如实说明
//
// 动词限定在这四个上是有意的:它们在 MySQL 5.6+ / PostgreSQL / SQLite 上都支持
// EXPLAIN。范围再宽一点,就会遇到"这个引擎的 EXPLAIN 不支持这种语句"的报错,而那句
// 报错和"你的语句语法错了"在文本上分不开 —— 把前者报成后者,就是拿一个假的语法错误
// 去拦一条正确的变更。
//
// ## DDL 为什么不做
//
// MySQL 与 Oracle 的 DDL 是隐式提交的:任何"试一下"都等于真的执行。PostgreSQL 的 DDL
// 可以在事务里回滚,但那会对目标表加 ACCESS EXCLUSIVE 锁 —— 在繁忙的生产表上,哪怕
// 最后回滚,也可能短暂阻塞线上查询。
//
// 所以 DDL 一律不做语法检查,并在结果里**明确标注没做**。把没做的检查报成通过,比不
// 检查糟得多:它让人以为这条 DDL 已经被验过了。

import (
	"context"
	"strings"
	"time"

	"aegisdb/internal/apimsg"
	"aegisdb/internal/model"
)

// precheckTimeout 一条语句的校验上限。EXPLAIN 只生成计划,慢通常意味着目标库本身
// 有压力 —— 这时更不该让收单一直等着。
const precheckTimeout = 10 * time.Second

// ErrSyntaxCheckUnsupported 说明这条语句(或这个引擎)没法在不执行的前提下校验。
// 它不是"语法没问题",调用方必须把它如实转述成"未检查"。
var ErrSyntaxCheckUnsupported = apimsg.Errorf("该语句无法在不执行的前提下校验语法")

// explainableVerbs 是可以安全 EXPLAIN 的动词。理由见文件顶部 —— 这个集合窄是故意的。
var explainableVerbs = map[string]bool{
	"SELECT": true, "INSERT": true, "UPDATE": true, "DELETE": true,
}

// SyntaxCheckable 说明这条语句会不会被真正校验。
//
// 调用方拿它来区分"检查通过"和"没检查" —— 这两件事在结果里必须分得开。
func SyntaxCheckable(engine, sql string) bool {
	switch engineFamily(engine) {
	case familyMySQL, familyPostgres, familySQLite:
	default:
		return false // Oracle 的 EXPLAIN PLAN 要写 PLAN_TABLE;Mongo 不是 SQL
	}
	// 已经是 EXPLAIN 的语句不再包一层:EXPLAIN EXPLAIN 在哪个引擎上都不合法,
	// 而且它本来就不会改数据。
	if strings.EqualFold(firstWord(strings.TrimSpace(StripComments(sql))), "EXPLAIN") {
		return false
	}
	return explainableVerbs[ParseVerb(sql)]
}

// CheckSyntax 让目标库校验一条语句。nil = 目标库认为它没问题。
//
// 返回的错误里带的是**数据库原话**:它会指出第几行第几个字符,那是这里唯一有用的
// 信息。包装成"语法错误"会把它丢掉。
func CheckSyntax(conn *model.Connection, sql string) error {
	if !RealExecSupported(conn) {
		return apimsg.Errorf("实例未配置真实执行凭据,无法校验语法")
	}
	if !SyntaxCheckable(conn.Engine, sql) {
		return ErrSyntaxCheckUnsupported
	}
	db, release, err := openConn(conn)
	if err != nil {
		return err
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), precheckTimeout)
	defer cancel()

	// 不带 ANALYZE。带了就会真的执行 —— 在 PostgreSQL 上 `EXPLAIN ANALYZE DELETE …`
	// 是真的删(见 PlanOnly 的注释)。这里拼的前缀是固定字面量,不取用户输入。
	stmt := "EXPLAIN " + strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(sql), ";"))
	rows, err := db.QueryContext(ctx, stmt)
	if err != nil {
		return err
	}
	// 计划本身不要,要的只是"能不能生成出来"。但必须把行读完/关掉,
	// 否则连接会留在有未读结果的状态上。
	defer rows.Close()
	for rows.Next() {
	}
	return rows.Err()
}

// LiveTableNames 取目标库当前的表/视图清单,用于对象检查的实时兜底。
//
// 一次查询取回整份清单,而不是"一个对象问一次":一份 200 条语句的发布脚本会引用几十
// 个对象,逐个去问就是几十次往返,而收单是同步的 —— 那几十次往返都加在提单方的等待上。
//
// 返回的键有两种写法,调用方两种都能查:
//
//	"schema.table"  限定名
//	"table"         不限定(同名对象在多个 schema 下时,任一个存在就算存在)
//
// 全部小写。大小写敏感性各引擎不同(MySQL 看平台、PG 折成小写、Oracle 折成大写),
// 在这里统一折成小写会**放宽**判断 —— 而这个方向是对的:对象检查宁可漏报,不可误报。
func LiveTableNames(conn *model.Connection) (map[string]bool, error) {
	if !RealExecSupported(conn) {
		return nil, apimsg.Errorf("实例未配置真实执行凭据,无法核对对象")
	}
	query := liveTableQuery(conn)
	if query == "" {
		return nil, apimsg.Errorf("引擎 %q 暂不支持对象核对", conn.Engine)
	}
	db, release, err := openConn(conn)
	if err != nil {
		return nil, err
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), precheckTimeout)
	defer cancel()

	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var schema, name string
		if err := rows.Scan(&schema, &name); err != nil {
			return nil, err
		}
		AddTableKey(out, schema, name)
		if len(out) >= 2*maxSchemaRows {
			break
		}
	}
	return out, rows.Err()
}

// AddTableKey 按 LiveTableNames 的约定登记一个对象(限定名 + 裸名,全小写)。
// 元数据缓存那一侧也用它,两边的键必须是同一套 —— 不然"缓存里有"和"实时有"会
// 在同一个名字上给出不同答案。
func AddTableKey(set map[string]bool, schema, name string) {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" {
		return
	}
	set[n] = true
	if s := strings.ToLower(strings.TrimSpace(schema)); s != "" {
		set[s+"."+n] = true
	}
}

// EngineFamilyOf 把引擎标签折成家族名,给包外的调用方说明"为什么这条没检查"。
// 家族判定只有一处(engineFamily),不另立一套 —— 两套迟早分叉,而分叉的表现是
// 同一台实例在判定和说明里被归成不同的引擎。
func EngineFamilyOf(engine string) string { return engineFamily(engine) }

func liveTableQuery(conn *model.Connection) string {
	switch engineFamily(conn.Engine) {
	case familyMySQL:
		// 连接上有库就只看那个库;没有就看全部用户库 —— 与元数据探查同一个口径。
		if strings.TrimSpace(conn.Database) != "" {
			return `SELECT table_schema, table_name FROM information_schema.tables
			        WHERE table_schema = DATABASE()`
		}
		return `SELECT table_schema, table_name FROM information_schema.tables
		        WHERE table_schema NOT IN ` + mysqlSystemSchemas
	case familyPostgres:
		return `SELECT n.nspname, c.relname
		        FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		        WHERE c.relkind IN ('r','p','v','m')
		          AND n.nspname NOT IN ('pg_catalog','information_schema')
		          AND n.nspname NOT LIKE 'pg_toast%'`
	case familyOracle:
		return `SELECT owner, table_name FROM all_tables
		        UNION ALL SELECT owner, view_name FROM all_views`
	case familySQLite:
		return `SELECT '', name FROM sqlite_master WHERE type IN ('table','view')
		        AND name NOT LIKE 'sqlite_%'`
	}
	return ""
}
