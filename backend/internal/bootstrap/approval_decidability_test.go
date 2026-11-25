package bootstrap

// 审批待办上"能不能点"这件事,必须由服务端说了算,并且说清楚理由。
//
// 起因是一个很具体的现场故障:审批人在待办里点"通过",页面毫无反应 —— 服务端其实
// 拒绝了(自己不能审自己发起的工单),但拒绝的理由被前端吞掉了,而按钮本来就不该
// 亮着。三件事各错一处,叠起来就成了"点了没用,也没人告诉我为什么"。
//
// 所以这里钉三条:
//   1. 拒绝的理由要准确 —— 不能把"不能自审"说成"不是审批链成员"。
//   2. 列表里每张单都带上"当前这个人能不能决定它",UI 据此决定按钮亮不亮。
//   3. 不能决定时要带上原因,好显示在按钮原来的位置。

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

type apDecidability struct {
	ID          int64  `json:"id"`
	Status      string `json:"status"`
	InitiatorID int64  `json:"initiatorId"`
	CanDecide   bool   `json:"canDecide"`
	BlockReason string `json:"blockReason"`
}

func (a *testApp) decidabilityOf(token string, id int64) apDecidability {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/approvals?page=1&pageSize=50", token, nil)
	var page struct {
		Items []apDecidability `json:"items"`
	}
	_ = json.Unmarshal(r.Data, &page)
	for _, it := range page.Items {
		if it.ID == id {
			return it
		}
	}
	a.t.Fatalf("工单 %d 不在待办列表里", id)
	return apDecidability{}
}

// 发起人自己去审,拒绝的理由必须说"不能审自己发起的",而不是"不是审批链成员"
// —— 他确实在审批链上,一条指错方向的报错会让人去查权限配置,查一整天。
func TestApproval_SelfApproveIsRefusedWithTheRealReason(t *testing.T) {
	app := newTestApp(t)
	owner := app.login("zhangwei@vela.io", "vela123")
	ap := app.submitProdHighRisk(owner) // zhangwei 既是发起人,也在审批链上

	r := app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/approve", owner, nil)
	if r.Code == 0 {
		t.Fatal("默认配置下发起人不该能审批自己发起的工单")
	}
	if strings.Contains(r.Msg, "审批链成员") {
		t.Errorf("理由指错了方向:%q —— 他就在审批链上,真正的原因是不能自审", r.Msg)
	}
	if !strings.Contains(r.Msg, "自己") {
		t.Errorf("理由里应说清是自己发起的工单,实际: %q", r.Msg)
	}

	// 驳回同理:自己发起的单,自己也不能驳。
	rj := app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/reject", owner, nil)
	if rj.Code == 0 {
		t.Error("发起人也不该能驳回自己发起的工单")
	}
}

// 不在审批链上的人,拒绝理由才是"审批链成员"。
func TestApproval_NonMemberIsRefusedAsANonMember(t *testing.T) {
	app := newTestApp(t)
	owner := app.login("zhangwei@vela.io", "vela123")
	ap := app.submitProdHighRisk(owner)

	outsider := app.login("chenhao@vela.io", "vela123") // l2,不在这条链上
	r := app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/approve", outsider, nil)
	if r.Code == 0 {
		t.Fatal("非审批链成员不该能审批")
	}
	if !strings.Contains(r.Msg, "审批链") {
		t.Errorf("非链成员的理由应指向审批链,实际: %q", r.Msg)
	}
}

// 列表要带上"这个人能不能决定这张单"。UI 只照着这个字段决定按钮亮不亮 ——
// 把规则在前端再算一遍,两份判断迟早会不一致,而不一致的那次就是一个亮着却
// 点不动的按钮。
func TestApproval_ListSaysWhetherTheViewerMayDecide(t *testing.T) {
	app := newTestApp(t)
	// 审批链是 owner 角色的成员。让 admin 发起,链上的人就不是发起人本人 ——
	// 这正是"两人控制"想要的常态,也是唯一能同时看到两种答案的组合。
	admin := app.login("linwei@vela.io", "vela123")
	ap := app.submitProdHighRisk(admin)

	// 发起人自己看:不能决定,且给得出原因。
	mine := app.decidabilityOf(admin, ap.ID)
	if mine.CanDecide {
		t.Error("发起人对自己的工单不该显示为可决定")
	}
	if strings.TrimSpace(mine.BlockReason) == "" {
		t.Error("不能决定时要给出原因,否则按钮位置只能是一片空白")
	}

	// 审批链上的人看:可以决定,且不带原因。
	approver := app.login("zhangwei@vela.io", "vela123")
	theirs := app.decidabilityOf(approver, ap.ID)
	if !theirs.CanDecide {
		t.Errorf("审批链上的成员应可决定,原因显示为 %q", theirs.BlockReason)
	}
	if theirs.BlockReason != "" {
		t.Errorf("可决定时不该带原因,实际 %q", theirs.BlockReason)
	}
}

// 自审批开关**只解除自己不能审自己这一条**,不会把人放上审批链。
// 这个区分很容易被当成开了就都能审,而那会是一个静悄悄的越权。
func TestApproval_SelfApproveSettingLiftsOnlyTheSelfBar(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	owner := app.login("zhangwei@vela.io", "vela123") // 既是发起人,也在审批链上
	ap := app.submitProdHighRisk(owner)

	if row := app.decidabilityOf(owner, ap.ID); row.CanDecide {
		t.Fatal("开关未开时发起人不该可决定")
	}
	eq(t, app.do(http.MethodPut, "/api/v1/settings", admin, map[string]any{
		"approval.allowSelfApprove": true,
	}).Code, 0, "enable self-approval")

	if row := app.decidabilityOf(owner, ap.ID); !row.CanDecide {
		t.Error("开关打开后,链上的发起人应显示为可决定 —— 否则开关开了按钮还是灰的")
	}

	// 但不在链上的人,开关开了也照样不能审。
	outsider := app.login("chenhao@vela.io", "vela123")
	r := app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/approve", outsider, nil)
	if r.Code == 0 {
		t.Error("自审批开关不该让非审批链成员也能审批")
	}
}

// 已经处理过的工单,谁都不能再决定。
func TestApproval_DecidedTicketIsNotDecidableByAnyone(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	ap := app.submitProdHighRisk(admin)
	approver := app.login("zhangwei@vela.io", "vela123")
	eq(t, app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/reject", approver, nil).Code, 0, "reject it")

	row := app.decidabilityOf(approver, ap.ID)
	if row.Status == "pending" {
		t.Fatal("工单应已被驳回")
	}
	if row.CanDecide {
		t.Error("已处理的工单不该再显示为可决定")
	}
}
