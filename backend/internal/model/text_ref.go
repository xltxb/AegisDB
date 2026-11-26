package model

// RuleRef 机制的第四、第五处应用:审批单的描述(前端命名空间 apText.)与发布预检的结论
// (preText.)。成因与前三处一样 —— 句子在 Go 里拼成中文,英文界面上原样露出来。
//
// 规范中文串照旧:审批单的 Command 进飞书卡片、进审计;预检的 Message 进发布单。它们一律
// 由这里的 Render* 生成,code 只是让界面用读者的语言把同一件事再讲一遍,认不出的 code
// 回落到中文原串 —— 历史数据只有原串。

import "strings"

// 审批单描述(apText.)。
const (
	// 开启执行窗口(args: name, scope, db, reason;part[0]: 时间表)。
	ApWindowOpen = "apWindowOpen"
	// 同上,申请理由没填(args: name, scope, db;part[0]: 时间表)。
	ApWindowOpenNoReason = "apWindowOpenNoReason"

	// 申请数据查询权限(args: scope, db, days, reason)。#173
	ApGrantApply = "apGrantApply"
	// 同上,另外申请了导出(args: scope, db, days, reason)。
	ApGrantApplyExport = "apGrantApplyExport"

	// 执行窗口的时间表。窗口的时间表本身就是一句话,所以是审批描述的一个 part。
	WinOnce         = "winOnce"         // 一次性(args: start, end,格式 2006-01-02 15:04)
	WinOnceUnset    = "winOnceUnset"    // 一次性,时间未设置
	WinDaily        = "winDaily"        // 班车 每天(args: start, end, tz)
	WinWeekly       = "winWeekly"       // 班车 按星期(args: days = ISO 星期逗号列表, start, end, tz)
	WinUnknownModel = "winUnknownModel" // 未知时间模型
)

// ApTextCodes 是全部审批描述 code。加 code 时同时加在这里:测试据此检查每个 code 都有
// 规范中文串、前端都有词条。
var ApTextCodes = []string{ApWindowOpen, ApWindowOpenNoReason, ApGrantApply, ApGrantApplyExport, WinOnce, WinOnceUnset, WinDaily, WinWeekly, WinUnknownModel}

// RenderApText 把审批描述渲染成规范中文串 —— 历来存进 tbl_approval.command 的那句话。
func RenderApText(r *RuleRef) string {
	if r == nil {
		return ""
	}
	a := r.Args
	inner := ""
	if len(r.Parts) > 0 {
		inner = RenderApText(&r.Parts[0])
	}
	switch r.Code {
	case ApWindowOpen:
		return "开启执行窗口「" + a["name"] + "」· " + a["scope"] + "/" + a["db"] + " · " + inner + " · 理由:" + a["reason"]
	case ApWindowOpenNoReason:
		return "开启执行窗口「" + a["name"] + "」· " + a["scope"] + "/" + a["db"] + " · " + inner + " · 理由:(未填)"
	case ApGrantApply:
		return "申请数据查询权限 · " + a["scope"] + "/" + a["db"] + " · " + a["days"] + " 天 · 理由:" + a["reason"]
	case ApGrantApplyExport:
		return "申请数据查询权限(含导出)· " + a["scope"] + "/" + a["db"] + " · " + a["days"] + " 天 · 理由:" + a["reason"]
	case WinOnce:
		return "一次性 " + a["start"] + " → " + a["end"]
	case WinOnceUnset:
		return "一次性(时间未设置)"
	case WinDaily:
		return "班车 每天 " + a["start"] + "-" + a["end"] + " (" + a["tz"] + ")"
	case WinWeekly:
		return "班车 周" + a["days"] + " " + a["start"] + "-" + a["end"] + " (" + a["tz"] + ")"
	case WinUnknownModel:
		return "(未知时间模型)"
	}
	return ""
}

