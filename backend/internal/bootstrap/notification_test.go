package bootstrap

import (
	"encoding/json"
	"net/http"
	"testing"
)

type notifItem struct {
	ID    int64  `json:"id"`
	Type  string `json:"type"`
	Title string `json:"title"`
	// Body 是弹窗真正显示给人看的那句话 —— 一条只有标题的通知,人还得再点开
	// 才知道是哪个任务。
	Body  string `json:"body"`
	RefNo string `json:"refNo"`
	Read  bool   `json:"read"`
}

type notifResp struct {
	Items  []notifItem `json:"items"`
	Unread int64       `json:"unread"`
}

func (a *testApp) notifications(token string) notifResp {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/notifications", token, nil)
	if r.Code != 0 {
		a.t.Fatalf("list notifications: code=%d msg=%s", r.Code, r.Msg)
	}
	var out notifResp
	if err := json.Unmarshal(r.Data, &out); err != nil {
		a.t.Fatalf("notifications decode: %v", err)
	}
	return out
}

// Bug fix: after an approval is rejected, the initiator must receive an in-app
// notification (previously they got nothing). Observable through /notifications.
func TestNotification_RejectNotifiesInitiator(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")

	// baseline: no rejection notice yet.
	if got := app.notifications(token); got.Unread != 0 {
		t.Fatalf("precondition: want 0 unread, got %d", got.Unread)
	}

	owner := app.login("zhangwei@vela.io", "vela123") // chain member (approver)
	ap := app.submitProdHighRisk(token)               // linwei is the initiator
	r := app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/reject", owner, nil)
	eq(t, r.Code, 0, "reject response code")

	got := app.notifications(token)
	if got.Unread < 1 {
		t.Fatalf("expected >=1 unread notification after rejection, got %d", got.Unread)
	}
	var hit *notifItem
	for i := range got.Items {
		if got.Items[i].RefNo == ap.ApNo {
			hit = &got.Items[i]
			break
		}
	}
	if hit == nil {
		t.Fatalf("no notification referencing approval %s", ap.ApNo)
	}
	eq(t, hit.Type, "approval-rejected", "notification type")
	if hit.Read {
		t.Error("new notification should be unread")
	}

	// marking read clears the unread count.
	rr := app.do(http.MethodPost, "/api/v1/notifications/read", token, map[string]any{})
	eq(t, rr.Code, 0, "mark-read response code")
	if after := app.notifications(token); after.Unread != 0 {
		t.Errorf("want 0 unread after mark-read, got %d", after.Unread)
	}
}

// The approve path notifies the initiator too (symmetry with reject).
func TestNotification_ApproveNotifiesInitiator(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	owner := app.login("zhangwei@vela.io", "vela123") // chain member (approver)

	ap := app.submitProdHighRisk(token)
	r := app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/approve", owner, nil)
	eq(t, r.Code, 0, "approve response code")

	got := app.notifications(token)
	found := false
	for _, it := range got.Items {
		if it.RefNo == ap.ApNo && it.Type == "approval-approved" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an approval-approved notification for %s", ap.ApNo)
	}
}
