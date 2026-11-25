package gateway

// 执行窗口的"不越库"判定:窗口按库开,放行前要确认这批语句**只触及**窗口那个库。
//
// 窗口是全系统唯一一处主动放宽闸门的地方,所以这里的每一步都往严的方向倒 ——
// 判不准就当越库,照常走审批。多走一次审批是一次多余的签字;少走一次是一条没人看过的
// 写操作落在**另一个库**上,而那个库根本没有开窗。
//
// ## 为什么不能再靠 sqlutil.ObjectRefs
//
// 它只认"关键字后面紧跟的那个名字",认不出就说认不出 —— 对发布单的对象检查是对的,
// 对窗口不对:窗口原先的做法是"认不出的不拦",于是 2026-10 黑盒里这些全都免审批地
// 改到了同实例的别库(工单 01 第 1 条):
//
//	UPDATE B . t2 SET …                       点号两侧有空格
//	UPDATE t1 a JOIN B.t2 b … / UPDATE t1, B.t2 …   第二张表
//	DELETE b FROM t1 a JOIN B.t2 b …
//	REPLACE INTO B.t2 …                        动词不在它的名单里
//	RENAME TABLE mv TO B.moved / DROP TABLE mv, B.dropme2
//	PREPARE s FROM 'UPDATE B.t2 …'; EXECUTE s  动态 SQL
//	WITH x AS (…) UPDATE B.t2 …                带 CTE 的一律不认
//
// ## 这里的做法
//
// 仍然不是解析器(ADR 0008),而是一个按目标引擎切词的扫描,规则只有几条,都朝严的一侧:
//
//  1. 语句头走白名单:DML、库内对象的 DDL、事务控制、会话级 SET。USE、PREPARE / EXECUTE、
//     GRANT、CREATE DATABASE / USER、ALTER SESSION(Oracle 的切 schema)…… 不在里面的一律不放行。
//  2. 出现在**表的位置**上(FROM / JOIN / UPDATE / INTO / TABLE / TO / LIKE / REFERENCES、
//     表列表里的逗号之后……)的限定名,限定符必须就是窗口库。
//  3. 带括号的限定名(函数 / 过程调用)同上:别库的函数体能写别库。
//  4. 其余位置的 `X.Y` 是"表或别名.列":X 必须是窗口库,或者是这条语句自己声明过的表名 / 别名。
//     认不出来的限定符一律当作别的库 —— 这一条兜住第 2 条漏掉的表位置。
//  5. 三段名 `X.Y.Z` 的 X 是库(schema.table.column),必须是窗口库。
//
// 切词按目标引擎的真实读法:MySQL 的 `#` 是注释、`--` 后面要跟空白才是注释、`/*! … */`
// 里的内容会被执行;Oracle 有 q'[…]' 引用。字面量的边界若取决于 `\'` 是不是转义(MySQL 的
// sql_mode),**两种读法都判**,任一种越库就算越库(见 backslash.go)。
//
// ## 判不出来的
//
// 视图、同义词、触发器、存储过程的**函数体**里写了别库,从语句文本上看不出来 —— 那是库里
// 已经存在的对象,由建它的人负责,和"不加窗口时它照样会写别库"是同一回事。这里拦的是
// **语句本身写出来**的越库。
//
// PostgreSQL 系只查 dblink:schema 在库里面,限定名到不了别的库。

import (
	"regexp"
	"strings"

	"velagateway/pkg/sqlutil"
)

// StaysInDatabase 报告这批语句是否都只触及 db 这个库(Oracle 是 schema)。
// 判不准的一律返回 false。
func StaysInDatabase(engine, db string, stmts []string) bool {
	db = strings.TrimSpace(db)
	if db == "" {
		return false
	}
	fam := engineFamily(engine)
	switch fam {
	case familyPostgres:
		for _, st := range stmts {
			if pgDblinkRe.MatchString(StripComments(st)) {
				return false
			}
		}
		return true
	case familyMongo:
		return true // 不是 SQL;窗口对它只按库匹配
	}
	for _, st := range stmts {
		for _, lx := range windowReadings(fam) {
			c := &scopeCheck{db: db, oracle: fam == familyOracle, tidb: isTiDBEngine(engine), raw: st}
			if !c.stays(groupNames(lx.lex(st))) {
				return false
			}
		}
	}
	return true
}

// PG 的 dblink 系函数能连到别的库执行任意语句。不抹字面量直接搜:引号里出现这个词
// 也算 —— 多拦一条无害的,好过放过一条真越库的。
var pgDblinkRe = regexp.MustCompile(`(?i)\bdblink\w*"?\s*\(`)

// windowReadings 是这个引擎下要各判一次的切词读法。
func windowReadings(fam string) []windowLexer {
	switch fam {
	case familyMySQL:
		// 反斜杠转义开着(默认)与关着(NO_BACKSLASH_ESCAPES)各读一遍。
		return []windowLexer{{mysql: true, backslash: true}, {mysql: true}}
	case familyOracle:
		return []windowLexer{{oracle: true}}
	}
	return []windowLexer{{}}
}

