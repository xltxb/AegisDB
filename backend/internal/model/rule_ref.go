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
	OutExecFailed     = "execFailed"     // · 数据库执行失败(args: err = 驱动原文,不翻译)
	OutMaintenance    = "maintenance"    // · 目标实例处于维护态,操作受限
	OutApWindowLive   = "apWindowLive"   // · 已批准,执行窗口已生效
	OutApExportQueued = "apExportQueued" // · 已批准,导出任务已进入队列
	OutApPipeline     = "apPipeline"     // · 已批准,由发布流水线继续执行
	OutApAwaitingExec = "apAwaitingExec" // · 已批准,等待发起人执行
)

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
