package bootstrap

// 审批决定带上渠道与拒绝原因(聊天审批卡片,.scratch/chat-approval-cards/PRD.md §5.4)。
//
// 审计要看得出是谁、从哪个渠道决定的:聊天渠道的操作人写「姓名 (Lark)」;站内与开放接口
// 的操作人写法是既有口径,一个字都不能变。给发起人的通知只写姓名。

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"aegisdb/internal/model"
	"aegisdb/internal/service"
)

type decidedRow struct {
	ApNo         string `json:"apNo"`
	Status       string `json:"status"`
	DecidedVia   string `json:"decidedVia"`
	RejectReason string `json:"rejectReason"`
}

// decidedRow 取 GET /approvals 里的那一行,并断言两个字段确实在 JSON 里(不是解码出来的零值)。
func (a *testApp) decidedRow(token, apNo string) decidedRow {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/approvals?ap="+apNo, token, nil)
	eq(a.t, r.Code, 0, "list approvals")
	var page struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(r.Data, &page); err != nil || len(page.Items) != 1 {
		a.t.Fatalf("approval %s: %d items, err %v", apNo, len(page.Items), err)
	}
	for _, key := range []string{`"decidedVia":`, `"rejectReason":`} {
		if !strings.Contains(string(page.Items[0]), key) {
			a.t.Errorf("GET /approvals 的 %s 里没有 %s", apNo, key)
		}
	}
	var row decidedRow
	_ = json.Unmarshal(page.Items[0], &row)
	return row
}

// auditOperators 是这张单的审计里记过的操作人(空的不算)。
func (a *testApp) auditOperators(token, apNo string) []string {
	a.t.Helper()
	var rows []struct {
		ApprovalNo string `json:"approvalNo"`
		Operator   string `json:"operator"`
	}
	if err := json.Unmarshal(a.auditItemsRaw(token, "?range=today&pageSize=200"), &rows); err != nil {
		a.t.Fatalf("audit decode: %v", err)
	}
	var out []string
	for _, r := range rows {
		if r.ApprovalNo == apNo && r.Operator != "" {
			out = append(out, r.Operator)
		}
	}
	return out
}

func (a *testApp) storedUser(email string) *model.User {
	a.t.Helper()
	u, err := a.repo.GetUserByEmailFold(email)
	if err != nil || u == nil {
		a.t.Fatalf("user %s: %v", email, err)
	}
	return u
}

// decisionNotif 是发起人收到的、关于这张单的那条结论通知。
func (a *testApp) decisionNotif(token, apNo string) escalationNotif {
	a.t.Helper()
	var page struct {
		Items []escalationNotif `json:"items"`
	}
	_ = json.Unmarshal(a.do(http.MethodGet, "/api/v1/notifications", token, nil).Data, &page)
	for _, n := range page.Items {
		if n.RefNo == apNo && (n.Type == model.NotifApprovalApproved || n.Type == model.NotifApprovalRejected) {
			return n
		}
	}
	a.t.Fatalf("发起人没有收到 %s 的结论通知", apNo)
	return escalationNotif{}
}

