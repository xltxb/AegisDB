package gateway

// 业务方数据查询的执行层(见 .scratch/biz-data-query/PRD.md 第 5.2~5.4 节)。
//
// 业务方拿到的是一条**只读、按库收窄、有行数上限、有执行时限**的查询通道。这里有四道,
// 任何一道不过都不执行:
//
//  1. BizReadOnlyCheck —— 只接受单条 SELECT / WITH…SELECT / EXPLAIN / DESC 表 / SHOW 表结构
//     (PG 系 / Oracle 只认 SHOW CREATE TABLE,由网关渲染,见 bizquery_showddl.go)
//     (bizquery_forms.go),拒绝带锁、带 INTO、带副作用函数的写法。
//  2. 同一个函数里:语句里出现的每个 `前缀.名字`,前缀不能是这台实例上**别的库**或系统库。
//  3. ApplyRowLimit —— 在最外层补 LIMIT;RealBizQuery 读取时再截断一次兜底。
//  4. RealBizQuery —— 在**只读事务**里执行,并让库端自己按时限停下(MySQL 系 max_execution_time、
//     PostgreSQL statement_timeout);context 超时后 MySQL 系另发 KILL QUERY(killquery.go)。
//     Oracle 的自治事务函数不受只读事务约束,执行前另查一遍依赖(bizquery_hidden.go)。
//
// 只读事务是解析之外的那道闸:网关的关键字判断拦不住所有写法(存储函数里的写、方言特有语法),
// 但只读事务里任何写操作都会被**数据库本身**拒绝。再往外还有库端的只读账号(业务查询源的约定)。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"aegisdb/internal/apimsg"
	"aegisdb/internal/model"
	"aegisdb/pkg/sqlutil"
)

// 带锁 / 写出 / 建表的写法:SELECT … FOR UPDATE、LOCK IN SHARE MODE、SELECT … INTO(OUTFILE、
// DUMPFILE、@变量,以及 PostgreSQL 的 SELECT INTO 新表)。
var bizLockRe = regexp.MustCompile(`(?i)\bFOR\s+(?:UPDATE|SHARE|NO\s+KEY\s+UPDATE|KEY\s+SHARE)\b|\bLOCK\s+IN\s+SHARE\s+MODE\b`)
var bizIntoRe = regexp.MustCompile(`(?i)\bINTO\b`)

// 读文件、睡眠 / 压测、加锁、改序列、跨库连接、Oracle 的工具包 —— 在只读查询里都不该出现。
// 2026-10 审查补:PostgreSQL 咨询锁(会话级的锁随连接回到池里还持着)、pg_notify(发通知),
// MySQL 等复制位点的几个函数(一等就是很久)。
var bizFuncRe = regexp.MustCompile(`(?i)\b(LOAD_FILE|SLEEP|BENCHMARK|GET_LOCK|RELEASE_LOCK|RELEASE_ALL_LOCKS|` +
	`MASTER_POS_WAIT|SOURCE_POS_WAIT|WAIT_FOR_EXECUTED_GTID_SET|WAIT_UNTIL_SQL_THREAD_AFTER_GTIDS|` +
	`PG_ADVISORY\w*|PG_TRY_ADVISORY\w*|PG_NOTIFY|` +
	`PG_SLEEP\w*|PG_READ_FILE|PG_READ_BINARY_FILE|PG_LS_DIR|PG_STAT_FILE|LO_IMPORT|LO_EXPORT|LO_GET|DBLINK\w*|` +
	`PG_TERMINATE_BACKEND|PG_CANCEL_BACKEND|PG_RELOAD_CONF|SET_CONFIG|SETVAL|NEXTVAL|` +
	// 2026-10-29:把字符串当 SQL 执行的 PG 函数 —— ts_stat('select pg_terminate_backend(…)…') 里的
	// 函数名在字符串里,上面的清单看不见,只读事务也拦不住踢会话;*_to_xml 还能把系统目录整个读出来。
	`TS_STAT|TS_REWRITE|(?:QUERY|TABLE|SCHEMA|DATABASE|CURSOR)_TO_XML(?:SCHEMA|_AND_XMLSCHEMA)?|` +
	// Oracle 的 URI 类型:HTTPURITYPE('http://…').getclob() 从库里发 HTTP 请求。
	`HTTPURITYPE|DBURITYPE|XDBURITYPE)\s*\(` +
	// Oracle 的包调用写作 包名.过程(…) 或 包名.函数 —— 包名后面是点,不是括号。
	`|\b(UTL_\w+|DBMS_\w+|OWA_\w+|HTP|HTF|HTTPURITYPE|DBURITYPE|XDBURITYPE|URIFACTORY)\s*\.`)

