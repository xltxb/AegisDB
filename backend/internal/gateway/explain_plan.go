package gateway

// EXPLAIN 只取执行计划,绝不真的执行被解释的语句(2026-10 用户要求,各引擎都适用)。
//
// 各引擎里"会执行"的 EXPLAIN 写法不少,判定层(parseVerbExplain)会按里面的语句判风险;
// 这里是执行层:真正发给数据库之前,把会执行的写法改成只出计划的写法。
//
//	MySQL / TiDB / PolarDB   EXPLAIN|DESC|DESCRIBE ANALYZE …  →  去掉 ANALYZE
//	MariaDB                  ANALYZE [FORMAT=JSON] <语句>     →  EXPLAIN [FORMAT=JSON] <语句>
//	PostgreSQL / DWS / Gauss EXPLAIN ANALYZE|ANALYSE|PERFORMANCE …、EXPLAIN (ANALYZE, …) …
//	                                                          →  去掉执行类选项
//	Oracle                   EXPLAIN PLAN FOR …:本身就不执行,但只把计划写进 PLAN_TABLE、
//	                         不返回结果 —— 在同一会话里接着用 DBMS_XPLAN.DISPLAY 读出来
//	SQLite                   EXPLAIN / EXPLAIN QUERY PLAN 本来就不执行
//
// 改写完再用判定层核一遍:改写后的语句必须是"只出计划"(PlanOnly),否则拒绝执行 ——
// 改写漏掉某种写法时,宁可报错,也不让它真的执行。
//
// 例外(2026-10 用户要求):被解释的是**纯只读查询**时保留 ANALYZE,照原样执行 —— 跑一遍
// SELECT 与直接执行它是一回事,而看实际行数 / 耗时正是 EXPLAIN ANALYZE 的用处。"纯只读"
// 见 analyzeIsReadOnly:带锁、INTO、有副作用的函数都不算,照旧去掉 ANALYZE。

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"

	"velagateway/internal/apimsg"
)

// planOnlyNotice 是改写之后告诉用户的那句话(规范中文;界面按 RuleRef 用读者的语言)。
const planOnlyNotice = "网关只取执行计划,不执行语句:已去掉会真实执行的 ANALYZE / PERFORMANCE 选项"

// leadingNoise 是语句开头的空白与注释(保留原样,不改写)。
var leadingNoise = regexp.MustCompile(`^(?:\s+|/\*[\s\S]*?\*/|--[^\n]*\n?|#[^\n]*\n?)*`)

// 执行类的 EXPLAIN 选项:去掉它们之后语句就只出计划。TIMING / BUFFERS / WAL / SERIALIZE
// 在 PostgreSQL(及 DWS 的老版本)里只能配合 ANALYZE 用,单独留下会报错,一并去掉。
var pgExecutingOpts = map[string]bool{
	"ANALYZE": true, "ANALYSE": true, "PERFORMANCE": true,
	"TIMING": true, "BUFFERS": true, "WAL": true, "SERIALIZE": true, "CPU": true, "DETAIL": true,
}

// planOnlySQL 把会真实执行的 EXPLAIN 改写成只出计划的写法;changed=false 表示原样即可。
// err 非空:是 EXPLAIN,但没能改成只出计划 —— 调用方必须拒绝执行。
func planOnlySQL(engine, query string) (out string, changed bool, err error) {
	head := leadingNoise.FindString(query)
	body := query[len(head):]
	lead := strings.ToUpper(firstWord(body))
	if lead != "EXPLAIN" && lead != "DESC" && lead != "DESCRIBE" && lead != "ANALYZE" {
		return query, false, nil
	}
	_, planOnly := parseVerbExplain(query)
	if planOnly {
		return query, false, nil // 本来就只出计划
	}
	if lead != "EXPLAIN" && !explainHead(strings.TrimSpace(body[len(lead):])) {
		return query, false, nil // DESC t、ANALYZE TABLE t:不是 EXPLAIN
	}
	if analyzeIsReadOnly(query) {
		return query, false, nil // 解释的是纯只读查询:让它真跑,拿实际执行数据
	}
	var rewritten string
	switch engineFamily(engine) {
	case familyMySQL:
		rewritten = mysqlPlanOnly(body, lead)
	case familyPostgres:
		rewritten = pgPlanOnly(body)
	default:
		rewritten = body
	}
	out = head + rewritten
	if _, ok := parseVerbExplain(out); !ok {
		return query, false, apimsg.Errorf("这条 EXPLAIN 会真实执行语句,网关无法把它改成只取执行计划;请去掉 ANALYZE / PERFORMANCE 后重试")
	}
	return out, true, nil
}

