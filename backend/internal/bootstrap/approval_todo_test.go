package bootstrap

// 右上角那枚"待办"数的是**此刻在等我动手**的工单,不是"我看得见的待审批"。
//
// 这两个数经常不一样,而混在一起的后果很具体:发起人提了一张单、正在等别人审,
// 徽章上却亮着 1 —— 他点进去找不到任何一个能按的按钮,于是学会了无视这枚徽章;
// 等真有一张在等他执行的单子时,他也不看了。
//
// 所以这里钉四条:
//   1. 待审批只算**能决定**的人:链上的人算,发起人自己不算(两人控制)。
//   2. 通过之后,它从审批人的待办里消失,出现在**能执行的人**的待执行里。
//   3. 执行完就都没了。
//   4. 和这张单毫无关系的人,从头到尾都是零。

import (
	"encoding/json"
	"net/http"
	"testing"
)

type todoView struct {
	Decide  int `json:"decide"`
	Execute int `json:"execute"`
	Confirm int `json:"confirm"`
	Items   []struct {
		ID      int64  `json:"id"`
		ApNo    string `json:"apNo"`
		Kind    string `json:"kind"`
		Command string `json:"command"`
		Stage   string `json:"stage"`
	} `json:"items"`
}

func (a *testApp) todoOf(token string) todoView {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/approvals/todo", token, nil)
	if r.Code != 0 {
		a.t.Fatalf("待办接口: code=%d msg=%s", r.Code, r.Msg)
	}
	var v todoView
	if err := json.Unmarshal(r.Data, &v); err != nil {
		a.t.Fatalf("待办解码: %v", err)
	}
	return v
}

func (v todoView) has(apNo, kind string) bool {
	for _, it := range v.Items {
		if it.ApNo == apNo && it.Kind == kind {
			return true
		}
	}
	return false
}

func TestApprovalTodo_FollowsTheTicketToWhoeverMustActNext(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	approver := app.login("zhangwei@aegisdb.io", "aegisdb123") // 审批链成员
	outsider := app.login("chenhao@aegisdb.io", "aegisdb123")  // 不在链上、够不到那台实例

	// 种子数据里本来就有几张演示工单挂在链上,所以下面全按**增量**看:这张单出现前后,
	// 各人的待办差多少。
	base := map[string]todoView{"admin": app.todoOf(admin), "approver": app.todoOf(approver), "outsider": app.todoOf(outsider)}

	dev, conn := app.sameDBConns(admin, "todo") // dev 备数据,prod 触发审批
	if r := app.execSQL(admin, dev, `CREATE TABLE t_todo (id INTEGER PRIMARY KEY)`); r.Code != 0 {
		t.Fatalf("建表: %s", r.Msg)
	}
	ap := app.interceptedTicket(admin, conn, `DROP TABLE t_todo`)

	// 1. 待审批:链上的人多了一条,发起人自己没多(他不能审自己的),局外人没多。
	if v := app.todoOf(approver); v.Decide != base["approver"].Decide+1 || !v.has(ap.ApNo, "decide") {
		t.Errorf("审批人应多 1 条待审批且指向这张单,基线 %+v 实际 %+v", base["approver"], v)
	}
	if v := app.todoOf(admin); v.Decide != base["admin"].Decide || v.Execute != base["admin"].Execute || v.has(ap.ApNo, "decide") {
		t.Errorf("发起人不能审自己的单,也还没到执行 —— 待办不该变,基线 %+v 实际 %+v", base["admin"], v)
	}
	if v := app.todoOf(outsider); v.Decide != base["outsider"].Decide || v.Execute != base["outsider"].Execute {
		t.Errorf("局外人的待办不该变,基线 %+v 实际 %+v", base["outsider"], v)
	}

	// 2. 通过:从审批人的待办里消失,出现在发起人的待执行里。
	eq(t, app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/approve", approver, nil).Code, 0, "审批通过")
	if v := app.todoOf(approver); v.Decide != base["approver"].Decide || v.has(ap.ApNo, "decide") {
		t.Errorf("批过之后这张单应从审批人的待审批里消失,基线 %+v 实际 %+v", base["approver"], v)
	}
	v := app.todoOf(admin)
	if v.Execute != base["admin"].Execute+1 || !v.has(ap.ApNo, "execute") {
		t.Errorf("通过之后应在发起人的待执行里,基线 %+v 实际 %+v", base["admin"], v)
	}
	if v.has(ap.ApNo, "decide") {
		t.Errorf("已批准的单不该再算待审批,实际 %+v", v)
	}
	for _, it := range v.Items {
		if it.Command == "" {
			t.Error("面板里每一行要带命令预览,否则人不知道那是哪条")
		}
	}

	// 3. 执行完就都没了。
	eq(t, app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/execute", admin, nil).Code, 0, "发起人执行")
	if v := app.todoOf(admin); v.Execute != base["admin"].Execute || v.has(ap.ApNo, "execute") {
		t.Errorf("执行完这张单应从待执行里消失,基线 %+v 实际 %+v", base["admin"], v)
	}
}

// 待办不等于"待审批"总数:ListApprovals 的 pending 把发起人自己的单也算进去 —— 那是
// 它的语义(我看得见的待审批),待办不能沿用它。
func TestApprovalTodo_IsNotTheVisiblePendingCount(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	ap := app.submitProdHighRisk(admin)

	page := app.approvalPage(admin, "?scope=all&page=1&pageSize=1")
	if page.Pending < 1 {
		t.Fatalf("前置:发起人看得见自己的待审批单 %s,pending 应≥1,实际 %d", ap.ApNo, page.Pending)
	}
	if v := app.todoOf(admin); v.has(ap.ApNo, "decide") {
		t.Errorf("自己发起、等别人审的单不是自己的待办,实际 %+v", v)
	}
}