// 标识符:反引号 / 双引号包着的,或裸的(\pL 开头,支持中文)。
const bizIdent = "(`[^`]+`|\"[^\"]+\"|[\\pL_][\\pL\\pN_$#]*)"

var bizQualifierRe = regexp.MustCompile(bizIdent + `\s*\.\s*(?:` + "`[^`]+`|\"[^\"]+\"|[\\pL_][\\pL\\pN_$#]*|\\*" + `)`)

// 各引擎的系统库 / 系统 schema:业务方不该查它们(账号、权限、会话、别人的连接)。
var bizSystemSchemas = map[string][]string{
	familyMySQL:  {"information_schema", "mysql", "performance_schema", "sys", "metrics_schema"},
	familyOracle: {"sys", "system", "audsys", "ctxsys", "mdsys", "xdb", "outln", "dbsnmp"},
}

// BizReadOnlyCheck 判一段 SQL 能否作为业务查询执行。能执行返回 nil,否则返回拒绝原因(code + 参数,
// 规范中文串见 model.RenderBizReason)。
//
// grantedDB 是这次查询所在的库;instanceDBs 是这台实例上全部的库(可授权的库列表),用来识别
// "引用了别的库"。PostgreSQL 系跨库查询本身做不到(限定符是 schema,不是库),不做库名检查。
func BizReadOnlyCheck(engine, query, grantedDB string, instanceDBs []string) *model.RuleRef {
	stmts := sqlutil.SplitStatements(query)
	if len(stmts) == 0 {
		return model.NewRuleRef(model.BizEmpty)
	}
	if len(stmts) > 1 {
		return model.NewRuleRef(model.BizMulti, "n", model.Itoa(len(stmts)))
	}
	st := stmts[0]
	// `#` 注释的读法也要过一遍:SELECT 1 #'<换行>, (SELECT … FROM mysql.user) #' 在标准词法里
	// 是字符串,在 MySQL 眼里是子查询(见 hash_comment.go)。
	if engineFamily(engine) == familyMySQL {
		for _, alt := range mysqlHashReadings(st) {
			if r := BizReadOnlyCheck(engine, alt, grantedDB, instanceDBs); r != nil {
				return r
			}
		}
	}
	verb := ParseVerb(st)
	plan := PlanOnly(st)
	// Oracle 的 EXPLAIN PLAN 要写 PLAN_TABLE,在只读事务里跑不了;执行层代为完成
	// (见 oracleExplainPlan),这里只认最朴素的一种写法,括号里必须是只读查询 —— 那一步
	// 不在只读事务里,判定不能再放宽。
	if engineFamily(engine) == familyOracle && strings.EqualFold(firstWord(StripComments(st)), "EXPLAIN") {
		inner, ok := OracleExplainInner(st)
		if !ok || ParseVerb(inner) != "SELECT" || !IsRead(inner) {
			return model.NewRuleRef(model.BizExplainForm)
		}
	}
	isShow := false
	switch {
	case plan:
		// EXPLAIN(不带 ANALYZE)只出计划,什么都不执行
	case verb == "SELECT" && IsRead(st):
	case (verb == "DESC" || verb == "DESCRIBE") && engineFamily(engine) == familyMySQL:
		if !bizMySQLDescRe.MatchString(bizStmtText(st)) && !bizForConnectionRe.MatchString(blankQuoted(bizStmtText(st))) {
			return model.NewRuleRef(model.BizDescForm)
		}
	case (verb == "DESC" || verb == "DESCRIBE") && engineFamily(engine) == familyOracle:
		if _, _, ok := OracleBizDescribe(st); !ok {
			return model.NewRuleRef(model.BizDescForm)
		}
	case verb == "SHOW" && engineFamily(engine) == familyMySQL:
		isShow = true // 白名单与库名在下面、"别的库"清单就绪之后判
	case verb == "SHOW" && (engineFamily(engine) == familyPostgres || engineFamily(engine) == familyOracle):
		// PG 系 / Oracle 没有 SHOW CREATE TABLE,由执行层代为渲染(bizquery_showddl.go)。
		// 只认这一种;schema 限定符照下面 SELECT 的规矩查(系统目录、别的 schema)。
		if _, _, ok := BizShowCreateTable(engine, st); !ok {
			return model.NewRuleRef(model.BizShowForm)
		}
	default:
		shown := verb
		if shown == "" {
			shown = "(无法识别)"
		}
		return model.NewRuleRef(model.BizNotSelect, "verb", shown)
	}

	// 下面的判断都先抹字面量再看结构。字面量的边界若取决于目标库认不认 \' 转义,"哪些是数据"
	// 就说不准 —— 这里不猜,直接拒绝(见 backslash.go)。
	if BackslashAmbiguous(st) {
		return model.NewRuleRef(model.BizBackslash)
	}

	fam := engineFamily(engine)
	if r := bizVariantCheck(fam, st); r != nil {
		return r
	}
	if r := ReadSideEffect(st); r != nil {
		return r
	}
	body := StripComments(st)
	if r := bizInfoLeak(fam, body); r != nil {
		return r
	}
	if fam == familyPostgres {
		return bizPGCatalog(body, grantedDB)
	}
	if fam != familyMySQL && fam != familyOracle {
		return nil
	}
	blocked := map[string]string{}
	for _, s := range bizSystemSchemas[fam] {
		blocked[strings.ToLower(s)] = model.BizSystemDB
	}
	for _, d := range instanceDBs {
		switch {
		case !strings.EqualFold(d, grantedDB):
			blocked[strings.ToLower(d)] = model.BizOtherDB
		case d != grantedDB:
			// 只差大小写、却在库列表里单独出现:这台库区分大小写,ORDERS 与 orders 是两个库
			// (2026-11 审计 V-12)。按原样比较,不能把授权的那个也一起挡了。
			blocked[exactDBKey(d)] = model.BizOtherDB
		}
	}
	if isShow {
		if r := bizShowCheck(st, blocked, grantedDB); r != nil {
			return r
		}
	}
	// 表名在反引号 / 双引号里的写法要认得:只抹单引号字符串,标识符保留原样。
	ids := blankStringLiterals(body)
	for _, m := range bizQualifierRe.FindAllStringSubmatch(ids, -1) {
		name := strings.Trim(m[1], "`\"")
		if code, bad := bizBlockedDB(blocked, name); bad {
			return model.NewRuleRef(code, "name", name, "db", grantedDB)
		}
	}
	return nil
}

