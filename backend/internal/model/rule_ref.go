package model

import (
	"strconv"
	"strings"
)

// RuleRef 是一次裁决背后那条规则的**机器可读身份**。
//
// 裁决里的 Rule 字符串仍然是规范记录:它进审批单、进审计链,而这两样东西不该因为
// 当时是谁在看而变成另一种语言。但规则名是在 Go 里拼好的中文句子,于是英文界面上
// 一整行英文提示中间会突兀地嵌着一句中文 —— 界面的语言是读的人的事,记录的语言不是。
//
// 所以把同一句话再表示一遍:一个稳定的 code 加上它的参数。客户端按 code 用读者的
// 语言讲同一件事,碰到不认识的 code 就回落到 Rule 字符串 —— 历史记录里存的正是那个
// 字符串,而新版前端总要能读懂旧数据。
//
// Parts 是嵌套:批量语句里每条命中各是一个 part,执行窗口放宽时原判是它的 part。
type RuleRef struct {
	Code  string            `json:"code"`
	Args  map[string]string `json:"args,omitempty"`
	Parts []RuleRef         `json:"parts,omitempty"`
}

// 规则标识。改动这些常量等于改动前后端的契约:前端的文案表按同样的 code 索引,
// 而旧客户端认不出新 code 时会回落到中文原串 —— 那是降级,不是出错。
const (
	RuleCapDeny       = "capDeny"       // 能力矩阵判禁止
	RuleCapApprove    = "capApprove"    // 能力矩阵判需审批
	RuleDictDeny      = "dictDeny"      // 高危命令字典判该分层禁止直接执行(args: tier)
	RuleDictApprove   = "dictApprove"   // 高危命令字典判需审批
	RuleStrictNoWhere = "strictNoWhere" // 严格模式:无 WHERE 的 DELETE / UPDATE
	RuleWritesFile    = "writesFile"    // SELECT … INTO OUTFILE / DUMPFILE:在数据库服务器上写文件
	// 已不再判定(SQLite 不再是目标库),保留给历史审计行显示 —— 删掉它,旧记录就显示成原始键。
	RuleSQLiteFileOp  = "sqliteFileOp"  // SQLite 的 ATTACH / DETACH / VACUUM INTO:碰网关主机上的文件
	RuleSQLPlusRefused = "sqlplusRefused" // SQL*Plus 命令,网关不支持(CONNECT / HOST …)(args: cmd)
	RuleUnavailable   = "unavailable"   // 判定层读不到,按最严处理(ED3)
	RuleScriptHigh    = "scriptHigh"    // 脚本含高危语句
	RuleExecWindow    = "execWindow"    // 执行窗口免审批放行(args: window;part: 原判)
	RuleBatch         = "batch"         // 批量:parts 为逐条命中
	RuleBatchHit      = "batchHit"      // 批量中的一条(args: pos, command;part: 该条规则)
	RuleBatchHitBare  = "batchHitBare"  // 同上,但没解析出动词
	RuleBatchMore     = "batchMore"     // 截断提示(args: n)
)

