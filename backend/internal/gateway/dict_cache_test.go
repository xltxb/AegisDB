package gateway

// 字典的编译结果可以复用,但**字典改了必须立刻跟上**。
//
// matchCommand 每条语句都要:查一次全表 + 把整张字典拼成一个正则再编译一次。而它在每条
// 语句的判定上都会走 —— 一个 200 条语句的脚本扫描就是 200 次。
//
// 缓存编译结果是显然的优化,而它唯一会出的错也是显然的:**字典改了而缓存没跟上**。运维
// 在界面上把 DROP 从 high 调成 off,下一条命令仍按 high 拦 —— 或者更糟,把某个词加进
// 字典而它迟迟不生效,而那正是他为了拦住某件事刚做的。
//
// 所以这一组用例先于缓存存在:它们现在就是绿的,加了缓存之后必须还是绿的。

import (
	"testing"

	"velagateway/internal/model"
)

func TestMatchCommand_ReflectsDictionaryChanges(t *testing.T) {
	store := &fakeStore{cmds: []model.RiskCommand{
		{Command: "DROP", TierCode: "prod", Level: model.RiskHigh},
	}}
	e := NewRiskEngine(store)

	if _, lvl, _ := e.matchCommand("DROP TABLE t", "prod"); lvl != model.RiskHigh {
		t.Fatalf("前置条件不成立:DROP 应当是 high,实际 %q", lvl)
	}

	// 运维把 DROP 调成 off,又把 TRUNCATE 加进来。
	store.cmds = []model.RiskCommand{
		{Command: "DROP", TierCode: "prod", Level: model.RiskOff},
		{Command: "TRUNCATE", TierCode: "prod", Level: model.RiskHigh},
	}

	if _, lvl, _ := e.matchCommand("DROP TABLE t", "prod"); lvl != model.RiskOff {
		t.Errorf("把 DROP 调成 off 之后仍按 %q 判 —— 他刚做的改动没生效", lvl)
	}
	if name, lvl, _ := e.matchCommand("TRUNCATE TABLE t", "prod"); lvl != model.RiskHigh || name != "TRUNCATE" {
		t.Errorf("新加进字典的词没生效:命中 %q、档位 %q —— 而那正是他刚为了拦住某件事加的", name, lvl)
	}
}

// 字典整个被清空之后,不能还按旧的拦。
func TestMatchCommand_EmptiedDictionaryStopsMatching(t *testing.T) {
	store := &fakeStore{cmds: []model.RiskCommand{
		{Command: "DROP", TierCode: "prod", Level: model.RiskHigh},
	}}
	e := NewRiskEngine(store)
	_, _, _ = e.matchCommand("DROP TABLE t", "prod") // 先让它缓存一次

	store.cmds = nil
	if name, lvl, _ := e.matchCommand("DROP TABLE t", "prod"); name != "" || lvl != model.RiskOff {
		t.Errorf("字典清空后仍命中 %q/%q", name, lvl)
	}
}

// 每个分层有自己的字典 —— 缓存不能把它们混成一份。
func TestMatchCommand_TiersKeepSeparateDictionaries(t *testing.T) {
	store := &fakeStore{cmds: []model.RiskCommand{
		{Command: "DROP", TierCode: "prod", Level: model.RiskHigh},
		{Command: "DROP", TierCode: "dev", Level: model.RiskOff},
	}}
	e := NewRiskEngine(store)

	if _, lvl, _ := e.matchCommand("DROP TABLE t", "prod"); lvl != model.RiskHigh {
		t.Errorf("PROD 上的 DROP 应当是 high,实际 %q", lvl)
	}
	if _, lvl, _ := e.matchCommand("DROP TABLE t", "dev"); lvl != model.RiskOff {
		t.Errorf("DEV 上的 DROP 应当是 off,实际 %q —— 两层的字典被混成了一份", lvl)
	}
	// 反过来再问一次:顺序不该影响结论。
	if _, lvl, _ := e.matchCommand("DROP TABLE t", "prod"); lvl != model.RiskHigh {
		t.Error("换个顺序问,PROD 的结论就变了")
	}
}
