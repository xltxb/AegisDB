package gateway

// 业务查询 / 数据导出共用的"只读查询里的副作用"判定,以及业务查询专有的两道:信息函数、
// PG 系统目录(2026-10 黑盒测试,.scratch/blackbox-fixes/issues/04-data-tools.md 第 1~4 条)。

import (
	"regexp"
	"strings"

	"aegisdb/internal/model"
)

// unquoteIdents 只抹单引号字符串、把标识符引号(反引号 / 双引号 / 方括号)换成空格:
// blankQuoted 连 `sleep`(3)、"pg_sleep"(2) 里的函数名一起抹掉,函数黑名单就这样被一对引号绕过
// (MySQL / PG 都认带引号的内置函数名,黑盒测试里真执行了)。与 analyzeIsReadOnly 的第三份读法同口径。
func unquoteIdents(body string) string {
	return strings.NewReplacer(`"`, " ", "`", " ", "[", " ", "]", " ").Replace(blankStringLiterals(body))
}

// bizFuncRef:文本里调用了 bizFuncRe 清单上的函数 / 包,返回拒绝原因。allow 里的名字(大写)跳过。
func bizFuncRef(text string, allow map[string]bool) *model.RuleRef {
	for _, m := range bizFuncRe.FindAllStringSubmatch(text, -1) {
		name := m[1]
		if name == "" {
			name = m[2]
		}
		if name = strings.ToUpper(name); !allow[name] {
			return model.NewRuleRef(model.BizFunc, "name", name)
		}
	}
	return nil
}

// exportPurePackages:导出里放行的 Oracle 纯函数包。导 CLOB / BLOB 列的标准写法就是
// DBMS_LOB.SUBSTR(…)、UTL_RAW.CAST_TO_VARCHAR2(…);8302027 让导出复用业务查询的整张函数清单
// (含 DBMS_* / UTL_* 一刀切),这些导出全被拒了(2026-10-26 回归排查)。只读、不碰会话 / 网络 /
// 文件 / 锁的几个包放行,其余(UTL_HTTP、UTL_FILE、DBMS_LOCK、DBMS_PIPE、DBMS_SCHEDULER……)照旧拒。
// 业务查询不受影响:那边一直不放行任何 Oracle 工具包。
var exportPurePackages = map[string]bool{
	"DBMS_LOB": true, "UTL_RAW": true, "DBMS_RANDOM": true, "UTL_I18N": true, "UTL_ENCODE": true,
	"UTL_URL": true, "DBMS_CRYPTO": true,
}

// ReadSideEffect 判一条以 SELECT 开头的查询真跑起来有没有副作用:加锁读(FOR UPDATE / FOR SHARE /
// LOCK IN SHARE MODE)、INTO(写文件、写变量、建表)、睡眠 / 加锁 / 改序列 / 跨库等函数。
// 没有返回 nil,否则返回拒绝原因(业务查询的 bizLock / bizInto / bizFunc)。
//
// 业务查询(BizReadOnlyCheck)与数据导出(service.exportSQLReadOnly)共用这一份 —— 导出原先只看
// 动词与写文件,SELECT … FOR UPDATE 在生产库上实打实持住了行锁。
//
// 两种字面量理解(backslash.go)都要过:MySQL 的 \' 能把后面的 SLEEP / FOR UPDATE 伪装成字符串。
// 业务查询在前面已经拒掉了这种歧义;导出没有那一步,靠这里兜。`#` 注释(hash_comment.go)同理:
// LOAD_FILE#x<换行>(…) 只有把 `#` 当注释才认得出是函数调用。
func ReadSideEffect(st string) *model.RuleRef { return readSideEffect(st, nil) }

// ExportReadSideEffect 是数据导出用的 ReadSideEffect:多放行 exportPurePackages 里的 Oracle 纯函数包;
// 知道目标引擎时只按它的字面量读法判 —— Oracle / PG 里 `\` 不是转义符,'D:\' 就是一个完整的字符串,
// 按 MySQL 读法会把后面字面量里的 for update、sleep(1) 当成代码误拒(8302027 起)。engine 为空时两种都判。
func ExportReadSideEffect(engine, st string) *model.RuleRef {
	readings := append(sqlReadings(st), hashReadings(st)...)
	// 只有 Oracle 的字面量里 `\` 一定不是转义。PG 系不行:E'…' 串里 \' 是转义
	// (standard_conforming_strings=off 时普通串也是),只按标准读法判,E'a\' ', pg_sleep(30), ' '
	// 里的 pg_sleep 就被当成了字符串内容(2026-10-26 复查)。PG 照旧两种读法都判。
	if engineFamily(engine) == familyOracle {
		readings = []string{st}
	}
	return readSideEffectIn(readings, exportPurePackages)
}

func readSideEffect(st string, allow map[string]bool) *model.RuleRef {
	return readSideEffectIn(append(sqlReadings(st), hashReadings(st)...), allow)
}