// 终端输出文本的标识 —— 同一套机制的第二处应用。
//
// 上面那组说的是"为什么拦下来",这组说的是"跑完之后终端上打的那句话"。成因完全
// 一样:`执行成功 · N 行受影响` 是在 Go 里拼的中文,而它会原样打进一个英文会话的
// 终端里。规范串仍然照旧下发并进记录(审计、工单的 Result),code 只是让界面用读者
// 的语言把同一件事再讲一遍。
//
// **以 `·` 开头的是提示,不是执行结果** —— 终端据此决定用黄色显示、并且不追加耗时
// (什么都没跑,没有可计时的东西)。两种语言的文案都必须保留这个前缀,否则一条提示
// 会被显示成一次成功的执行。
const (
	OutExecAffected   = "execAffected"   // 执行成功(args: n = 受影响行数)
	OutNothingToRun   = "nothingToRun"   // · 只有注释或空白,没有下发
	OutExecFailed     = "execFailed"     // · 数据库执行失败(args: err = 驱动原文,不翻译)
	OutExecCancelled  = "execCancelled"  // · 操作员取消(注意:不等于没执行)
	OutSQLPlusSetIgnored = "sqlplusSetIgnored" // · SQL*Plus 的 SET 命令,网关不下发(args: option)
	OutSQLPlusRefused    = "sqlplusRefused"    // · SQL*Plus 命令,网关不支持(args: cmd, reason = 规范中文串)
	OutSQLPlusIgnored    = "sqlplusIgnored"    // · SQL*Plus 客户端命令,这条通道不处理(args: cmd)
	OutSQLPlusExit       = "sqlplusExit"       // · EXIT / QUIT:脚本在此结束(args: cmd)
	OutSQLPlusWhenever   = "sqlplusWhenever"   // · WHENEVER …:对脚本后面的语句生效(args: text)
	OutSQLPlusVarDeclared = "sqlplusVarDeclared" // · VARIABLE 已声明(args: name, type)
	OutSQLPlusVarBad      = "sqlplusVarBad"      // · VARIABLE 类型不支持(args: text)
	OutSQLPlusPrintBad    = "sqlplusPrintBad"    // · PRINT 的变量没有声明(args: name)
	OutSQLPlusPrintNone   = "sqlplusPrintNone"   // · PRINT:还没有声明任何绑定变量
	// 批量 / 脚本执行完成,但流程被 WHENEVER / EXIT 改过(args: n, rows;part: 下面三种说明之一)
	OutBatchDoneFlow       = "batchDoneFlow"
	OutFlowExited          = "flowExited"          // 在第几条 EXIT 处结束(args: pos)
	OutFlowContinued       = "flowContinued"       // WHENEVER SQLERROR CONTINUE:k 条失败(args: f, failed)
	OutFlowExitedContinued = "flowExitedContinued" // 两者都有(args: pos, f, failed)
	OutExecTimeout    = "execTimeout"    // · 超过执行时限,已要求目标库终止(args: sec)
	OutBatchDone      = "batchDone"      // 批量执行完成(args: n = 语句数, rows)
	// 批量执行完成,但其中有写语句影响了 0 行(args: n, rows, z = 条数, zero = 第几条,逗号分隔,
	// 超过 10 条只列前 10 条并以 … 结尾)。
	OutBatchDoneZero = "batchDoneZero"
	OutBatchFailed    = "batchFailed"    // · 批量停在第几条(args: done, total, pos, err)
	OutMaintenance    = "maintenance"    // · 目标实例处于维护态,操作受限
	OutApWindowLive   = "apWindowLive"   // · 已批准,执行窗口已生效
	OutApExportQueued = "apExportQueued" // · 已批准,导出任务已进入队列
	OutApPipeline     = "apPipeline"     // · 已批准,由发布流水线继续执行
	OutApAwaitingExec = "apAwaitingExec" // · 已批准,等待发起人执行
)

// 业务方数据查询的拒绝原因 —— 同一套机制的第三处应用(前端命名空间 bizText.)。
//
// 业务方看到的是"为什么这条查询不能执行"。规范中文串照旧进 msg 和审计(RenderBizReason),
// code 让界面用读者的语言讲同一件事;前端还按 code 决定要不要刷新左侧的授权列表(授权被撤销、
// 到期、实例取消标记时),而不是去匹配中文原话 —— 原话改一个字,那种匹配就悄悄失效。
const (
	BizNoSQL          = "bizNoSQL"          // 请输入查询语句
	BizNoSource       = "bizNoSource"       // 实例不存在
	BizNotSource      = "bizNotSource"      // 实例已不是业务查询源(args: source)
	BizMaintenance    = "bizMaintenance"    // 实例处于维护态(args: source)
	BizNoGrant        = "bizNoGrant"        // 没有此刻生效的授权(args: source, db)
	BizExportOff      = "bizExportOff"      // 导出功能未开启
	BizExportDenied   = "bizExportDenied"   // 授权不含导出(args: source, db)
	BizDBListFailed   = "bizDBListFailed"   // 取不到实例的库列表(args: source, err)
	BizSimulationOff  = "bizSimulationOff"  // 没有凭据的连接在生产上不模拟(args: err)
	BizBusyUser       = "bizBusyUser"       // 本人并发已满(args: n)
	BizBusySource     = "bizBusySource"     // 实例并发已满
	BizDailyLimit     = "bizDailyLimit"     // 本人今天的查询次数已到上限(args: n)
	BizEmpty          = "bizEmpty"          // 语句为空
	BizMulti          = "bizMulti"          // 多条语句(args: n)
	BizNotSelect      = "bizNotSelect"      // 不是 SELECT(args: verb)
	BizBackslash      = "bizBackslash"      // 反斜杠转义的引号,理解有歧义
	BizLock           = "bizLock"           // 加锁(args: clause)
	BizInto           = "bizInto"           // INTO
	BizFunc           = "bizFunc"           // 不允许的函数 / 包(args: name)
	BizSystemDB       = "bizSystemDB"       // 系统库(args: name, db)
	BizOtherDB        = "bizOtherDB"        // 未授权的库(args: name, db)
	BizLimitNotNumber = "bizLimitNotNumber" // 最外层 LIMIT / FETCH 不是数字
	// 敏感字段经子查询 / UNION / CTE 改名,打码可能兜不住(args: column)。终端遇到它是"顺手
	// 看到"、导出要审批;业务查询没有审批这条路,直接拒绝。
	BizSensitiveEvasion = "bizSensitiveEvasion"
	// Oracle 的 EXPLAIN 只支持 EXPLAIN PLAN FOR <只读查询>(不带 SET STATEMENT_ID / INTO)。
	BizExplainForm = "bizExplainForm"
	// 2026-10-29:SHOW 白名单之外的写法;DESC 后面不是一个表名。
	BizShowForm = "bizShowForm"
	BizDescForm = "bizDescForm"
	// EXPLAIN / DESC FOR CONNECTION:看别人会话的语句。
	BizOtherSession = "bizOtherSession"
	// Oracle 数据库链接(args: name)。
	BizDBLink = "bizDBLink"
	// Oracle:语句经自定义函数 / 视图用到了带自治事务的对象(args: name)。
	BizAutonomous = "bizAutonomous"
	// Oracle:执行前查数据字典确认没成(args: err)。
	BizHiddenCheckFailed = "bizHiddenCheckFailed"

	// 以下三个不是"拒绝",是执行了但没做成(data.error / data.errorRef,code 为 0)。
	BizExecTimeout   = "bizExecTimeout"   // 超过时限,已自动取消(args: sec)
	BizExecCancelled = "bizExecCancelled" // 已取消(浏览器关闭、网络断开)
	BizExecFailed    = "bizExecFailed"    // 库报错(args: err = 驱动原文,不翻译)
)