// 发布预检的结论(preText.)。带 err 参数的,err 是驱动 / 库的原文,不翻译。
const (
	PreNoCreds = "preNoCreds" // 实例没有真实执行凭据,什么都没查
	// 在线变更(.scratch/osc-product):生产大表的索引变更由执行阶段改走在线变更
	PreOSCOnline       = "preOscOnline"       // 将以在线变更执行(args: table, rows, size)
	PreOSCBlocked      = "preOscBlocked"      // 在线变更前置检查的阻塞项(args: code, reason)
	PreOSCWarn         = "preOscWarn"         // 在线变更前置检查的警告(args: code, reason)
	PreOSCBigDirect    = "preOscBigDirect"    // 大表上在线变更做不了,将直接执行、可能长时间锁表(args: table, rows, size, why)
	PreOSCUnread       = "preOscUnread"       // 在线变更前置检查没能完成(args: err)
	PreColsUnreadable  = "preColsUnreadable"  // 取不到字段(args: err)
	PreObjsUnreadable  = "preObjsUnreadable"  // 取不到对象清单(args: err)
	PreObjDropped      = "preObjDropped"      // 对象在本次发布的前序语句里已被删除
	PreObjMissing      = "preObjMissing"      // 对象不存在
	PreObjExists       = "preObjExists"       // 对象已存在,该语句可能失败
	PreObjUnrecognised = "preObjUnrecognised" // 认不出语句引用了哪些对象
	PreSyntaxOracle    = "preSyntaxOracle"    // Oracle 不能不执行就校验语法
	PreSyntaxNotSQL    = "preSyntaxNotSQL"    // 引擎不是 SQL
	PreSyntaxSkipped   = "preSyntaxSkipped"   // 未做语法检查(认不出动词)
	PreSyntaxVerb      = "preSyntaxVerb"      // 这种语句不能不执行就校验语法(args: verb)
	PreSyntaxAfterMiss = "preSyntaxAfterMiss" // 引用对象缺失,语法检查跳过
	PreSyntaxError     = "preSyntaxError"     // 语法错误(args: err)
	PreTableColsUnread = "preTableColsUnread" // 读不到这张表的字段
	PreColMissing      = "preColMissing"      // 字段不存在,某操作会失败(part[0]: 操作,PreOp*)
	PreColExists       = "preColExists"       // 字段已存在,加列会失败
	PreRenameTarget    = "preRenameTarget"    // 改名的目标字段已存在
	PreProbeNotNull    = "preProbeNotNull"    // 表里已有数据,加非空无默认值的字段会失败
	PreProbeNulls      = "preProbeNulls"      // 字段存在 NULL,改成非空会失败
	PreProbeDuplicates = "preProbeDuplicates" // 字段上存在重复值,加唯一约束会失败
	PreProbeBudget     = "preProbeBudget"     // 数据探测已达上限(args: n)
	PreProbeFailed     = "preProbeFailed"     // 数据探测失败(args: err)
	PreNoteTruncated   = "preNoteTruncated"   // 语句太多,只检查了前几条(args: total, n)
	PreNoteSyntaxCap   = "preNoteSyntaxCap"   // 语法检查有上限,其余未查(args: n)
)

// PreColMissing 的操作,作为它的 part —— 一个名词短语,前端经 {inner} 插进句子里。
const (
	PreOpDropColumn   = "preOpDropColumn"
	PreOpModifyColumn = "preOpModifyColumn"
	PreOpRename       = "preOpRename"
	PreOpAddUnique    = "preOpAddUnique"
)

// PrecheckCodes 是全部预检 code(用途同 ApTextCodes)。
var PrecheckCodes = []string{
	PreNoCreds, PreColsUnreadable, PreObjsUnreadable, PreObjDropped, PreObjMissing, PreObjExists,
	PreObjUnrecognised, PreSyntaxOracle, PreSyntaxNotSQL, PreSyntaxSkipped, PreSyntaxVerb, PreSyntaxAfterMiss,
	PreSyntaxError, PreTableColsUnread, PreColMissing, PreColExists, PreRenameTarget, PreProbeNotNull,
	PreProbeNulls, PreProbeDuplicates, PreProbeBudget, PreProbeFailed, PreNoteTruncated, PreNoteSyntaxCap,
	PreOpDropColumn, PreOpModifyColumn, PreOpRename, PreOpAddUnique,
}

