package bootstrap

// 在线变更的终端入口与审批(.scratch/osc-product 工单 03),走真实接口、对着真 MySQL。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"velagateway/internal/model"
)

type oscExecOut struct {
	Intercepted bool   `json:"intercepted"`
	ApprovalNo  string `json:"approvalNo"`
	Rule        string `json:"rule"`
	Output      string `json:"output"`
	Notice      string `json:"notice"`
	OSC         *struct {
		JobID int64  `json:"jobId"`
		Table string `json:"table"`
		Rows  int64  `json:"rows"`
	} `json:"osc"`
	OSCBlockers []struct {
		Code, Reason string
	} `json:"oscBlockers"`
}

func (a *testApp) termExec(token string, connID int64, sql string) oscExecOut {
	a.t.Helper()
	r := a.do(http.MethodPost, "/api/v1/terminal/exec", token, map[string]any{"connectionId": connID, "sql": sql, "reason": "在线变更用例"})
	var out oscExecOut
	_ = json.Unmarshal(r.Data, &out)
	return out
}

func (a *testApp) oscTermSetup() (admin string, prod int64, schema string) {
	a.t.Helper()
	admin = a.login("linwei@vela.io", "vela123")
	prod, _, schema = a.oscTestConns(admin)
	raw := oscRaw(a.t, schema)
	var vals []string
	for i := 1; i <= 300; i++ {
		vals = append(vals, fmt.Sprintf("(%d)", i))
	}
	for _, q := range []string{
		"CREATE TABLE nopk (a INT)",
		"INSERT INTO nopk VALUES " + strings.Join(vals, ","),
		"ANALYZE TABLE nopk",
	} {
		if _, err := raw.Exec(q); err != nil {
			a.t.Fatal(err)
		}
	}
	eq(a.t, a.do(http.MethodPut, "/api/v1/settings", admin, map[string]any{
		"osc.minRows": 100, "osc.minBytes": 1 << 40, "osc.chunkSize": 50}).Code, 0, "设阈值")
	return admin, prod, schema
}

func (a *testApp) approvalByNoFull(token, apNo string) model.Approval {
	a.t.Helper()
	ap, err := a.repo.GetApprovalByApNo(apNo)
	if err != nil {
		a.t.Fatalf("取工单 %s: %v", apNo, err)
	}
	return *ap
}

func TestOSCTerminal_Routing(t *testing.T) {
	app := newTestApp(t)
	admin, prod, _ := app.oscTermSetup()

	// 大表索引变更:改成在线变更工单
	out := app.termExec(admin, prod, "ALTER TABLE big ADD INDEX idx_a (a)")
	if !out.Intercepted || out.OSC == nil || out.OSC.Table != "big" || out.OSC.Rows < 100 || !strings.Contains(out.Rule, "在线变更") {
		t.Fatalf("大表索引变更应生成在线变更工单: %+v", out)
	}
	ap := app.approvalByNoFull(admin, out.ApprovalNo)
	if model.ApprovalType(&ap) != model.ApTypeOSC {
		t.Errorf("工单类型应为 osc: %+v", ap)
	}
	if j, _ := app.repo.GetOSCJob(ap.OSCJobID); j == nil || j.Status != model.OSCAwaiting || j.ApNo != ap.ApNo {
		t.Errorf("任务应等待执行并挂在工单上: %+v", j)
	}

	// 小表:照原路径(不是在线变更)
	if out := app.termExec(admin, prod, "ALTER TABLE small ADD INDEX idx_a (a)"); out.OSC != nil {
		t.Errorf("小表不该走在线变更: %+v", out)
	}
	// 混批:整批拒绝,要求单独提交
	if out := app.termExec(admin, prod, "SELECT 1; ALTER TABLE big ADD INDEX idx_b (b)"); out.Intercepted || !strings.Contains(out.Output, "单独提交") {
		t.Errorf("混批应要求单独提交: %+v", out)
	}
	// 前置检查阻塞:没有主键,不生成工单,逐条给原因
	if out := app.termExec(admin, prod, "ALTER TABLE nopk ADD INDEX idx_a (a)"); out.Intercepted || len(out.OSCBlockers) == 0 {
		t.Errorf("没有主键应被前置检查拦下: %+v", out)
	}
	// 大表上做不了的 DDL:照原路径,但带"可能长时间锁表"
	out = app.termExec(admin, prod, "ALTER TABLE big PARTITION BY HASH(id) PARTITIONS 2")
	if out.OSC != nil || !strings.Contains(out.Notice, "长时间锁表") {
		t.Errorf("大表直接 DDL 应带提示: %+v", out)
	}
	if out.Intercepted {
		if ap := app.approvalByNoFull(admin, out.ApprovalNo); !strings.Contains(ap.Notice, "长时间锁表") || ap.NoticeRef == nil {
			t.Errorf("提示应写进工单: %+v", ap)
		}
	}
	// 预检:同样说要审批,规则是在线变更
	r := app.do(http.MethodPost, "/api/v1/risk/check", admin, map[string]any{"connectionId": prod, "sql": "ALTER TABLE big ADD INDEX idx_c (b)"})
	var rc struct {
		RequiresApproval bool   `json:"requiresApproval"`
		MatchedRule      string `json:"matchedRule"`
	}
	_ = json.Unmarshal(r.Data, &rc)
	if !rc.RequiresApproval || !strings.Contains(rc.MatchedRule, "在线变更") {
		t.Errorf("预检应说要审批(在线变更): %+v", rc)
	}
}

