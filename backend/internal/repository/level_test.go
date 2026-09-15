package repository

// 档位值要在门口就认得出来。
//
// LevelOf 在**读**的那一侧兜底(读不懂当 deny),那是对的 —— 库里可能已经有脏值。但
// 写的时候不能靠兜底:把 "Deny" 收下来、存进去、再在读的时候悄悄变成 deny,等于让人
// 以为自己写对了,而他下次照着改,改的是一个从来没生效过的值。

import (
	"testing"

	"velagateway/internal/model"
	"velagateway/internal/testsupport"
)

func newCapDB(t *testing.T) *Repo {
	t.Helper()
	db := testsupport.NewDB(t)
	return New(db)
}

func TestSetMatrix_RefusesAnUnreadableLevel(t *testing.T) {
	r := newCapDB(t)
	// "Deny" 不在此列 —— 大小写是同一个 deny,归一化收得住(见下一条用例)。
	for _, bad := range []string{"denied", "禁止", "", "  ", "allow ok"} {
		err := r.SetMatrix(1, map[string]map[string]string{"ddl": {"prod": bad}})
		if err == nil {
			t.Errorf("档位 %q 被收下了 —— 它读不懂,而写的人不会知道", bad)
		}
	}
	// 一格坏的,整次保存都不该落地(别留下一半生效的矩阵)。
	if lvl, _ := r.CapabilityLevel(1, "ddl", "prod"); lvl != model.LevelAllow {
		t.Errorf("被拒的保存留下了痕迹:该格现在是 %q(无配置该读作 allow)", lvl)
	}
}

func TestSetMatrix_NormalisesCasingAndPadding(t *testing.T) {
	r := newCapDB(t)
	if err := r.SetMatrix(1, map[string]map[string]string{"ddl": {"prod": " DENY "}}); err != nil {
		t.Fatalf("大小写和空白是同一个 deny,不该被拒:%v", err)
	}
	if lvl, _ := r.CapabilityLevel(1, "ddl", "prod"); lvl != model.LevelDeny {
		t.Errorf("存进去的是 %q,want %q", lvl, model.LevelDeny)
	}
}

// 库里已有的脏值(这次改动之前写进去的,或者别人直接改的库)读出来必须是 deny。
func TestCapabilityLevel_ExistingDirtyValueReadsAsDeny(t *testing.T) {
	r := newCapDB(t)
	// 绕开 SetMatrix 直接写,模拟历史数据。
	if err := r.db.Create(&model.RoleCapability{
		RoleID: 1, Capability: "ddl", TierCode: "prod", Level: "Deny",
	}).Error; err != nil {
		t.Fatalf("create: %v", err)
	}
	if lvl, _ := r.CapabilityLevel(1, "ddl", "prod"); lvl != model.LevelDeny {
		t.Errorf("库里的 %q 读成了 %q —— 判定对认不出的值兜底是放行", "Deny", lvl)
	}
}
