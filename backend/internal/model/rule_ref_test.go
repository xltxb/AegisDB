package model_test

import (
	"testing"

	"velagateway/internal/model"
)

// 规范中文串是**存进审批单和审计链**的那句话。把渲染改成由 code 生成之后,这些串
// 必须一字不差地保持原样 —— 否则历史记录与新记录说的是两句话,而事后追责比对的正是
// 这一列文本。
func TestRenderRule_CanonicalTextUnchanged(t *testing.T) {
	cases := []struct {
		ref  *model.RuleRef
		want string
	}{
		{model.NewRuleRef(model.RuleCapDeny), "能力矩阵 · 该环境禁止此操作"},
		{model.NewRuleRef(model.RuleCapApprove), "能力矩阵 · 需审批"},
		{model.NewRuleRef(model.RuleDictApprove), "高危命令字典 · 需审批"},
		{model.NewRuleRef(model.RuleDictDeny, "tier", "PROD"), "高危命令字典 · PROD 禁止直接执行"},
		{model.NewRuleRef(model.RuleStrictNoWhere), "严格模式 · 无 WHERE 的 DELETE / UPDATE"},
		{model.NewRuleRef(model.RuleUnavailable), "风险控制暂时不可用 · 已按最严处理"},
		{model.NewRuleRef(model.RuleScriptHigh), "脚本含高危语句 · 需审批"},
		{nil, ""},
	}
	for _, c := range cases {
		if got := model.RenderRule(c.ref); got != c.want {
			t.Errorf("RenderRule = %q, want %q", got, c.want)
		}
	}
}

// 嵌套的两种:执行窗口把原判包在里面,批量把逐条命中包在里面。两者的中文串都要和
// 从前拼出来的一模一样。
func TestRenderRule_NestedMatchesOldConcatenation(t *testing.T) {
	inner := model.NewRuleRef(model.RuleDictDeny, "tier", "PROD")

	win := model.NewRuleRef(model.RuleExecWindow, "window", "凌晨发车")
	win.Parts = []model.RuleRef{*inner}
	want := "执行窗口「凌晨发车」· 免审批放行(原判:高危命令字典 · PROD 禁止直接执行)"
	if got := model.RenderRule(win); got != want {
		t.Errorf("窗口:\n got %q\nwant %q", got, want)
	}

	hit1 := model.NewRuleRef(model.RuleBatchHit, "pos", "2", "command", "TRUNCATE")
	hit1.Parts = []model.RuleRef{*inner}
	hit2 := model.NewRuleRef(model.RuleBatchHitBare, "pos", "5")
	hit2.Parts = []model.RuleRef{*model.NewRuleRef(model.RuleCapApprove)}
	batch := model.NewRuleRef(model.RuleBatch)
	batch.Parts = []model.RuleRef{*hit1, *hit2, *model.NewRuleRef(model.RuleBatchMore, "n", "3")}
	want = "第2条 TRUNCATE · 高危命令字典 · PROD 禁止直接执行 + 第5条 · 能力矩阵 · 需审批 + …另有 3 条命中"
	if got := model.RenderRule(batch); got != want {
		t.Errorf("批量:\n got %q\nwant %q", got, want)
	}
}

// 认不出的 code 渲染成空串,而不是把 code 本身当文案吐出来:客户端遇到这种情况要
// 回落到服务端给的 Rule 字符串,一个假装是文案的 "someNewCode" 会让它以为有话可说。
func TestRenderRule_UnknownCodeIsEmpty(t *testing.T) {
	if got := model.RenderRule(model.NewRuleRef("somethingNewerThanThisBuild")); got != "" {
		t.Errorf("未知 code 应渲染为空串,得到 %q", got)
	}
}