func readSideEffectIn(readings []string, allow map[string]bool) *model.RuleRef {
	for _, r := range readings {
		body := StripComments(r)
		masked := blankQuoted(body) // 关键字判断:引号里的内容一律不算
		if m := bizLockRe.FindString(masked); m != "" {
			return model.NewRuleRef(model.BizLock, "clause", strings.Join(strings.Fields(m), " "))
		}
		if bizIntoRe.MatchString(masked) {
			return model.NewRuleRef(model.BizInto)
		}
		if ref := bizFuncRef(masked, allow); ref != nil {
			return ref
		}
		// 引号包着的函数名:只对函数清单看这份读法 —— 列名叫 `into`、"for" 不该被当成关键字
		if ref := bizFuncRef(unquoteIdents(body), allow); ref != nil {
			return ref
		}
		if fn := EffectfulCall(r); fn != "" {
			return model.NewRuleRef(model.BizFunc, "name", fn)
		}
	}
	return nil
}

// ---- 信息函数(第 3 条) ----

// 带括号调用的信息函数:后端账号、服务器地址、配置项。DATABASE() / VERSION() / current_database()
// 不在其中 —— 业务方本来就知道自己连的是哪个库,版本号写 SQL 时用得上。
var bizInfoFuncRe = regexp.MustCompile(`(?i)(?:^|[^\pL\pN_$#])(USER|CURRENT_USER|SESSION_USER|SYSTEM_USER|CURRENT_ROLE|` +
	`INET_SERVER_ADDR|INET_SERVER_PORT|INET_CLIENT_ADDR|INET_CLIENT_PORT|CURRENT_SETTING|SYS_CONTEXT)\s*\(`)

// 不带括号也是调用的关键字(MySQL / PG / Oracle 都认 CURRENT_USER 这种写法)。前面是点的
// (o.current_user)是列,不算。
var bizInfoWordRe = regexp.MustCompile(`(?i)(^|[^\pL\pN_$#.@])(CURRENT_USER|SESSION_USER|SYSTEM_USER|CURRENT_ROLE|USER|UID)($|[^\pL\pN_$#])`)

// 只在部分引擎上算调用的裸关键字:USER 在 PG 系 / Oracle 是保留字(列名叫 user 必须加引号),
// UID 只在 Oracle 是;在 MySQL 上它们都是普通列名。
//
// SESSION_USER / SYSTEM_USER / CURRENT_ROLE 在 MySQL 上必须带括号才是调用(那由 bizInfoFuncRe 管),
// 裸写就是列名 —— 8302027 对所有引擎都拦,MySQL 上名叫 system_user 的列被误拒(2026-10-26)。
var bizInfoWordFamilies = map[string][]string{
	"USER": {familyPostgres, familyOracle}, "UID": {familyOracle},
	"SESSION_USER": {familyPostgres, familyOracle}, "SYSTEM_USER": {familyPostgres, familyOracle},
	"CURRENT_ROLE": {familyPostgres, familyOracle},
}

var bizAsBeforeRe = regexp.MustCompile(`(?i)\bAS\s*$`)

// MySQL 系统变量:@@hostname、@@GLOBAL.datadir、@@`basedir`(反引号已在 unquoteIdents 里去掉)。
var bizSysVarRe = regexp.MustCompile(`@@\s*(?:(?i:GLOBAL|SESSION|LOCAL)\s*\.\s*)?([\pL_][\pL\pN_$]*)`)

// 业务查询可以读的系统变量:版本、字符集、时区、事务与 SQL 模式这类写 SQL 时需要知道的。
// 其余一律拒绝(@@hostname、@@datadir、@@basedir、@@port、@@secure_file_priv、@@server_uuid…)——
// 用白名单而不是黑名单:MySQL 每个版本都在加变量,黑名单永远补不全。
var bizSysVarAllowed = map[string]bool{
	"version": true, "version_comment": true, "innodb_version": true,
	"time_zone": true, "sql_mode": true, "autocommit": true,
	"transaction_isolation": true, "tx_isolation": true, "transaction_read_only": true, "tx_read_only": true,
	"character_set_client": true, "character_set_connection": true, "character_set_results": true,
	"character_set_database": true, "character_set_server": true,
	"collation_connection": true, "collation_database": true, "collation_server": true,
	"lower_case_table_names": true, "max_allowed_packet": true, "max_execution_time": true, "max_statement_time": true,
	"sql_select_limit": true, "auto_increment_increment": true, "auto_increment_offset": true,
	"div_precision_increment": true, "group_concat_max_len": true, "default_storage_engine": true,
	"lc_time_names": true, "sql_auto_is_null": true, "identity": true, "last_insert_id": true,
	"default_week_format": true, "explicit_defaults_for_timestamp": true,
}

