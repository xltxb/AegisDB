package service

// payload 的历史版本表不能悄悄落后于代码。
//
// 这条链的规矩是:往哈希里加字段时**不重算历史行**(重算等于把证据重新签一遍)。代价是
// 每加一次,就多一个历史版本,而读侧校验必须认全它们。
//
// 下一个加字段的人会改 auditPayloadFor 的最新分支——这是自然的做法,而且看起来完全正确。
// 如果他忘了同时把版本号加一、把旧的那一版留在表里,后果是:**整条历史链在他手里静默
// 变成"全部被篡改"**。没有编译错误,没有测试红,只有下一次有人按下"校验"时看到一片红,
// 然后得出结论说这个校验不准、把它关掉。
//
// 所以这组用例钉的不是某个具体形状,而是那条规矩本身:
//   · 每一版都得能算出哈希,且各版互不相同(否则版本号是假的);
//   · 最新版必须真的是 currentAuditPayloadVersion;
//   · 各版的字段集合必须逐层包含——历史上每次都是"加字段",没有删过。

import (
	"encoding/json"
	"testing"
	"time"

	"velagateway/internal/model"
)

func sampleRow() *model.AuditLog {
	return &model.AuditLog{
		OccurredAt: time.Date(2026, 8, 1, 10, 30, 0, 0, time.UTC),
		ActorName:  "Lin Wei", Instance: "order-cluster", Database: "orders",
		Command: "DELETE FROM orders", Risk: "high", Result: "rejected",
		ApprovalNo: "AP-2288", Operator: "外部审批人", Env: "prod", TierCode: "prod",
	}
}

func keysOf(t *testing.T, b []byte) map[string]bool {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("payload 不是合法 JSON:%v", err)
	}
	out := map[string]bool{}
	for k := range m {
		out[k] = true
	}
	return out
}

func TestAuditPayloadVersions_EachIsDistinctAndGrows(t *testing.T) {
	a := sampleRow()
	prevKeys := map[string]bool{}
	seen := map[string]int{}
	for v := 1; v <= currentAuditPayloadVersion; v++ {
		b := auditPayloadFor(a, v)
		if old, dup := seen[string(b)]; dup {
			t.Errorf("v%d 与 v%d 算出来的 payload 一模一样 —— 版本号是假的,多出来的那一版什么也不区分", v, old)
		}
		seen[string(b)] = v

		keys := keysOf(t, b)
		for k := range prevKeys {
			if !keys[k] {
				t.Errorf("v%d 少了 v%d 有的字段 %q —— 历史上每次都是加字段,没有删过;真要删,这组用例和那张表都得一起改", v, v-1, k)
			}
		}
		prevKeys = keys
	}
}

// 写入用的那一版必须是最新版。这条看着废话,而它正是"加了字段却忘了加版本号"的那一刻
// 唯一会响的地方。
func TestAuditPayload_WritesTheNewestVersion(t *testing.T) {
	a := sampleRow()
	if string(auditPayload(a)) != string(auditPayloadFor(a, currentAuditPayloadVersion)) {
		t.Fatal("auditPayload 写出来的不是最新版")
	}
	// 最新版必须比前一版**多**些东西。往 auditPayloadFor 最新分支里加字段而不动版本号,
	// 这里不会响 —— 会响的是下面那条。
	if currentAuditPayloadVersion > 1 {
		if string(auditPayloadFor(a, currentAuditPayloadVersion)) ==
			string(auditPayloadFor(a, currentAuditPayloadVersion-1)) {
			t.Error("最新版与前一版没有差别")
		}
	}
}

// 最新版的字段集合被钉死在这里。
//
// 往 payload 里加字段,这条用例会红 —— 那正是它的用处:它逼你在改的那一刻想起"旧行
// 怎么办",而不是等到有人按下校验、看见一片红的时候。
//
// 改法:把新字段加进 auditPayloadFor 的一个**新**分支(v+1)、把
// currentAuditPayloadVersion 加一、把新字段补进下面这张表。**不要**去改 v1..v4 的
// 任何一个分支——那些行已经按当时那一版签过名了。
func TestAuditPayload_NewestShapeIsPinned(t *testing.T) {
	want := map[string]bool{
		"time": true, "actor": true, "instance": true, "command": true,
		"risk": true, "result": true, "ap": true,
		"database": true, "operator": true, "env": true, "tier": true,
	}
	got := keysOf(t, auditPayload(sampleRow()))
	for k := range want {
		if !got[k] {
			t.Errorf("最新版少了字段 %q", k)
		}
	}
	for k := range got {
		if !want[k] {
			t.Errorf("最新版多了字段 %q —— 加字段要同时新开一版并把版本号加一,否则历史行会被整片报成篡改(见本文件顶部)", k)
		}
	}
}