func TestDecidedVia(t *testing.T) {
	const initiator, approver, name = "linwei@aegisdb.io", "zhangwei@aegisdb.io", "Zhang Wei"

	t.Run("console", func(t *testing.T) {
		app := newTestApp(t)
		token := app.login(initiator, "aegisdb123")
		ap := app.submitProdHighRisk(token)
		eq(t, app.do(http.MethodPost, "/api/v1/approvals/"+itoa(app.approvalIDByNo(ap.ApNo))+"/approve",
			app.login(approver, "aegisdb123"), nil).Code, 0, "站内通过")
		row := app.decidedRow(token, ap.ApNo)
		eq(t, row.Status, model.StatusApproved, "status")
		eq(t, row.DecidedVia, "console", "decided_via")
		eq(t, row.RejectReason, "", "reject_reason")
		eq(t, strings.Join(app.auditOperators(token, ap.ApNo), "|"), name, "站内审计操作人")
	})

	t.Run("console reject keeps the plain sentence", func(t *testing.T) {
		app := newTestApp(t)
		token := app.login(initiator, "aegisdb123")
		ap := app.submitProdHighRisk(token)
		eq(t, app.do(http.MethodPost, "/api/v1/approvals/"+itoa(app.approvalIDByNo(ap.ApNo))+"/reject",
			app.login(approver, "aegisdb123"), nil).Code, 0, "站内驳回")
		row := app.decidedRow(token, ap.ApNo)
		eq(t, row.DecidedVia, "console", "decided_via")
		eq(t, row.RejectReason, "", "reject_reason")
		eq(t, app.decisionNotif(token, ap.ApNo).Body, name+" 驳回了你的命令：DROP TABLE orders;", "没有原因时的通知一字不变")
		eq(t, strings.Join(app.auditOperators(token, ap.ApNo), "|"), name, "站内审计操作人")
	})

	for via, operator := range map[string]string{"lark": name + " (Lark)", "telegram": name + " (Telegram)"} {
		t.Run(via+" approve", func(t *testing.T) {
			app := newTestApp(t)
			token := app.login(initiator, "aegisdb123")
			ap := app.submitProdHighRisk(token)
			// 通过时带的原因不算数:reject_reason 只属于拒绝。
			if _, err := app.svc.DecideApprovalVia(app.storedUser(approver), app.approvalIDByNo(ap.ApNo), true, via, "不该留下"); err != nil {
				t.Fatal(err)
			}
			row := app.decidedRow(token, ap.ApNo)
			eq(t, row.Status, model.StatusApproved, "status")
			eq(t, row.DecidedVia, via, "decided_via")
			eq(t, row.RejectReason, "", "reject_reason")
			eq(t, strings.Join(app.auditOperators(token, ap.ApNo), "|"), operator, "审计操作人")
			body := app.decisionNotif(token, ap.ApNo).Body
			if !strings.HasPrefix(body, name+" 通过了") || strings.Contains(body, "(") {
				t.Errorf("给发起人的通知只写姓名,实际 %q", body)
			}
		})
	}

	t.Run("lark reject with reason", func(t *testing.T) {
		app := newTestApp(t)
		token := app.login(initiator, "aegisdb123")
		ap := app.submitProdHighRisk(token)
		if _, err := app.svc.DecideApprovalVia(app.storedUser(approver), app.approvalIDByNo(ap.ApNo), false, "lark", "  影响大促\n"); err != nil {
			t.Fatal(err)
		}
		row := app.decidedRow(token, ap.ApNo)
		eq(t, row.Status, model.StatusRejected, "status")
		eq(t, row.DecidedVia, "lark", "decided_via")
		eq(t, row.RejectReason, "影响大促", "reject_reason 去掉首尾空白")
		eq(t, strings.Join(app.auditOperators(token, ap.ApNo), "|"), name+" (Lark)", "审计操作人")
		n := app.decisionNotif(token, ap.ApNo)
		eq(t, n.Body, name+" 驳回了你的命令：DROP TABLE orders;\n原因：影响大促", "通知带原因,只写姓名")
		if n.BodyRef == "" {
			t.Error("带原因的通知没有 ref,英文界面只能显示中文")
		}
	})

	t.Run("blank reason is no reason", func(t *testing.T) {
		app := newTestApp(t)
		token := app.login(initiator, "aegisdb123")
		ap := app.submitProdHighRisk(token)
		if _, err := app.svc.DecideApprovalVia(app.storedUser(approver), app.approvalIDByNo(ap.ApNo), false, "lark", " \n "); err != nil {
			t.Fatal(err)
		}
		eq(t, app.decidedRow(token, ap.ApNo).RejectReason, "", "reject_reason")
		eq(t, app.decisionNotif(token, ap.ApNo).Body, name+" 驳回了你的命令：DROP TABLE orders;", "通知")
	})

	// 列宽 512;通知正文那一列也是 512,而它前面还有姓名与命令节选 —— 列宽不对,
	// 生产超一个字就是 1406:要么决定失败,要么发起人收不到通知。
	t.Run("over-long reason is clipped and nothing overflows", func(t *testing.T) {
		app := newTestApp(t)
		token := app.login(initiator, "aegisdb123")
		ap := app.submitProdHighRisk(token)
		long := strings.Repeat("慢", 600)
		if _, err := app.svc.DecideApprovalVia(app.storedUser(approver), app.approvalIDByNo(ap.ApNo), false, "lark", long); err != nil {
			t.Fatal(err)
		}
		got := app.decidedRow(token, ap.ApNo).RejectReason
		eq(t, utf8.RuneCountInString(got), 512, "reject_reason 截到 512 字(省略号算在内)")
		if !strings.HasPrefix(got, strings.Repeat("慢", 511)) {
			t.Error("截断应保留开头")
		}
		body := app.decisionNotif(token, ap.ApNo).Body
		if n := utf8.RuneCountInString(body); n > 512 {
			t.Errorf("通知正文 %d 字,超出 tbl_notification.body 的 512", n)
		}
		if !strings.Contains(body, "原因：慢慢慢") {
			t.Errorf("通知里没有原因:%q", body)
		}
	})

	// 聊天平台送来的文字不受我们控制:MySQL 拒收坏 UTF-8,PG 拒收 NUL,两者都会让这次决定写不进去。
	t.Run("bytes a store would refuse are dropped", func(t *testing.T) {
		app := newTestApp(t)
		token := app.login(initiator, "aegisdb123")
		ap := app.submitProdHighRisk(token)
		if _, err := app.svc.DecideApprovalVia(app.storedUser(approver), app.approvalIDByNo(ap.ApNo), false, "lark", "a\x00b\xffc"); err != nil {
			t.Fatal(err)
		}
		eq(t, app.decidedRow(token, ap.ApNo).RejectReason, "abc", "reject_reason")
	})

	// 规则与站内同一处:自己发起的、不在链上的,换个渠道照样拒绝,单子不动。
	t.Run("chat is not a way around the rules", func(t *testing.T) {
		app := newTestApp(t)
		token := app.login(initiator, "aegisdb123")
		ap := app.submitProdHighRisk(token)
		id := app.approvalIDByNo(ap.ApNo)
		for _, email := range []string{initiator, "chenhao@aegisdb.io"} {
			if _, err := app.svc.DecideApprovalVia(app.storedUser(email), id, true, "lark", ""); err == nil {
				t.Errorf("%s 不该能决定这张单", email)
			}
		}
		row := app.decidedRow(token, ap.ApNo)
		eq(t, row.Status, model.StatusPending, "status")
		eq(t, row.DecidedVia, "", "decided_via")
	})

	t.Run("second decision does not overwrite the first", func(t *testing.T) {
		app := newTestApp(t)
		token := app.login(initiator, "aegisdb123")
		ap := app.submitProdHighRisk(token)
		id, u := app.approvalIDByNo(ap.ApNo), app.storedUser(approver)
		if _, err := app.svc.DecideApprovalVia(u, id, true, "telegram", ""); err != nil {
			t.Fatal(err)
		}
		if _, err := app.svc.DecideApprovalVia(u, id, false, "lark", "晚了一步"); err != service.ErrAlreadyDecided {
			t.Errorf("第二次决定应得到 ErrAlreadyDecided,实际 %v", err)
		}
		row := app.decidedRow(token, ap.ApNo)
		eq(t, row.DecidedVia, "telegram", "decided_via")
		eq(t, row.RejectReason, "", "reject_reason")
	})

	t.Run("concurrent decisions: the row is all winner", func(t *testing.T) {
		app := newTestApp(t)
		token := app.login(initiator, "aegisdb123")
		u := app.storedUser(approver)
		for i := 0; i < 10; i++ {
			ap := app.submitProdHighRisk(token)
			id := app.approvalIDByNo(ap.ApNo)
			errs := make(chan error, 2)
			go func() { _, err := app.svc.DecideApproval(u, id, true); errs <- err }()
			go func() { _, err := app.svc.DecideApprovalVia(u, id, false, "lark", "驳回原因"); errs <- err }()
			wins := 0
			for j := 0; j < 2; j++ {
				if <-errs == nil {
					wins++
				}
			}
			eq(t, wins, 1, "恰好一个成功")
			got := app.decidedRow(token, ap.ApNo)
			got.ApNo = ""
			if got != (decidedRow{Status: model.StatusApproved, DecidedVia: "console"}) &&
				got != (decidedRow{Status: model.StatusRejected, DecidedVia: "lark", RejectReason: "驳回原因"}) {
				t.Errorf("两次决定混在了一行里:%+v", got)
			}
		}
	})
}

