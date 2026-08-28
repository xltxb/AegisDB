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
	setEscapesSessionRe = regexp.MustCompile(`(?is)^\s*SET\s+(GLOBAL|PERSIST|PERSIST_ONLY|PASSWORD|ROLE|SESSION\s+AUTHORIZATION)\b`)
	// 一条普通的会话设置:SET [SESSION|LOCAL] …
	setSessionRe = regexp.MustCompile(`(?is)^\s*SET\b`)
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
	if !setSessionRe.MatchString(s) {
		return false
	}
	return !setEscapesSessionRe.MatchString(s)
}
