package model

// RuleRef 机制的第六处应用:发布流水线的阶段日志与发布单的错误条(前端命名空间 rlLog.)。
//
// 成因与前几处一样:日志是在 Go 里拼的中文,英文界面上整段露出来(2026-10-30 用户在 REL-1 上
// 报的)。规范中文照旧写进 tbl_release_stage.log / tbl_release.error —— 对外接口
// (ReleaseStatusForClient)、Webhook、审计读的都是它;同一段话再以逐行 RuleRef 存一份
// (log_refs / error_ref,迁移 0059),界面按读者的语言渲染,认不出的 code 整段回落到中文原串。
//
// 一行一个 ref;args["cat"]="1" 的 ref 接在上一行末尾而不换行(人工确认的备注、执行完成后的
// EXIT / WHENEVER 尾注、在线变更进度的几个尾巴)。语句回显、结果集预览是 RelLogRaw,原样显示。
// 带 out 参数的行,out 是执行器那句话的规范串;它的机器可读身份在 parts[0],命名空间由
// args["ns"] 指明(outText / rlLog),前端渲染不出来就用 out。

import "strings"

const (
	RelLogRaw       = "raw"       // 原样文本(args: text)—— 语句回显、结果集预览、未知的进度阶段
	RelLogStageErr  = "stageErr"  // 阶段因一个错误失败(args: err,原文)
	RelLogNote      = "note"      // 接在上一行的用户备注(args: note,原文)
	RelLogUnknown   = "unknownType"
	RelLogReadBody  = "readBodyFail" // args: err
	RelLogWaitMan   = "waitManual"
	RelLogWaitManBy = "waitManualWho" // args: who(、分隔), names(, 分隔)

	RelLogRevHead    = "reviewHead"    // args: dialect, n
	RelLogRevCounts  = "reviewCounts"  // args: e, w, i
	RelLogRevFinding = "reviewFinding" // args: level(大写), stmt, rule(规则 code), name, msg
	RelLogRevMore    = "reviewMore"    // args: n

	RelLogApExists     = "apExists"     // args: no
	RelLogApCreateFail = "apCreateFail" // args: err
	RelLogApCreated    = "apCreated"    // args: no
	RelLogApApproved   = "apApproved"   // args: no
	RelLogApRejected   = "apRejected"   // args: no
	RelLogApExpired    = "apExpired"    // args: no
	RelLogApVoided     = "apVoidedAborted"

	RelLogBkNoSQL = "bkNoSQL"
	RelLogBkEmpty = "bkEmpty"
	RelLogBkVerb  = "bkVerb" // args: verb
	RelLogBkFile  = "bkFile"
	RelLogBkFunc  = "bkFunc" // args: fn
	RelLogBkFail  = "bkFail" // args: pos, total, out, ms
	RelLogBkDone  = "bkDone" // args: n

	RelLogStmtOk       = "stmtOk"       // args: pos, total, out, ms
	RelLogStmtFail     = "stmtFail"     // args: pos, total, out, ms
	RelLogStmtFailCont = "stmtFailCont" // args: pos, total, out, ms
	RelLogAutoOn       = "autoOn"
	RelLogAutoOff      = "autoOff"
	RelLogWaitExec     = "waitExec"
	RelLogWaitExecBy   = "waitExecWho" // args: who, names
	RelLogNoCreator    = "noCreator"
	RelLogMaint        = "maint"
	RelLogTierFail     = "tierFail"
	RelLogNoStmts      = "noStmts"
	RelLogRecheckDeny  = "recheckDeny"    // args: rule;parts[0] 裁决的 RuleRef(ns=ruleText)
	RelLogRecheckAppr  = "recheckApprove" // 同上
	RelLogOSCNeedAppr  = "oscNeedApproval"
	RelLogHalted       = "haltedAt"     // args: done, total
	RelLogHaltedFail   = "haltedAtFail" // args: done, total, pos, out
	RelLogExecDone     = "execDone"     // args: n, rows
	RelLogExecNote     = "execNote"     // 接在执行完成后面(args: note;parts[0] 尾注 ref,ns=outText)
	RelLogPrevTrunc    = "previewTrunc" // args: n
	RelLogPrevElided   = "previewElided" // args: n, max

	RelLogOSCHead       = "oscHead"       // args: pos, total, table, rows, size
	RelLogOSCPreFail    = "oscPreFail"    // args: err
	RelLogOSCBlocked    = "oscBlocked"    // args: reason(;分隔);parts: 每条阻塞项 {code: oscItem, args: code, reason}
	RelLogOSCWarn       = "oscWarn"       // args: code, reason
	RelLogOSCCreateFail = "oscCreateFail" // args: err
	RelLogOSCLost       = "oscLost"
	RelLogOSCDone       = "oscDone"     // args: copied, replayed, old
	RelLogOSCAborted    = "oscAborted"  // args: err
	RelLogOSCFailed     = "oscFailed"   // args: err
	RelLogOSCProgress   = "oscProgress" // args: copied, est, replayed;parts[0] 阶段
	RelLogOSCProgPct    = "oscProgressPct" // 同上 + pct
	RelLogOSCCut        = "oscCut"       // 接在进度后面(args: n)
	RelLogOSCThrottled  = "oscThrottled" // 接在进度后面
	RelLogOSCPaused     = "oscPaused"    // 接在进度后面
	RelLogOSCItem       = "oscItem"      // 在线变更检查项(args: code, reason —— 前端按 code 翻,同 preOscBlocked)

	RelLogVfNoSQL     = "vfNoSQL"
	RelLogVfNotRead   = "vfNotRead"
	RelLogVfFile      = "vfFile"
	RelLogVfFunc      = "vfFunc"   // args: fn
	RelLogVfFail      = "vfFail"   // args: out
	RelLogVfDone      = "vfDone"   // args: out, ms
	RelLogVfWantEmpty = "vfWantEmpty" // args: n
	RelLogVfWantRows  = "vfWantRows"

	RelLogNotified      = "notified"
	RelLogConfirmedCont = "confirmedContinue" // args: who
	RelLogConfirmedExec = "confirmedExec"     // args: who

	// 执行器没带 OutputRef 的几句(查询结果、行数未知的成功)。
	RelLogOutOk        = "outOk"
	RelLogOutRows      = "outRows"      // args: n
	RelLogOutRowsTrunc = "outRowsTrunc" // args: n, max

	// 发布单的错误条(tbl_release.error_ref)。
	RelErrQueueFull   = "errQueueFull"
	RelErrPanic       = "errPanic"       // args: err
	RelErrConnGone    = "errConnGone"
	RelErrStageFailed = "errStageFailed" // args: type, stage(规范中文名), log(日志开头,规范串);parts: 日志开头几行
	RelErrApRejected  = "errApRejected"  // args: no
	RelErrApExpired   = "errApExpired"   // args: no
	RelErrAbortedOSC  = "errAbortedOsc"  // args: who
	RelErrAborted     = "errAborted"     // args: who
	RelErrRestart     = "errRestart"
)

