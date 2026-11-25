// Package sqlparse 是网关唯一接触 SQL 解析器的地方:按方言选解析器,把上游各不相同的 AST
// 收敛成 Statement。上游类型不外露,换解析器只改这个包。
//
// 这里的结果是**旁证,不是裁决**。解析器读的是标准语法,不等于目标服务器的读法:三个 MySQL 系解析器都
// 把 `/*M! DROP TABLE t */` 读成 0 条语句,MariaDB 却会执行注释体。所以调用方只能拿它收紧现有判定,
// 不能拿它放宽,解析失败或读出 0 条语句时维持现有判定。见 docs/adr/0019-sql-parser-additive.md。
package sqlparse

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// 方言代码,取值与 review.DialectFor 的结果相同。review 以后要依赖本包,所以这里不反过来引用它。
const (
	DialectMySQL  = "mysql"
	DialectTiDB   = "tidb"
	DialectOracle = "oracle"
	DialectDWS    = "dws"
)

// MaxStatementBytes 限制单条语句的长度,超过的不解析。入参应是分句之后的一条语句。
const MaxStatementBytes = 1 << 20

// Status 是一次解析的结局。
type Status int

const (
	// StatusOK 解析成功。Statements 可能为空:文本里只有解析器眼中的注释。
	StatusOK Status = iota
	// StatusUnsupported 没有解析:方言没有解析器、语句超长或不是合法 UTF-8。
	StatusUnsupported
	// StatusError 解析器报语法错误或 panic。不代表语句非法,可能只是解析器不认这种写法。
	StatusError
)

func (s Status) String() string {
	switch s {
	case StatusOK:
		return "ok"
	case StatusUnsupported:
		return "unsupported"
	}
	return "error"
}

// Kind 是语句的大类。
type Kind string

const (
	KindSelect  Kind = "select"
	KindInsert  Kind = "insert"
	KindUpdate  Kind = "update"
	KindDelete  Kind = "delete"
	KindMerge   Kind = "merge"
	KindDDL     Kind = "ddl"
	KindDCL     Kind = "dcl"
	KindRead    Kind = "read"    // SHOW / DESC 这类元数据读取
	KindSession Kind = "session" // SET / USE
	KindTxn     Kind = "txn"     // BEGIN / COMMIT / ROLLBACK
	KindCall    Kind = "call"
	KindPLSQL   Kind = "plsql" // Oracle 匿名块
	KindOther   Kind = "other"
)

// Table 是语句里出现的一张表。CTE 的名字也会出现在这里,解析器不做名字解析。
// Oracle 未加引号的标识符按服务器的规则折成大写。
type Table struct {
	Schema string
	Name   string
}

// Statement 是一条语句收敛后的样子。
type Statement struct {
	Kind Kind
	// Verb 是大写动词。EXPLAIN 取被解释的语句的动词,与 gateway.ParseVerb 相同。
	Verb   string
	Tables []Table
	// HasWhere 只说最外层有没有 WHERE,不判断 WHERE 是否恒真。
	HasWhere bool
	// ReadOnly 指语法上不写数据:SELECT 不带 INTO 和锁、SHOW、DESC、只出计划的 EXPLAIN。
	// 看不见函数的副作用,`SELECT f()` 和 `SELECT SLEEP(5)` 在这里也是 true。
	ReadOnly bool
	// PlanOnly 是不带 ANALYZE 的 EXPLAIN。
	PlanOnly bool
	// WritesFile 是 SELECT … INTO OUTFILE / DUMPFILE,MySQL 系才有。
	WritesFile bool
}

// Result 是 Parse 的结果。
type Result struct {
	Status     Status
	Statements []Statement
	Err        string
}

// Parse 解析一条已经分好句的语句。不会 panic。
func Parse(dialect, sql string) (res Result) {
	var parse func(string) ([]Statement, error)
	switch dialect {
	case DialectTiDB:
		parse = parseTiDB
	case DialectMySQL:
		parse = parseMySQL
	case DialectOracle:
		parse = parseOracle
	default:
		return Result{Status: StatusUnsupported, Err: "方言没有解析器: " + dialect}
	}
	if len(sql) > MaxStatementBytes {
		return Result{Status: StatusUnsupported, Err: "语句超长"}
	}
	if !utf8.ValidString(sql) {
		return Result{Status: StatusUnsupported, Err: "不是合法 UTF-8"}
	}
	// 解析器在极端输入上会很慢(Oracle 上 5 万层 CASE 要 11 秒),而调用方在请求路径上。goroutine 杀不掉,
	// 所以超时后不等它,让它跑完自己退出;占着名额的最多 maxInFlight 个,名额满了就不解析。
	select {
	case inFlight <- struct{}{}:
	default:
		return Result{Status: StatusUnsupported, Err: "解析器繁忙"}
	}
	done := make(chan Result, 1)
	go func() {
		defer func() { <-inFlight }()
		done <- runParser(parse, sql)
	}()
	timer := time.NewTimer(parseTimeout)
	defer timer.Stop()
	select {
	case res = <-done:
		return res
	case <-timer.C:
		return Result{Status: StatusError, Err: "解析超时"}
	}
}

func runParser(parse func(string) ([]Statement, error), sql string) (res Result) {
	defer func() {
		if r := recover(); r != nil {
			res = Result{Status: StatusError, Err: fmt.Sprintf("解析器 panic: %v", r)}
		}
	}()
	stmts, err := parse(sql)
	if err != nil {
		return Result{Status: StatusError, Err: err.Error()}
	}
	return Result{Status: StatusOK, Statements: stmts}
}

const (
	parseTimeout = 2 * time.Second
	maxInFlight  = 64
)

var inFlight = make(chan struct{}, maxInFlight)

// kindOf 按动词归类。
func kindOf(verb string) Kind {
	switch verb {
	case "SELECT":
		return KindSelect
	case "INSERT", "REPLACE":
		return KindInsert
	case "UPDATE":
		return KindUpdate
	case "DELETE":
		return KindDelete
	case "MERGE":
		return KindMerge
	case "CREATE", "ALTER", "DROP", "RENAME", "TRUNCATE", "FLASHBACK", "PURGE":
		return KindDDL
	case "GRANT", "REVOKE":
		return KindDCL
	case "SHOW", "DESC", "DESCRIBE":
		return KindRead
	case "SET", "USE":
		return KindSession
	case "BEGIN", "START", "COMMIT", "ROLLBACK", "SAVEPOINT", "RELEASE":
		return KindTxn
	case "CALL", "EXEC", "EXECUTE":
		return KindCall
	}
	return KindOther
}

// verbFromType 从 AST 类型名取动词:CreateTableStmt → CREATE。三个解析器都按这种规则给语句类型起名,
// 用类型名而不是原文开头,才谈得上"解析器的读法"。
func verbFromType(name string) string {
	for i, r := range name {
		if i > 0 && r >= 'A' && r <= 'Z' {
			return strings.ToUpper(name[:i])
		}
	}
	return strings.ToUpper(name)
}

// tableSet 按出现顺序收集表名并去重。
type tableSet struct {
	seen map[Table]bool
	list []Table
}

func (s *tableSet) add(schema, name string) {
	if name == "" {
		return
	}
	t := Table{Schema: schema, Name: name}
	if s.seen == nil {
		s.seen = map[Table]bool{}
	}
	if !s.seen[t] {
		s.seen[t] = true
		s.list = append(s.list, t)
	}
}
