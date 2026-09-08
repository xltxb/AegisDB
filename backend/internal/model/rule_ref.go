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
