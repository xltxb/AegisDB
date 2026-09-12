package bootstrap

// 反斜杠转义:同一串字节,在两族引擎里是两条不同的语句。
//
//	UPDATE orders SET status='x\' WHERE 1=1 --'
//
// MySQL 默认 sql_mode 下 `\'` 是转义的引号 —— 字符串一直延伸到末尾那个引号,整条
// 语句**没有 WHERE**,是一次整表更新。PostgreSQL 的标准字符串里反斜杠不转义,那里
// 的 `WHERE 1=1` 是真的子句。
//
// 判定层原先两边都按"反斜杠不是转义"读,于是在 MySQL 上把字面量里那截当成了结构:
// 无 WHERE 拦截**整层被跳过**,风险从 high 降成 mid,而审批人看到的规则文案写着
// "能力矩阵 · 需审批" —— 他以为自己在批一条带条件的更新,实际上是一次清表。

import (
	"encoding/json"
	"net/http"
	"testing"

	"velagateway/internal/model"
)

// connIDByEngine 挑一台引擎名里带某个词的实例 —— 这一组要的正是"同一条语句、
// 两族引擎"的对照,按 env 取到的是哪一台并不确定。
func (a *testApp) connIDByEngine(token, want string) int64 {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/connections", token, nil)
	if r.Code != 0 {
		a.t.Fatalf("list connections: code=%d msg=%s", r.Code, r.Msg)
	}
	var conns []struct {
		ID     int64  `json:"id"`
		Env    string `json:"env"`
		Engine string `json:"engine"`
	}
	if err := json.Unmarshal(r.Data, &conns); err != nil {
		a.t.Fatalf("connections decode: %v", err)
	}
	for _, c := range conns {
		if c.Env == "prod" && len(c.Engine) >= len(want) && c.Engine[:len(want)] == want {
			return c.ID
		}
	}
	a.t.Fatalf("没有 prod 上引擎以 %q 开头的实例", want)
	return 0
}

const escapedNoWhereSQL = `UPDATE orders SET status='x\' WHERE 1=1 --'`

func TestStrictNoWhere_BackslashEscapeDoesNotSkipTheGate(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")

	// 先把基线钉住:去掉反斜杠,这就是一条摆明的整表更新。
	base := app.riskCheck(token, app.connIDByEngine(token, "MySQL"), `UPDATE orders SET status='x'`)
	if base.MatchedRuleRef == nil || base.MatchedRuleRef.Code != model.RuleStrictNoWhere {
		t.Fatalf("前置条件不成立:PROD 上裸的整表更新应当命中严格模式,实际 rule=%v risk=%s",
			base.MatchedRuleRef, base.Risk)
	}

	got := app.riskCheck(token, app.connIDByEngine(token, "MySQL"), escapedNoWhereSQL)
	if got.MatchedRuleRef == nil || got.MatchedRuleRef.Code != model.RuleStrictNoWhere {
		t.Errorf("MySQL 上这条整表更新绕过了无 WHERE 拦截:rule=%v risk=%s —— 审批人看到的理由是错的",
			got.MatchedRuleRef, got.Risk)
	}
	if got.Risk != model.RiskHigh {
		t.Errorf("绕过让风险从 high 降成了 %s", got.Risk)
	}
}

// 反过来那一半同样要守住:PostgreSQL 上这条语句真的带 WHERE,判成无 WHERE 就是
// 误报 —— 一条普通的条件更新被升成 high,只会让人开始怀疑网关判错了库。
func TestStrictNoWhere_StandardStringsAreNotOverGated(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")

	got := app.riskCheck(token, app.connIDByEngine(token, "PostgreSQL"), escapedNoWhereSQL)
	if got.MatchedRuleRef != nil && got.MatchedRuleRef.Code == model.RuleStrictNoWhere {
		t.Errorf("PostgreSQL 的标准字符串里反斜杠不转义,这条语句带着 WHERE,不该命中严格模式")
	}
}
