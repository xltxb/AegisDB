package service

// 导出任务的终止。
//
// 导出查询可能跑很久(大表全量、没走索引的条件)。提交的人要能在系统里把它停下来,而"停下来"
// 指的是**目标库上的语句停下** —— 所以终止执行中的任务是取消 worker 手里那条查询的 context:
// MySQL 系由 pinForKill 另开连接发 KILL QUERY,PostgreSQL 由 lib/pq 发取消请求,Oracle 由
// go-ora 发 break(均已在真实库上验证,见 gateway 的 *_test)。
//
// 状态先改、再取消:终止请求原子地把任务从 awaiting / pending / running 改成 cancelled,抢不到
// 说明它已经结束了。worker 那边的收尾(done / failed)都带着"仍是 running"的条件,输给终止时
// 什么都不写,只删掉自己写出的分卷。

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"aegisdb/internal/apimsg"
	"aegisdb/internal/model"
)

// ErrExportFinished:任务已经结束(完成、失败、过期或已终止),无从终止。
var ErrExportFinished = errors.New("export job already finished")

func (s *Services) registerExportRun(id int64, cancel context.CancelFunc) {
	s.exportRunsMu.Lock()
	defer s.exportRunsMu.Unlock()
	if s.exportRuns == nil {
		s.exportRuns = map[int64]context.CancelFunc{}
	}
	s.exportRuns[id] = cancel
}

func (s *Services) unregisterExportRun(id int64) {
	s.exportRunsMu.Lock()
	defer s.exportRunsMu.Unlock()
	delete(s.exportRuns, id)
}

// cancelExportRun 取消本进程里正在跑的那一次;不在跑(还在排队、或已经跑完)就什么都不做。
func (s *Services) cancelExportRun(id int64) {
	s.exportRunsMu.Lock()
	cancel := s.exportRuns[id]
	s.exportRunsMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// CancelExport 终止一个导出任务。只有提交人或平台管理员能终止;别人的任务回 ErrNotFound
// (不透露它存在)。已经结束的任务回 ErrExportFinished。
//
// 待审批的任务一并撤回它的审批单 —— 否则审批人还会看到一张不再需要的单子,而批了也不会跑。
func (s *Services) CancelExport(actor *model.User, id int64) (*model.ExportJob, error) {
	if actor == nil {
		return nil, ErrForbidden
	}
	job, err := s.Repo.GetExportJob(id)
	if err != nil || (job.UserID != actor.ID && !s.IsAdmin(actor)) {
		return nil, ErrNotFound
	}
	was := job.Status
	m := apimsg.Sprintf("已被 %s 终止", actor.Name)
	msg := m.S
	applied, err := s.Repo.UpdateExportJobIf(id,
		[]string{model.ExportAwaiting, model.ExportPending, model.ExportRunning},
		map[string]any{"status": model.ExportCancelled, "error": clip(msg, 250), "error_ref": apimsg.RefJSON(m.Ref), "finished_at": time.Now()})
	if err != nil {
		return nil, err
	}
	if !applied {
		return nil, ErrExportFinished
	}
	s.cancelExportRun(id) // 在跑就停下它的查询(目标库上的语句一起停)

	if was == model.ExportAwaiting && job.ApprovalID != 0 {
		if cerr := s.CancelApproval(actor, job.ApprovalID); cerr != nil && !errors.Is(cerr, ErrAlreadyDecided) {
			slog.Warn("export cancelled but its approval could not be withdrawn", "job", id, "approval", job.ApprovalID, "err", cerr)
		}
	}

	conn, _ := s.Repo.GetConnection(job.ConnectionID)
	s.recordAuditSrc(model.AuditSourceExport, actor, conn, fmt.Sprintf("EXPORT CANCEL #%d(终止时状态 %s)%s", id, was, job.SQL),
		model.RiskLow, model.ResultCancelled, job.ApNo, "exec")
	if job.UserID != actor.ID {
		s.notify(job.UserID, model.NotifExportFailed, apimsg.T("导出已被终止"), apimsg.Sprintf("%s · %s", job.Name, m), itoa64(id))
	}
	if out, gerr := s.Repo.GetExportJob(id); gerr == nil {
		return out, nil
	}
	return job, nil
}

// removeFiles 删掉一份不再交付的归档(被终止的任务在跑完那一刻才输掉时)。
func removeFiles(files []string) {
	for _, f := range files {
		if err := os.Remove(f); err != nil {
			slog.Warn("cancelled export: could not remove part", "file", f, "err", err)
		}
	}
}
