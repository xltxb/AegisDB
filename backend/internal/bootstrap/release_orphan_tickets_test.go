package bootstrap

import (
	"strings"
	"testing"

	"gorm.io/gorm"

	"aegisdb/internal/model"
	"aegisdb/internal/repository"
	"aegisdb/internal/service"
)

// issue #113:#111 之前残留的审批单 —— 发布单早就结束了,单还在请人审批 —— 启动时清掉。
func TestStartupVoidsTicketsOfEndedReleases(t *testing.T) {
	app := newTestApp(t)
	db, err := OpenDB(app.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if sqlDB, derr := db.DB(); derr == nil {
		t.Cleanup(func() { sqlDB.Close() })
	}
	repo := repository.New(db)

	ticket := func(apNo string, releaseID int64, status string) int64 {
		ap := model.Approval{ApNo: apNo, ReleaseID: releaseID, Status: status, Env: "prod", Instance: "i", Command: "c", RiskLevel: "mid"}
		mustCreate(t, db, &ap)
		return ap.ID
	}
	release := func(no, status string) int64 {
		rel := model.Release{RelNo: no, Title: no, Status: status}
		mustCreate(t, db, &rel)
		return rel.ID
	}

	orphans := map[string]int64{}
	for _, st := range []string{"failed", "aborted", "success"} {
		orphans[st] = ticket("AP-9"+st, release("REL-9"+st, st), "pending")
	}
	orphans["发布单已不存在"] = ticket("AP-9gone", 987654, "pending")

	// 阶段记着这张单:日志里说清它作废了
	failed := release("REL-9log", "failed")
	logged := ticket("AP-9log", failed, "pending")
	orphans["阶段记着的"] = logged
	stage := model.ReleaseStage{ReleaseID: failed, StepOrder: 0, Name: "审批", Type: model.StageApprove, Status: "failed", ApprovalID: logged, ApprovalNo: "AP-9log"}
	mustCreate(t, db, &stage)

	kept := map[string]int64{
		"等待中的发布单": ticket("AP-8w", release("REL-8w", "waiting"), "pending"),
		"普通工单":    ticket("AP-8p", 0, "pending"),
	}
	decided := ticket("AP-8d", release("REL-8d", "failed"), "approved")

	for range 2 { // 可重复执行
		_ = service.New(repo, app.svc.Engine, app.svc.JWT)
	}

	status := func(id int64) string {
		ap, err := repo.GetApproval(id)
		if err != nil {
			t.Fatal(err)
		}
		return ap.Status
	}
	for what, id := range orphans {
		eq(t, status(id), "expired", "残留的审批单应作废:"+what)
	}
	for what, id := range kept {
		eq(t, status(id), "pending", "不该动:"+what)
	}
	eq(t, status(decided), "approved", "已有的决定不动")
	st, _ := repo.StagesOfRelease(failed)
	if !strings.Contains(st[0].Log, "AP-9log") {
		t.Errorf("阶段日志应说明审批单作废,实际 %q", st[0].Log)
	}
}

func mustCreate(t *testing.T, db *gorm.DB, v any) {
	t.Helper()
	if err := db.Create(v).Error; err != nil {
		t.Fatal(err)
	}
}
