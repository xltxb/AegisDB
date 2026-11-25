package service

// 发布阶段日志的构造器:日志先写成逐行 RuleRef,规范中文(log 列)由 model.RenderRelLogLines
// 从同一份 refs 渲染出来 —— 两份写法迟早分叉,分叉的表现是英文界面说的和记录里写的不是同一件事。
// 规则与取舍见 model/release_log_ref.go。

import (
	"encoding/json"
	"regexp"
	"strings"

	"velagateway/internal/gateway"
	"velagateway/internal/model"
	"velagateway/pkg/sqlutil"
)

// relLog 是一段阶段日志。零值可用。
type relLog struct{ refs []model.RuleRef }

func relLogOf(code string, kv ...string) relLog {
	var l relLog
	l.add(code, kv...)
	return l
}

// add 另起一行。
func (l *relLog) add(code string, kv ...string) *model.RuleRef {
	l.refs = append(l.refs, *model.NewRuleRef(code, kv...))
	return &l.refs[len(l.refs)-1]
}

// cat 接在上一行末尾。
func (l *relLog) cat(code string, kv ...string) *model.RuleRef {
	r := l.add(code, kv...)
	if r.Args == nil {
		r.Args = map[string]string{}
	}
	r.Args["cat"] = "1"
	return r
}

// raw 原样一行(可以含换行):语句回显、结果集预览。
func (l *relLog) raw(text string) {
	if text != "" {
		l.add(model.RelLogRaw, "text", text)
	}
}

func (l *relLog) extend(o relLog) { l.refs = append(l.refs, o.refs...) }

func (l relLog) clone() relLog { return relLog{refs: append([]model.RuleRef(nil), l.refs...)} }

func (l relLog) text() string { return model.RenderRelLogLines(l.refs) }

// fields 是写进阶段那一行的 log / log_refs。规范串超过列宽被截时 refs 置空 —— 半截的 refs 与
// 截过的原串对不上,界面回落显示原串。
func (l relLog) fields() map[string]any {
	text := l.text()
	refs := ""
	if len(text) <= releaseLogMax {
		refs = refsJSON(l.refs)
	}
	return map[string]any{"log": clip(text, releaseLogMax), "log_refs": refs}
}

const releaseLogMax = 20000

func refsJSON(refs []model.RuleRef) string {
	if len(refs) == 0 {
		return ""
	}
	b, err := json.Marshal(refs)
	if err != nil {
		return ""
	}
	return string(b)
}

func refJSON(r *model.RuleRef) string {
	if r == nil {
		return ""
	}
	b, err := json.Marshal(r)
	if err != nil {
		return ""
	}
	return string(b)
}

// withOut 给一行挂上执行器那句话:out 是规范串(clipAt>0 时截断),机器可读身份放在 parts[0]、
// 命名空间写在 args["ns"]。执行器没给 OutputRef 的几句由 execOutRef 认。
func withOut(r *model.RuleRef, res gateway.ExecResult, clipAt int) {
	out := res.Output
	if clipAt > 0 {
		out = clip(out, clipAt)
	}
	r.Args["out"] = out
	if ns, part := execOutRef(res); part != nil {
		r.Args["ns"] = ns
		r.Parts = []model.RuleRef{*part}
	}
}

var outRowsRe = regexp.MustCompile(`^\+ ([0-9,]+)(?:\+ rows \(显示前 ([0-9]+)\)| rows)$`)

func execOutRef(res gateway.ExecResult) (string, *model.RuleRef) {
	if res.OutputRef != nil {
		return "outText", res.OutputRef
	}
	if res.Output == "执行成功" {
		return "rlLog", model.NewRuleRef(model.RelLogOutOk)
	}
	if m := outRowsRe.FindStringSubmatch(res.Output); m != nil {
		if m[2] != "" {
			return "rlLog", model.NewRuleRef(model.RelLogOutRowsTrunc, "n", m[1], "max", m[2])
		}
		return "rlLog", model.NewRuleRef(model.RelLogOutRows, "n", m[1])
	}
	return "", nil
}

// gateWhoNames 是 gateWhoLabel 的英文分隔版(界面用,"、"在英文里不是分隔符)。
func gateWhoNames(people []model.User) string {
	return strings.ReplaceAll(gateWhoLabel(people), "、", ", ")
}

// redactRefs 给 refs 里的每个参数打码 —— log_refs 带着语句回显与驱动报错,与 log 同一个风险,
// 见 releaseView。
func redactRefs(raw string, many bool) string {
	if raw == "" {
		return ""
	}
	var walk func(r *model.RuleRef)
	walk = func(r *model.RuleRef) {
		for k, v := range r.Args {
			r.Args[k] = sqlutil.RedactSecrets(v)
		}
		for i := range r.Parts {
			walk(&r.Parts[i])
		}
	}
	if many {
		var refs []model.RuleRef
		if json.Unmarshal([]byte(raw), &refs) != nil {
			return ""
		}
		for i := range refs {
			walk(&refs[i])
		}
		return refsJSON(refs)
	}
	var r model.RuleRef
	if json.Unmarshal([]byte(raw), &r) != nil {
		return ""
	}
	walk(&r)
	return refJSON(&r)
}

func msText(ms int) string { return model.Itoa(ms) }

// execLogText 是 execLogStmt 去掉行尾换行:一个 ref 就是一行,换行由拼接时加。
func execLogText(one string) string { return strings.TrimSuffix(execLogStmt(one), "\n") }

// recheckFail:执行前复判拒绝 / 要审批。规则名的规范串进参数,它的 RuleRef 进 parts(ruleText.)。
func recheckFail(code string, v gateway.Verdict) stageOutcome {
	out := failStage(code, "rule", v.Rule)
	if v.Ref != nil {
		r := &out.log.refs[0]
		r.Args["ns"] = "ruleText"
		r.Parts = []model.RuleRef{*v.Ref}
	}
	return out
}

// releaseErrFields 把发布单的错误条(规范串 + ref)并进要更新的字段。ref 为 nil = 清空。
func releaseErrFields(ref *model.RuleRef, fields map[string]any) map[string]any {
	fields["error"] = clip(model.RenderRelLog(ref), 400)
	fields["error_ref"] = refJSON(ref)
	return fields
}
