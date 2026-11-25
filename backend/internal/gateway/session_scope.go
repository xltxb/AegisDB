package gateway

// 会话级设置 —— 只影响当前连接的语句。
//
// 它们既不读数据也不写数据,但按动词判会落进 write(`SET`)甚至 ddl
// (Oracle 的 `ALTER SESSION`)。后果很具体:一个只读分析师在 DWS 上想跑
// `SET search_path TO dwd; SELECT …`,第一句就被拦下或推去审批 —— 而设 schema
// 恰恰是读数据之前必须做的那一步。
//
// 放宽只放宽到**会话边界**为止。同一个 `SET` 关键字底下藏着影响面完全不同的东西:
//
//	SET search_path TO dwd        只影响这条连接
//	SET GLOBAL read_only = 0      改整个服务器
//	SET PASSWORD FOR … = '…'      改口令
//	SET ROLE dba_admin            提权
//	ALTER SESSION SET …           只影响这条连接
//	ALTER SYSTEM SET …            改整个实例
//
// 所以这里不是"SET 放行",而是逐条认出**确实只作用于本会话**的那些,其余一律
// 走原来的判定 —— 认不出来就当成没放宽,这是安全的方向。
//
// 它和 PlanOnly 是同一个形状:一个关于"这条语句到底做了什么"的判断,由判定链在
// 能力矩阵之前短路使用,而不去改动 ParseVerb 返回的动词。动词仍然是 SET/ALTER,
// 因为它确实是 —— 别处(只读导出的把关、发布单的 DML/DDL 归类)靠的就是那个动词。

import (
	"regexp"
	"strings"
)

var (
	// SET 后面跟这些词时,影响面超出当前会话。
	//
	//   GLOBAL / PERSIST / PERSIST_ONLY  MySQL:改服务器,PERSIST 还会写进配置文件
	//   PASSWORD                         MySQL:改口令
	//   ROLE / SESSION AUTHORIZATION     PostgreSQL:换掉当前身份,是提权动作
	//
	// 这条正则曾经有两个洞,都是"只看语句开头的第一个赋值"造成的:
	//
	//   SET @@GLOBAL.read_only = 0        作用域写在变量名上,SET 后面紧跟的是 @@
	//   SET SESSION a=1, GLOBAL b=2       MySQL 允许混写,逃逸词在第二个赋值上
	//
	// 两条都会让 SessionScoped 误判为"只影响本会话",于是只需 select 能力就能改
	// **整个服务器**(关全局只读、关 binlog 掩盖写入)。所以现在不锚定开头,而是
	// 在整条语句里找作用域标记 —— 逗号/括号/空白后面的关键字形式,以及 @@ 形式。
	//
	// 宁可多认:一条 SET @x = 'GLOBAL' 会被判成逃逸,于是走原来的判定(按写处理)。
	// 多拦一条无害的会话设置,好过放过一条改服务器的语句。
	setEscapesSessionRe = regexp.MustCompile(
		`(?is)(^\s*SET\s+|[\s,(])(GLOBAL|PERSIST|PERSIST_ONLY|PASSWORD|ROLE|SESSION\s+AUTHORIZATION)\b` +
			`|@@\s*(GLOBAL|PERSIST|PERSIST_ONLY)\s*\.`)
	// 一条普通的会话设置:SET [SESSION|LOCAL] …
	setSessionRe = regexp.MustCompile(`(?is)^\s*SET\b`)
	// MariaDB / Percona 的 SET STATEMENT a=1 FOR <语句>:执行的是 <语句>,不是一条会话设置。
	// \b 不会命中 PostgreSQL 的 SET statement_timeout(下划线是单词字符)。
	setStatementRe = regexp.MustCompile(`(?is)^\s*SET\s+STATEMENT\b`)
	forWordRe      = regexp.MustCompile(`(?i)\bFOR\b`)
	// SET STATEMENT 能盖掉只读守卫在会话上设的这两个变量(见 readonly_guard.go)。
	setStatementGuardVarRe = regexp.MustCompile(`(?i)\b(transaction_read_only|tx_read_only)\b`)
	// Oracle:ALTER SESSION 只影响本连接;ALTER SYSTEM 影响整个实例。
	alterSessionRe = regexp.MustCompile(`(?is)^\s*ALTER\s+SESSION\b`)
)

// SessionScoped reports whether sql only configures the CURRENT session.
//
// 判断读的是剥掉注释后的文本:`/* x */ SET GLOBAL …` 和 `SET GLOBAL …` 必须得到
// 同一个答案,否则注释就成了绕过这条判断的办法。
func SessionScoped(sql string) bool {
	s := strings.TrimSpace(StripComments(sql))
	if alterSessionRe.MatchString(s) {
		return true
	}
	if !setSessionRe.MatchString(s) || setStatementRe.MatchString(s) {
		return false
	}
	return !setEscapesSessionRe.MatchString(s)
}

// setStatementInners 解开 SET STATEMENT … FOR <语句>,返回要判的内层语句(不是这种写法时为空)。
//
// 判定必须按内层来:它是真正要执行的东西,外层只是给这一条语句临时改几个变量。两种字面量理解
// (backslash.go)都解,边界可能不同:a='\'' FOR DROP TABLE t #' 在 MySQL 默认模式下 FOR 不在字符串里。
// 没有顶层 FOR 时返回空,调用方按原来的动词(SET → write)判。
func setStatementInners(sql string) []string {
	var out []string
	for _, r := range sqlReadings(sql) {
		s := strings.TrimSpace(StripComments(r))
		if !setStatementRe.MatchString(s) {
			continue
		}
		if i := topLevelFor(blankQuoted(s)); i >= 0 {
			if inner := strings.TrimSpace(s[i+len("FOR"):]); inner != "" && !contains(out, inner) {
				out = append(out, inner)
			}
		}
	}
	return out
}

// topLevelFor 找第一个不在括号里的 FOR(masked 已抹掉字面量,与原文等长)。
func topLevelFor(masked string) int {
	for _, loc := range forWordRe.FindAllStringIndex(masked, -1) {
		depth := 0
		for _, c := range masked[:loc[0]] {
			switch c {
			case '(':
				depth++
			case ')':
				depth--
			}
		}
		if depth == 0 {
			return loc[0]
		}
	}
	return -1
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
