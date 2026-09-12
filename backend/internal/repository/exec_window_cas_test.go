package repository

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"velagateway/internal/model"
)

func newWindowDB(t *testing.T) *Repo {
	t.Helper()
	path := filepath.Join(t.TempDir(), "win.db")
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if sqlDB, derr := db.DB(); derr == nil {
		t.Cleanup(func() { sqlDB.Close() })
	}
	if err := db.AutoMigrate(&model.ExecWindow{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return New(db)
}

// 窗口的决策必须**在数据库那一次更新里**就绑定到作出它的那张单。
//
// 「先读窗口、比对 ApprovalID、再写决策」这个顺序里有一道缝,而它正好能放进一次
// 改窗口:
//
//	 ① 审批人认领了旧单 → 旧单 approved
//	 ② applyWindowDecision 读窗口,此刻窗口还指着旧单 → 比对通过
//	 ③ 另一个人改窗口 → 窗口回到 pending 并链上新单;作废旧单失败(它已经 approved)
//	 ④ ② 那条线程继续往下写:只按 `id + status=pending` 更新 —— 而改后的窗口正是
//	    pending,于是**改后的定义被旧单上的那个签字批准了**
//
// 这正是 #2 要堵的那件事从另一条路钻回来。读-then-写挡不住它,只有把 approval_id
// 放进同一条 UPDATE 的 WHERE 里才挡得住:谁的 RowsAffected 是 1 谁赢,由数据库裁。
func TestSetExecWindowDecision_BindsToTheDecidingTicket(t *testing.T) {
	repo := newWindowDB(t)
	now := time.Now()

	w := &model.ExecWindow{
		Name: "班车", Enabled: true, ConnectionID: 7, Database: "orders_db",
		Kind: model.WindowOnce, Timezone: "Asia/Shanghai",
		Status: model.WindowPending, ApprovalID: 11,
	}
	if err := repo.CreateExecWindow(w); err != nil {
		t.Fatalf("建窗口: %v", err)
	}

	// 别的单批不动这个窗口 —— 哪怕它此刻正是 pending。
	ok, err := repo.SetExecWindowDecision(w.ID, 22, model.WindowApproved, now)
	if err != nil {
		t.Fatalf("决策: %v", err)
	}
	if ok {
		t.Error("一张不属于这个窗口的单把它批准了 —— 改窗口那一瞬正好能塞进来")
	}
	got, _ := repo.GetExecWindow(w.ID)
	if got.Status != model.WindowPending {
		t.Errorf("窗口被别的单改了状态:%s", got.Status)
	}

	// 它自己那张单可以。
	ok, err = repo.SetExecWindowDecision(w.ID, 11, model.WindowApproved, now)
	if err != nil {
		t.Fatalf("决策: %v", err)
	}
	if !ok {
		t.Fatal("窗口当前那张单反而批不动它")
	}
	got, _ = repo.GetExecWindow(w.ID)
	if got.Status != model.WindowApproved {
		t.Errorf("决策没落库:%s", got.Status)
	}

	// 已经决定过的窗口不会被第二次决定覆盖(原有的 status=pending 条件仍然要在)。
	ok, _ = repo.SetExecWindowDecision(w.ID, 11, model.WindowRejected, now)
	if ok {
		t.Error("一个已经决定过的窗口被第二次决定覆盖了")
	}
}