// BizReasonCodes 是全部业务查询拒绝原因。加 code 时同时加在这里:测试据此检查每个 code 都有
// 规范中文串、前端都有词条 —— 漏了的话,界面上那一种拒绝就只能回落到中文原话。
var BizReasonCodes = []string{
	BizNoSQL, BizNoSource, BizNotSource, BizMaintenance, BizNoGrant, BizExportOff, BizExportDenied,
	BizDBListFailed, BizSimulationOff, BizBusyUser, BizBusySource, BizDailyLimit, BizEmpty, BizMulti,
	BizNotSelect, BizBackslash, BizLock, BizInto, BizFunc, BizSystemDB, BizOtherDB, BizLimitNotNumber, BizSensitiveEvasion, BizExplainForm,
	BizShowForm, BizDescForm, BizOtherSession, BizDBLink, BizAutonomous, BizHiddenCheckFailed,
	BizExecTimeout, BizExecCancelled, BizExecFailed,
}

// RenderBizReason 把业务查询的拒绝原因渲染成规范中文串(msg 与审计里的那句话)。
func RenderBizReason(r *RuleRef) string {
	if r == nil {
		return ""
	}
	a := r.Args
	switch r.Code {
	case BizNoSQL:
		return "请输入查询语句"
	case BizNoSource:
		return "实例不存在"
	case BizNotSource:
		return "实例 " + a["source"] + " 已不是业务查询源,查询授权不再生效"
	case BizMaintenance:
		return "实例 " + a["source"] + " 处于维护态,暂不能查询"
	case BizNoGrant:
		return "你没有 " + a["source"] + " / " + a["db"] + " 的有效查询授权(可能已到期或被撤销)"
	case BizExportOff:
		return "数据查询的导出功能未开启"
	case BizExportDenied:
		return "你对 " + a["source"] + " / " + a["db"] + " 的授权不包含导出"
	case BizDBListFailed:
		return "无法确认实例 " + a["source"] + " 上的库列表,暂不能查询:" + a["err"]
	case BizSimulationOff:
		return a["err"]
	case BizBusyUser:
		return "你已有 " + a["n"] + " 条查询在执行,请等它们结束再试"
	case BizBusySource:
		return "这台实例上的业务查询已满,请稍后再试"
	case BizDailyLimit:
		return "你今天已执行 " + a["n"] + " 次查询,达到每日上限,明天再试"
	case BizEmpty:
		return "语句为空"
	case BizMulti:
		return "一次只能执行一条语句,这里有 " + a["n"] + " 条"
	case BizNotSelect:
		return "数据查询只允许只读查询(SELECT、WITH … SELECT、EXPLAIN、DESC 表、SHOW 表结构),不能执行 " + a["verb"]
	case BizShowForm:
		return "数据查询里的 SHOW 只能看本库的表结构:SHOW CREATE TABLE / VIEW、SHOW COLUMNS、SHOW INDEX、SHOW TABLES、SHOW TABLE STATUS"
	case BizDescForm:
		return "DESC 后面只能写一个表或视图名(可带本库前缀)"
	case BizOtherSession:
		return "数据查询不能查看别的会话正在执行的语句(FOR CONNECTION)"
	case BizDBLink:
		return "数据查询不能经数据库链接 " + a["name"] + " 访问别的库"
	case BizAutonomous:
		return "语句用到的 " + a["name"] + " 带自治事务(PRAGMA AUTONOMOUS_TRANSACTION),在查询里调用也会真的改数据,数据查询不能执行"
	case BizHiddenCheckFailed:
		return "无法确认语句用到的函数是否会改数据,暂不能查询:" + a["err"]
	case BizBackslash:
		return `语句里有用反斜杠转义的引号(\' 或 \"),不同数据库设置下对它的理解不同,网关无法可靠判定;请改用两个连写的引号表示引号,例如 'O''Brien'`
	case BizLock:
		return "数据查询不能加锁(" + a["clause"] + ")"
	case BizInto:
		return "数据查询不能使用 INTO(写文件、写变量或建表)"
	case BizFunc:
		return "数据查询不能调用 " + a["name"]
	case BizSystemDB:
		return "不能查询系统库 " + a["name"] + " 的数据:你的授权只覆盖库 " + a["db"]
	case BizOtherDB:
		return "不能查询未授权的库 " + a["name"] + " 的数据:你的授权只覆盖库 " + a["db"]
	case BizExecTimeout:
		return "查询超过 " + a["sec"] + " 秒,已自动取消"
	case BizExecCancelled:
		return "查询已取消"
	case BizExecFailed:
		return "查询失败: " + a["err"]
	case BizExplainForm:
		return "Oracle 上请写成 EXPLAIN PLAN FOR 加一条只读查询(不带 SET STATEMENT_ID、INTO),网关会直接返回执行计划"
	case BizSensitiveEvasion:
		return "查询经子查询、UNION 或 WITH 给敏感字段 " + a["column"] + " 改了名,网关无法确认结果已脱敏;请直接查询该字段(结果会自动脱敏),或去掉它"
	case BizLimitNotNumber:
		return "查询末尾的 LIMIT / FETCH 只能写数字(例如 LIMIT 100),网关需要确认返回的行数不超过授权上限"
	}
	return ""
}

