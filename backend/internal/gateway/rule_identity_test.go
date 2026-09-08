package gateway

import (
	"strings"
	"testing"

	"velagateway/internal/model"
)

// 每一条被拦下的裁决都要带上规则标识,而且标识渲染出来必须和 Rule 字段一字不差。
//
// 两者一旦分叉,审计里记下的规则就不是操作者当时看到的那条 —— 而事后追责比对的正是
// 这一列文本。所以这里钉的不是"有没有 code",是"两条路说的是不是同一句话"。
func TestVerdict_RefAndRuleAlwaysAgree(t *testing.T) {
	store := &fakeStore{
		cmds: []model.RiskCommand{
			{Command: "DROP", TierCode: "PROD", Level: model.RiskHigh},
			{Command: "TRUNCATE", TierCode: "PROD", Level: model.RiskMid},
		},
		caps:   map[string]string{"grant|prod": model.LevelDeny},
		strict: true,
	}
	e := NewRiskEngine(store)

	for _, sql := range []string{
		"DROP TABLE orders",     // 字典 · 高
		"TRUNCATE TABLE orders", // 字典 · 中
		"GRANT ALL ON x TO y",   // 能力矩阵 · 禁止
		"DELETE FROM orders",    // 严格模式 · 无 WHERE
		"SELECT 1",              // 放行:两边都该是空
	} {
		v := e.Evaluate(1, "prod", sql)
		if v.Action == ActionAllow {
			if v.Ref != nil || v.Rule != "" {
				t.Errorf("%q 放行的裁决不该带规则,得到 ref=%v rule=%q", sql, v.Ref, v.Rule)
			}
			continue
		}
		if v.Ref == nil {
			t.Errorf("%q 被拦下却没有规则标识", sql)
			continue
		}
		if got := model.RenderRule(v.Ref); got != v.Rule {
			t.Errorf("%q: Rule=%q 但 RenderRule(Ref)=%q", sql, v.Rule, got)
		}
	}
}

// 字典拦截的理由里,分层名要是**这次判定用的**那一层。
//
// 它从前硬写着 PROD:在 UAT 上被字典拦下的人读到的是"PROD 禁止直接执行",一条对不上
// 自己所在环境的理由 —— 只会让人以为网关判错了库,而不是以为自己踩了 UAT 的字典。
func TestDictionaryDeny_NamesTheTierItJudgedOn(t *testing.T) {
	store := &fakeStore{cmds: []model.RiskCommand{
		{Command: "ALTER", TierCode: "UAT", Level: model.RiskHigh},
		{Command: "ALTER", TierCode: "PROD", Level: model.RiskHigh},
	}}
	e := NewRiskEngine(store)

	for _, tier := range []string{"uat", "prod"} {
		v := e.Evaluate(1, tier, "ALTER TABLE customers ADD COLUMN c INT")
		if v.Ref == nil || v.Ref.Code != model.RuleDictDeny {
			t.Fatalf("%s: 期待字典禁止,得到 %+v", tier, v.Ref)
		}
		want := strings.ToUpper(tier)
		if v.Ref.Args["tier"] != want {
			t.Errorf("%s: 规则标识里的分层是 %q,应为 %q", tier, v.Ref.Args["tier"], want)
		}
		if !strings.Contains(v.Rule, want+" 禁止直接执行") {
			t.Errorf("%s: 规则文案说的是别的分层:%q", tier, v.Rule)
		}
	}
}
