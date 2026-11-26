package bootstrap

// 导出任务的终止。
//
// 一条导出查询可能要跑很久(大表全量、没走索引的条件),提交的人要能在系统里把它停下来 ——
// 而"停下来"指的是**目标库上的语句停下**,不是界面上不再显示。钉住:
//   - 执行中的任务:终止后状态为 cancelled,写明谁终止的;已写出的分卷删掉;worker 之后不会
//     再把它改成 failed / done;写审计;
//   - 待审批的任务:终止后审批单一并撤回,审批人再批也不会跑;
//   - 只有提交人(或平台管理员)能终止;已经结束的任务不能终止。

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"aegisdb/internal/model"
)

// slowExportConn 建一个真实的 PostgreSQL 目标库连接,给需要"真的在库上跑"的导出用。
func (a *testApp) slowExportConn(token string) int64 {
	a.t.Helper()
	r := a.do(http.MethodPost, "/api/v1/connections", token,
		targetConnBody(a.t, "slow-pg", "dev", newTargetDB(a.t, "CREATE TABLE t (n INTEGER)")))
	if r.Code != 0 {
		a.t.Fatalf("create pg conn: %s", r.Msg)
	}
	var c struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(r.Data, &c)
	return c.ID
}

// 流式吐出 2 亿行的 generate_series:在 PostgreSQL 上要跑好几分钟。用流式而不是 COUNT(*):
// 聚合查询没有行可吐,取消前后都看不出"worker 在边读边写"这件事。
const slowExportSQL = "SELECT x FROM generate_series(1, 200000000) AS x"

func (a *testApp) jobStatus(token string, id int64) exportJob {
	a.t.Helper()
	for _, j := range a.exportJobs(token) {
		if j.ID == id {
			return j
		}
	}
	a.t.Fatalf("export job %d not listed", id)
	return exportJob{}
}

func (a *testApp) waitStatus(token string, id int64, want string, within time.Duration) exportJob {
	a.t.Helper()
	deadline := time.Now().Add(within)
	for {
		j := a.jobStatus(token, id)
		if j.Status == want {
			return j
		}
		if time.Now().After(deadline) {
			a.t.Fatalf("export job %d: want status %s within %v, still %s (%s)", id, want, within, j.Status, j.Error)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestExport_CancelRunningJob(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	dir := t.TempDir()
	eq(t, app.do(http.MethodPut, "/api/v1/settings", admin, map[string]any{"export.savePath": dir, "export.maxRows": 0}).Code, 0, "导出路径(不限行数:慢查询不能先撞上上限)")
	conn := app.slowExportConn(admin)

	id := app.submitExport(admin, conn, slowExportSQL, "slow")
	app.waitStatus(admin, id, model.ExportRunning, 5*time.Second)
	time.Sleep(2600 * time.Millisecond) // 过了 worker 的模拟延迟(0.7~2.3s),查询真的在库上跑

	start := time.Now()
	eq(t, app.do(http.MethodPost, "/api/v1/export/jobs/"+itoa(id)+"/cancel", admin, nil).Code, 0, "终止执行中的任务")
	j := app.waitStatus(admin, id, model.ExportCancelled, 5*time.Second)
	if !strings.Contains(j.Error, "终止") {
		t.Errorf("应写明被谁终止,实际 %q", j.Error)
	}

	// worker 那边的查询要真的停下来:它结束时不能再把状态改成 failed / done
	time.Sleep(1500 * time.Millisecond)
	if j := app.jobStatus(admin, id); j.Status != model.ExportCancelled {
		t.Errorf("worker 收尾后状态被改成了 %s", j.Status)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("终止用了 %v", d)
	}
	if n := countFilesUnder(t, dir); n != 0 {
		t.Errorf("终止的任务不该留下文件,实际 %d 个", n)
	}
	var n int64
	app.repo.DB().Model(&model.AuditLog{}).Where("command LIKE ?", "EXPORT CANCEL #"+itoa(id)+"%").Count(&n)
	eq(t, n, int64(1), "终止写一条审计")

	// 终止之后 worker 空出来了:新的导出照常能跑完
	id2 := app.submitExport(admin, conn, "SELECT 1 AS one", "after")
	eq(t, app.waitStatus(admin, id2, model.ExportDone, 10*time.Second).Status, model.ExportDone, "后续任务照常完成")
}

func TestExport_CancelAwaitingJobWithdrawsItsTicket(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	conn := app.exportSetup(admin)
	eq(t, app.do(http.MethodPost, "/api/v1/export", admin, map[string]any{
		"connectionId": conn, "sql": "SELECT 1", "name": "raw", "includeSensitive": true,
	}).Code, 0, "提交含敏感字段的导出")
	job := app.lastExportJob(conn)
	eq(t, job.Status, model.ExportAwaiting, "停在待审批")

	eq(t, app.do(http.MethodPost, "/api/v1/export/jobs/"+itoa(job.ID)+"/cancel", admin, nil).Code, 0, "终止待审批的任务")
	eq(t, app.lastExportJob(conn).Status, model.ExportCancelled, "任务已终止")
	var ap model.Approval
	_ = app.repo.DB().First(&ap, job.ApprovalID).Error
	eq(t, ap.Status, model.StatusCancelled, "审批单一并撤回")

	// 审批人手慢一步再点批准:单子已撤回,任务不会被放出来
	approver := app.login("zhangwei@aegisdb.io", "aegisdb123")
	app.do(http.MethodPost, "/api/v1/approvals/"+itoa(job.ApprovalID)+"/approve", approver, nil)
	time.Sleep(300 * time.Millisecond)
	eq(t, app.lastExportJob(conn).Status, model.ExportCancelled, "撤回后再批也不跑")
}

func TestExport_CancelRules(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	dir := t.TempDir()
	eq(t, app.do(http.MethodPut, "/api/v1/settings", admin, map[string]any{"export.savePath": dir, "export.maxRows": 0}).Code, 0, "导出路径(不限行数:慢查询不能先撞上上限)")
	conn := app.slowExportConn(admin)

	// 已经结束的任务不能终止
	done := app.submitExport(admin, conn, "SELECT 1 AS one", "quick")
	app.waitStatus(admin, done, model.ExportDone, 10*time.Second)
	if r := app.do(http.MethodPost, "/api/v1/export/jobs/"+itoa(done)+"/cancel", admin, nil); r.Code == 0 {
		t.Error("已完成的任务不该能终止")
	}
	eq(t, app.jobStatus(admin, done).Status, model.ExportDone, "已完成的任务状态不变")

	// 别人的任务终止不了(也不透露它存在)
	id := app.submitExport(admin, conn, slowExportSQL, "slow")
	app.waitStatus(admin, id, model.ExportRunning, 5*time.Second)
	other := app.login("chenhao@aegisdb.io", "aegisdb123")
	if r := app.do(http.MethodPost, "/api/v1/export/jobs/"+itoa(id)+"/cancel", other, nil); r.Code == 0 {
		t.Error("不是提交人、也不是管理员,不该能终止别人的导出")
	}
	eq(t, app.jobStatus(admin, id).Status, model.ExportRunning, "别人终止不了,任务照常在跑")
	eq(t, app.do(http.MethodPost, "/api/v1/export/jobs/"+itoa(id)+"/cancel", admin, nil).Code, 0, "提交人自己能终止")
	app.waitStatus(admin, id, model.ExportCancelled, 5*time.Second)
	if r := app.do(http.MethodPost, "/api/v1/export/jobs/"+itoa(id)+"/cancel", admin, nil); r.Code == 0 {
		t.Error("已终止的任务再终止一次应当报已结束")
	}
}
