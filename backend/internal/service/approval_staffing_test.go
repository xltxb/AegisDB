package service

// 审批人配置自检的核心判定。
//
// 起因是一个真实环境:owner 角色里只有一个人,而所有高危工单都由他发起。
// 每一层校验都"通过"了 —— 角色有成员、审批链建得出来、工单也创建成功 ——
// 然后两人控制让它们一张都批不掉。工单静静地堆在待办里,没有任何一处报错。
//
// 所以这里判的不是"链上有没有人",而是"这条链上到底有没有人**能**批"。
// 这两个问题的答案在只有一个人时并不相同,而正是那次不相同把人卡住了。

import (
	"strings"
	"testing"

	"velagateway/internal/model"
)

func human(id int64, name string) model.User {
	return model.User{ID: id, Name: name, Kind: model.UserKindHuman}
}

func service_(id int64, name string) model.User {
	return model.User{ID: id, Name: name, Kind: model.UserKindService}
}

func TestChainStaffing_NoMembersAtAll(t *testing.T) {
	got := chainStaffingIssue("默认审批链", nil, false)
	if got == nil {
		t.Fatal("空角色必须报告")
	}
	if !strings.Contains(got.Problem, "没有成员") {
		t.Errorf("问题描述应说明没有成员,实际: %q", got.Problem)
	}
	if got.Severity != StaffingBlocked {
		t.Errorf("没有成员时连工单都建不出来,应为 blocked,实际 %q", got.Severity)
	}
}

// 角色里"明明有人"却一个都不能批 —— 必须点破是服务账号,否则配置的人会盯着
// 成员列表百思不解。
func TestChainStaffing_ServiceAccountsOnly(t *testing.T) {
	got := chainStaffingIssue("默认审批链", []model.User{service_(9, "升级单平台")}, false)
	if got == nil {
		t.Fatal("只有服务账号时必须报告")
	}
	if !strings.Contains(got.Problem, "服务账号") {
		t.Errorf("要点破是服务账号,实际: %q", got.Problem)
	}
	if got.Severity != StaffingBlocked {
		t.Errorf("应为 blocked,实际 %q", got.Severity)
	}
}

// 这一条是整个自检存在的理由:链上恰好一个人,谁都没配错,工单也建得出来,
// 但只要是他自己发起的,两人控制下就永远没人能批。
func TestChainStaffing_SinglePersonDeadlocksUnderTwoPersonControl(t *testing.T) {
	got := chainStaffingIssue("默认审批链", []model.User{human(1, "Lin Wei")}, false)
	if got == nil {
		t.Fatal("链上只有一个人且未开自审批时,必须提前告警")
	}
	if got.Severity != StaffingDeadlock {
		t.Errorf("应为 deadlock,实际 %q", got.Severity)
	}
	if !strings.Contains(got.Problem, "Lin Wei") {
		t.Errorf("要说清是谁,实际: %q", got.Problem)
	}
	// 两条出路都要给:加人,或开自审批。只说一条会把人往单一方向推。
	if !strings.Contains(got.Fix, "自审批") {
		t.Errorf("建议里应包含自审批这条出路,实际: %q", got.Fix)
	}
}

// 开了自审批,一个人的链就是可用的 —— 单人运维的正当形态,不该继续告警。
func TestChainStaffing_SinglePersonIsFineWhenSelfApproveIsOn(t *testing.T) {
	if got := chainStaffingIssue("默认审批链", []model.User{human(1, "Lin Wei")}, true); got != nil {
		t.Errorf("开了自审批后不该再告警,实际: %+v", got)
	}
}

// 两个真人 = 两人控制成立,不告警。
func TestChainStaffing_TwoHumansIsHealthy(t *testing.T) {
	if got := chainStaffingIssue("默认审批链", []model.User{human(1, "A"), human(2, "B")}, false); got != nil {
		t.Errorf("两个真人不该告警,实际: %+v", got)
	}
}

// 一个真人 + 一个服务账号,仍然只有一个人能批 —— 服务账号不能用来凑人数。
// 这是最容易看走眼的一种:成员列表里有两行,自检必须只数能批的那一行。
func TestChainStaffing_ServiceAccountDoesNotCountTowardsTwoPersonControl(t *testing.T) {
	all := []model.User{human(1, "Lin Wei"), service_(2, "升级单平台")}
	got := chainStaffingIssue("默认审批链", all, false)
	if got == nil {
		t.Fatal("服务账号不能充当第二个审批人,应告警")
	}
	if got.Severity != StaffingDeadlock {
		t.Errorf("应为 deadlock,实际 %q", got.Severity)
	}
}
