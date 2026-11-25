package gateway

// 一条命令对**数据**做了什么:增 / 删 / 改 / 查。
//
// 这是和「事件类型」(intercept / approve / exec / login,见 model.AuditEventTypes)正交的
// 另一个维度:事件类型回答"这一步在网关的流程里是什么",这里回答"它动了数据没有、怎么动的"。
// 两个都要,谁也替不了谁 —— 一条 approve 可能放行的是 DROP,也可能是 SELECT。
//
// ## 为什么不沿用原来那段
//
// 原先只有 webhook 里一个 eventAction:取整条命令的**第一个词**比对四个 switch 分支,
// 认不出的一律当成"查"。三处错得很具体:
//
//   登录、配置变更、流程标签      全被报成"查"。一次登录不是一次查询。
//   EXPLAIN ANALYZE DELETE        PostgreSQL 上它真的删,而"第一个词"看到的是 EXPLAIN。
//   多语句脚本                    只看第一条。首条是 SELECT、后面跟着 DROP 的脚本被报成"查"。
//
// 所以这里改成建立在 ParseVerb 之上 —— 它已经处理了注释、引号、CTE(WITH 里藏着的 DELETE
// 真的会删)和 EXPLAIN 拆包,那些都是踩过坑写出来的(见 cte_verb_test.go)。再造一个只会
// 把同样的坑重踩一遍。

import (
	"strings"

	"aegisdb/pkg/sqlutil"
)

// 四类,加一个"不是对数据的操作"。取值直接对应事件中心 action 枚举里的四个
// (create=增 / read=查 / update=改 / delete=删),ActionOther 是我们自己多出来的一类。
const (
	ActionCreate = "create"
	ActionRead   = "read"
	ActionUpdate = "update"
	ActionDelete = "delete"
	// ActionOther:登录、配置变更、流程标签,以及认不出的语句。
	//
	// 它和"空"不是一回事:空表示这一行写下时还没有这一列(迁移 0040 之前);other 表示
	// **归过类了,结论是它不属于增删改查**。把这两种混成一个,就分不清"没记"和"记了、
	// 但不是数据操作"。
	ActionOther = "other"
)

// actionRank 决定一段多语句脚本取哪一条的结论:删 > 改 > 增 > 查 > 其它。
//
// 取"最重的那一条",因为这一列要回答的是"这条命令最重做了什么"。一个含 DROP 的脚本必须
// 能在"删"里被找到 —— 按第一条算的话它会躲在"查"里,而那正是事后追查最不能出的错。
// 与判定层对批量的口径一致:逐条看,最严的那条说了算。
var actionRank = map[string]int{
	ActionOther:  0,
	ActionRead:   1,
	ActionCreate: 2,
	ActionUpdate: 3,
	ActionDelete: 4,
}

// verbAction 把一个已经解析好的动词映射到四类之一。认不出的返回 ActionOther —— 不兜底成
// "查",那是原来那段最大的毛病:它让一堆根本不是查询的东西看起来像查询。
func verbAction(verb string) string {
	switch strings.ToUpper(strings.TrimSpace(verb)) {
	case "INSERT", "CREATE":
		return ActionCreate
	case "DELETE", "DROP", "TRUNCATE":
		return ActionDelete
	// REPLACE 与 MERGE 都是"有则改、无则增"。归到改:它们会**覆盖已有的行**,而这正是
	// 事后要找的那件事;归到增会让一次覆盖从"改"的结果里消失。
	// GRANT / REVOKE 改的是权限,同样是改。
	case "UPDATE", "ALTER", "REPLACE", "MERGE", "RENAME", "COMMENT", "GRANT", "REVOKE":
		return ActionUpdate
	case "SELECT", "SHOW", "DESC", "DESCRIBE", "EXPLAIN", "TABLE", "VALUES", "FETCH", "HELP":
		return ActionRead
	}
	return ActionOther
}

// StatementAction 判一条语句。
func StatementAction(sql string) string {
	if strings.TrimSpace(sql) == "" {
		return ActionOther
	}
	// SCHEMA-DIFF 是网关自己记的"读了两个库的结构"(见 service/schemadiff.go):读结构,不改任何东西。
	// 按整个前缀认,不把 SCHEMA 当成读动词 —— 那会误伤别的东西。
	if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(sql)), "SCHEMA-DIFF ") {
		return ActionRead
	}
	// EXPLAIN(不带 ANALYZE)只出计划、什么都不执行 —— 它是查,不管包着的是什么。
	// 带 ANALYZE 的那种真的会跑,ParseVerb 会把里面的动词拆出来,于是走下面的常规路径。
	if PlanOnly(sql) {
		return ActionRead
	}
	return verbAction(ParseVerb(sql))
}

// DataAction 判一整条审计命令 —— 它可能是一段多语句脚本。
//
// 逐条判,取最重的一条(见 actionRank)。拆不出语句(整段都是空白/注释)时返回 ActionOther。
func DataAction(sql string) string {
	stmts := sqlutil.SplitStatements(sql)
	if len(stmts) == 0 {
		return StatementAction(sql)
	}
	best := ActionOther
	for _, st := range stmts {
		if a := StatementAction(st); actionRank[a] > actionRank[best] {
			best = a
		}
	}
	return best
}

// IsDataAction 认得出才算数 —— 供筛选白名单用。ActionOther 也算认得出:它是一个真实的
// 结论("这不是对数据的操作"),不是"没归类"。
func IsDataAction(s string) bool {
	_, ok := actionRank[s]
	return ok
}

// DataActions 是五类,顺序固定(按轻重),供界面下拉与筛选白名单共用。
var DataActions = []string{ActionCreate, ActionDelete, ActionUpdate, ActionRead, ActionOther}
