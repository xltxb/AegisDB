package bootstrap

// 2026-10 黑盒(工单 01 第 6、7 条)。

import (
	"encoding/json"
	"net/http"
	"testing"
)

type apStepView struct {
	ID        int64  `json:"id"`
	ApNo      string `json:"apNo"`
	CanDecide bool   `json:"canDecide"`
	Steps     []struct {
		StepOrder int     `json:"stepOrder"`
		Approver  string  `json:"approver"`
		Status    string  `json:"status"`
		ActedAt   *string `json:"actedAt"`
	} `json:"steps"`
}

func (a *testApp) approvalsPage(token, query string) (items []apStepView, total int64) {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/approvals"+query, token, nil)
	if r.Code != 0 {
		a.t.Fatalf("list approvals%s: code=%d msg=%s", query, r.Code, r.Msg)
	}
	var page struct {
		Items []apStepView `json:"items"`
		Total int64        `json:"total"`
	}
	if err := json.Unmarshal(r.Data, &page); err != nil {
		a.t.Fatal(err)
	}
	return page.Items, page.Total
}

func findAp(items []apStepView, apNo string) *apStepView {
	for i := range items {
		if items[i].ApNo == apNo {
			return &items[i]
		}
	}
	return nil
}

// 第 6 条:链上第 2 步的 Li Na 批准,steps 里记的是 Li Na 那一步 —— 黑盒里显示成
// "Lin Wei / Zhang Wei: approved",而审计 operator 是实际批的人,两边对不上。
func TestApproval_StepRecordsTheActualApprover(t *testing.T) {
	for _, verb := range []string{"approve", "reject"} {
		t.Run(verb, func(t *testing.T) {
			app := newTestApp(t)
			admin := app.login("linwei@aegisdb.io", "aegisdb123")
			lina := app.login("lina@aegisdb.io", "aegisdb123")
			ap := app.submitProdHighRisk(admin)

			if r := app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/"+verb, lina, nil); r.Code != 0 {
				t.Fatalf("%s: code=%d msg=%s", verb, r.Code, r.Msg)
			}
			items, _ := app.approvalsPage(admin, "?ap="+ap.ApNo)
			got := findAp(items, ap.ApNo)
			if got == nil {
				t.Fatal("找不到工单")
			}
			want := map[string]string{"approve": "approved", "reject": "rejected"}[verb]
			var linaSeen bool
			for _, s := range got.Steps {
				switch s.Approver {
				case "Li Na":
					linaSeen = true
					if s.Status != want || s.ActedAt == nil {
						t.Errorf("Li Na 那一步应记 %s 并带时间: %+v", want, s)
					}
				default:
					if s.Status == want || s.Status == "active" {
						t.Errorf("%s 没有动手,不该记成 %s / 仍挂 active: %+v", s.Approver, s.Status, s)
					}
				}
			}
			if !linaSeen {
				t.Fatalf("前提:Li Na 应在审批链上: %+v", got.Steps)
			}
		})
	}
}

// 第 7 条:不在审批链上的平台管理员,scope=all 也要看到全部待审单(只读,canDecide 仍按链)。
func TestApproval_PlatformAdminSeesAllPendingInScopeAll(t *testing.T) {
	app := newTestApp(t)
	l2 := app.login("chenhao@aegisdb.io", "aegisdb123")
	wangmin := app.login("wangmin@aegisdb.io", "aegisdb123") // 平台管理员,不是 owner,不在默认审批链上
	ap := app.submitProdHighRisk(l2)

	items, total := app.approvalsPage(wangmin, "?scope=all&status=pending")
	got := findAp(items, ap.ApNo)
	if got == nil || total == 0 {
		t.Fatalf("平台管理员的「全部」应看到别人的待审单 %s,实际 total=%d", ap.ApNo, total)
	}
	if got.CanDecide {
		t.Error("不在链上的管理员只读:canDecide 应为 false")
	}
	// 普通成员的 all 不变:不在链上、不是发起人,看不到
	ro := app.login("sunqi@aegisdb.io", "aegisdb123")
	if items, _ := app.approvalsPage(ro, "?scope=all&status=pending"); findAp(items, ap.ApNo) != nil {
		t.Error("非管理员的「全部」不该看到与自己无关的工单")
	}
	// 管理员也不能因此替链上的人批
	if r := app.do(http.MethodPost, "/api/v1/approvals/"+itoa(got.ID)+"/approve", wangmin, nil); r.Code == 0 {
		t.Error("不在链上的管理员不该能批准")
	}
}
