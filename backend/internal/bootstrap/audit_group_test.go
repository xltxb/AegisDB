package bootstrap

// 审计日志按审批单合并:同一张审批单的操作(提交被拦截 → 审批 → 执行)在列表里合成一条,
// 显示最新的那一步并标出共几条;点开能看到这张单子的全部操作,按时间先后。
// 没有审批单的记录照常一条一行。分页、总数按合并后的条目算。

import (
	"encoding/json"
	"net/http"
	"testing"

	"aegisdb/internal/model"
)

type auditGroupPage struct {
	Items []struct {
		ID         int64  `json:"id"`
		ApprovalNo string `json:"approvalNo"`
		EventType  string `json:"eventType"`
		Command    string `json:"command"`
	} `json:"items"`
	Total  int64            `json:"total"`
	Groups map[string]int64 `json:"groups"`
}

func TestAudit_GroupedByApproval(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	dev, prod := app.sameFileConns(admin, "audit-group")
	if r := app.execSQL(admin, dev, `CREATE TABLE t_grp (id INTEGER PRIMARY KEY)`); r.Code != 0 {
		t.Fatalf("建表: %s", r.Msg)
	}

	ap := app.interceptedTicket(admin, prod, `DROP TABLE t_grp`) // 1. 提交被拦截
	approver := app.login("zhangwei@aegisdb.io", "aegisdb123")
	eq(t, app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/approve", approver, nil).Code, 0, "2. 审批通过")
	eq(t, app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/execute", admin, nil).Code, 0, "3. 发起人执行")
	app.execSQL(admin, dev, `SELECT 1`) // 与审批单无关的一条

	var n int64
	app.repo.DB().Model(&model.AuditLog{}).Where("approval_no = ?", ap.ApNo).Count(&n)
	eq(t, n, int64(3), "前提:这张审批单在审计里有 3 条")

	var flat, grouped auditGroupPage
	_ = json.Unmarshal(app.do(http.MethodGet, "/api/v1/audit?pageSize=500", admin, nil).Data, &flat)
	r := app.do(http.MethodGet, "/api/v1/audit?pageSize=500&group=approval", admin, nil)
	eq(t, r.Code, 0, "按审批单合并的列表")
	_ = json.Unmarshal(r.Data, &grouped)

	seen := 0
	for _, it := range grouped.Items {
		if it.ApprovalNo == ap.ApNo {
			seen++
			eq(t, it.EventType, "exec", "合并后显示最新的那一步(执行)")
		}
	}
	eq(t, seen, 1, "同一张审批单在列表里只出现一次")
	eq(t, grouped.Groups[ap.ApNo], int64(3), "标出这张审批单共 3 条操作")
	eq(t, grouped.Total, flat.Total-2, "总数按合并后的条目算(3 条合成 1 条)")
	eq(t, int64(len(grouped.Items)), grouped.Total, "这一页就是全部条目")
	if flat.Groups != nil && len(flat.Groups) > 0 {
		t.Error("不合并时不带 groups")
	}

	// 点开:这张审批单的全部操作,按时间先后
	var trail []struct {
		EventType  string `json:"eventType"`
		ApprovalNo string `json:"approvalNo"`
	}
	r = app.do(http.MethodGet, "/api/v1/audit/approval/"+ap.ApNo, admin, nil)
	eq(t, r.Code, 0, "审批单的操作记录")
	_ = json.Unmarshal(r.Data, &trail)
	if len(trail) != 3 || trail[0].EventType != "intercept" || trail[1].EventType != "approve" || trail[2].EventType != "exec" {
		t.Fatalf("应按时间先后是 intercept → approve → exec,实际 %+v", trail)
	}
}

// 看不到全部审计的人(l2:有审计菜单,但只看自己的行):合并列表与操作记录都用同一个可见范围,
// 别人的审批单既不出现在列表里,也查不到它的操作记录。
func TestAudit_GroupedRespectsVisibility(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	_, prod := app.sameFileConns(admin, "audit-group-vis")
	ap := app.interceptedTicket(admin, prod, `DROP TABLE nothing_here`)

	l2 := app.login("chenhao@aegisdb.io", "aegisdb123")
	var page auditGroupPage
	r := app.do(http.MethodGet, "/api/v1/audit?pageSize=500&group=approval", l2, nil)
	eq(t, r.Code, 0, "l2 有审计菜单")
	_ = json.Unmarshal(r.Data, &page)
	for _, it := range page.Items {
		if it.ApprovalNo == ap.ApNo {
			t.Errorf("l2 不该在合并列表里看到别人的审批单 %s", ap.ApNo)
		}
	}
	var trail []json.RawMessage
	r = app.do(http.MethodGet, "/api/v1/audit/approval/"+ap.ApNo, l2, nil)
	_ = json.Unmarshal(r.Data, &trail)
	if r.Code == 0 && len(trail) != 0 {
		t.Errorf("l2 不该查到别人审批单的操作记录,实际 %d 条", len(trail))
	}
}