// 开放接口替人批:渠道记 api;审计里那条决定的操作人仍是审批人姓名,不加后缀。
func TestDecidedVia_OpenAPI(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	token := app.approverCredential(admin, "oa-via", nil, []string{"release:read", "release:approve"}, 0)
	for action, status := range map[string]string{"approve": model.StatusApproved, "reject": model.StatusRejected} {
		rel := app.consoleReleaseAwaitingL2(admin, "渠道-"+action)
		r, d := app.openDecide(token, rel.RelNo, "chenhao@aegisdb.io", action)
		eq(t, r.Code, 0, "open "+action)
		row := app.decidedRow(admin, d.ApprovalNo)
		eq(t, row.Status, status, "status")
		eq(t, row.DecidedVia, "api", "decided_via")
		eq(t, row.RejectReason, "", "开放接口的意见写在审计里,不是拒绝原因")
		eq(t, strings.Join(app.auditOperators(admin, d.ApprovalNo), "|"), app.storedUser("chenhao@aegisdb.io").Name, "审计操作人")
	}
}

// 没有人做决定的几条路,decided_via 一律留空:超时、撤回、作废、窗口改动。
func TestDecidedVia_EmptyWhenNobodyDecided(t *testing.T) {
	check := func(t *testing.T, app *testApp, token, apNo, status string) {
		t.Helper()
		row := app.decidedRow(token, apNo)
		eq(t, row.Status, status, "status")
		eq(t, row.DecidedVia, "", "decided_via")
		eq(t, row.RejectReason, "", "reject_reason")
	}

	t.Run("timeout", func(t *testing.T) {
		app := newTestApp(t)
		admin := app.login("linwei@aegisdb.io", "aegisdb123")
		app.setSettings(admin, map[string]any{"approval.onTimeout": "auto-reject", "approval.timeoutMinutes": 0})
		ap := app.submitProdHighRisk(admin)
		app.svc.SweepApprovalTimeouts()
		check(t, app, admin, ap.ApNo, model.StatusExpired)
	})

	t.Run("cancel", func(t *testing.T) {
		app := newTestApp(t)
		admin := app.login("linwei@aegisdb.io", "aegisdb123")
		ap := app.submitProdHighRisk(admin)
		eq(t, app.cancel(admin, app.approvalIDByNo(ap.ApNo)).Code, 0, "撤回")
		check(t, app, admin, ap.ApNo, model.StatusCancelled)
	})

	t.Run("release ended", func(t *testing.T) {
		app := newTestApp(t)
		admin := app.login("linwei@aegisdb.io", "aegisdb123")
		rel := app.consoleReleaseAwaitingL2(admin, "终止作废")
		apNo := ""
		for _, st := range rel.Stages {
			if st.ApprovalNo != "" {
				apNo = st.ApprovalNo
			}
		}
		if apNo == "" {
			t.Fatal("审批阶段没有单号")
		}
		eq(t, app.do(http.MethodPost, "/api/v1/releases/"+itoa(rel.ID)+"/abort", admin, nil).Code, 0, "终止发布单")
		check(t, app, admin, apNo, model.StatusExpired)
	})

	// 启动清理的孤儿单:发布单已经不在跑,名下还挂着待审批。
	t.Run("orphan release ticket", func(t *testing.T) {
		app := newTestApp(t)
		admin := app.login("linwei@aegisdb.io", "aegisdb123")
		ap := &model.Approval{ApNo: "AP-ORPHAN", ReleaseID: 999999, ConnectionID: app.connIDByEnv(admin, "prod"),
			Env: "prod", Instance: "i", Command: "SELECT 1", RiskLevel: model.RiskMid, Status: model.StatusPending,
			InitiatorID: app.storedUser("linwei@aegisdb.io").ID}
		if err := app.repo.CreateApproval(ap, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := app.repo.VoidOrphanReleaseTickets(); err != nil {
			t.Fatal(err)
		}
		check(t, app, admin, "AP-ORPHAN", model.StatusExpired)
	})

	// 旧窗口单:改窗口时撤掉;即便还挂着 pending,聊天渠道点下去也只会把它撤掉,不算一次决定。
	t.Run("stale window ticket", func(t *testing.T) {
		app := newTestApp(t)
		l2 := app.login("chenhao@aegisdb.io", "aegisdb123")
		admin := app.login("linwei@aegisdb.io", "aegisdb123")
		prod := app.connIDByEnv(admin, "prod")
		winID, oldNo := app.applyWindow(l2, prod, "orders_db", "渠道留空")
		now := time.Now()
		eq(t, app.do(http.MethodPut, "/api/v1/exec-windows/"+itoa(winID), l2,
			windowBody(prod, "orders_db", "渠道留空", now.Add(-time.Minute), now.Add(2*time.Hour))).Code, 0, "改窗口")
		check(t, app, admin, oldNo, model.StatusCancelled)

		id := app.approvalIDByNo(oldNo)
		if ok, err := app.repo.ClaimApproval(id, model.StatusCancelled, model.StatusPending); err != nil || !ok {
			t.Fatalf("拨回 pending: %v %v", ok, err)
		}
		if _, err := app.svc.DecideApprovalVia(app.storedUser("zhangwei@aegisdb.io"), id, true, "lark", ""); err != service.ErrAlreadyDecided {
			t.Errorf("旧窗口单应得到 ErrAlreadyDecided,实际 %v", err)
		}
		check(t, app, admin, oldNo, model.StatusCancelled)
	})
}