// analyzeIsReadOnly:会执行的 EXPLAIN 里被解释的语句是不是纯只读查询(SELECT / WITH / VALUES /
// TABLE,且不改数据)。下面这些虽然以 SELECT 开头,真跑起来却有副作用,一律不算:
//
//	FOR UPDATE / FOR SHARE / LOCK IN SHARE MODE   加锁,在生产库上会挡住别人的写
//	INTO …                                        OUTFILE 写服务器文件;PostgreSQL 的 SELECT INTO 建新表
//	SLEEP / GET_LOCK / NEXTVAL / DBLINK / UTL_* … 睡眠、加锁、改序列、跨库、Oracle 工具包
//
// 两种读法都要过:去掉注释后(INTO/**/OUTFILE),以及保留注释(MySQL 的 `--1` 不是注释、
// /*!50000 … */ 会被执行)—— 与 WritesServerFile 同一个立场,误判只会多去掉一次 ANALYZE。
func analyzeIsReadOnly(query string) bool {
	switch ParseVerb(query) {
	case "SELECT", "WITH", "VALUES", "TABLE":
	default:
		return false
	}
	if !IsRead(query) {
		return false
	}
	for _, r := range sqlReadings(query) {
		if EffectfulCall(r) != "" {
			return false
		}
		// 第三份:只抹字符串、去掉标识符引号 —— blankQuoted 连 "pg_sleep"(100) 里的函数名一起抹了,
		// 带引号的函数名就这样从清单底下溜过去,EXPLAIN ANALYZE 真去睡 100 秒(2026-10 审查)。
		// 这份读法**只查函数清单**,与 ReadSideEffect 同口径:关键字(INTO / FOR UPDATE)放在这份上判,
		// MySQL 双引号字符串 "check into it"、反引号列名 `into` 都被当成了语法,只读查询被去掉 ANALYZE
		// (2026-10-26 讨论 15)。
		unquoted := strings.NewReplacer(`"`, " ", "`", " ", "[", " ", "]", " ").Replace(blankStringLiterals(StripComments(r)))
		if bizFuncRe.MatchString(unquoted) {
			return false
		}
		for _, text := range []string{blankQuoted(StripComments(r)), blankQuoted(r)} {
			if bizLockRe.MatchString(text) || bizIntoRe.MatchString(text) || bizFuncRe.MatchString(text) {
				return false
			}
		}
	}
	return true
}

// mysqlPlanOnly:EXPLAIN|DESC|DESCRIBE ANALYZE … 去掉 ANALYZE;MariaDB 的 ANALYZE <语句> 换成 EXPLAIN。
func mysqlPlanOnly(body, lead string) string {
	rest := strings.TrimLeft(body[len(lead):], " \t\r\n")
	if lead == "ANALYZE" {
		return "EXPLAIN " + rest
	}
	if strings.EqualFold(firstWord(rest), "ANALYZE") {
		rest = strings.TrimLeft(rest[len("ANALYZE"):], " \t\r\n")
	}
	return body[:len(lead)] + " " + rest
}

