package service

// issue #111:发布单进入失败 / 终止,它名下还待审批的单一并作废 —— 留着就是请人对一张已经死掉的单做决定。

import (
	"strings"
	"testing"

	"gorm.io/gorm"

	"aegisdb/internal/model"
)

func seedTicket(t *testing.T, db *gorm.DB, apNo string, releaseID int64, status string) int64 {
	t.Helper()
	ap := model.Approval{ApNo: apNo, ReleaseID: releaseID, Status: status, Env: "prod", Instance: "i", Command: "c", RiskLevel: "mid"}
	if err := db.Create(&ap).Error; err != nil {
		t.Fatal(err)
	}
	return ap.ID
}

// approveStage 把一张发布单的第 i 个阶段改成审批阶段,可带上它建出来的单。
func approveStage(t *testing.T, db *gorm.DB, releaseID int64, i int, apID int64, apNo string) {
	t.Helper()
	if err := db.Model(&model.ReleaseStage{}).Where("release_id = ? AND step_order = ?", releaseID, i).
		Updates(map[string]any{"type": model.StageApprove, "approval_id": apID, "approval_no": apNo}).Error; err != nil {
		t.Fatal(err)
	}
}

func wantTicket(t *testing.T, s *Services, id int64, status string) {
	t.Helper()
	ap, err := s.Repo.GetApproval(id)
	if err != nil {
		t.Fatal(err)
	}
	if ap.Status != status {
		t.Errorf("审批单 %s 状态 %s,应为 %s", ap.ApNo, ap.Status, status)
	}
}

func restart(t *testing.T, s *Services) {
	t.Helper()
	_, voided, err := s.Repo.FailStuckReleases()
	if err != nil {
		t.Fatal(err)
	}
	s.afterVoided(voided) // 与 service.New 启动时一样
}

// 阶段已经停在审批上,发布单状态还没来得及写:进程断了。
func TestRestartVoidsTheTicketOfAnInterruptedRelease(t *testing.T) {
	s, db := interruptedApp(t)
	id := seedRelease(t, db, "REL-1", "running", "waiting", "pending")
	ap := seedTicket(t, db, "AP-1", id, model.StatusPending)
	approveStage(t, db, id, 0, ap, "AP-1")
	restart(t, s)
	wantTicket(t, s, ap, model.StatusExpired)
	wantRelease(t, s, id, "failed", "skipped", "skipped")
	if st, _ := s.Repo.StagesOfRelease(id); !strings.Contains(st[0].Log, "AP-1") {
		t.Errorf("阶段日志没说审批单作废: %q", st[0].Log)
	}
}

// 单已经建出来(还要发飞书、写审计),阶段上还没记下它的单号:进程断了。
func TestRestartVoidsATicketItsStageNeverRecorded(t *testing.T) {
	s, db := interruptedApp(t)
	id := seedRelease(t, db, "REL-1", "running", "running", "pending")
	approveStage(t, db, id, 0, 0, "")
	ap := seedTicket(t, db, "AP-1", id, model.StatusPending)
	restart(t, s)
	wantTicket(t, s, ap, model.StatusExpired)
	st, _ := s.Repo.StagesOfRelease(id)
	if st[0].ApprovalID != ap || st[0].ApprovalNo != "AP-1" {
		t.Errorf("阶段应补记它建的单,实际 %d %q", st[0].ApprovalID, st[0].ApprovalNo)
	}
}

func TestPanicVoidsTheTicket(t *testing.T) {
	s, db := interruptedApp(t)
	id := seedRelease(t, db, "REL-1", "running", "running", "pending")
	approveStage(t, db, id, 0, 0, "")
	ap := seedTicket(t, db, "AP-1", id, model.StatusPending)
	s.guardRelease(id, func() { panic("boom") })
	wantTicket(t, s, ap, model.StatusExpired)
}

// 已经有人做出的决定、别的发布单的单、与发布无关的单,一概不动。
func TestVoidingLeavesOtherTicketsAlone(t *testing.T) {
	s, db := interruptedApp(t)
	id := seedRelease(t, db, "REL-1", "running", "success", "running")
	parked := seedRelease(t, db, "REL-2", "waiting", "waiting")
	decided := seedTicket(t, db, "AP-1", id, model.StatusApproved)
	other := seedTicket(t, db, "AP-2", parked, model.StatusPending)
	plain := seedTicket(t, db, "AP-3", 0, model.StatusPending)
	restart(t, s)
	wantTicket(t, s, decided, model.StatusApproved)
	wantTicket(t, s, other, model.StatusPending)
	wantTicket(t, s, plain, model.StatusPending)
}
