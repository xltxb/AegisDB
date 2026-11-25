package repository

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"aegisdb/internal/model"
)

// 2026-10 黑盒(工单 01 第 6、7 条):审批步骤记在实际审批人名下;平台管理员的「全部」看全量。

func newApprovalRepo(t *testing.T) *Repo {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "ap.db")), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if sqlDB, derr := db.DB(); derr == nil {
		t.Cleanup(func() { sqlDB.Close() })
	}
	if err := db.AutoMigrate(&model.Approval{}, &model.ApprovalStep{}); err != nil {
		t.Fatal(err)
	}
	return New(db)
}

// 链是"任一人通过即生效":第 2 步的人批了,记在第 2 步上,而不是记在当时 active 的第 1 步。
func TestDecideStepBy_RecordsTheActualApprover(t *testing.T) {
	r := newApprovalRepo(t)
	ap := &model.Approval{ApNo: "AP-1", Status: model.StatusPending, InitiatorID: 9}
	steps := []model.ApprovalStep{
		{StepOrder: 1, ApproverID: 11, Approver: "Zhang Wei", Status: "active"},
		{StepOrder: 2, ApproverID: 12, Approver: "Li Na", Status: "waiting"},
	}
	if err := r.CreateApproval(ap, steps); err != nil {
		t.Fatal(err)
	}
	if err := r.DecideStepBy(ap.ID, 12, model.StatusApproved, time.Now()); err != nil {
		t.Fatal(err)
	}
	got, _ := r.StepsOf(ap.ID)
	by := map[string]model.ApprovalStep{}
	for _, s := range got {
		by[s.Approver] = s
	}
	if s := by["Li Na"]; s.Status != model.StatusApproved || s.ActedAt == nil {
		t.Errorf("实际审批人 Li Na 那一步应记 approved 并带时间: %+v", s)
	}
	if s := by["Zhang Wei"]; s.Status == model.StatusApproved || s.Status == "active" || s.ActedAt != nil {
		t.Errorf("没动手的 Zhang Wei 不该被记成审批人,也不该还挂着 active: %+v", s)
	}
}

// 审批人不在链上(外部回调等):退回旧行为,记在 active 那一步。
func TestDecideStepBy_FallsBackToActiveStep(t *testing.T) {
	r := newApprovalRepo(t)
	ap := &model.Approval{ApNo: "AP-2", Status: model.StatusPending}
	if err := r.CreateApproval(ap, []model.ApprovalStep{{StepOrder: 1, ApproverID: 11, Approver: "Zhang Wei", Status: "active"}}); err != nil {
		t.Fatal(err)
	}
	if err := r.DecideStepBy(ap.ID, 0, model.StatusRejected, time.Now()); err != nil {
		t.Fatal(err)
	}
	got, _ := r.StepsOf(ap.ID)
	if len(got) != 1 || got[0].Status != model.StatusRejected {
		t.Errorf("找不到实际审批人时应记在 active 那一步: %+v", got)
	}
}

// scope=global(平台管理员的「全部」):不在链上、不是发起人,也看得到全部工单。
func TestApprovalScope_GlobalSeesEveryTicket(t *testing.T) {
	r := newApprovalRepo(t)
	for i, no := range []string{"AP-A", "AP-B"} {
		ap := &model.Approval{ApNo: no, Status: model.StatusPending, InitiatorID: int64(100 + i)}
		if err := r.CreateApproval(ap, []model.ApprovalStep{{StepOrder: 1, ApproverID: 11, Status: "active"}}); err != nil {
			t.Fatal(err)
		}
	}
	const outsider = 77
	if _, n, _ := r.ListApprovalsPaged("all", outsider, "", model.StatusPending, "", "", 0, 50, nil); n != 0 {
		t.Fatalf("前提:普通 all 只含自己发起 / 自己要批的,实际 %d", n)
	}
	if _, n, _ := r.ListApprovalsPaged(ApprovalScopeGlobal, outsider, "", model.StatusPending, "", "", 0, 50, nil); n != 2 {
		t.Errorf("global 应看到全部 2 张待审单,实际 %d", n)
	}
	if n, _ := r.CountPendingApprovals(ApprovalScopeGlobal, outsider); n != 2 {
		t.Errorf("global 的待审徽章应为 2,实际 %d", n)
	}
}
