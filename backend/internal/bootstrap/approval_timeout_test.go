package bootstrap

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"aegisdb/internal/model"
)

type auditResultRow struct {
	Result     string `json:"result"`
	ApprovalNo string `json:"approvalNo"`
}

func (a *testApp) countEscalations(token, apNo string) int {
	a.t.Helper()
	var rows []auditResultRow
	_ = json.Unmarshal(a.auditItemsRaw(token, ""), &rows)
	n := 0
	for _, row := range rows {
		if row.ApprovalNo == apNo && row.Result == "warn" {
			n++
		}
	}
	return n
}

// Under the auto-escalate timeout policy, each overdue approval must escalate
// exactly ONCE — not on every 60s sweep. Regression for auto-escalate never
// marking the ticket, so repeated sweeps re-audited + re-notified forever (R13).
func TestApprovalTimeout_AutoEscalateOnlyOnce(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")

	// escalate immediately-overdue pending approvals
	eq(t, app.do(http.MethodPut, "/api/v1/settings", admin, map[string]any{
		"approval.onTimeout": "auto-escalate", "approval.timeoutMinutes": 0,
	}).Code, 0, "configure auto-escalate")

	// create a pending high-risk approval
	prod := app.connIDByEnv(admin, "prod")
	r := app.do(http.MethodPost, "/api/v1/terminal/exec", admin, map[string]any{
		"connectionId": prod, "sql": "DROP TABLE orders;", "reason": "cleanup",
	})
	eq(t, r.Code, 42200, "high-risk intercepted")
	var er struct {
		ApprovalNo string `json:"approvalNo"`
	}
	_ = json.Unmarshal(r.Data, &er)
	if er.ApprovalNo == "" {
		t.Fatal("expected an approval number")
	}

	// run the timeout sweep three times (simulating three 60s ticks)
	app.svc.SweepApprovalTimeouts()
	app.svc.SweepApprovalTimeouts()
	app.svc.SweepApprovalTimeouts()

	if n := app.countEscalations(admin, er.ApprovalNo); n != 1 {
		t.Errorf("expected exactly 1 escalation audit for %s, got %d", er.ApprovalNo, n)
	}
}

type escalationNotif struct {
	Type    string `json:"type"`
	Title   string `json:"title"`
	Body    string `json:"body"`
	BodyRef string `json:"bodyRef"`
	RefNo   string `json:"refNo"`
}

func (a *testApp) escalationNotifs(token, apNo string) []escalationNotif {
	a.t.Helper()
	var page struct {
		Items []escalationNotif `json:"items"`
	}
	_ = json.Unmarshal(a.do(http.MethodGet, "/api/v1/notifications", token, nil).Data, &page)
	var out []escalationNotif
	for _, n := range page.Items {
		if n.Type == "approval-escalated" && n.RefNo == apNo {
			out = append(out, n)
		}
	}
	return out
}

// issue #131:「超时自动升级」从前只写一条审计告警,没有任何人知道。现在通知默认审批人(DBA 负责人
// 成员)并往飞书群发一张卡片,每张单一次;单子仍是待审批。站内通知不带语句 —— 发布单可以用别的
// 审批链,负责人不一定看得到那张单。
func TestApprovalTimeout_EscalationNotifiesOwnersAndLark(t *testing.T) {
	received := make(chan string, 16)
	lark := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		received <- string(b)
		_, _ = w.Write([]byte(`{"code":0,"msg":"success"}`))
	}))
	defer lark.Close()

	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	app.setSettings(admin, map[string]any{
		"approval.onTimeout": "auto-escalate", "approval.timeoutMinutes": 0,
		"notify.lark": true, "notify.larkWebhook": lark.URL,
	})
	ap := app.submitProdHighRisk(admin)
	select {
	case <-received: // 建单那张卡片
	case <-time.After(3 * time.Second):
		t.Fatal("no approval card")
	}

	app.svc.SweepApprovalTimeouts()
	app.svc.SweepApprovalTimeouts()

	for _, email := range []string{"zhangwei@aegisdb.io", "lina@aegisdb.io"} {
		ns := app.escalationNotifs(app.login(email, "aegisdb123"), ap.ApNo)
		if len(ns) != 1 {
			t.Fatalf("%s: want exactly 1 escalation notification, got %+v", email, ns)
		}
		if strings.Contains(strings.ToUpper(ns[0].Body), "DROP") {
			t.Errorf("通知不该带语句:%q", ns[0].Body)
		}
		if ns[0].BodyRef == "" {
			t.Error("通知正文应带 ref,英文界面才能翻译")
		}
	}
	if n := len(app.escalationNotifs(admin, ap.ApNo)); n != 0 {
		t.Errorf("不是负责人的发起人不该收到升级通知,got %d", n)
	}
	// 测试数据里本来就有别的超时单,它们也各发一张:收完再数。
	mine := 0
	for quiet := false; !quiet; {
		select {
		case body := <-received:
			if strings.Contains(body, "审批超时未处理 · "+ap.ApNo) {
				mine++
			}
		case <-time.After(time.Second):
			quiet = true
		}
	}
	eq(t, mine, 1, "每张单恰好一张升级卡片")
	eq(t, app.approvalRow(admin, ap.ApNo).Status, "pending", "升级不改变单子的状态")
}

// 负责人角色没有成员时回落平台管理员,与默认审批人同一套。
func TestApprovalTimeout_EscalationFallsBackToAdmins(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	owner, err := app.repo.GetRoleByCode("owner")
	if err != nil {
		t.Fatal(err)
	}
	members, _ := app.repo.MembersOfRole(owner.ID)
	for _, m := range members {
		_ = app.repo.RemoveMember(owner.ID, m.ID)
	}
	app.setSettings(admin, map[string]any{"approval.onTimeout": "auto-escalate", "approval.timeoutMinutes": 0})
	ap := app.submitProdHighRisk(admin)
	app.svc.SweepApprovalTimeouts()
	eq(t, len(app.escalationNotifs(admin, ap.ApNo)), 1, "回落到管理员")
}

// 扫描先列出超时的单、再逐张抢占:中间被人决定掉的单不能再升级。
func TestApprovalTimeout_DecidedTicketCannotBeEscalated(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	ap := app.submitProdHighRisk(admin)
	id := ap.ID
	if ok, err := app.repo.ClaimApproval(id, "pending", "rejected"); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if claimed, _ := app.repo.ClaimEscalation(id); claimed {
		t.Error("已有结论的单不能再升级")
	}
}

// 库里没有 approval.onTimeout 时什么都不做(保持等待)。设置页的兜底与它一致,
// 改这里要同时改 frontend/src/lib/settingOptions.ts(issue #134)。
func TestApprovalTimeout_MissingPolicyKeepsWaiting(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	app.setSettings(admin, map[string]any{"approval.timeoutMinutes": 0})
	if err := app.repo.DB().Where("k = ?", "approval.onTimeout").Delete(&model.Setting{}).Error; err != nil {
		t.Fatal(err)
	}
	if v, _ := app.repo.GetSetting("approval.onTimeout"); v != "" {
		t.Fatalf("setting still present: %q", v)
	}
	ap := app.submitProdHighRisk(admin)

	app.svc.SweepApprovalTimeouts()

	eq(t, app.approvalRow(admin, ap.ApNo).Status, "pending", "单子仍待审批")
	eq(t, app.countEscalations(admin, ap.ApNo), 0, "不该升级")
}