func (a *testApp) waitApprovalExec(apNo string) model.Approval {
	a.t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for {
		ap, _ := a.repo.GetApprovalByApNo(apNo)
		if ap != nil && ap.ExecStatus != "" && ap.ExecStatus != model.ExecStatusRunning {
			return *ap
		}
		if time.Now().After(deadline) {
			a.t.Fatalf("工单 %s 一直没有执行结果: %+v", apNo, ap)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestOSCTerminal_ApproveExecuteCompletes(t *testing.T) {
	app := newTestApp(t)
	admin, prod, schema := app.oscTermSetup()
	out := app.termExec(admin, prod, "ALTER TABLE big ADD INDEX idx_a (a)")
	ap := app.approvalByNoFull(admin, out.ApprovalNo)
	approver := app.login("zhangwei@vela.io", "vela123")
	eq(t, app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/approve", approver, nil).Code, 0, "审批通过")
	r := app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/execute", admin, nil)
	if r.Code != 0 || !strings.Contains(string(r.Data), "在线变更已开始") {
		t.Fatalf("执行应启动在线变更: code=%d msg=%s data=%s", r.Code, r.Msg, r.Data)
	}
	done := app.waitApprovalExec(ap.ApNo)
	if done.ExecStatus != model.ExecStatusSuccess || !strings.Contains(done.Result, "_big_del") {
		t.Fatalf("迁移跑完,工单应为执行成功并写明保留的旧表: %+v", done)
	}
	raw := oscRaw(t, schema)
	if !oscHasIndex(t, raw, "big", "idx_a") {
		t.Error("索引应已加上")
	}
	// 一张单只能执行一次
	if r := app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/execute", admin, nil); r.Code == 0 {
		t.Error("执行过的在线变更单不能再执行")
	}
}

func TestOSCTerminal_RejectAbortsTheJob(t *testing.T) {
	app := newTestApp(t)
	admin, prod, _ := app.oscTermSetup()
	out := app.termExec(admin, prod, "ALTER TABLE big ADD INDEX idx_a (a)")
	ap := app.approvalByNoFull(admin, out.ApprovalNo)
	approver := app.login("zhangwei@vela.io", "vela123")
	eq(t, app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/reject", approver, map[string]any{"reason": "不行"}).Code, 0, "驳回")
	if j, _ := app.repo.GetOSCJob(ap.OSCJobID); j.Status != model.OSCAborted {
		t.Errorf("驳回后任务应作废: %+v", j)
	}
}

func TestOSCTerminal_ControlNeedsDDLRights(t *testing.T) {
	app := newTestApp(t)
	admin, prod, schema := app.oscTermSetup()
	eq(t, app.do(http.MethodPut, "/api/v1/settings", admin, map[string]any{"osc.chunkSize": 1}).Code, 0, "一行一块")
	out := app.termExec(admin, prod, "ALTER TABLE big ADD INDEX idx_a (a)")
	ap := app.approvalByNoFull(admin, out.ApprovalNo)
	approver := app.login("zhangwei@vela.io", "vela123")
	eq(t, app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/approve", approver, nil).Code, 0, "审批通过")
	eq(t, app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/execute", admin, nil).Code, 0, "执行")
	app.waitOSC(ap.OSCJobID, func(j *model.OSCJob) bool { return j.Phase == "copying" })

	jobURL := "/api/v1/osc/jobs/" + itoa(ap.OSCJobID)
	eq(t, app.do(http.MethodPost, jobURL+"/pause", admin, nil).Code, 0, "暂停")
	var view struct {
		Paused     bool `json:"paused"`
		CanControl bool `json:"canControl"`
	}
	_ = json.Unmarshal(app.do(http.MethodGet, jobURL, admin, nil).Data, &view)
	if !view.Paused || !view.CanControl {
		t.Errorf("暂停后应看到 paused,管理员可控制: %+v", view)
	}

	// 只读角色:没有 DDL 权限,不能中止
	roID := app.roleIDByCode(admin, "ro")
	eq(t, app.do(http.MethodPost, "/api/v1/users", admin, map[string]any{
		"email": "ro-osc@vela.io", "name": "只读", "password": "initpass9", "roleIds": []int64{roID}}).Code, 0, "建只读账号")
	ro := app.login("ro-osc@vela.io", "initpass9")
	if r := app.do(http.MethodPost, jobURL+"/abort", ro, nil); r.Code == 0 {
		t.Error("没有 DDL 权限的人不能中止在线变更")
	}
	// L2:生产 DDL 要审批(自己不能直接执行)、也没有审批资格,不是发起人 —— 同样不能控制
	// 别人的迁移(2026-10 安全审计 L-OSC2,从前"不被拒绝"就算有权)
	l2 := app.login("chenhao@vela.io", "vela123")
	if r := app.do(http.MethodPost, jobURL+"/abort", l2, nil); r.Code == 0 {
		t.Error("DDL 需审批、无审批资格的人不能中止别人的在线变更")
	}

	eq(t, app.do(http.MethodPost, jobURL+"/abort", admin, nil).Code, 0, "中止")
	done := app.waitApprovalExec(ap.ApNo)
	if done.ExecStatus != model.ExecStatusFailed || !strings.Contains(done.Result, "中止") {
		t.Errorf("中止后工单应为执行失败并说明: %+v", done)
	}
	if oscHasIndex(t, oscRaw(t, schema), "big", "idx_a") {
		t.Error("中止后原表不动")
	}
	// 通知要按失败着色:沿用 approval-approved 会弹成一条绿色的"成功"
	if n, ok := app.waitForNotif(admin, "exec-failed", 5*time.Second); !ok || !strings.Contains(n.Title, "未完成") {
		t.Errorf("中止后发起人应收到执行失败类的通知: %+v ok=%v", n, ok)
	}
}

// 执行窗口内,在线变更同样免审批:窗口本来就是"这段时间里的变更已经批过了"。
//
// 仍然生成那张在线变更工单 —— 进度、暂停 / 终止、执行结果都挂在工单详情上 —— 但由窗口
// 直接批准、当场开始,不进任何人的待审批队列。工单上写明是哪扇窗口放行的。
func TestOSCTerminal_ExecWindowSkipsApproval(t *testing.T) {
	app := newTestApp(t)
	admin, prod, schema := app.oscTermSetup()
	app.openWindow(admin, prod, schema, time.Now().Add(-time.Minute), time.Now().Add(time.Hour))

	// 预检:窗口内不该再弹审批理由框
	r := app.do(http.MethodPost, "/api/v1/risk/check", admin, map[string]any{"connectionId": prod, "sql": "ALTER TABLE big ADD INDEX idx_a (a)"})
	var rc struct {
		RequiresApproval bool   `json:"requiresApproval"`
		MatchedRule      string `json:"matchedRule"`
	}
	_ = json.Unmarshal(r.Data, &rc)
	if rc.RequiresApproval {
		t.Errorf("执行窗口内的在线变更,预检不该要求审批: %+v", rc)
	}

	out := app.termExec(admin, prod, "ALTER TABLE big ADD INDEX idx_a (a)")
	if out.Intercepted || out.ApprovalNo == "" || out.OSC == nil || !strings.Contains(out.Output, "在线变更已开始") {
		t.Fatalf("窗口内应直接开始在线变更(并给出工单号看进度): %+v", out)
	}
	ap := app.approvalByNoFull(admin, out.ApprovalNo)
	if ap.Status != model.StatusApproved {
		t.Errorf("工单应已由执行窗口批准: status=%s", ap.Status)
	}
	steps, _ := app.repo.StepsOf(ap.ID)
	if len(steps) != 1 || steps[0].Status != "approved" || !strings.Contains(steps[0].Approver, "执行窗口") {
		t.Errorf("审批链上应只有一步、由执行窗口通过: %+v", steps)
	}
	if !strings.Contains(out.Rule, "执行窗口") {
		t.Errorf("终端上应说明是执行窗口放行的: %q", out.Rule)
	}
	done := app.waitApprovalExec(ap.ApNo)
	if done.ExecStatus != model.ExecStatusSuccess {
		t.Fatalf("迁移应跑完: %+v", done)
	}
	if !oscHasIndex(t, oscRaw(t, schema), "big", "idx_a") {
		t.Error("索引应已加上")
	}
}