// exactDBKey:blocked 里要按原样比较的库名(其余按小写比较)。= 不会出现在小写库名的开头。
func exactDBKey(name string) string { return "=" + name }

func bizBlockedDB(blocked map[string]string, name string) (string, bool) {
	if code, bad := blocked[exactDBKey(name)]; bad {
		return code, true
	}
	code, bad := blocked[strings.ToLower(name)]
	return code, bad
}

// 最外层是否已有行数限制:LIMIT、FETCH FIRST/NEXT、OFFSET(与 FETCH 配对)。
var bizLimitWordRe = regexp.MustCompile(`(?i)\bLIMIT\b|\bFETCH\s+(?:FIRST|NEXT)\b`)

// 最外层限制子句能认的写法(从 LIMIT / FETCH 起到语句末尾):
//
//	LIMIT n | LIMIT ALL | LIMIT off, n | LIMIT n OFFSET off [ROW|ROWS]
//	FETCH FIRST|NEXT [n] ROW|ROWS ONLY|WITH TIES
//
// 行数所在的子匹配:LIMIT 的第 1 组(或 MySQL 逗号写法的第 3 组)、FETCH 的第 1 组。
var (
	bizLimitClauseRe = regexp.MustCompile(`(?is)^LIMIT\s+(ALL|\d+)(\s*,\s*(\d+))?(\s+OFFSET\s+\d+(\s+ROWS?)?)?\s*$`)
	bizFetchClauseRe = regexp.MustCompile(`(?is)^FETCH\s+(?:FIRST|NEXT)\s+(\d+\s+)?ROWS?\s+(?:ONLY|WITH\s+TIES)\s*$`)
)