// 在线变更进度阶段(oscProgress 的 parts[0])。code = "oscPh_" + 阶段。
var relLogOSCPhase = map[string]string{
	"oscPh_preparing": "准备", "oscPh_copying": "拷贝", "oscPh_catching_up": "追平", "oscPh_cutting_over": "切换",
	"oscPh_done": "完成", "oscPh_failed": "失败", "oscPh_aborted": "已中止",
}

// RelLogCodes 是全部 rlLog code。加 code 时同时加在这里:测试据此检查每个 code 都有规范中文串、
// 前端 zh.json5 的词条与之一字不差。
var RelLogCodes = []string{
	RelLogRaw, RelLogStageErr, RelLogNote, RelLogUnknown, RelLogReadBody, RelLogWaitMan, RelLogWaitManBy,
	RelLogRevHead, RelLogRevCounts, RelLogRevFinding, RelLogRevMore,
	RelLogApExists, RelLogApCreateFail, RelLogApCreated, RelLogApApproved, RelLogApRejected, RelLogApExpired, RelLogApVoided,
	RelLogBkNoSQL, RelLogBkEmpty, RelLogBkVerb, RelLogBkFile, RelLogBkFunc, RelLogBkFail, RelLogBkDone,
	RelLogStmtOk, RelLogStmtFail, RelLogStmtFailCont, RelLogAutoOn, RelLogAutoOff, RelLogWaitExec, RelLogWaitExecBy,
	RelLogNoCreator, RelLogMaint, RelLogTierFail, RelLogNoStmts, RelLogRecheckDeny, RelLogRecheckAppr, RelLogOSCNeedAppr,
	RelLogHalted, RelLogHaltedFail, RelLogExecDone, RelLogExecNote, RelLogPrevTrunc, RelLogPrevElided,
	RelLogOSCHead, RelLogOSCPreFail, RelLogOSCBlocked, RelLogOSCWarn, RelLogOSCCreateFail, RelLogOSCLost, RelLogOSCDone,
	RelLogOSCAborted, RelLogOSCFailed, RelLogOSCProgress, RelLogOSCProgPct, RelLogOSCCut, RelLogOSCThrottled, RelLogOSCPaused, RelLogOSCItem,
	"oscPh_preparing", "oscPh_copying", "oscPh_catching_up", "oscPh_cutting_over", "oscPh_done", "oscPh_failed", "oscPh_aborted",
	RelLogVfNoSQL, RelLogVfNotRead, RelLogVfFile, RelLogVfFunc, RelLogVfFail, RelLogVfDone, RelLogVfWantEmpty, RelLogVfWantRows,
	RelLogNotified, RelLogConfirmedCont, RelLogConfirmedExec, RelLogOutOk, RelLogOutRows, RelLogOutRowsTrunc,
	RelErrQueueFull, RelErrPanic, RelErrConnGone, RelErrStageFailed, RelErrApRejected, RelErrApExpired,
	RelErrAbortedOSC, RelErrAborted, RelErrRestart,
}