// ---- 切词 ----

type wtokKind int

const (
	wtWord   wtokKind = iota // 裸词:关键字或标识符
	wtQuoted                 // 引号包着的标识符(反引号、双引号)
	wtString                 // 字符串字面量 —— 内容不要
	wtNumber
	wtPunct
)

type wtok struct {
	kind wtokKind
	text string
}

type windowLexer struct {
	mysql, oracle bool
	backslash     bool // 字符串里的 \ 是转义(MySQL 默认模式)
}

func (lx windowLexer) lex(s string) []wtok {
	var out []wtok
	n := len(s)
	execDepth := 0 // MySQL /*! … */ 里:内容照常切词,只丢掉起止标记
	for i := 0; i < n; {
		c := s[i]
		switch {
		case c <= ' ':
			i++
		case c == '-' && i+1 < n && s[i+1] == '-' && (!lx.mysql || i+2 >= n || s[i+2] <= ' '):
			// MySQL 只有 `-- ` 才是注释:1--1 是"1 减负 1",后面的东西会被执行
			for i < n && s[i] != '\n' {
				i++
			}
		case c == '#' && lx.mysql:
			for i < n && s[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < n && s[i+1] == '*':
			// 可执行注释:MySQL /*!ver、MariaDB /*M!ver、TiDB /*T![feature](sqlutil.ExecCommentOpener)
			if lx.mysql {
				if m := sqlutil.ExecCommentOpener(s, i); m > 0 {
					i += m
					execDepth++
					continue
				}
			}
			if j := strings.Index(s[i+2:], "*/"); j >= 0 {
				i += 2 + j + 2
			} else {
				i = n
			}
		case c == '*' && i+1 < n && s[i+1] == '/' && execDepth > 0:
			execDepth--
			i += 2
		case lx.oracle && (c == 'q' || c == 'Q') && i+2 < n && s[i+1] == '\'' && !prevIsWord(s, i):
			i = skipOracleQ(s, i+1)
			out = append(out, wtok{kind: wtString})
		case lx.oracle && (c == 'n' || c == 'N') && i+3 < n && (s[i+1] == 'q' || s[i+1] == 'Q') && s[i+2] == '\'' && !prevIsWord(s, i):
			i = skipOracleQ(s, i+2)
			out = append(out, wtok{kind: wtString})
		case c == '\'':
			// 内容留着:Oracle 的 EXECUTE IMMEDIATE '写死的语句' 要把它当一条语句再判一次(见 stays)。
			// 其余地方只看 kind,不读字符串内容。
			var lit string
			lit, i = scanQuoted(s, i, '\'', lx.backslash)
			out = append(out, wtok{kind: wtString, text: lit})
		case c == '"':
			// 双引号:Oracle / PG / ANSI_QUOTES 下是标识符,MySQL 默认是字符串。一律当标识符 ——
			// 边界两种理解一样,而当成标识符只会多认出一个限定名(严的一侧)。
			var name string
			name, i = scanQuoted(s, i, '"', lx.backslash)
			out = append(out, wtok{kind: wtQuoted, text: name})
		case c == '`':
			var name string
			name, i = scanQuoted(s, i, '`', false)
			out = append(out, wtok{kind: wtQuoted, text: name})
		case c >= '0' && c <= '9':
			j := i
			for j < n && windowWordByte(s[j], lx.mysql) {
				j++
			}
			w := s[i:j]
			if allDigits(w) {
				// 纯数字不会是标识符(MySQL 规定),1.5 / 1.5e3 整个是一个数
				if j < n && s[j] == '.' {
					j++
					for j < n && windowWordByte(s[j], lx.mysql) {
						j++
					}
				}
				out = append(out, wtok{kind: wtNumber, text: s[i:j]})
			} else {
				out = append(out, wtok{kind: wtWord, text: w}) // MySQL 允许数字开头的标识符
			}
			i = j
		case windowWordByte(c, lx.mysql) && c != '#':
			j := i
			for j < n && windowWordByte(s[j], lx.mysql) {
				j++
			}
			out = append(out, wtok{kind: wtWord, text: s[i:j]})
			i = j
		default:
			out = append(out, wtok{kind: wtPunct, text: string(c)})
			i++
		}
	}
	return out
}

// windowWordByte:裸词里能出现的字节。# 在 Oracle 标识符里合法,在 MySQL 里是注释。
func windowWordByte(c byte, mysql bool) bool {
	switch {
	case c == '_' || c == '$' || c >= 0x80:
		return true
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	case c == '#':
		return !mysql
	}
	return false
}

func prevIsWord(s string, i int) bool {
	return i > 0 && windowWordByte(s[i-1], false)
}

func allDigits(w string) bool {
	for i := 0; i < len(w); i++ {
		if w[i] < '0' || w[i] > '9' {
			return false
		}
	}
	return w != ""
}

// scanQuoted 读一个以 s[i] 开头的引号段,返回去掉引号后的内容与下一个位置。
// 连写的引号是转义;backslash 时 \x 也是。没有收尾的引号吞到末尾 —— 库端会报语法错。
func scanQuoted(s string, i int, q byte, backslash bool) (string, int) {
	var b strings.Builder
	n := len(s)
	for j := i + 1; j < n; j++ {
		ch := s[j]
		if backslash && ch == '\\' && j+1 < n {
			b.WriteByte(s[j+1])
			j++
			continue
		}
		if ch == q {
			if j+1 < n && s[j+1] == q {
				b.WriteByte(q)
				j++
				continue
			}
			return b.String(), j + 1
		}
		b.WriteByte(ch)
	}
	return b.String(), n
}

// skipOracleQ 跳过 Oracle 的 q'X…X'(i 指向那个引号)。[ ( { < 的收尾是对应的右括号。
func skipOracleQ(s string, i int) int {
	if i+1 >= len(s) {
		return len(s)
	}
	open := s[i+1]
	closeCh := open
	switch open {
	case '[':
		closeCh = ']'
	case '(':
		closeCh = ')'
	case '{':
		closeCh = '}'
	case '<':
		closeCh = '>'
	}
	for j := i + 2; j+1 < len(s); j++ {
		if s[j] == closeCh && s[j+1] == '\'' {
			return j + 2
		}
	}
	return len(s)
}

// ---- 名字 ----

type wpart struct {
	s      string
	quoted bool
}

// wnode 是切词之后再把 `a . b . c` 拼成一个名字的结果。
type wnode struct {
	kind  wtokKind // wtWord 表示一个名字(含带引号的),parts 是它的各段
	parts []wpart
	text  string // wtPunct 的那个字符
}

// word 是单段裸词的大写形式;别的(带限定、带引号、标点)返回 ""。
func (n wnode) word() string {
	if n.kind != wtWord || len(n.parts) != 1 || n.parts[0].quoted {
		return ""
	}
	return strings.ToUpper(n.parts[0].s)
}

func (n wnode) isPunct(p string) bool { return n.kind == wtPunct && n.text == p }

func groupNames(toks []wtok) []wnode {
	var out []wnode
	for i := 0; i < len(toks); {
		t := toks[i]
		if t.kind != wtWord && t.kind != wtQuoted {
			out = append(out, wnode{kind: t.kind, text: t.text})
			i++
			continue
		}
		parts := []wpart{{t.text, t.kind == wtQuoted}}
		j := i + 1
		for j+1 < len(toks) && toks[j].kind == wtPunct && toks[j].text == "." {
			nx := toks[j+1]
			switch {
			case nx.kind == wtWord || nx.kind == wtQuoted || nx.kind == wtNumber:
				parts = append(parts, wpart{nx.text, nx.kind == wtQuoted})
			case nx.kind == wtPunct && nx.text == "*":
				parts = append(parts, wpart{"*", false})
			default:
				goto done
			}
			j += 2
		}
	done:
		out = append(out, wnode{kind: wtWord, parts: parts})
		i = j
	}
	return out
}

// ---- 判定 ----

type scopeCheck struct {
	db     string
	oracle bool
	tidb   bool // TiDB:库名不分大小写;BATCH / SPLIT / ADMIN CHECK 等方言语句头
	raw    string
}

func isTiDBEngine(engine string) bool { return strings.Contains(strings.ToLower(engine), "tidb") }

// 语句头的白名单。不在这里的(USE、PREPARE、EXECUTE、GRANT、FLUSH、KILL、XA……)一律不放行。
var windowHeads = map[string]bool{
	"SELECT": true, "INSERT": true, "UPDATE": true, "DELETE": true, "REPLACE": true, "MERGE": true,
	"WITH": true, "TABLE": true, "VALUES": true,
	"CREATE": true, "ALTER": true, "DROP": true, "TRUNCATE": true, "RENAME": true, "COMMENT": true,
	"SET": true, "BEGIN": true, "START": true, "COMMIT": true, "ROLLBACK": true, "SAVEPOINT": true, "RELEASE": true,
	"LOCK": true, "UNLOCK": true, "EXPLAIN": true, "DESC": true, "DESCRIBE": true, "SHOW": true,
	"ANALYZE": true, "OPTIMIZE": true, "CHECK": true, "REPAIR": true, "CHECKSUM": true,
	"LOAD": true, "CALL": true, "DO": true, "DECLARE": true,
}

// CREATE / ALTER / DROP 的对象种类白名单:库里面的对象。DATABASE、SCHEMA、USER、ROLE、
// SESSION、SYSTEM、TABLESPACE、SERVER、PUBLIC SYNONYM…… 都不在一个库里。
var windowObjectKinds = map[string]bool{
	"TABLE": true, "VIEW": true, "INDEX": true, "TRIGGER": true, "PROCEDURE": true, "FUNCTION": true,
	"EVENT": true, "SEQUENCE": true, "PACKAGE": true, "TYPE": true, "SYNONYM": true,
}

// 种类前面可能出现的修饰词(CREATE OR REPLACE、UNIQUE、DEFINER = …、ALGORITHM = …)。
var windowKindModifiers = map[string]bool{
	"OR": true, "REPLACE": true, "UNIQUE": true, "FULLTEXT": true, "SPATIAL": true, "BITMAP": true,
	"TEMPORARY": true, "TEMP": true, "GLOBAL": true, "PRIVATE": true, "ONLINE": true, "OFFLINE": true,
	"IGNORE": true, "EDITIONABLE": true, "NONEDITIONABLE": true, "EDITIONING": true, "FORCE": true,
	"NOFORCE": true, "NO": true, "ALGORITHM": true, "DEFINER": true, "SQL": true, "SECURITY": true,
	"INVOKER": true, "UNDEFINED": true, "MERGE": true, "TEMPTABLE": true, "CURRENT_USER": true,
	"AGGREGATE": true, "MATERIALIZED": true, "IF": true, "NOT": true, "EXISTS": true,
}

// 后续语句(`;` 之后,存储过程 / PL/SQL 块里)不能出现的头:它们换库或越出库。
var windowDeniedLaterHeads = map[string]bool{
	"USE": true, "CONNECT": true, "GRANT": true, "REVOKE": true, "FLUSH": true, "KILL": true,
	"SHUTDOWN": true, "PURGE": true, "RESET": true, "INSTALL": true, "UNINSTALL": true, "HANDLER": true,
}

// 紧跟在这些词后面的名字是一张表(或库里的对象)。
var windowTableWords = map[string]bool{
	"FROM": true, "JOIN": true, "STRAIGHT_JOIN": true, "UPDATE": true, "INTO": true, "TABLE": true,
	"TABLES": true, "USING": true, "DELETE": true, "TO": true, "LIKE": true, "REFERENCES": true,
	"RENAME": true, "REPLACE": true, "INSERT": true, "TRUNCATE": true, "EXISTS": true, "VIEW": true,
	"TRIGGER": true, "PROCEDURE": true, "FUNCTION": true, "EVENT": true, "SEQUENCE": true,
	"PACKAGE": true, "BODY": true, "TYPE": true, "SYNONYM": true, "INDEX": true, "FOR": true,
	"CALL": true, "DESC": true, "DESCRIBE": true, "EXPLAIN": true, "IGNORE": true, "LOW_PRIORITY": true,
	"HIGH_PRIORITY": true, "DELAYED": true, "QUICK": true, "ONLY": true,
}

// 这些词开始一个**表列表**:同一层括号里接下来的逗号后面还是表(UPDATE t1, B.t2)。
// ON 不结束列表:`t1 JOIN t2 ON …, t3` 里的 t3 仍是表。
var windowListWords = map[string]bool{
	"FROM": true, "JOIN": true, "STRAIGHT_JOIN": true, "UPDATE": true, "INTO": true, "TABLE": true,
	"TABLES": true, "USING": true, "DELETE": true,
}

// 这些词结束表列表:后面的逗号分隔的是赋值、列、排序键……
var windowListStops = map[string]bool{
	"SET": true, "WHERE": true, "VALUES": true, "VALUE": true, "SELECT": true, "ORDER": true,
	"GROUP": true, "HAVING": true, "LIMIT": true, "UNION": true, "WINDOW": true, "RETURNING": true,
	"DUPLICATE": true, "INTERSECT": true, "EXCEPT": true, "MINUS": true, "CONNECT": true, "PIVOT": true,
}

// 表名后面的这些词不是别名。
var windowAliasStops = map[string]bool{
	"SET": true, "WHERE": true, "ON": true, "USING": true, "JOIN": true, "INNER": true, "LEFT": true,
	"RIGHT": true, "CROSS": true, "NATURAL": true, "STRAIGHT_JOIN": true, "FULL": true, "OUTER": true,
	"VALUES": true, "VALUE": true, "SELECT": true, "ORDER": true, "GROUP": true, "HAVING": true,
	"LIMIT": true, "UNION": true, "PARTITION": true, "USE": true, "FORCE": true, "IGNORE": true,
	"FOR": true, "LOCK": true, "WINDOW": true, "READ": true, "WRITE": true, "TO": true, "ADD": true,
	"DROP": true, "MODIFY": true, "CHANGE": true, "RENAME": true, "WITH": true, "AS": true, "INTO": true,
	"FROM": true, "RETURNING": true, "LOW_PRIORITY": true, "DEFAULT": true, "ENGINE": true,
}

func (c *scopeCheck) isDB(p wpart) bool {
	name := p.s
	if c.oracle && !p.quoted {
		name = strings.ToUpper(name) // Oracle 不带引号的标识符一律按大写解析
	}
	// TiDB 的库名永远不分大小写(与 lower_case_table_names 无关)。
	if c.tidb {
		return strings.EqualFold(name, c.db)
	}
	// 其余引擎按原样比:Linux 上的 MySQL 库名区分大小写,orders_db 与 ORDERS_DB 是两个库。
	return name == c.db
}

func (c *scopeCheck) stays(nodes []wnode) bool {
	if len(nodes) == 0 {
		return true
	}
	nodes, body := c.plsqlUnitHeader(nodes)
	if body != nil && !c.plsqlBodyStays(body) {
		return false
	}
	nodes = c.tidbBatchBody(nodes)
	if len(nodes) == 0 {
		return false
	}
	kind, ok := c.headAllowed(nodes)
	if !ok {
		return false
	}
	// MySQL 建存储过程 / 函数 / 事件:过程体里的 PREPARE / EXECUTE 此刻不执行(要等调用时),
	// 不按动态 SQL 判越库;过程体里写明的别库对象照判。班车外照旧按字典走审批(2026-10-26 讨论 14)。
	mysqlRoutine := !c.oracle && nodes[0].word() == "CREATE" && (kind == "PROCEDURE" || kind == "FUNCTION" || kind == "EVENT")
	tablePos := c.tablePositions(nodes, kind)
	declared := c.declaredNames(nodes, tablePos)

	for i, n := range nodes {
		if n.kind == wtPunct {
			switch n.text {
			case "@":
				if c.oracle {
					return false // db link:t@remote 落在另一个库上
				}
			case ";":
				if !c.laterHeadAllowed(nodes[i+1:]) {
					return false
				}
			}
			continue
		}
		if n.kind != wtWord {
			continue
		}
		switch n.word() {
		case "EXECUTE":
			// Oracle 的 EXEC[UTE] 过程(…) 是 SQL*Plus 对 BEGIN 过程(…); END; 的简写(oracle_sqlplus.go),
			// 不是动态 SQL;只有 EXECUTE IMMEDIATE 才是。
			if c.oracle && (i+1 >= len(nodes) || nodes[i+1].word() != "IMMEDIATE") {
				break
			}
			if c.oracle && c.immediateLiteralStays(nodes, i+2) {
				break
			}
			if mysqlRoutine {
				break
			}
			return false // 动态 SQL:真正执行的语句在字符串里,看不见
		case "PREPARE", "DEALLOCATE":
			if mysqlRoutine {
				break
			}
			return false
		}
		if len(n.parts) < 2 {
			continue
		}
		if !c.qualifiedOK(nodes, i, tablePos[i], declared) {
			return false
		}
	}
	return true
}

// Oracle 的存储代码单元:CREATE 它们只是把代码存进 schema,代码体里的语句此刻并不执行。
// 触发器不在里面 —— 它挂在表上,建好后别人的 DML 就会触发,照旧整句判。
var plsqlUnitKinds = map[string]bool{"PACKAGE": true, "PROCEDURE": true, "FUNCTION": true, "TYPE": true}

// plsqlUnitHeader:Oracle 的 CREATE PACKAGE [BODY] / PROCEDURE / FUNCTION / TYPE 只判到 IS / AS
// 为止的头部(建的是哪个 schema 的对象、参数类型),代码体不判。
//
// 代码体里 `dbms_output.put_line(…)`、`pk_other.f(…)`、`rec.field`、EXECUTE IMMEDIATE 随处都是,
// 整句判时每个包体都被当成越库,班车时段里上包照样整份送审(2026-10-26 生产 AP-2646,
// 窗口 AP-2638 生效中)。这些语句要等有人调用时才执行,与上面"库里已有对象的函数体"是同一回事;
// 而在窗口里调用 `pk.proc(…)` 时,限定名照常要过第 3 条。
//
// 找不到 IS / AS(如 WRAPPED 的包体)就整句判,不放宽。
//
// 2026-10-26 讨论 06-1:代码体也查一部分 —— 只查"写明了的越库":表位置上带别的 schema 限定的名字、
// db link、动态 SQL / 作业类系统包(见 plsqlBodyStays)。否则窗口里先建一个写 hr 的过程、再 EXEC 它,
// 两步就免审批改了别的 schema。返回 body 为 nil 表示不是存储代码单元(整句照常判)。
func (c *scopeCheck) plsqlUnitHeader(nodes []wnode) (header, body []wnode) {
	if !c.oracle || nodes[0].word() != "CREATE" || !plsqlUnitKinds[objectKind(nodes, 1)] {
		return nodes, nil
	}
	for i, n := range nodes {
		if w := n.word(); w == "IS" || w == "AS" {
			return nodes[:i], nodes[i+1:]
		}
	}
	return nodes, nil
}

// plsqlBodyStays 查存储代码体里写明的越库(讨论 06-1)。不查的:EXECUTE IMMEDIATE、普通包调用
// (pk.f()、hr.pkg.f())、记录字段 —— 代码体此刻不执行,这些留给调用时的判定。
//
// 表位置只认 DML 真正落表的那几处:FROM / JOIN / UPDATE / DELETE / TRUNCATE TABLE / INSERT INTO /
// MERGE INTO / USING 以及表列表里逗号后的名字。SELECT … INTO r.f、FETCH … INTO、BULK COLLECT INTO、
// RETURNING … INTO 后面是变量,不是表。
func (c *scopeCheck) plsqlBodyStays(body []wnode) bool {
	pos := c.tablePositions(body, "")
	for i, n := range body {
		if n.isPunct("@") {
			return false // db link
		}
		if n.kind != wtWord || len(n.parts) < 2 {
			continue
		}
		if oracleDynamicPackages[strings.ToUpper(n.parts[len(n.parts)-2].s)] {
			return false // DBMS_SQL.PARSE / SYS.DBMS_SQL.PARSE
		}
		if !pos[i] || !c.bodyTableWord(body, i) {
			continue
		}
		if !c.isDB(n.parts[0]) {
			return false
		}
	}
	return true
}

// bodyTableWord:tablePositions 标出的位置里,排除 INTO 后面是变量的那几种。
func (c *scopeCheck) bodyTableWord(nodes []wnode, i int) bool {
	if i == 0 {
		return false
	}
	if nodes[i-1].isPunct(",") {
		// 列表里逗号后的名字:跟着开这个列表的那个词走(FROM a, hr.b 是表;FETCH c INTO r.a, r.b 是变量)
		for j := i - 2; j >= 0; j-- {
			if windowListWords[nodes[j].word()] {
				return c.bodyTableWord(nodes, j+1)
			}
		}
		return false
	}
	switch nodes[i-1].word() {
	case "FROM", "JOIN", "UPDATE", "DELETE", "TRUNCATE", "TABLE", "USING":
		return true
	case "INTO":
		if i >= 2 {
			switch nodes[i-2].word() {
			case "INSERT", "MERGE", "ALL", "FIRST":
				return true
			}
		}
	}
	return false
}

// immediateLiteralStays:EXECUTE IMMEDIATE 后面是**一整个写死的字符串**(不是变量、不是 || 拼接)时,
// 把字符串当一条语句再判一次越库(讨论 06-2)。BEGIN EXECUTE IMMEDIATE 'truncate table t'; END; 只动
// 自己 schema 的表,不该因为"动态"二字送审;拼出来的语句看不见,照旧送审。
func (c *scopeCheck) immediateLiteralStays(nodes []wnode, j int) bool {
	if j >= len(nodes) || nodes[j].kind != wtString || strings.TrimSpace(nodes[j].text) == "" {
		return false
	}
	if j+1 < len(nodes) {
		next := nodes[j+1]
		switch {
		case next.isPunct(";"):
		case next.word() == "USING" || next.word() == "INTO" || next.word() == "RETURNING" || next.word() == "BULK":
		default:
			return false // '…' || v_tab 之类的拼接
		}
	}
	inner := strings.TrimSpace(nodes[j].text)
	inner = strings.TrimSuffix(inner, ";")
	sub := &scopeCheck{db: c.db, oracle: c.oracle, tidb: c.tidb, raw: inner}
	for _, lx := range windowReadings(familyOracle) {
		if !sub.stays(groupNames(lx.lex(inner))) {
			return false
		}
	}
	return true
}

// tidbBatchBody:TiDB 非事务 DML `BATCH [ON col] LIMIT n [DRY RUN [QUERY]] <DML>`,判的是后面那条 DML。
// 找不到 DML 返回空(整句不放行)。
func (c *scopeCheck) tidbBatchBody(nodes []wnode) []wnode {
	if !c.tidb || nodes[0].word() != "BATCH" {
		return nodes
	}
	for i, n := range nodes {
		switch n.word() {
		case "INSERT", "UPDATE", "DELETE", "REPLACE":
			return nodes[i:]
		}
	}
	return nil
}

// oracleSessionSafeRe:只改本会话格式 / 优化器行为的 ALTER SESSION。脚本开头常见的
// `ALTER SESSION SET NLS_DATE_FORMAT = …` 从前让整批丢了窗口(a8d3768)。CURRENT_SCHEMA(换 schema)、
// CONTAINER(换 PDB)、EDITION 等不在里面,照旧不放行。
var oracleSessionSafeRe = regexp.MustCompile(`(?is)^\s*ALTER\s+SESSION\s+(?:` +
	`SET(?:\s+(?:NLS_\w+|TIME_ZONE|DDL_LOCK_TIMEOUT|OPTIMIZER_\w+|STATISTICS_LEVEL|SKIP_UNUSABLE_INDEXES)\s*=\s*(?:'[^']*'|[\w.+:-]+))+` +
	`|(?:ENABLE|DISABLE|FORCE)\s+PARALLEL\s+(?:DML|DDL|QUERY)(?:\s+PARALLEL\s+\d+)?` +
	`)\s*;?\s*$`)

// TiDB 方言里落在一张表上的语句头(后面跟 TABLE / INDEX,表名照常按表位置判)。
func tidbTableHead(nodes []wnode) bool {
	w := func(i int) string {
		if i < len(nodes) {
			return nodes[i].word()
		}
		return ""
	}
	switch w(0) {
	case "SPLIT":
		return w(1) == "TABLE" || (w(1) == "PARTITION" && w(2) == "TABLE")
	case "ADMIN":
		return (w(1) == "CHECK" || w(1) == "CHECKSUM") && (w(2) == "TABLE" || w(2) == "INDEX")
	case "FLASHBACK", "RECOVER":
		return w(1) == "TABLE"
	}
	return false
}

// headAllowed 判语句头,顺带返回 CREATE / ALTER / DROP 的对象种类。
func (c *scopeCheck) headAllowed(nodes []wnode) (string, bool) {
	if nodes[0].isPunct("(") {
		return "", true // (SELECT …) UNION (SELECT …)
	}
	head := nodes[0].word()
	if c.tidb && tidbTableHead(nodes) {
		return "", true
	}
	if c.oracle && (head == "EXEC" || head == "EXECUTE") {
		return "", len(nodes) > 1 && nodes[1].word() != "IMMEDIATE"
	}
	if !windowHeads[head] {
		return "", false
	}
	next := ""
	if len(nodes) > 1 {
		next = nodes[1].word()
	}
	switch head {
	case "SET":
		// 只放会话级的设置;SET GLOBAL / PASSWORD / ROLE 越出了这个库
		return "", SessionScoped(c.raw) && !alterSessionRe.MatchString(StripComments(c.raw))
	case "CREATE", "ALTER", "DROP":
		kind := objectKind(nodes, 1)
		if c.oracle && head == "ALTER" && kind == "SESSION" {
			return kind, oracleSessionSafeRe.MatchString(StripComments(c.raw))
		}
		return kind, windowObjectKinds[kind]
	case "RENAME":
		return "", next != "USER"
	case "LOCK":
		return "", next == "TABLE" || next == "TABLES"
	}
	return "", true
}

// laterHeadAllowed 判 `;` 之后那一句的头。存储过程体、PL/SQL 块里有 `;`,头不在语句白名单里
// 也正常(END、IF、RETURN……),所以这里只拦明确越库的那几种。
func (c *scopeCheck) laterHeadAllowed(rest []wnode) bool {
	if len(rest) == 0 {
		return true
	}
	head := rest[0].word()
	if windowDeniedLaterHeads[head] {
		return false
	}
	switch head {
	case "CREATE", "ALTER", "DROP":
		switch objectKind(rest, 1) {
		case "DATABASE", "SCHEMA", "USER", "ROLE", "SESSION", "SYSTEM", "TABLESPACE", "SERVER", "PUBLIC":
			return false
		}
	}
	return true
}

// objectKind 跳过修饰词,取 CREATE / ALTER / DROP 后面的对象种类(大写)。
func objectKind(nodes []wnode, i int) string {
	for ; i < len(nodes); i++ {
		n := nodes[i]
		if n.kind == wtPunct || n.kind == wtString || n.kind == wtNumber {
			continue // DEFINER = 'u'@'%'、CURRENT_USER()
		}
		if i > 0 && (nodes[i-1].isPunct("=") || nodes[i-1].isPunct("@")) {
			continue // DEFINER = root@localhost、ALGORITHM = MERGE
		}
		w := n.word()
		if windowKindModifiers[w] {
			continue
		}
		return w
	}
	return ""
}

// tablePositions 标出每个名字是不是落在表的位置上。
func (c *scopeCheck) tablePositions(nodes []wnode, kind string) []bool {
	pos := make([]bool, len(nodes))
	depth := 0
	list := map[int]bool{}
	onIsTable := kind == "INDEX" || kind == "TRIGGER" // CREATE INDEX i ON t / CREATE TRIGGER … ON t
	for i, n := range nodes {
		if n.kind == wtPunct {
			switch n.text {
			case "(":
				depth++
				list[depth] = false
			case ")":
				delete(list, depth)
				if depth > 0 {
					depth--
				}
			case ";":
				list = map[int]bool{}
				depth = 0
			}
			continue
		}
		if n.kind != wtWord {
			continue
		}
		var prev wnode
		if i > 0 {
			prev = nodes[i-1]
		}
		pw := prev.word()
		switch {
		case pw == "UPDATE" && i > 1 && nodes[i-2].word() == "KEY":
			// ON DUPLICATE KEY UPDATE t1.a = …:后面是赋值,不是表
		case windowTableWords[pw]:
			pos[i] = true
		case pw == "ON" && onIsTable:
			pos[i] = true
			onIsTable = false
		case prev.isPunct(",") && list[depth]:
			pos[i] = true
		}
		w := n.word()
		switch {
		case w == "UPDATE" && pw == "KEY":
			list[depth] = false
		case windowListWords[w]:
			list[depth] = true
		case windowListStops[w]:
			list[depth] = false
		}
	}
	return pos
}

// declaredNames 收集这条语句自己声明的表名与别名 —— 表达式里 `X.col` 的 X 只能是它们。
func (c *scopeCheck) declaredNames(nodes []wnode, tablePos []bool) map[string]bool {
	d := map[string]bool{"NEW": true, "OLD": true} // 触发器里的 NEW.col / :NEW.col
	add := func(p wpart) { d[strings.ToUpper(p.s)] = true }
	alias := func(j int) {
		if j < len(nodes) && nodes[j].word() == "AS" {
			j++
		}
		if j < len(nodes) && nodes[j].kind == wtWord && len(nodes[j].parts) == 1 && !windowAliasStops[nodes[j].word()] {
			add(nodes[j].parts[0])
		}
	}
	for i, n := range nodes {
		switch {
		case n.kind == wtWord && tablePos[i]:
			add(n.parts[len(n.parts)-1])
			alias(i + 1)
		case n.isPunct(")"):
			alias(i + 1) // 派生表 (SELECT …) AS x、VALUES (…) AS new
		}
		// CTE:name AS ( … )
		if n.kind == wtWord && len(n.parts) == 1 && i+2 < len(nodes) && nodes[i+1].word() == "AS" && nodes[i+2].isPunct("(") {
			add(n.parts[0])
		}
	}
	return d
}

// oracleDynamicPackages:执行动态 SQL、DDL 或提交作业的 Oracle 系统包。窗口放行"包.成员"时它们例外。
var oracleDynamicPackages = map[string]bool{
	"DBMS_SQL": true, "DBMS_SYS_SQL": true, "DBMS_UTILITY": true, "DBMS_DDL": true,
	"DBMS_SCHEDULER": true, "DBMS_JOB": true, "DBMS_IJOB": true, "DBMS_PARALLEL_EXECUTE": true,
	"DBMS_REDEFINITION": true, "DBMS_ISCHED": true, "DBMS_AQADM": true,
}

// qualifiedOK 判一个带限定的名字。
func (c *scopeCheck) qualifiedOK(nodes []wnode, i int, tablePos bool, declared map[string]bool) bool {
	n := nodes[i]
	parts := n.parts
	if i > 0 && nodes[i-1].isPunct("@") {
		return true // MySQL 的 @@session.x / @var:是变量,不是对象
	}
	var next wnode
	if i+1 < len(nodes) {
		next = nodes[i+1]
	}
	last := strings.ToUpper(parts[len(parts)-1].s)
	if (last == "NEXTVAL" || last == "CURRVAL") && !tablePos {
		parts = parts[:len(parts)-1] // seq.NEXTVAL:剩下的是序列名
		if len(parts) < 2 {
			return true
		}
		return c.isDB(parts[0])
	}
	if next.isPunct("%") && !tablePos && len(parts) == 2 {
		return true // Oracle 的 t.col%TYPE:只取类型
	}
	if i >= 3 && nodes[i-1].word() == "COLUMN" && nodes[i-2].word() == "ON" && nodes[i-3].word() == "COMMENT" {
		// COMMENT ON COLUMN t.col:两段是"表.列"(表在当前库),三段是"库.表.列"。
		// 从前按第 4 条当成"别库.对象",建表脚本里的列注释让整批在班车里照样送审(2026-10-26 AP-2643)。
		return len(parts) == 2 || c.isDB(parts[0])
	}
	if c.oracle && len(parts) == 2 && oracleDynamicPackages[strings.ToUpper(parts[0].s)] {
		// 能拿字符串执行任意语句 / 建作业的系统包:与 EXECUTE IMMEDIATE 同一回事,真正执行的语句看不见
		// (2026-10-26 复查:DBMS_UTILITY.EXEC_DDL_STATEMENT('DROP TABLE hr.emp') 免审批放行了)。
		return false
	}
	if c.oracle && len(parts) == 2 && (!tablePos || (i > 0 && nodes[i-1].word() == "CALL")) {
		// Oracle 里表达式位置上的两段名几乎都是"包.成员":pk_util.f(1)、DBMS_STATS.GATHER_TABLE_STATS(…)、
		// pk_const.c_max,或者记录 / 游标的字段 r.id。a8d3768 按"第一段是库"判,班车里调用包的 DML、
		// 匿名块全部丢了窗口(2026-10-26 回归排查)。表位置上的名字(FROM / INTO / UPDATE …)与
		// 三段名 schema.pkg.f 照旧要求第一段是窗口 schema。
		// 认不出的一种:别的 schema 里的独立过程 hr.proc(…) —— 与"库里已有对象的函数体"同一类,判不出来。
		return true
	}
	if tablePos || next.isPunct("(") || len(parts) >= 3 {
		// 表的位置、函数 / 过程调用、三段名:第一段是库
		return c.isDB(parts[0])
	}
	q := parts[0]
	return c.isDB(q) || declared[strings.ToUpper(q.s)]
}
