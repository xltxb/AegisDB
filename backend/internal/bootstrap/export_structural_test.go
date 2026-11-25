package bootstrap

// 结构可能绕过脱敏的导出:要人看一眼,但**不放原值**。
//
// 这一组钉的是一次真实的回退。M-10 的修复把"查询含派生表 / UNION / CTE、脱敏可能覆盖
// 不到"的导出转去审批 —— 做法是把 includeSensitive 置 true。而这一位同时也是 worker
// "脱不脱敏"的开关:批准之后跑出来的是**原值**,可发起人根本没勾原值,审批人以为批的是
// "结构复杂看一眼"。两件事 —— 要不要人看、要不要原值 —— 从此必须是两个变量。

import (
	"net/http"
	"strings"
	"testing"

	"velagateway/internal/model"
)

const nestedSQL = "SELECT x FROM (SELECT id_card AS x FROM t_user) s"

// 没勾原值、结构可疑:停在 awaiting、有单可批;任务上**不能**记成要原值,
// 审批单文案要说清"仍按脱敏导出"。
func TestStructuralExport_ReviewedButNeverUnmasked(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	conn := app.exportSetup(admin)
	app.addSensitiveColumn(admin, "*", "id_card", "partial")

	eq(t, app.do(http.MethodPost, "/api/v1/export", admin, map[string]any{
		"connectionId": conn, "sql": nestedSQL, "name": "nested",
	}).Code, 0, "提交(未勾原值)")

	job := app.lastExportJob(conn)
	eq(t, job.Status, model.ExportAwaiting, "结构可能绕过脱敏的导出要先等审批")
	if job.IncludeSensitive {
		t.Fatal("发起人没要原值,任务上不能记成要原值 —— 这一位就是 worker 脱不脱敏的开关")
	}
	if job.ApprovalID == 0 {
		t.Fatal("必须生成审批单,否则任务永远停在那里")
	}
	var ap model.Approval
	if err := app.repo.DB().First(&ap, job.ApprovalID).Error; err != nil {
		t.Fatalf("读审批单: %v", err)
	}
	if !strings.Contains(ap.Command, "仍按脱敏导出") || strings.Contains(ap.Command, "原值") {
		t.Errorf("审批单要让审批人知道批的不是原值,实际: %q", ap.Command)
	}

	// 批准之后离开 awaiting —— 而 IncludeSensitive 仍然是 false。
	// 这一条是回退的直接守卫:worker 拿到的就是这一位,它是 false,产出就是脱敏的。
	approver := app.login("zhangwei@vela.io", "vela123")
	eq(t, app.do(http.MethodPost, "/api/v1/approvals/"+itoa(job.ApprovalID)+"/approve", approver, nil).Code, 0, "审批通过")
	after := app.lastExportJob(conn)
	if after.Status == model.ExportAwaiting {
		t.Error("批准之后任务应当离开 awaiting")
	}
	if after.IncludeSensitive {
		t.Fatal("批准不能把一份脱敏导出变成原值导出")
	}
}

// 平铺的查询哪怕碰了带规则的表也直接跑:打码本来就兜得住,不该逢敏感表就审批 ——
// 一道逢查询就要审批的闸,两天之内就会被要求关掉。
func TestStructuralExport_FlatQueryOnSensitiveTableRunsStraightAway(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	conn := app.exportSetup(admin)
	app.addSensitiveColumn(admin, "*", "id_card", "partial")

	eq(t, app.do(http.MethodPost, "/api/v1/export", admin, map[string]any{
		"connectionId": conn, "sql": "SELECT id_card, name FROM t_user", "name": "flat",
	}).Code, 0, "提交平铺查询")
	job := app.lastExportJob(conn)
	if job.Status == model.ExportAwaiting {
		t.Error("平铺查询不该去等审批")
	}
	if job.IncludeSensitive {
		t.Error("默认不该带原值")
	}
}

// 挂过审批单的任务,worker 放行前一律复核 —— 不只看 IncludeSensitive。
// 结构类任务这一位是 false,但它同样是靠一张单才从 awaiting 走到 pending 的。
func TestStructuralExport_WorkerRechecksTheTicketEvenWithoutRawFlag(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	conn := app.exportSetup(admin)
	app.addSensitiveColumn(admin, "*", "id_card", "partial")
	eq(t, app.do(http.MethodPost, "/api/v1/export", admin, map[string]any{
		"connectionId": conn, "sql": nestedSQL, "name": "nested",
	}).Code, 0, "提交")
	job := app.lastExportJob(conn)
	if job.IncludeSensitive || job.ApprovalID == 0 {
		t.Fatalf("前提不成立: IncludeSensitive=%v ApprovalID=%d", job.IncludeSensitive, job.ApprovalID)
	}
	// 单子还是 pending(没批),任务却被塞成 pending —— 复核必须拒绝
	if err := app.svc.SensitiveExportApproved(&job); err == nil {
		t.Fatal("审批单未批准时,复核必须拒绝放行")
	}
}

// 没提到敏感字段的 CTE 直接跑(按脱敏)—— 这是现网提出来的:规则是 * 不限表,
// 之前每条 WITH 都进审批,而绝大多数 WITH 根本没碰敏感字段。
func TestStructuralExport_CTEWithoutSensitiveColumnsRunsStraightAway(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	conn := app.exportSetup(admin)
	app.addSensitiveColumn(admin, "*", "id_card", "partial")

	eq(t, app.do(http.MethodPost, "/api/v1/export", admin, map[string]any{
		"connectionId": conn, "sql": "WITH u AS (SELECT id, name FROM t_user) SELECT name FROM u", "name": "cte",
	}).Code, 0, "提交不碰敏感字段的 CTE")
	job := app.lastExportJob(conn)
	if job.Status == model.ExportAwaiting {
		t.Error("没提到敏感字段的 CTE 不该去等审批")
	}
	if job.IncludeSensitive {
		t.Error("默认不该带原值")
	}
}