// ApplyRowLimit 让查询的最外层行数不超过 n(MySQL / TiDB / PostgreSQL 系)。
// 返回改写后的语句;第二个返回值非 nil 时表示不能执行、以及原因。
//
//   - 最外层没有限制:末尾补 LIMIT n(先换一行,免得落进末尾那行 -- 注释里)。
//   - 最外层已有 LIMIT / FETCH FIRST:更小的保留;更大的(含 LIMIT ALL)改成 n —— 只靠读取时
//     截断不够,MySQL 驱动关闭结果集时会把剩下的行读完,库端照样跑完整条语句。
//   - 限制写的不是数字(变量、表达式、子查询):网关确认不了它有多大,不执行。
//
// Oracle 不改写:FETCH FIRST 要 12c 以上,11g 会直接报语法错误;它靠读取截断与执行时限。
// 不采用 SELECT * FROM (原语句) LIMIT n:MySQL 会丢掉子查询里的 ORDER BY,结果顺序不再可信。
func ApplyRowLimit(engine, query string, n int) (string, *model.RuleRef) {
	if n <= 0 {
		return query, nil
	}
	fam := engineFamily(engine)
	if fam != familyMySQL && fam != familyPostgres {
		return query, nil
	}
	st := strings.TrimSpace(query)
	for strings.HasSuffix(st, ";") {
		st = strings.TrimSpace(strings.TrimSuffix(st, ";"))
	}
	if ParseVerb(st) != "SELECT" || PlanOnly(st) || strings.EqualFold(firstWord(StripComments(st)), "EXPLAIN") {
		return query, nil
	}
	// 在去掉注释的文本上定位与改写:blankQuoted 不改长度,两者的下标一一对应。
	clean := strings.TrimSpace(StripComments(st))
	masked := blankQuoted(clean)
	for _, loc := range bizLimitWordRe.FindAllStringIndex(masked, -1) {
		if parenDepth(masked[:loc[0]]) != 0 {
			continue // 子查询里的限制不算
		}
		tail := masked[loc[0]:]
		var cnt []int // 行数在 tail 里的位置;nil 表示 FETCH 省略了行数(= 1 行)
		if m := bizLimitClauseRe.FindStringSubmatchIndex(tail); m != nil {
			cnt = m[2:4]
			if m[6] >= 0 { // LIMIT off, n
				cnt = m[6:8]
			}
		} else if m := bizFetchClauseRe.FindStringSubmatchIndex(tail); m != nil {
			if m[2] < 0 {
				return clean, nil
			}
			cnt = []int{m[2], m[2] + len(strings.TrimSpace(tail[m[2]:m[3]]))}
		} else {
			return "", model.NewRuleRef(model.BizLimitNotNumber)
		}
		s, e := loc[0]+cnt[0], loc[0]+cnt[1]
		if v, err := strconv.Atoi(clean[s:e]); err == nil && v <= n {
			return clean, nil // 原有的限制更小,保留
		}
		return clean[:s] + strconv.Itoa(n) + clean[e:], nil
	}
	return fmt.Sprintf("%s\nLIMIT %d", st, n), nil
}

// CrossDatabaseChecked 报告 BizReadOnlyCheck 是否对这个引擎做"别的库"检查(MySQL 系、Oracle)。
func CrossDatabaseChecked(engine string) bool {
	f := engineFamily(engine)
	return f == familyMySQL || f == familyOracle
}

