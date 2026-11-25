package bootstrap

import (
	"testing"

	"velagateway/internal/model"
)

// 拦截理由要以**机器可读的身份**过一遍线,而不只是一句拼好的中文。
//
// 从前前端只拿到那句中文,于是英文界面上一整行英文提示中间嵌着一句中文 —— 界面的
// 语言是读的人的事。中文串仍然照发:它是审批单和审计链里的规范记录,也是前端碰上
// 这个版本还不认识的 code 时的回落。
func TestRiskCheck_CarriesMachineReadableRule(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	prod := app.connIDByEnv(token, "prod")

	r := app.riskCheckIn(token, prod, "DROP TABLE orders_2024_q3", "orders_db")
	if !r.RequiresApproval {
		t.Fatal("PROD 上的 DROP 应当需要审批")
	}
	if r.MatchedRule == "" {
		t.Error("规范中文串仍要发出去 —— 老客户端和未知 code 都靠它")
	}
	if r.MatchedRuleRef == nil {
		t.Fatal("没有规则标识,前端就只能显示服务端语言的文案")
	}
	if got := model.RenderRule(r.MatchedRuleRef); got != r.MatchedRule {
		t.Errorf("标识与文案说的不是同一句话:\n ref -> %q\nrule   %q", got, r.MatchedRule)
	}
}

// 批量粘贴时,逐条命中各自是一个 part —— 拼好的长句子没法翻译,而这正是操作者一次
// 看到最多规则名的场合。
func TestRiskCheck_BatchRuleIsStructuredPerStatement(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	prod := app.connIDByEnv(token, "prod")

	r := app.riskCheckIn(token, prod,
		"SELECT 1; DROP TABLE a; DROP TABLE b", "orders_db")
	if r.MatchedRuleRef == nil || r.MatchedRuleRef.Code != model.RuleBatch {
		t.Fatalf("多条语句应当给出批量标识,得到 %+v", r.MatchedRuleRef)
	}
	if len(r.MatchedRuleRef.Parts) != 2 {
		t.Fatalf("两条被拦下的语句应各占一个 part,得到 %d 个", len(r.MatchedRuleRef.Parts))
	}
	for i, p := range r.MatchedRuleRef.Parts {
		if len(p.Parts) != 1 || p.Parts[0].Code == "" {
			t.Errorf("第 %d 个 part 没有嵌套它自己那条规则:%+v", i+1, p)
		}
		if p.Args["pos"] == "" {
			t.Errorf("第 %d 个 part 没带语句序号,操作者就认不出是哪一条", i+1)
		}
	}
	if got := model.RenderRule(r.MatchedRuleRef); got != r.MatchedRule {
		t.Errorf("批量标识与文案不一致:\n ref -> %q\nrule   %q", got, r.MatchedRule)
	}
}
