package service

// 审计链**覆盖哪些字段**。
//
// 一个改了还能对上的字段不是证据,是一句话。所以每一个会被人引用来下结论的字段,都必须
// 是"改它就破链"的。这条用例逐个字段验这件事:把它换掉,哈希必须变。
//
// 它同时钉住反方向的一半:**哈希要稳定**。同样的输入算两遍必须一样,否则整条链在重启、
// 换机器、换 Go 版本之后就对不上了(map 的遍历顺序是随机的,靠 fmt 拼字符串就会踩这个坑;
// 这里走 json.Marshal,它按键排序)。
//
// 第三件事是**分代**:载荷的键集随迁移长过(0039 的 event、0040 的 action、0041 的 ip)。
// 校验老行要按它写下那天的键集重算,所以每一代都得能单独算出来,而且这一代加进去的那个键
// 在上一代里必须不存在 —— 否则"按老键集重算"根本没有意义。

import (
	"encoding/json"
	"testing"
	"time"

	"aegisdb/internal/model"
)

func sampleAuditRow() *model.AuditLog {
	return &model.AuditLog{
		OccurredAt: time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC),
		ActorName:  "Lin Wei", Instance: "prod-order-01", Database: "orders",
		Env: "prod", TierCode: "prod",
		Command: "DROP TABLE orders_2024_q3;", Risk: "high", Result: "rejected",
		ApprovalNo: "AP-1", Operator: "",
		EventType: "intercept", DataAction: "delete", ClientIP: "10.0.0.9", Source: "terminal",
	}
}

func samplePayload(t *testing.T, mutate func(*model.AuditLog)) string {
	t.Helper()
	a := sampleAuditRow()
	if mutate != nil {
		mutate(a)
	}
	return string(auditHashPayload(a))
}

// 同样的输入,算两遍必须一模一样。
func TestAuditHashPayload_IsStable(t *testing.T) {
	for i := 0; i < 50; i++ {
		if a, b := samplePayload(t, nil), samplePayload(t, nil); a != b {
			t.Fatalf("同样的输入算出了两份不同的载荷:\n  %s\n  %s", a, b)
		}
	}
}

// 每一个会被引用来下结论的字段,都在载荷里。
//
// 这条用例的价值在于它是**逐个**验的:漏掉任何一个,那个字段就可以被改而不破链,而表面上
// 一切正常 —— 链照样验得过,只是它担保的东西少了一样。
func TestAuditHashPayload_CoversEveryFieldWorthQuoting(t *testing.T) {
	base := samplePayload(t, nil)
	cases := []struct {
		name   string
		mutate func(*model.AuditLog)
	}{
		{"时间", func(a *model.AuditLog) { a.OccurredAt = a.OccurredAt.Add(time.Second) }},
		{"操作人", func(a *model.AuditLog) { a.ActorName = "someone else" }},
		{"实例", func(a *model.AuditLog) { a.Instance = "prod-order-02" }},
		{"目标库", func(a *model.AuditLog) { a.Database = "payments" }},
		{"环境", func(a *model.AuditLog) { a.Env = "dev" }},
		{"分层", func(a *model.AuditLog) { a.TierCode = "dev" }},
		{"命令", func(a *model.AuditLog) { a.Command = "SELECT 1" }},
		{"风险", func(a *model.AuditLog) { a.Risk = "low" }},
		{"结果", func(a *model.AuditLog) { a.Result = "executed" }},
		{"工单号", func(a *model.AuditLog) { a.ApprovalNo = "AP-2" }},
		{"授权人", func(a *model.AuditLog) { a.Operator = "Zhang Wei" }},
		{"事件类型", func(a *model.AuditLog) { a.EventType = "login" }},
		{"增删改查", func(a *model.AuditLog) { a.DataAction = "read" }},
		{"来源 IP", func(a *model.AuditLog) { a.ClientIP = "10.0.0.10" }},
		{"入口来源", func(a *model.AuditLog) { a.Source = "query" }},
	}
	for _, c := range cases {
		if got := samplePayload(t, c.mutate); got == base {
			t.Errorf("改掉「%s」之后载荷没变 —— 这个字段可以被改而不破链", c.name)
		}
	}
}

// 分代:每一代恰好比上一代多出它自己那个键。
//
// 这是 VerifyAuditChain 逐代重试的前提 —— 老行按老键集才算得对。反过来也要钉住:新键
// **不能**出现在老代里,否则按老代重算一样带上了今天的字段,分代就是摆设。
func TestAuditPayloadGen_EachGenerationAddsExactlyItsOwnKey(t *testing.T) {
	a := sampleAuditRow()
	added := map[int]string{
		auditGen2: "env", // tier 同代,取一个代表
		auditGen3: "event",
		auditGen4: "action",
		auditGen5: "ip",
		auditGen6: "source",
	}
	keysOf := func(gen int) map[string]any {
		var m map[string]any
		if err := json.Unmarshal(auditPayloadGen(a, gen), &m); err != nil {
			t.Fatalf("第 %d 代载荷解不开: %v", gen, err)
		}
		return m
	}
	for gen := auditGenOldest + 1; gen <= auditGenCurrent; gen++ {
		key := added[gen]
		if key == "" {
			continue
		}
		if _, ok := keysOf(gen)[key]; !ok {
			t.Errorf("第 %d 代的载荷里没有它自己加的键 %q", gen, key)
		}
		if _, ok := keysOf(gen - 1)[key]; ok {
			t.Errorf("键 %q 出现在了第 %d 代里 —— 它是第 %d 代才加的,老行按老键集重算会带上今天的字段", key, gen-1, gen)
		}
	}
	// 最老那一代必须仍然算得出来:0039 之前写下的行只能按它验。
	if len(keysOf(auditGenOldest)) == 0 {
		t.Error("最老的一代算出了空载荷")
	}
}