// RenderRelLog 把一个 rlLog ref 渲染成规范中文 —— 写进 log / error 列的那句话。
func RenderRelLog(r *RuleRef) string {
	if r == nil {
		return ""
	}
	a := r.Args
	inner := ""
	if len(r.Parts) > 0 {
		inner = RenderRelLog(&r.Parts[0])
	}
	if zh, ok := relLogOSCPhase[r.Code]; ok {
		return zh
	}
	switch r.Code {
	case RelLogRaw:
		return a["text"]
	case RelLogStageErr:
		return "· " + a["err"]
	case RelLogNote:
		return " · " + a["note"]
	case RelLogUnknown:
		return "· 未知的阶段类型 " + a["type"]
	case RelLogReadBody:
		return "· 无法读取发布内容: " + a["err"]
	case RelLogWaitMan:
		return "· 等待人工确认"
	case RelLogWaitManBy:
		return "· 等待「" + a["who"] + "」人工确认"
	case RelLogRevHead:
		return "· 方言 " + a["dialect"] + " · 共 " + a["n"] + " 条语句"
	case RelLogRevCounts:
		return "· 命中 " + a["e"] + " 错误 / " + a["w"] + " 警告 / " + a["i"] + " 提示"
	case RelLogRevFinding:
		return "  [" + a["level"] + "] 第" + a["stmt"] + "条 " + a["name"] + ": " + a["msg"]
	case RelLogRevMore:
		return "  …另有 " + a["n"] + " 条,详见审查结果"
	case RelLogApExists:
		return "· 已存在审批单 " + a["no"]
	case RelLogApCreateFail:
		return "· 审批单创建失败: " + a["err"]
	case RelLogApCreated:
		return "· 已创建审批单 " + a["no"] + ",等待审批"
	case RelLogApApproved:
		return "· 审批单 " + a["no"] + " 已通过"
	case RelLogApRejected:
		return "· 审批单 " + a["no"] + " 被驳回"
	case RelLogApExpired:
		return "· 审批单 " + a["no"] + " 已超时失效"
	case RelLogApVoided:
		return "· 审批单 " + a["no"] + " 已作废(发布单已终止)"
	case RelLogBkNoSQL:
		return "· 未配置备份语句,本阶段跳过(物理备份需由 DBA 侧完成)"
	case RelLogBkEmpty:
		return "· 备份配置中没有可执行的语句,本阶段跳过"
	case RelLogBkVerb:
		return "· 备份语句只允许复制数据(CREATE/INSERT/SELECT),拒绝执行 " + a["verb"] + ";如需其它操作请放入发布内容走完整判定"
	case RelLogBkFile:
		return "· 备份语句不得写出文件(INTO OUTFILE / DUMPFILE);备份应写回数据库表,落盘请交由 DBA 侧的物理备份完成"
	case RelLogBkFunc:
		return "· 备份语句不得调用有副作用的函数 " + a["fn"]
	case RelLogBkFail:
		return "· 备份第 " + a["pos"] + "/" + a["total"] + " 条失败: " + a["out"] + " (" + a["ms"] + "ms)"
	case RelLogBkDone:
		return "· 备份完成 · " + a["n"] + " 条语句"
	case RelLogStmtOk:
		return "· [" + a["pos"] + "/" + a["total"] + "] " + a["out"] + " (" + a["ms"] + "ms)"
	case RelLogStmtFail:
		return "· 第 " + a["pos"] + "/" + a["total"] + " 条失败: " + a["out"] + " (" + a["ms"] + "ms)"
	case RelLogStmtFailCont:
		return "· 第 " + a["pos"] + "/" + a["total"] + " 条失败(WHENEVER SQLERROR CONTINUE,继续): " + a["out"] + " (" + a["ms"] + "ms)"
	case RelLogAutoOn:
		return "· 自动执行:分层允许自动执行,未经人工确认直接下发"
	case RelLogAutoOff:
		return "· 当前分层未开启「允许自动执行」,改为等待人工确认"
	case RelLogWaitExec:
		return "· 等待人工确认执行(创建者或审批角色点击「确认执行」后才会落库)"
	case RelLogWaitExecBy:
		return "· 等待「" + a["who"] + "」确认执行后才会落库"
	case RelLogNoCreator:
		return "· 发布人不存在,未执行"
	case RelLogMaint:
		return "· 目标实例处于维护态,未执行"
	case RelLogTierFail:
		return "· 实例的分层标签解析失败,未执行"
	case RelLogNoStmts:
		return "· 发布内容中没有可执行的语句"
	case RelLogRecheckDeny:
		return "· 执行前复判被拒绝(" + a["rule"] + "),未执行"
	case RelLogRecheckAppr:
		return "· 执行前复判需要审批(" + a["rule"] + "),但本次发布没有已通过的审批单,未执行"
	case RelLogOSCNeedAppr:
		return "· 发布内容里有生产大表的在线变更,须有已通过的审批,未执行"
	case RelLogHalted:
		return "· 已执行 " + a["done"] + "/" + a["total"] + " 条后中止"
	case RelLogHaltedFail:
		return "· 已执行 " + a["done"] + "/" + a["total"] + " 条后中止 · 第 " + a["pos"] + " 条失败: " + a["out"]
	case RelLogExecDone:
		return "· 执行完成 · " + a["n"] + " 条语句 · 影响 " + a["rows"] + " 行"
	case RelLogExecNote:
		return " · " + a["note"]
	case RelLogPrevTrunc:
		return "    … 结果集超出返回上限已被截断,日志预览前 " + a["n"] + " 行"
	case RelLogPrevElided:
		return "    … 已省略 " + a["n"] + " 行(日志仅预览前 " + a["max"] + " 行,需完整数据请走数据导出)"
	case RelLogOSCHead:
		return "· [" + a["pos"] + "/" + a["total"] + "] 表 " + a["table"] + " 约 " + a["rows"] + " 行 / " + a["size"] + ",以在线变更执行"
	case RelLogOSCPreFail:
		return "  在线变更前置检查失败: " + a["err"]
	case RelLogOSCBlocked:
		return "  在线变更前置检查未通过:" + a["reason"]
	case RelLogOSCWarn:
		return "  提示:" + a["reason"]
	case RelLogOSCCreateFail:
		return "  建立在线变更任务失败: " + a["err"]
	case RelLogOSCLost:
		return "  在线变更任务丢失"
	case RelLogOSCDone:
		return "  在线变更完成 · 拷贝 " + a["copied"] + " 行 · 重放 " + a["replayed"] + " 行 · 原表已保留为 " + a["old"] + ",请 DBA 择时手动删除"
	case RelLogOSCAborted:
		return "  在线变更已中止:" + a["err"]
	case RelLogOSCFailed:
		return "  在线变更失败:" + a["err"]
	case RelLogOSCProgress:
		return "  进度:" + inner + " · 已拷贝 " + a["copied"] + " / 约 " + a["est"] + " 行 · 已重放 " + a["replayed"] + " 行"
	case RelLogOSCProgPct:
		return "  进度:" + inner + " · 已拷贝 " + a["copied"] + " / 约 " + a["est"] + " 行(约 " + a["pct"] + "%) · 已重放 " + a["replayed"] + " 行"
	case RelLogOSCCut:
		return " · 切换尝试 " + a["n"] + " 次"
	case RelLogOSCThrottled:
		return " · 库端繁忙,限流中"
	case RelLogOSCPaused:
		return " · 已暂停"
	case RelLogOSCItem:
		return a["reason"]
	case RelLogVfNoSQL:
		return "· 未配置校验语句,本阶段跳过"
	case RelLogVfNotRead:
		return "· 校验语句必须是只读查询"
	case RelLogVfFile:
		return "· 校验语句不得写出文件(INTO OUTFILE / DUMPFILE)"
	case RelLogVfFunc:
		return "· 校验语句必须是只读查询,不得调用有副作用的函数 " + a["fn"]
	case RelLogVfFail:
		return "· 校验执行失败: " + a["out"]
	case RelLogVfDone:
		return "· 校验完成 · " + a["out"] + " (" + a["ms"] + "ms)"
	case RelLogVfWantEmpty:
		return "· 期望结果为空,实际返回 " + a["n"] + " 行"
	case RelLogVfWantRows:
		return "· 期望有结果,实际返回 0 行"
	case RelLogNotified:
		return "· 已推送发布事件(审计 + Webhook)"
	case RelLogConfirmedCont:
		return "· 已由 " + a["who"] + " 人工确认继续"
	case RelLogConfirmedExec:
		return "· 已由 " + a["who"] + " 确认执行"
	case RelLogOutOk:
		return "执行成功"
	case RelLogOutRows:
		return "+ " + a["n"] + " rows"
	case RelLogOutRowsTrunc:
		return "+ " + a["n"] + "+ rows (显示前 " + a["max"] + ")"
	case RelErrQueueFull:
		return "发布队列已满,请稍后重试"
	case RelErrPanic:
		return "发布执行异常: " + a["err"]
	case RelErrConnGone:
		return "目标连接已不存在"
	case RelErrStageFailed:
		return "阶段「" + a["stage"] + "」失败: " + a["log"]
	case RelErrApRejected:
		return "审批单 " + a["no"] + " 被驳回"
	case RelErrApExpired:
		return "审批单 " + a["no"] + " 已超时失效"
	case RelErrAbortedOSC:
		return "已由 " + a["who"] + " 终止(在线变更已中止)"
	case RelErrAborted:
		return "已由 " + a["who"] + " 终止"
	case RelErrRestart:
		return "网关重启,发布中断(执行阶段结果未知,请人工确认后重新发起)"
	}
	return ""
}

// RenderRelLogLines 把一段日志的 refs 拼成规范中文:每个 ref 一行,cat 的接在上一行末尾。
func RenderRelLogLines(refs []RuleRef) string {
	var b strings.Builder
	for i := range refs {
		if i > 0 && refs[i].Args["cat"] != "1" {
			b.WriteByte('\n')
		}
		b.WriteString(RenderRelLog(&refs[i]))
	}
	return b.String()
}
