package bootstrap

// 含敏感字段的导出:批准之后才跑。
//
// 导出默认脱敏。但确实有需要原值的场景(对账、迁移、监管调取),所以有一个"包含敏感
// 字段"的选项 —— 而勾上它不能立刻生效:一份带原值的 CSV 出了网关就再也管不到了。
//
// 下面钉住整条闸的四个关节:不进队列、有单可批、批不了就拿不到原值、批准即入队。

import (
	"net/http"
	"testing"

	"velagateway/internal/model"
)

func (a *testApp) lastExportJob(connID int64) model.ExportJob {
	a.t.Helper()
	var job model.ExportJob
	if err := a.repo.DB().Where("connection_id = ?", connID).Order("id desc").First(&job).Error; err != nil {
		a.t.Fatalf("读导出任务: %v", err)
	}
	return job
}

func (a *testApp) exportSetup(token string) int64 {
	a.t.Helper()
	eq(a.t, a.do(http.MethodPut, "/api/v1/settings", token,
		map[string]any{"export.savePath": a.t.TempDir()}).Code, 0, "配置导出路径")
	return a.connIDByEnv(token, "dev")
}

// 普通导出照旧:不勾选项就直接进队列,不惊动任何人。
func TestSensitiveExport_APlainExportStillRunsStraightAway(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	conn := app.exportSetup(admin)

	eq(t, app.do(http.MethodPost, "/api/v1/export", admin, map[string]any{
		"connectionId": conn, "sql": "SELECT 1", "name": "plain",
	}).Code, 0, "提交普通导出")

	job := app.lastExportJob(conn)
	if job.Status == model.ExportAwaiting {
		t.Error("没勾包含敏感字段的导出不该去等审批")
	}
	if job.IncludeSensitive {
		t.Error("默认不该带原值")
	}
}

// 勾了"包含敏感字段"就不进队列,而是停在 awaiting 并生成审批单。
func TestSensitiveExport_WaitsForApprovalInsteadOfRunning(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	conn := app.exportSetup(admin)

	eq(t, app.do(http.MethodPost, "/api/v1/export", admin, map[string]any{
		"connectionId": conn, "sql": "SELECT 1", "name": "raw", "includeSensitive": true,
	}).Code, 0, "提交含敏感字段的导出")

	job := app.lastExportJob(conn)
	eq(t, job.Status, model.ExportAwaiting, "含敏感字段的导出必须先等审批")
	if !job.IncludeSensitive {
		t.Error("任务上应当记着它要的是原值")
	}
	if job.ApprovalID == 0 || job.ApNo == "" {
		t.Fatal("必须生成审批单 —— 否则这个任务永远停在那里,而人以为提交成功了")
	}

	// 审批单要指回这个导出任务,而且风险按高危记:它不改数据,但后果是明文 PII 出库。
	var ap model.Approval
	if err := app.repo.DB().First(&ap, job.ApprovalID).Error; err != nil {
		t.Fatalf("读审批单: %v", err)
	}
	eq(t, ap.ExportJobID, job.ID, "审批单指回导出任务")
	eq(t, ap.RiskLevel, model.RiskHigh, "敏感数据出库按高危")
}

// 导出的审批单走不了"发起人手动执行"那条路 —— 它的 Command 是一句描述而不是可执行
// 的语句,放它过去,网关会把那句描述当成命令发给数据库。
func TestSensitiveExport_ItsTicketIsNotManuallyExecutable(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	conn := app.exportSetup(admin)
	eq(t, app.do(http.MethodPost, "/api/v1/export", admin, map[string]any{
		"connectionId": conn, "sql": "SELECT 1", "name": "raw", "includeSensitive": true,
	}).Code, 0, "提交")
	job := app.lastExportJob(conn)

	approver := app.login("zhangwei@vela.io", "vela123")
	eq(t, app.do(http.MethodPost, "/api/v1/approvals/"+itoa(job.ApprovalID)+"/approve", approver, nil).Code, 0, "审批通过")

	r := app.do(http.MethodPost, "/api/v1/approvals/"+itoa(job.ApprovalID)+"/execute", admin, nil)
	if r.Code == 0 {
		t.Error("导出审批单不该能被手动执行 —— 它授权的是让导出任务后台跑,不是执行一条命令")
	}
}

// 批准之后任务离开 awaiting,由 worker 去跑。
func TestSensitiveExport_ApprovalReleasesTheJob(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	conn := app.exportSetup(admin)
	eq(t, app.do(http.MethodPost, "/api/v1/export", admin, map[string]any{
		"connectionId": conn, "sql": "SELECT 1", "name": "raw", "includeSensitive": true,
	}).Code, 0, "提交")
	job := app.lastExportJob(conn)

	approver := app.login("zhangwei@vela.io", "vela123")
	eq(t, app.do(http.MethodPost, "/api/v1/approvals/"+itoa(job.ApprovalID)+"/approve", approver, nil).Code, 0, "审批通过")

	after := app.lastExportJob(conn)
	if after.Status == model.ExportAwaiting {
		t.Error("批准之后任务应当离开 awaiting 进入队列")
	}
}

// 驳回之后任务不能跑 —— 它永远拿不到原值。
func TestSensitiveExport_ARejectedTicketNeverRuns(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	conn := app.exportSetup(admin)
	eq(t, app.do(http.MethodPost, "/api/v1/export", admin, map[string]any{
		"connectionId": conn, "sql": "SELECT 1", "name": "raw", "includeSensitive": true,
	}).Code, 0, "提交")
	job := app.lastExportJob(conn)

	approver := app.login("zhangwei@vela.io", "vela123")
	eq(t, app.do(http.MethodPost, "/api/v1/approvals/"+itoa(job.ApprovalID)+"/reject", approver, nil).Code, 0, "驳回")

	after := app.lastExportJob(conn)
	eq(t, after.Status, model.ExportAwaiting, "被驳回的导出应当停在原地,永远不跑")
}

// 最后一道:即便有人把一个"要原值"的任务直接摆成 pending(绕开建单那一步),
// worker 在放行原值之前还会自己核一次审批。这道复核是重复的 —— 故意的:这类旁路
// 最常见的失效方式是后来有人加了新路径、忘了先检查。
func TestSensitiveExport_TheWorkerRefusesToUnmaskWithoutAGrantedTicket(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	conn := app.exportSetup(admin)
	eq(t, app.do(http.MethodPost, "/api/v1/export", admin, map[string]any{
		"connectionId": conn, "sql": "SELECT 1", "name": "raw", "includeSensitive": true,
	}).Code, 0, "提交")
	job := app.lastExportJob(conn)

	// 把审批单从任务上摘掉,模拟"标着要原值、却没有有效授权"的一行。
	if err := app.repo.DB().Model(&model.ExportJob{}).Where("id = ?", job.ID).
		Updates(map[string]any{"approval_id": 0, "status": model.ExportPending}).Error; err != nil {
		t.Fatalf("构造无授权任务: %v", err)
	}
	fresh, err := app.repo.GetExportJob(job.ID)
	if err != nil {
		t.Fatalf("重读任务: %v", err)
	}
	if err := app.svc.SensitiveExportApproved(fresh); err == nil {
		t.Fatal("没有有效审批单时,worker 必须拒绝放行原值")
	}
}
