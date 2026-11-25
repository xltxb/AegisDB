package bootstrap

// 开放接口的发布单审批:外部审批系统替审批链上的人点「通过 / 驳回」。
//
// 要守住的是凭据**打不开**的那几扇门:凭据本身不是审批人(必须点名一个在链上的人)、
// 发起人不能借外部系统批自己的单、够不到的实例上的单碰不了、没勾 release:approve 的
// 凭据(包括默认集合)用不了。以及控制台和外部两边先到先得,同一张单不会被决定两次。

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
)

type openDecide struct {
	RelNo          string `json:"relNo"`
	ApprovalNo     string `json:"approvalNo"`
	Status         string `json:"status"`
	AlreadyDecided bool   `json:"alreadyDecided"`
	ReleaseStatus  string `json:"releaseStatus"`
}

func (a *testApp) openDecide(token, relNo, approver, action string) (apiResp, openDecide) {
	a.t.Helper()
	r := a.openDo(http.MethodPost, "/api/v1/open/releases/"+relNo+"/approval", token, map[string]any{
		"approver": approver, "action": action, "comment": "外部审批系统意见",
	})
	var v openDecide
	if r.Code == 0 {
		if err := json.Unmarshal(r.Data, &v); err != nil {
			a.t.Fatalf("decode decide: %v", err)
		}
	}
	return r, v
}

// approverCredential 造一个外部审批系统:服务账号(owner 角色,可选实例标签)+ 一把凭据。
func (a *testApp) approverCredential(admin, name string, tags []string, scopes []string, pipelineID int64) string {
	a.t.Helper()
	sa := a.createServiceAccount(admin, name, []int64{a.roleIDByCode(admin, "owner")}, tags)
	body := map[string]any{"name": name + "-凭据", "userId": sa.ID, "scopes": scopes}
	if pipelineID > 0 {
		body["pipelineId"] = pipelineID
	}
	cr := a.do(http.MethodPost, "/api/v1/api-clients", admin, body)
	if cr.Code != 0 {
		a.t.Fatalf("issue credential: code=%d msg=%s", cr.Code, cr.Msg)
	}
	var issued struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(cr.Data, &issued)
	return issued.Token
}

// consoleReleaseAwaitingL2 由管理员在控制台提一张单,停在指定 l2 角色审批的阶段。
func (a *testApp) consoleReleaseAwaitingL2(admin, title string) releaseView {
	a.t.Helper()
	pid := a.createPipeline(admin, title+"-流程", "", []map[string]any{
		{"name": "人工审批", "type": "approve", "config": `{"approverRole":"l2"}`},
		{"name": "执行变更", "type": "execute"},
	})
	r := a.submitRelease(admin, map[string]any{
		"title": title, "pipelineId": pid, "connectionId": a.connIDByEnv(admin, "staging"),
		"sql": "SELECT 1;", "reason": "外部审批回归",
	})
	if r.Code != 0 {
		a.t.Fatalf("submit: code=%d msg=%s", r.Code, r.Msg)
	}
	var rel releaseView
	_ = json.Unmarshal(r.Data, &rel)
	return a.waitRelease(admin, rel.ID, "waiting", "failed", "success")
}