// bizInfoLeak:语句读了后端账号、主机名、数据目录这类信息,返回拒绝原因(bizFunc,name 为函数名或 @@变量)。
func bizInfoLeak(fam, body string) *model.RuleRef {
	unq := unquoteIdents(body)
	if m := bizInfoFuncRe.FindStringSubmatch(unq); m != nil {
		return model.NewRuleRef(model.BizFunc, "name", strings.ToUpper(m[1])+"()")
	}
	// @@变量只有 MySQL 系有。PG / GaussDB 里 @@ 是全文检索运算符(tsv @@ to_tsquery('x')),
	// 8302027 对所有引擎都按系统变量拦,全文检索查询被拒(2026-10-26 回归排查)。
	if fam == familyMySQL || fam == "" {
		for _, m := range bizSysVarRe.FindAllStringSubmatch(unq, -1) {
			if !bizSysVarAllowed[strings.ToLower(m[1])] {
				return model.NewRuleRef(model.BizFunc, "name", "@@"+strings.ToLower(m[1]))
			}
		}
	}
	// 裸关键字在 blankQuoted 上找:PG 的 "user" 是加了引号的列名,不是调用
	masked := blankQuoted(body)
	for _, loc := range bizInfoWordRe.FindAllStringSubmatchIndex(masked, -1) {
		word := strings.ToUpper(masked[loc[4]:loc[5]])
		if fams, limited := bizInfoWordFamilies[word]; limited && !containsStr(fams, fam) {
			continue
		}
		if bizAsBeforeRe.MatchString(masked[:loc[4]]) {
			continue // SELECT x AS user:这是列别名
		}
		return model.NewRuleRef(model.BizFunc, "name", word)
	}
	return nil
}

// ---- PG 系统目录(第 1 条) ----

// PG 系(PostgreSQL / GaussDB / DWS)的系统 schema。pg_catalog 永远在 search_path 最前面,所以
// 不带前缀的 pg_authid 一样能读到 —— 光拦 schema 限定符不够,pg_ 开头的表名本身也要拦。
var bizPGSystemSchemas = map[string]bool{"pg_catalog": true, "information_schema": true, "dbe_perf": true}

// 系统表 / 视图的名字前缀:pg_authid、pg_shadow、pg_stat_*、pg_toast.*;GaussDB / DWS 的 pgxc_*。
var bizPGSystemPrefixes = []string{"pg_", "pgxc_"}

// 函数形式的系统视图:pg_stat_get_*(会话、语句,即 pg_stat_activity 的底层)、pg_ls_*(列服务器目录)。
var bizPGSystemFuncPrefixes = []string{"pg_stat_get_", "pg_ls_"}

var bizIdentRe = regexp.MustCompile(bizIdent)

// bizPGCatalog:PG 系上引用了系统目录(表、视图、schema,或函数形式的系统视图),返回拒绝原因。
//
// 一个标识符后面紧跟左括号是函数调用 —— pg_size_pretty(…) 这类普通函数照常放行;否则它是表、
// 视图、schema 或列,pg_ 开头的一律拒绝。代价是用户自己叫 pg_xxx 的表 / 列也查不了,这在 PG 上
// 本来就是不推荐的命名(pg_ 前缀保留给系统)。
func bizPGCatalog(body, grantedDB string) *model.RuleRef {
	ids := blankStringLiterals(body) // 只抹字符串:"pg_authid" 是加了引号的表名
	for _, loc := range bizIdentRe.FindAllStringIndex(ids, -1) {
		raw := ids[loc[0]:loc[1]]
		name := strings.ToLower(strings.Trim(raw, "`\""))
		call := strings.HasPrefix(strings.TrimLeft(ids[loc[1]:], " \t\r\n"), "(")
		if call {
			for _, p := range bizPGSystemFuncPrefixes {
				if strings.HasPrefix(name, p) {
					return model.NewRuleRef(model.BizFunc, "name", strings.ToUpper(name))
				}
			}
			continue
		}
		bad := bizPGSystemSchemas[name]
		for _, p := range bizPGSystemPrefixes {
			bad = bad || strings.HasPrefix(name, p)
		}
		if bad {
			return model.NewRuleRef(model.BizSystemDB, "name", strings.Trim(raw, "`\""), "db", grantedDB)
		}
	}
	return nil
}

// bizCredentialColumns 是业务查询出口的口令列兜底:系统目录与 mysql.user 里存口令哈希的列。
// 判定层已经拦了系统库 / 系统目录,这里是"万一漏过"的最后一道 —— 不依赖管理员配置的脱敏字典。
var bizCredentialColumns = []SensitiveRule{
	{Table: "*", Column: "rolpassword", Style: MaskFull},
	{Table: "*", Column: "passwd", Style: MaskFull},
	{Table: "*", Column: "authentication_string", Style: MaskFull},
}

// maskBizResultSet 是业务查询出口的打码:管理员配置的脱敏字典,再加上口令列兜底。
// maskOff:分层关掉了脱敏 —— 只剩口令列兜底。
func maskBizResultSet(query string, cols []string, data [][]string, maskOff bool) []string {
	var rules []SensitiveRule
	if !maskOff {
		rules = append(rules, currentSensitiveRules()...)
	}
	rules = append(rules, bizCredentialColumns...)
	styleOf := sensitiveMaskPlan(query, cols, rules)
	var targets []int
	var names []string
	for i := range cols {
		if _, ok := styleOf[i]; ok {
			targets = append(targets, i)
			names = append(names, cols[i])
		}
	}
	MaskRows(data, targets, styleOf)
	return names
}