// RenderPrecheck 把预检结论渲染成规范中文串 —— 存进发布单的 message / note。
func RenderPrecheck(r *RuleRef) string {
	if r == nil {
		return ""
	}
	a := r.Args
	inner := ""
	if len(r.Parts) > 0 {
		inner = RenderPrecheck(&r.Parts[0])
	}
	switch r.Code {
	case PreOSCOnline:
		return "生产大表(约 " + a["rows"] + " 行 / " + a["size"] + "):执行阶段将以在线变更执行,须有已通过的审批"
	case PreOSCBlocked:
		return "在线变更前置检查未通过:" + a["reason"]
	case PreOSCWarn:
		return "在线变更提示:" + a["reason"]
	case PreOSCBigDirect:
		return "生产大表(约 " + a["rows"] + " 行 / " + a["size"] + "),在线变更不支持(" + a["why"] + "),将直接执行,可能长时间锁表"
	case PreOSCUnread:
		return "在线变更前置检查没能完成:" + a["err"]
	case PreNoCreds:
		return "该实例未配置真实执行凭据,语法与对象均未检查"
	case PreColsUnreadable:
		return "无法核对字段(" + a["err"] + "),字段与约束检查已跳过"
	case PreObjsUnreadable:
		return "无法核对对象(" + a["err"] + "),对象检查已跳过"
	case PreObjDropped:
		return "该对象在本次发布的前序语句里已被删除"
	case PreObjMissing:
		return "对象不存在"
	case PreObjExists:
		return "对象已存在,该语句可能失败"
	case PreObjUnrecognised:
		return "未能识别这条语句引用的对象,对象检查已跳过"
	case PreSyntaxOracle:
		return "Oracle 无法在不执行的前提下校验语法(EXPLAIN PLAN 会写 PLAN_TABLE,DDL 隐式提交),未做语法检查"
	case PreSyntaxNotSQL:
		return "该引擎不是 SQL,未做语法检查"
	case PreSyntaxSkipped:
		return "未做语法检查"
	case PreSyntaxVerb:
		return a["verb"] + " 语句无法在不执行的前提下校验语法(DDL 隐式提交或加表锁),未做语法检查"
	case PreSyntaxAfterMiss:
		return "引用对象缺失,语法检查已跳过(否则只会重复报同一件事)"
	case PreSyntaxError:
		return a["err"]
	case PreTableColsUnread:
		return "没能读到这张表的字段,字段与约束检查已跳过"
	case PreColMissing:
		return "字段不存在," + inner + "会失败"
	case PreOpDropColumn:
		return "删除字段"
	case PreOpModifyColumn:
		return "修改字段"
	case PreOpRename:
		return "改名"
	case PreOpAddUnique:
		return "加唯一约束"
	case PreColExists:
		return "字段已存在,这条加列语句会失败"
	case PreRenameTarget:
		return "改名的目标字段已存在,这条语句会失败"
	case PreProbeNotNull:
		return "表中已有数据,加非空且无默认值的字段会失败"
	case PreProbeNulls:
		return "该字段存在 NULL 值,改成非空会失败"
	case PreProbeDuplicates:
		return "这些字段上存在重复值,加唯一约束会失败"
	case PreProbeBudget:
		return "数据探测已达上限 " + a["n"] + " 次,这一项未核对"
	case PreProbeFailed:
		return "未能核对数据(" + a["err"] + ")"
	case PreNoteTruncated:
		return "语句共 " + a["total"] + " 条,只检查了前 " + a["n"] + " 条"
	case PreNoteSyntaxCap:
		return "语法检查上限 " + a["n"] + " 条,其余未检查。"
	}
	return ""
}

// JoinPrecheckNotes 把几条附注按原来的写法拼成一行(Note 字段历来是一个串)。
func JoinPrecheckNotes(refs []RuleRef) string {
	parts := make([]string, 0, len(refs))
	for i := range refs {
		parts = append(parts, RenderPrecheck(&refs[i]))
	}
	return strings.Join(parts, " ")
}