// 主路径:外部系统提的单,同一套外部系统按查到的审批人替他批;流水线接着往下走。
func TestOpenApproveByChainMember(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	pid := app.createPipeline(admin, "外部审批流程", "", []map[string]any{
		{"name": "人工审批", "type": "approve", "config": `{"approverRole":"l2"}`},
		{"name": "执行变更", "type": "execute"},
	})
	token := app.approverCredential(admin, "oa-system", nil,
		[]string{"release:create", "release:read", "release:approve"}, pid)

	r := app.openDo(http.MethodPost, "/api/v1/open/releases", token, map[string]any{
		"title": "外部审批单", "instance": "order-cluster-stg", "sql": "SELECT 1;", "externalRef": "OA-1",
	})
	eq(t, r.Code, 0, "open create")
	var created openRelease
	_ = json.Unmarshal(r.Data, &created)

	// 状态轮询里审批阶段带上审批人:外部系统据此知道 approver 填谁。
	var stage struct {
		Type      string `json:"type"`
		Approvers []struct {
			Name    string `json:"name"`
			Account string `json:"account"`
			Status  string `json:"status"`
		} `json:"approvers"`
	}
	found := false
	for i := 0; i < 300 && !found; i++ {
		g := app.openDo(http.MethodGet, "/api/v1/open/releases/"+created.RelNo, token, nil)
		var v struct {
			Stages []json.RawMessage `json:"stages"`
		}
		_ = json.Unmarshal(g.Data, &v)
		for _, raw := range v.Stages {
			_ = json.Unmarshal(raw, &stage)
			if stage.Type == "approve" && len(stage.Approvers) > 0 {
				found = true
				break
			}
		}
	}
	if !found {
		t.Fatal("the approve stage should list its approvers once the ticket exists")
	}
	hasChen := false
	for _, ap := range stage.Approvers {
		if ap.Account == "chenhao@aegisdb.io" {
			hasChen = true
		}
	}
	if !hasChen {
		t.Fatalf("l2 member chenhao should be listed with his account, got %+v", stage.Approvers)
	}

	r, d := app.openDecide(token, created.RelNo, "ChenHao@aegisdb.io", "approve") // 邮箱不分大小写
	eq(t, r.Code, 0, "a chain member approves through the open API")
	eq(t, d.Status, "approved", "ticket approved")
	eq(t, d.AlreadyDecided, false, "this call made the decision")

	// 审批过了,流水线继续:审批阶段走完,不再卡在审批。推进它的是扫描器(所有审批入口共用,
	// 见 ResumeReleaseApprovals),测试里手动扫一次,不等后台的定时。
	app.svc.ResumeReleaseApprovals()
	approveDone := false
	for i := 0; i < 300 && !approveDone; i++ {
		for _, st := range app.openRelease(token, created.RelNo).Stages {
			if st.Type == "approve" && st.Status == "success" {
				approveDone = true
			}
		}
		if !approveDone {
			time.Sleep(50 * time.Millisecond)
		}
	}
	if !approveDone {
		t.Fatal("the pipeline should move past the approval once it is approved")
	}

	// 审计:是谁(陈浩)、经由哪个系统(API:oa-system-凭据)。
	var rows []struct {
		Actor    string `json:"actor"`
		Operator string `json:"operator"`
		Command  string `json:"command"`
	}
	_ = json.Unmarshal(app.auditItemsRaw(admin, "?range=today&pageSize=200"), &rows)
	via := false
	for _, row := range rows {
		if strings.Contains(row.Command, "open.release.approve") && strings.Contains(row.Command, created.RelNo) {
			via = true
			eq(t, row.Operator, "API:oa-system-凭据", "the asking system is named")
			if !strings.Contains(row.Command, "chenhao@aegisdb.io") {
				t.Errorf("the audit line should name the approver, got %q", row.Command)
			}
		}
	}
	if !via {
		t.Error("no audit row for the external approval")
	}
}

// 凭据不是审批人:点名的人必须在链上;发起人借外部系统也批不了自己的单。
func TestOpenApproveRefusesWhoCannotDecide(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	token := app.approverCredential(admin, "oa-refuse", nil, []string{"release:read", "release:approve"}, 0)
	rel := app.consoleReleaseAwaitingL2(admin, "外部审批拒绝")
	eq(t, rel.Status, "waiting", "parks on approval")

	// owner(张伟)在全局默认链上,但不在这张单指定的 l2 链上。
	r, _ := app.openDecide(token, rel.RelNo, "zhangwei@aegisdb.io", "approve")
	eq(t, r.Code, 40300, "someone off the chain cannot approve through the API")
	// 发起人本人。
	r, _ = app.openDecide(token, rel.RelNo, "linwei@aegisdb.io", "approve")
	eq(t, r.Code, 40300, "the initiator cannot approve their own release through the API")
	// 不存在的账号、非法 action、没写审批人。
	r, _ = app.openDecide(token, rel.RelNo, "nobody@aegisdb.io", "approve")
	eq(t, r.Code, 40001, "unknown approver")
	if !strings.Contains(r.Msg, "nobody@aegisdb.io") {
		t.Errorf("should name the unknown account, got %q", r.Msg)
	}
	r, _ = app.openDecide(token, rel.RelNo, "chenhao@aegisdb.io", "yes")
	eq(t, r.Code, 40001, "action must be approve|reject")
	r, _ = app.openDecide(token, rel.RelNo, "", "approve")
	eq(t, r.Code, 40001, "approver is required")
	r, _ = app.openDecide(token, "REL-999999", "chenhao@aegisdb.io", "approve")
	eq(t, r.Code, 40001, "unknown release")

	// 上面每一次都没有改变这张单。
	ap := stageByType(app.getRelease(admin, rel.ID), "approve")
	eq(t, ap.Status, "waiting", "nothing above decided the ticket")
}

// release:approve 必须显式勾选:默认集合没有它。
func TestOpenApproveNeedsExplicitScope(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	token := app.approverCredential(admin, "ci-default", nil, nil, 0) // 没勾 = 默认集合
	rel := app.consoleReleaseAwaitingL2(admin, "缺范围")
	r, _ := app.openDecide(token, rel.RelNo, "chenhao@aegisdb.io", "approve")
	if r.Code == 0 {
		t.Fatal("a credential without release:approve must not decide anything")
	}
	ap := stageByType(app.getRelease(admin, rel.ID), "approve")
	eq(t, ap.Status, "waiting", "still waiting")
}