// pgPlanOnly:EXPLAIN 之后的裸选项去掉 ANALYZE / ANALYSE / PERFORMANCE(VERBOSE 留着),
// 括号选项列表里去掉执行类选项;列表空了连括号一起去掉。
func pgPlanOnly(body string) string {
	rest := strings.TrimLeft(body[len("EXPLAIN"):], " \t\r\n")
	var kept []string
	for {
		w := strings.ToUpper(firstWord(rest))
		if w == "ANALYZE" || w == "ANALYSE" || w == "PERFORMANCE" {
			rest = strings.TrimLeft(rest[len(w):], " \t\r\n")
			continue
		}
		if w == "VERBOSE" {
			kept = append(kept, "VERBOSE")
			rest = strings.TrimLeft(rest[len(w):], " \t\r\n")
			continue
		}
		break
	}
	if _, isQuery := parenQuery(rest); !isQuery && strings.HasPrefix(rest, "(") { // (SELECT …) 是语句,不是选项列表
		if i := strings.Index(rest, ")"); i > 0 {
			var opts []string
			for _, o := range strings.Split(rest[1:i], ",") {
				o = strings.TrimSpace(o)
				if o != "" && !pgExecutingOpts[strings.ToUpper(firstWord(o))] {
					opts = append(opts, o)
				}
			}
			rest = strings.TrimLeft(rest[i+1:], " \t\r\n")
			if len(opts) > 0 {
				kept = append(kept, "("+strings.Join(opts, ", ")+")")
			}
		}
	}
	return strings.Join(append(append([]string{"EXPLAIN"}, kept...), rest), " ")
}

// ---- Oracle:EXPLAIN PLAN 之后读回计划 ----

var (
	reOraExplainPlan = regexp.MustCompile(`(?is)^EXPLAIN\s+PLAN\b`)
	reOraStatementID = regexp.MustCompile(`(?is)\bSET\s+STATEMENT_ID\s*=\s*'((?:[^']|'')*)'`)
	reOraInto        = regexp.MustCompile(`(?is)\bINTO\s+((?:"[^"]+"|[A-Za-z0-9_$#]+)(?:\s*\.\s*(?:"[^"]+"|[A-Za-z0-9_$#]+))?)\s+FOR\b`)
)

func isOracleExplainPlan(query string) bool {
	return reOraExplainPlan.MatchString(query[len(leadingNoise.FindString(query)):])
}

// oracleTerminalExplainPlan 执行 EXPLAIN PLAN(必要时补上 STATEMENT_ID),再用 DBMS_XPLAN.DISPLAY 读出
// 这一条的计划。runner 必须是钉住的同一个会话:PLAN_TABLE 是会话级临时表(见 realRun)。
func oracleTerminalExplainPlan(ctx context.Context, runner sqlRunner, query string) (ExecResult, error) {
	head := leadingNoise.FindString(query)
	body := strings.TrimRight(strings.TrimSpace(query[len(head):]), ";")
	id := ""
	if m := reOraStatementID.FindStringSubmatch(body); m != nil {
		id = strings.ReplaceAll(m[1], "''", "'")
	} else {
		var b [6]byte
		_, _ = rand.Read(b[:])
		id = "vela_" + hex.EncodeToString(b[:])
		loc := reOraExplainPlan.FindStringIndex(body)
		body = body[:loc[1]] + " SET STATEMENT_ID = '" + id + "'" + body[loc[1]:]
	}
	table := "PLAN_TABLE"
	if m := reOraInto.FindStringSubmatch(body); m != nil {
		table = m[1]
	}
	if _, err := runner.ExecContext(ctx, body); err != nil {
		return ExecResult{}, err
	}
	rows, err := runner.QueryContext(ctx, "SELECT PLAN_TABLE_OUTPUT FROM TABLE(DBMS_XPLAN.DISPLAY(:1, :2, 'TYPICAL'))", table, id)
	if err != nil {
		return ExecResult{}, apimsg.Errorf("读取执行计划: %w", err)
	}
	defer rows.Close()
	var data [][]string
	for rows.Next() {
		var line *string
		if err := rows.Scan(&line); err != nil {
			return ExecResult{}, err
		}
		v := ""
		if line != nil {
			v = *line
		}
		data = append(data, []string{v})
	}
	if err := rows.Err(); err != nil {
		return ExecResult{}, err
	}
	return ExecResult{Output: fmt.Sprintf("+ %s rows", thousands(len(data))), Rows: len(data),
		Columns: []string{"PLAN_TABLE_OUTPUT"}, Data: data}, nil
}