// RealDatabaseNames 现查实例上**这个账号看得到的全部库**,给 BizReadOnlyCheck 识别"别的库"用。
//
// 不能用元数据缓存 / RealSchema:它们从 information_schema.tables 汇总,没有表的库不在里面,
// 表多了还会被 maxSchemaRows 截断 —— 漏掉的库就不算"别的库",跨库检查对它失效。这里查的是
// 库本身(MySQL 系 SCHEMATA、Oracle 的 ALL_USERS),账号看不到的库它本来也查不了。
//
// PostgreSQL 系不做跨库检查,返回 nil。断连重试的入口 RealDatabaseNames 在 brokenconn.go。
func realDatabaseNamesOnce(conn *model.Connection) ([]string, error) {
	var q string
	switch engineFamily(conn.Engine) {
	case familyMySQL:
		q = "SELECT schema_name FROM information_schema.schemata"
	case familyOracle:
		q = "SELECT username FROM all_users"
	default:
		return nil, nil
	}
	target := *conn
	if engineFamily(conn.Engine) == familyMySQL {
		target.Database = "" // 不绑库:目标库不存在也不该让这一步失败
	}
	db, release, err := openConn(&target)
	if err != nil {
		return nil, err
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// BizQueryResult 是一次业务查询的结果。
type BizQueryResult struct {
	Columns       []string
	Data          [][]string
	Truncated     bool
	MaskedColumns []string
}

// oracleExplainRe 认 Oracle 的 `EXPLAIN PLAN FOR <语句>`。SET STATEMENT_ID / INTO 不认:
// statement_id 由网关生成,计划表只用默认的 PLAN_TABLE。
var oracleExplainRe = regexp.MustCompile(`(?is)^\s*EXPLAIN\s+PLAN\s+FOR\s+(.+?)[\s;]*$`)

// OracleExplainInner 从 `EXPLAIN PLAN FOR <语句>` 里取出被解释的那条语句。
func OracleExplainInner(query string) (string, bool) {
	m := oracleExplainRe.FindStringSubmatch(strings.TrimSpace(StripComments(query)))
	if m == nil || strings.TrimSpace(m[1]) == "" {
		return "", false
	}
	return strings.TrimSpace(m[1]), true
}

// oracleExplainPlan 代业务方完成 Oracle 的 EXPLAIN PLAN,返回 DBMS_XPLAN 的输出。
//
// EXPLAIN PLAN 只解析、不执行被解释的语句,但它要往 PLAN_TABLE 写计划 —— 在只读事务里
// 报 ORA-01456;看计划又要调 DBMS_XPLAN,而判定层不让业务方调任何 DBMS_ 包。所以由网关在
// 同一个会话里做完三步:用自己生成的 statement_id 写计划、用 DBMS_XPLAN.DISPLAY 读出来、
// 回滚。PLAN_TABLE 里什么都不留,被解释的语句一行数据都不碰。判定层已保证括号里是只读查询。
func oracleExplainPlan(ctx context.Context, sc *sql.Conn, conn *model.Connection, inner string) (*sql.Tx, *sql.Rows, error) {
	tx, err := sc.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	if schema := oracleTargetSchema(conn); schema != "" {
		if _, err := tx.ExecContext(ctx, `ALTER SESSION SET CURRENT_SCHEMA = "`+schema+`"`); err != nil {
			_ = tx.Rollback()
			return nil, nil, apimsg.Errorf("切换 schema 到 %s 失败: %w", schema, err)
		}
	}
	id := fmt.Sprintf("AEGISDB%d", time.Now().UnixNano())
	if _, err := tx.ExecContext(ctx, "EXPLAIN PLAN SET STATEMENT_ID = '"+id+"' FOR "+inner); err != nil {
		_ = tx.Rollback()
		return nil, nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT plan_table_output FROM TABLE(DBMS_XPLAN.DISPLAY('PLAN_TABLE', :1, 'TYPICAL'))`, id)
	if err != nil {
		_ = tx.Rollback()
		return nil, nil, err
	}
	return tx, rows, nil
}

// RealBizQuery 在只读事务里执行一条已经通过 BizReadOnlyCheck 的查询,最多读 maxRows 行。
// 报错里的敏感值按与打码同一套规则抹掉(H5,见 sensitive_error.go)。
func RealBizQuery(ctx context.Context, conn *model.Connection, query string, maxRows int, timeout time.Duration) (BizQueryResult, error) {
	if schema, name, ok := BizShowCreateTable(conn.Engine, query); ok {
		out, err := bizShowCreate(ctx, conn, schema, name)
		return out, maskErrorText(query, err, append(append([]SensitiveRule{}, activeSensitiveRules(ctx)...), bizCredentialColumns...))
	}
	out, err := runBizQueryUnmasked(ctx, conn, query, maxRows, timeout)
	var refused *BizRefused
	if errors.As(err, &refused) {
		return out, refused
	}
	return out, maskErrorText(query, err, append(append([]SensitiveRule{}, activeSensitiveRules(ctx)...), bizCredentialColumns...))
}

func runBizQueryUnmasked(ctx context.Context, conn *model.Connection, query string, maxRows int, timeout time.Duration) (BizQueryResult, error) {
	var out BizQueryResult
	db, release, err := openConn(conn)
	if err != nil {
		return out, err
	}
	defer release()
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// 池里的空闲连接被中间设备断掉(见 brokenconn.go):断在用户的 SQL 发出之前(取线程号、设时限、
	// 开事务),换新连接再来一次;已经发出去的不重跑。
	var sent bool
	out, err = realBizQuery(ctx, db, conn, query, maxRows, timeout, &sent)
	if isBrokenConn(err) && !sent && ctx.Err() == nil {
		slog.Warn("数据查询准备阶段连接被对端断开,换新连接重试一次", "conn", connLabel(conn), "err", err)
		dropIdleConns(conn)
		out, err = realBizQuery(ctx, db, conn, query, maxRows, timeout, &sent)
	}
	if err != nil && ctx.Err() != nil && !errors.Is(err, ctx.Err()) {
		err = fmt.Errorf("%w (%v)", ctx.Err(), err)
	}
	return out, err
}

// sent 在用户的 SQL 发往库端之前置为 true。
func realBizQuery(ctx context.Context, db *sql.DB, conn *model.Connection, query string, maxRows int, timeout time.Duration, sent *bool) (BizQueryResult, error) {
	var out BizQueryResult
	fam := engineFamily(conn.Engine)

	// 独占一条连接:会话变量、只读事务、KILL 看守都得落在同一个物理会话上。
	var sc *sql.Conn
	var stop = func() {}
	if killable(conn) {
		r, pinned, st, err := pinForKill(ctx, db, conn)
		if err != nil {
			return out, err
		}
		_ = r
		sc, stop = pinned, st
	} else {
		pinned, err := db.Conn(ctx)
		if err != nil {
			return out, err
		}
		sc = pinned
	}
	defer sc.Close()
	defer stop() // 先于 sc.Close:等进行中的 KILL 做完再归还连接
	// 被超时 / 取消打断的连接不还回池子,直接丢掉。go-ora 取消时发 break,库端回的
	// ORA-01013 会留在这条连接上,落到**下一个**借到它的人头上 —— 可能是别人的终端语句,
	// 第一条就莫名其妙地报"用户请求取消"。MySQL 系这条连接已被 KILL / 断开,本来也不能再用。
	// 先于 stop() 与 sc.Close() 执行(defer 后进先出)。
	defer func() {
		if ctx.Err() != nil {
			_ = sc.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()
	// Oracle 在下面的事务里切了 schema:ALTER SESSION 不随事务回滚,归还前要切回来(见 oracleSchemaRestorer)。
	// 原 schema 在这里、事务开始之前读 —— 事务里第一条必须是 SET TRANSACTION READ ONLY。
	// 后注册先执行:在事务回滚之后、上面那道"丢弃被打断的连接"之前。
	if fam == familyOracle && oracleTargetSchema(conn) != "" {
		defer oracleSchemaRestorer(ctx, sc)()
	}

	ms := timeout.Milliseconds()
	switch fam {
	case familyMySQL:
		// 库端时限:max_execution_time 只作用于只读 SELECT,正好是这里的全部。执行完恢复原值,
		// 连接回到池子里不带着这次的设置。MariaDB 的变量叫 max_statement_time(秒)。
		name, val := "max_execution_time", fmt.Sprint(ms)
		if strings.Contains(strings.ToLower(conn.Engine), "mariadb") {
			name, val = "max_statement_time", fmt.Sprintf("%.3f", timeout.Seconds())
		}
		var orig string
		if err := sc.QueryRowContext(ctx, "SELECT @@SESSION."+name).Scan(&orig); err == nil {
			if _, err := sc.ExecContext(ctx, "SET SESSION "+name+" = "+val); err == nil {
				defer func() {
					rctx, rc := context.WithTimeout(context.Background(), 3*time.Second)
					defer rc()
					_, _ = sc.ExecContext(rctx, "SET SESSION "+name+" = "+orig)
				}()
			}
		}
	}

	// Oracle 的 EXPLAIN PLAN:不走只读事务,由网关代为写计划、读计划、回滚。
	if fam == familyOracle {
		if inner, ok := OracleExplainInner(query); ok {
			*sent = true
			tx, rows, err := oracleExplainPlan(ctx, sc, conn, inner)
			if err != nil {
				return out, err
			}
			defer tx.Rollback() // 计划只写在这个事务里,读完即回滚
			defer rows.Close()
			return scanBizRows(rows, query, maxRows, maskingOff(ctx))
		}
	}

	var opts *sql.TxOptions
	if (fam == familyMySQL && !isTiDBEngine(conn.Engine)) || fam == familyPostgres {
		opts = &sql.TxOptions{ReadOnly: true} // START TRANSACTION READ ONLY / BEGIN READ ONLY
	}
	tx, err := sc.BeginTx(ctx, opts)
	if err != nil && opts != nil && tidbNoopReadOnly(err) {
		// 引擎标签写成 MySQL 的 TiDB:它把 READ ONLY 当成"只有空实现"的功能,默认直接报 1235
		// (2026-10-27 UAT-TIDB 数据查询全部失败)。TiDB 没有会话级只读,开了 tidb_enable_noop_functions
		// 也只是个空操作 —— 只读由网关的判定(BizReadOnlyCheck)保证,事务照旧永远回滚。
		tx, err = sc.BeginTx(ctx, nil)
	}
	if err != nil {
		return out, err
	}
	defer tx.Rollback() // 只读查询,永远回滚

	switch fam {
	case familyPostgres:
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("SET LOCAL statement_timeout = %d", ms)); err != nil {
			return out, err
		}
	case familyOracle:
		// 必须是事务里的第一条语句。
		if _, err := tx.ExecContext(ctx, "SET TRANSACTION READ ONLY"); err != nil {
			return out, err
		}
		if schema := oracleTargetSchema(conn); schema != "" {
			if _, err := tx.ExecContext(ctx, `ALTER SESSION SET CURRENT_SCHEMA = "`+schema+`"`); err != nil {
				return out, apimsg.Errorf("切换 schema 到 %s 失败: %w", schema, err)
			}
		}
		// DESC:翻成数据字典查询,不经函数,不必查依赖。
		if owner, name, ok := OracleBizDescribe(query); ok {
			args := []any{name}
			if owner != "" {
				args = append(args, owner)
			}
			*sent = true
			rows, err := tx.QueryContext(ctx, bizOracleDescSQL(owner), args...)
			if err != nil {
				return out, err
			}
			defer rows.Close()
			if out, err = scanBizRows(rows, query, maxRows, maskingOff(ctx)); err == nil && len(out.Data) == 0 {
				err = apimsg.Errorf("ORA-04043: 对象 %s 不存在(或没有权限查看)", name)
			}
			return out, err
		}
		// 自定义函数里的自治事务:只读事务管不到,执行前查一遍依赖(bizquery_hidden.go)。
		ref, err := oracleHiddenEffect(ctx, tx, conn, query)
		if err != nil {
			if ctx.Err() != nil {
				return out, err
			}
			ref = model.NewRuleRef(model.BizHiddenCheckFailed, "err", err.Error())
		}
		if ref != nil {
			return out, &BizRefused{Ref: ref}
		}
	}

	*sent = true
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	return scanBizRows(rows, query, maxRows, maskingOff(ctx))
}

// scanBizRows 读至多 maxRows 行,再按敏感字段规则打码。
// tidbNoopReadOnly:TiDB 拒绝 START TRANSACTION READ ONLY 时的报错 —— Error 1235 "function READ ONLY has only
// noop implementation in tidb now, use tidb_enable_noop_functions to enable these functions"。
// 只认这一种,别的开事务失败照旧报错。
func tidbNoopReadOnly(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "tidb_enable_noop_functions") ||
		(strings.Contains(msg, "1235") && strings.Contains(strings.ToLower(msg), "noop"))
}

func scanBizRows(rows *sql.Rows, query string, maxRows int, maskOff bool) (BizQueryResult, error) {
	var out BizQueryResult
	cols, err := rows.Columns()
	if err != nil {
		return out, err
	}
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	data := [][]string{}
	for rows.Next() {
		if maxRows > 0 && len(data) >= maxRows {
			out.Truncated = true
			break
		}
		if err := rows.Scan(ptrs...); err != nil {
			return out, err
		}
		rec := make([]string, len(cols))
		for i, v := range vals {
			rec[i] = cellString(v)
		}
		data = append(data, rec)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	// 业务方永远只看脱敏值:与终端同一个出口规则(脱敏字典),没有原值旁路;另加口令列兜底。
	out.MaskedColumns = maskBizResultSet(query, cols, data, maskOff)
	out.Columns, out.Data = cols, data
	return out, nil
}
