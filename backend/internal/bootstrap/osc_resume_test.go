package bootstrap

// 在线变更的断点续跑(.scratch/osc-product 工单 07)。InterruptRunningOSC 让库端停在"进程没了"
// 的样子(影子表在、任务 running、检查点已落库),再走一遍启动收尾。

import (
	"database/sql"
	"net/http"
	"strings"
	"testing"
	"time"

	"aegisdb/internal/model"
)

// oscInterruptedTicket:终端发起 → 审批 → 执行 → 暂停在拷贝中途 → 检查点落库 → 模拟进程退出 → 启动收尾。
func (a *testApp) oscInterruptedTicket() (admin string, ap model.Approval, raw *sql.DB) {
	a.t.Helper()
	admin, prod, schema := a.oscTermSetup()
	eq(a.t, a.do(http.MethodPut, "/api/v1/settings", admin, map[string]any{"osc.chunkSize": 1}).Code, 0, "一行一块")
	out := a.termExec(admin, prod, "ALTER TABLE big ADD INDEX idx_a (a)")
	ap = a.approvalByNoFull(admin, out.ApprovalNo)
	approver := a.login("zhangwei@aegisdb.io", "aegisdb123")
	eq(a.t, a.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/approve", approver, nil).Code, 0, "审批通过")
	eq(a.t, a.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/execute", admin, nil).Code, 0, "执行")
	a.waitOSC(ap.OSCJobID, func(j *model.OSCJob) bool { return j.Phase == "copying" && j.RowsCopied > 0 })
	eq(a.t, a.do(http.MethodPost, "/api/v1/osc/jobs/"+itoa(ap.OSCJobID)+"/pause", admin, nil).Code, 0, "暂停")
	a.waitOSC(ap.OSCJobID, func(j *model.OSCJob) bool { return j.Checkpoint != "" && j.Paused })
	a.svc.InterruptRunningOSC(10 * time.Second)

	raw = oscRaw(a.t, schema)
	// 网关不在的这段时间,业务照常写
	for _, q := range []string{
		"INSERT INTO big VALUES (5001, 5001, 'down'), (5002, 5002, 'down')",
		"UPDATE big SET b = 'changed' WHERE id IN (2, 150, 299)",
		"DELETE FROM big WHERE id IN (3, 151)",
	} {
		if _, err := raw.Exec(q); err != nil {
			a.t.Fatal(err)
		}
	}
	a.svc.ReconcileOSCJobs()
	if _, err := a.repo.FailStuckApprovalExecutions(); err != nil {
		a.t.Fatal(err)
	}
	return admin, a.approvalByNoFull(admin, out.ApprovalNo), raw
}

func bigChecksum(t *testing.T, raw *sql.DB, table string) string {
	t.Helper()
	var n, h sql.NullString
	if err := raw.QueryRow("SELECT COUNT(*), SUM(CRC32(CONCAT_WS('|', id, IFNULL(a, '<n>'), IFNULL(b, '<n>')))) FROM " + table).Scan(&n, &h); err != nil {
		t.Fatal(err)
	}
	return n.String + "/" + h.String
}

func TestOSCResume_RestartThenContinueCompletes(t *testing.T) {
	app := newTestApp(t)
	admin, ap, raw := app.oscInterruptedTicket()
	j, _ := app.repo.GetOSCJob(ap.OSCJobID)
	if j.Status != model.OSCInterrupted || !strings.Contains(j.Error, "续跑") {
		t.Fatalf("有检查点的中断任务应判为可续跑: %+v", j)
	}
	if ap.ExecStatus != model.ExecStatusRunning {
		t.Fatalf("可续跑的工单应仍是执行中,不被统一判失败: %+v", ap)
	}
	if !oscHasTable(t, raw, "_big_gho") {
		t.Fatal("中断后影子表应保留")
	}
	before := j.RowsCopied

	eq(t, app.do(http.MethodPost, "/api/v1/osc/jobs/"+itoa(j.ID)+"/continue", admin, nil).Code, 0, "续跑")
	done := app.waitApprovalExec(ap.ApNo)
	f, _ := app.repo.GetOSCJob(j.ID)
	if done.ExecStatus != model.ExecStatusSuccess || f.Status != model.OSCDone || f.RowsCopied < before {
		t.Fatalf("续跑应完成并接着之前的进度: 工单 %+v 任务 %+v", done, f)
	}
	if !oscHasIndex(t, raw, "big", "idx_a") {
		t.Error("续跑完成后新索引应在原表上")
	}
	if a, b := bigChecksum(t, raw, "big"), bigChecksum(t, raw, "_big_del"); a != b {
		t.Errorf("新表应与切换时的原表逐行一致: 新 %s 旧 %s", a, b)
	}
}

func TestOSCResume_AbandonDropsTheGhost(t *testing.T) {
	app := newTestApp(t)
	admin, ap, raw := app.oscInterruptedTicket()
	eq(t, app.do(http.MethodPost, "/api/v1/osc/jobs/"+itoa(ap.OSCJobID)+"/abort", admin, nil).Code, 0, "放弃")
	f, _ := app.repo.GetOSCJob(ap.OSCJobID)
	done := app.approvalByNoFull(admin, ap.ApNo)
	if f.Status != model.OSCAborted || done.ExecStatus != model.ExecStatusFailed || !strings.Contains(done.Result, "放弃") {
		t.Fatalf("放弃后任务已中止、工单执行失败并说明: 任务 %+v 工单 %+v", f, done)
	}
	if oscHasTable(t, raw, "_big_gho") || oscHasIndex(t, raw, "big", "idx_a") {
		t.Error("放弃后影子表已删、原表不动")
	}
}

// 停在 cut-over 里、其实已经换过去了:启动收尾要认出来判完成,而不是让人去续跑一个已经结束的迁移。
func TestOSCResume_SettlesACutOverThatAlreadyHappened(t *testing.T) {
	app := newTestApp(t)
	prod, raw := app.oscSetup(50)
	j := app.newOSCJob(prod, "ALTER TABLE big ADD INDEX idx_a (a)")
	app.repo.UpdateOSCJob(j.ID, map[string]any{"status": model.OSCRunning, "phase": "cutting_over", "checkpoint": `{"copyDone":true}`})
	for _, q := range []string{"CREATE TABLE _big_del LIKE big", "ALTER TABLE big ADD INDEX idx_a (a)"} {
		if _, err := raw.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	app.svc.ReconcileOSCJobs()
	f := app.waitOSC(j.ID, func(x *model.OSCJob) bool { return x.Status != model.OSCInterrupted })
	if f.Status != model.OSCDone || f.OldTable != "_big_del" {
		t.Fatalf("RENAME 已完成的应判完成: %+v", f)
	}
}

func TestOSCResume_SettlesAMissingGhostAsFailed(t *testing.T) {
	app := newTestApp(t)
	prod, _ := app.oscSetup(50)
	j := app.newOSCJob(prod, "ALTER TABLE big ADD INDEX idx_a (a)")
	app.repo.UpdateOSCJob(j.ID, map[string]any{"status": model.OSCRunning, "phase": "copying", "checkpoint": `{"copyDone":false}`})
	app.svc.ReconcileOSCJobs()
	f := app.waitOSC(j.ID, func(x *model.OSCJob) bool { return x.Status != model.OSCInterrupted })
	if f.Status != model.OSCFailed || !strings.Contains(f.Error, "影子表已不在") {
		t.Fatalf("影子表没了的应判失败并说明: %+v", f)
	}
}
