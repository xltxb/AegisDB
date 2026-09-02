package service

// 含敏感字段的导出:批准之后才跑。
//
// 导出默认脱敏(ADR 0009)。但确实有需要原值的场景 —— 对账、迁移、监管调取。让人
// 自己勾一个开关就放行是不行的:一份带身份证号的 CSV 落到磁盘、发进聊天工具,比在
// 终端上看一眼跑得远得多,而这恰恰是脱敏在导出这一路最要紧的原因。
//
// 所以放开它要有人签字。三条边界:
//   - 勾了"包含敏感字段"的任务**不进队列**,停在 awaiting
//   - 批准之后由 worker 后台执行(不是发起人手动执行 —— 它授权的是"让 worker 去跑")
//   - worker 在真正放行原值之前**自己再核一次**审批状态

import (
	"fmt"
	"log/slog"

	"velagateway/internal/model"
)

// raiseSensitiveExportApproval parks the job on an approval ticket.
func (s *Services) raiseSensitiveExportApproval(u *model.User, conn *model.Connection, job *model.ExportJob) error {
	tierCode := ""
	if t, err := s.tierOf(conn); err == nil {
		tierCode = t.Code
	}
	// Command 是一句**描述**而不是可执行的语句:这张单授权的事情是"让导出 worker 去
	// 跑这次导出",不是"在终端里执行一句 SQL"。审批人看到的应该是这件事的样子 ——
	// 前缀里明写"含敏感字段(原值)",因为那才是他要签字放行的东西。
	ap := &model.Approval{
		ApNo: s.nextApNo(), ConnectionID: conn.ID, Env: conn.Env, TierCode: tierCode,
		Instance: conn.Name, Database: job.Database,
		Command:  "EXPORT [含敏感字段(原值)] " + job.SQL,
		Keyword:  "EXPORT", InitiatorID: u.ID, Initiator: u.Name,
		Reason:   "导出包含敏感字段的原始值",
		// 敏感数据出库按高危记。它不改任何数据,但风险等级衡量的是后果,而这条的
		// 后果是明文 PII 落到一个网关管不到的地方。
		RiskLevel:   model.RiskHigh,
		Status:      model.StatusPending,
		AuditID:     s.nextAuditID(),
		ExportJobID: job.ID,
	}
	steps := s.defaultChainSteps()
	if len(steps) > 0 {
		steps[0].Status = "active"
	}
	if err := s.Repo.CreateApproval(ap, steps); err != nil {
		slog.Error("含敏感字段导出建单失败", "job", job.ID, "err", err)
		return err
	}
	if err := s.Repo.LinkExportApproval(job.ID, ap.ID, ap.ApNo); err != nil {
		return err
	}
	s.recordAudit(u, conn, "EXPORT [含敏感字段] "+job.SQL, model.RiskHigh, model.ResultPending, ap.ApNo, "intercept")
	s.Webhook.SendLarkApproval(ap)
	s.dispatchExternalApproval(u, ap)
	job.ApNo = ap.ApNo
	job.ApprovalID = ap.ID
	return nil
}

// releaseApprovedExport is what an approved export ticket actually authorises:
// the job goes into the queue and the worker runs it. 它不是"执行一条命令" ——
// 所以这张单走不了发起人手动执行那条路(见 canExecuteApproved)。
func (s *Services) releaseApprovedExport(ap *model.Approval) {
	job, err := s.Repo.GetExportJob(ap.ExportJobID)
	if err != nil {
		slog.Error("已批准的导出任务不存在", "job", ap.ExportJobID, "apNo", ap.ApNo)
		return
	}
	if job.Status != model.ExportAwaiting {
		// 已经被放行过、或已失败 —— 不能再排一次:同一张单排两次会产出两份带原值的
		// 归档,而人只知道有一份。
		return
	}
	if err := s.Repo.SetExportStatus(job.ID, model.ExportPending); err != nil {
		slog.Error("导出任务放行失败", "job", job.ID, "err", err)
		return
	}
	select {
	case s.exportQueue <- job.ID:
	default:
		s.failExport(job.ID, "导出队列已满,请稍后重试")
	}
}

// sensitiveExportApproved re-verifies, at the moment of actually unmasking, that
// this job really carries a granted approval.
//
// 调用方(runExportJob)本来就只会在 job.IncludeSensitive 为真时走到这里,所以这
// 一次复核是重复的 —— 故意的。它守的东西一旦漏了就是明文 PII 直接写进 CSV,而
// 这类旁路最常见的失效方式不是逻辑写错,是后来有人加了一条新路径、忘了先检查。
// 把检查放在**放行原值的那一刻**,新路径就绕不过去。
func (s *Services) SensitiveExportApproved(job *model.ExportJob) error {
	if job.ApprovalID == 0 {
		return fmt.Errorf("导出任务标记为包含敏感字段,却没有关联审批单")
	}
	ap, err := s.Repo.GetApproval(job.ApprovalID)
	if err != nil {
		return fmt.Errorf("关联的审批单不存在")
	}
	if ap.Status != model.StatusApproved {
		return fmt.Errorf("关联的审批单当前是 %s,不是已批准", ap.Status)
	}
	if ap.ExportJobID != job.ID {
		// 审批单指向别的任务:说明这个 approval_id 是被拼上去的,不是这次审批的结果。
		return fmt.Errorf("审批单与导出任务不匹配")
	}
	return nil
}
