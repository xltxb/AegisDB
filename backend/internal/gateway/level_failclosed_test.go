package gateway

// 读不懂的档位必须当成拒绝,不能当成放行。
//
// 能力矩阵的档位是三个字符串:allow / approve / deny。判定这样消费它们 ——
//
//	if lvl == deny    { 拒 }
//	if lvl == approve { 送审 }
//	否则                { 放行 }
//
// 最后那个"否则"是个兜底,而它兜的方向是**放行**。于是任何读不懂的值都是放行:一个
// 大写的 "Deny"、一个尾随空格的 "deny "、一次导入脚本写进去的 "denied"。
//
// 更糟的是并集。多角色取最宽松那一档,而未知值在档位表里排 0 —— 也就是最宽松。所以
// 它不只是自己放行,它还会**压过**另一个角色上明明白白的 deny。
//
// 同一个查询函数对 capability 和 tier 是显式大小写不敏感的(注释写着:存进去的大小写
// 本来就不一致),唯独档位值原样返回。两个方向的不一致撞在一起,而失效的方向是开放的。

import (
	"testing"

	"velagateway/internal/model"
)

func TestEvaluate_UnreadableLevelRefuses(t *testing.T) {
	for _, lvl := range []string{"Deny", "DENY", " deny ", "denied", "拒绝", ""} {
		store := &fakeStore{caps: map[string]string{"ddl|prod": lvl}}
		v := NewRiskEngine(store).EvaluateFor([]int64{1}, "", "prod", "DROP TABLE tbl_order")
		if v.Action == ActionAllow {
			t.Errorf("档位存成 %q,DROP TABLE 被放行了 —— 读不懂的档位兜到了放行那一侧", lvl)
		}
	}
}

// 归一化要认得住大小写与空白:那是同一个 deny,不是一个读不懂的值。
func TestEvaluate_LevelCasingAndPaddingStillMeanWhatTheySay(t *testing.T) {
	for _, tc := range []struct {
		lvl  string
		want string
	}{
		{"ALLOW", ActionAllow},
		{" allow ", ActionAllow},
		{"Approve", ActionApprove},
		{"DENY", ActionDeny},
	} {
		store := &fakeStore{caps: map[string]string{"ddl|prod": tc.lvl}}
		v := NewRiskEngine(store).EvaluateFor([]int64{1}, "", "prod", "DROP TABLE tbl_order")
		if v.Action != tc.want {
			t.Errorf("档位 %q:判定 %q,want %q", tc.lvl, v.Action, tc.want)
		}
	}
}

// 并集里,读不懂的那一档不能压过另一个角色上真正的 deny。
func TestEvaluate_UnreadableLevelDoesNotWinTheUnion(t *testing.T) {
	store := &multiRoleStore{caps: map[int64]string{
		1: model.LevelDeny, // 这个角色明确禁止
		2: "Deny",          // 这个角色的值读不懂
	}}
	v := NewRiskEngine(store).EvaluateFor([]int64{1, 2}, "", "prod", "DROP TABLE tbl_order")
	if v.Action == ActionAllow {
		t.Error("一个读不懂的档位压过了另一个角色上明确的 deny")
	}
}

// multiRoleStore 按角色给出不同档位 —— fakeStore 是按 capability|tier 索引的,分不开角色。
type multiRoleStore struct{ caps map[int64]string }

func (m *multiRoleStore) CapabilityLevel(roleID int64, _, _ string) (string, error) {
	if lvl, ok := m.caps[roleID]; ok {
		return lvl, nil
	}
	return model.LevelAllow, nil
}
func (m *multiRoleStore) RiskCommands() ([]model.RiskCommand, error) { return nil, nil }
func (m *multiRoleStore) StrictNoWhere(string) (bool, error)         { return false, nil }