// NewRuleRef 按 key/value 交替的参数建一个规则标识,省掉每处都写 map 字面量。
// 参数个数为奇数时丢掉最后一个 —— 这只可能是写错了,而规则标识不值得为此 panic。
func NewRuleRef(code string, kv ...string) *RuleRef {
	r := &RuleRef{Code: code}
	if len(kv) >= 2 {
		r.Args = map[string]string{}
		for i := 0; i+1 < len(kv); i += 2 {
			r.Args[kv[i]] = kv[i+1]
		}
	}
	return r
}

// RenderRule 把规则标识渲染成规范中文串 —— 也就是历来存进审批单和审计链的那句话。
//
// 裁决的 Rule 字段现在一律由它生成,而不是各处各自拼一遍:两份写法迟早分叉,而分叉
// 的表现是"审计里记的规则"和"界面上显示的规则"不是同一条,那正是事后追责时唯一能
// 抓的凭据。
func RenderRule(r *RuleRef) string {
	if r == nil {
		return ""
	}
	inner := ""
	if len(r.Parts) > 0 {
		inner = RenderRule(&r.Parts[0])
	}
	switch r.Code {
	case RuleCapDeny:
		return "能力矩阵 · 该环境禁止此操作"
	case RuleCapApprove:
		return "能力矩阵 · 需审批"
	case RuleDictDeny:
		return "高危命令字典 · " + r.Args["tier"] + " 禁止直接执行"
	case RuleDictApprove:
		return "高危命令字典 · 需审批"
	case RuleStrictNoWhere:
		return "严格模式 · 无 WHERE 的 DELETE / UPDATE"
	case RuleSQLiteFileOp:
		return "SQLite 的 ATTACH / DETACH / VACUUM INTO 会碰网关主机上的文件 · 禁止"
	case RuleWritesFile:
		return "写数据库服务器文件(INTO OUTFILE / DUMPFILE)· 禁止"
	case RuleSQLPlusRefused:
		return "SQL*Plus 命令 " + r.Args["cmd"] + " · 网关不支持"
	case RuleUnavailable:
		return "风险控制暂时不可用 · 已按最严处理"
	case RuleScriptHigh:
		return "脚本含高危语句 · 需审批"
	case RuleExecWindow:
		return "执行窗口「" + r.Args["window"] + "」· 免审批放行(原判:" + inner + ")"
	case RuleBatch:
		parts := make([]string, 0, len(r.Parts))
		for i := range r.Parts {
			parts = append(parts, RenderRule(&r.Parts[i]))
		}
		return strings.Join(parts, " + ")
	case RuleBatchHit:
		return "第" + r.Args["pos"] + "条 " + r.Args["command"] + " · " + inner
	case RuleBatchHitBare:
		return "第" + r.Args["pos"] + "条 · " + inner
	case RuleBatchMore:
		return "…另有 " + r.Args["n"] + " 条命中"
	}
	return ""
}

// Itoa 让判定层拼参数时不必各自 import strconv。
func Itoa(n int) string { return strconv.Itoa(n) }