// 凭据能碰的发布单 = 服务账号实例范围内的。
func TestOpenApproveRespectsInstanceScope(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	// 只够得到沙盒;单子在预发实例上。
	token := app.approverCredential(admin, "oa-sandbox", []string{"sandbox"}, []string{"release:approve"}, 0)
	rel := app.consoleReleaseAwaitingL2(admin, "越界实例")
	r, _ := app.openDecide(token, rel.RelNo, "chenhao@aegisdb.io", "approve")
	eq(t, r.Code, 40300, "a release outside the service account's instances is off limits")
	ap := stageByType(app.getRelease(admin, rel.ID), "approve")
	eq(t, ap.Status, "waiting", "still waiting")
}

// 两边都能批,先到先得:控制台先批了,外部再来拿到的是已有结论,不报错也不改。
func TestOpenApproveFirstDecisionWins(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	l2 := app.login("chenhao@aegisdb.io", "aegisdb123")
	token := app.approverCredential(admin, "oa-race", nil, []string{"release:approve"}, 0)
	rel := app.consoleReleaseAwaitingL2(admin, "先到先得")
	ap := stageByType(rel, "approve")

	eq(t, app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ApprovalID)+"/approve", l2, nil).Code, 0, "console approves first")
	r, d := app.openDecide(token, rel.RelNo, "chenhao@aegisdb.io", "reject")
	eq(t, r.Code, 0, "a late call is answered, not refused")
	eq(t, d.AlreadyDecided, true, "reported as already decided")
	eq(t, d.Status, "approved", "the console's decision stands")
}

// 驳回:审批单 rejected,发布单停下来。
func TestOpenRejectStopsRelease(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	token := app.approverCredential(admin, "oa-reject", nil, []string{"release:approve"}, 0)
	rel := app.consoleReleaseAwaitingL2(admin, "外部驳回")

	r, d := app.openDecide(token, rel.RelNo, "chenhao@aegisdb.io", "reject")
	eq(t, r.Code, 0, "reject through the API")
	eq(t, d.Status, "rejected", "ticket rejected")
	app.svc.ResumeReleaseApprovals()
	done := app.waitRelease(admin, rel.ID, "failed", "aborted")
	if done.Status == "success" {
		t.Fatal("a rejected release must not run")
	}
}

// 流程里没有审批阶段(或还没走到):说清楚,不是 404 也不是放行。
func TestOpenApproveWithoutApprovalStage(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	token := app.approverCredential(admin, "oa-noapprove", nil, []string{"release:approve"}, 0)
	pid := app.createPipeline(admin, "无审批流程", "", []map[string]any{
		{"name": "规范审查", "type": "review", "config": `{"failOn":"none"}`},
		{"name": "执行变更", "type": "execute"},
	})
	r := app.submitRelease(admin, map[string]any{
		"title": "无审批", "pipelineId": pid, "connectionId": app.connIDByEnv(admin, "dev"),
		"sql": "SELECT 1;", "reason": "外部审批回归",
	})
	eq(t, r.Code, 0, "submit")
	var rel releaseView
	_ = json.Unmarshal(r.Data, &rel)
	rr, _ := app.openDecide(token, rel.RelNo, "chenhao@aegisdb.io", "approve")
	eq(t, rr.Code, 40001, "no approval stage to decide")
	if !strings.Contains(rr.Msg, "审批阶段") {
		t.Errorf("should say there is no approval stage, got %q", rr.Msg)
	}
}

// 审批链暂时读不出来,不等于不在链上:不按拒绝答复、不按拒绝记审计,单子不动,恢复之后照常。
func TestOpenApproveChainUnreadable(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	token := app.approverCredential(admin, "oa-broken", nil, []string{"release:approve"}, 0)
	rel := app.consoleReleaseAwaitingL2(admin, "链读不出来")
	const name = "test:open_chain"
	if err := app.repo.DB().Callback().Query().Before("gorm:query").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "tbl_approval_step" {
			_ = tx.AddError(errors.New("injected-db-detail"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	r, _ := app.openDecide(token, rel.RelNo, "chenhao@aegisdb.io", "approve")
	_ = app.repo.DB().Callback().Query().Remove(name)
	if r.Code != 50000 || strings.Contains(r.Msg, "审批链上") || strings.Contains(r.Msg, "injected") {
		t.Errorf("code=%d msg=%s", r.Code, r.Msg)
	}
	for _, row := range app.auditCommandsLike("open.release.approve") {
		if strings.Contains(row, "不在") || strings.Contains(row, "injected") {
			t.Errorf("审计: %s", row)
		}
	}
	if r, _ := app.openDecide(token, rel.RelNo, "chenhao@aegisdb.io", "approve"); r.Code != 0 {
		t.Errorf("恢复之后: %d %s", r.Code, r.Msg)
	}
}
