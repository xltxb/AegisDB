package service

// issue #109:发布单走到失败,不论经哪条路(队列满、panic、重启收尾),都要与正常失败一样收尾:
// 没跑到的阶段 skipped,中断的阶段 failed。留着「待执行」读起来像是还会跑。

import (
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"aegisdb/internal/model"
	"aegisdb/internal/repository"
)

func interruptedApp(t *testing.T) (*Services, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "rel.db")), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if sqlDB, derr := db.DB(); derr == nil {
		sqlDB.SetMaxOpenConns(1)
		t.Cleanup(func() { sqlDB.Close() })
	}
	if err := db.AutoMigrate(&model.Release{}, &model.ReleaseStage{}, &model.Approval{}, &model.Setting{}); err != nil {
		t.Fatal(err)
	}
	// 没人收的无缓冲队列 = 队列满
	return &Services{Repo: repository.New(db), releaseQueue: make(chan int64)}, db
}

// seedRelease 建一张发布单,阶段状态按给定顺序。
func seedRelease(t *testing.T, db *gorm.DB, no, status string, stages ...string) int64 {
	t.Helper()
	rel := model.Release{RelNo: no, Title: no, Status: status}
	if err := db.Create(&rel).Error; err != nil {
		t.Fatal(err)
	}
	for i, st := range stages {
		if err := db.Create(&model.ReleaseStage{ReleaseID: rel.ID, StepOrder: i, Name: st, Type: model.StageNotify, Status: st}).Error; err != nil {
			t.Fatal(err)
		}
	}
	return rel.ID
}

func wantRelease(t *testing.T, s *Services, id int64, status string, stages ...string) {
	t.Helper()
	rel, err := s.Repo.GetRelease(id)
	if err != nil {
		t.Fatal(err)
	}
	if rel.Status != status {
		t.Errorf("发布单 %s 状态 %s,应为 %s", rel.RelNo, rel.Status, status)
	}
	got, err := s.Repo.StagesOfRelease(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(stages) {
		t.Fatalf("%s 阶段数 %d,应为 %d", rel.RelNo, len(got), len(stages))
	}
	for i, st := range got {
		if st.Status != stages[i] {
			t.Errorf("%s 第 %d 个阶段 %s,应为 %s", rel.RelNo, i+1, st.Status, stages[i])
		}
	}
}

func TestQueueFullSkipsStages(t *testing.T) {
	s, db := interruptedApp(t)
	id := seedRelease(t, db, "REL-1", "pending", "pending", "pending")
	s.enqueueRelease(id)
	wantRelease(t, s, id, "failed", "skipped", "skipped")
}

func TestPanicFailsTheRunningStageAndSkipsTheRest(t *testing.T) {
	s, db := interruptedApp(t)
	id := seedRelease(t, db, "REL-1", "running", "success", "running", "pending", "waiting")
	s.guardRelease(id, func() { panic("boom") })
	wantRelease(t, s, id, "failed", "success", "failed", "skipped", "skipped")
}

// 停在人工闸上、或已经结束之后才 panic(例如发通知时):状态已经完整落库,不改判。
func TestPanicAfterTheRunSettledKeepsItsOutcome(t *testing.T) {
	s, db := interruptedApp(t)
	done := seedRelease(t, db, "REL-1", "success", "success")
	parked := seedRelease(t, db, "REL-2", "waiting", "success", "waiting", "pending")
	s.guardRelease(done, func() { panic("notify") })
	s.guardRelease(parked, func() { panic("notify") })
	wantRelease(t, s, done, "success", "success")
	wantRelease(t, s, parked, "waiting", "success", "waiting", "pending")
}

// 队列满时就地续跑:panic 不外泄,单子照样收尾。
func TestInlineResumeIsGuarded(t *testing.T) {
	s, db := interruptedApp(t)
	id := seedRelease(t, db, "REL-1", "waiting", "success", "pending")
	if err := db.Callback().Query().Before("gorm:query").Register("boom", func(tx *gorm.DB) {
		if tx.Statement.Table == "tbl_connection" || (tx.Statement.Schema != nil && tx.Statement.Schema.Table == "tbl_connection") {
			panic("boom")
		}
		if _, ok := tx.Statement.Model.(*model.Connection); ok {
			panic("boom")
		}
	}); err != nil {
		t.Fatal(err)
	}
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("panic 漏到了调用方: %v", r)
			}
		}()
		s.continueRelease(id)
	}()
	wantRelease(t, s, id, "failed", "success", "skipped")
}

func TestRestartSettlesOnlyTheInterruptedReleases(t *testing.T) {
	s, db := interruptedApp(t)
	stuck := seedRelease(t, db, "REL-1", "running", "success", "running", "pending")
	parked := seedRelease(t, db, "REL-2", "waiting", "waiting", "pending")
	queued := seedRelease(t, db, "REL-3", "pending", "pending")
	n, _, err := s.Repo.FailStuckReleases()
	if err != nil || n != 1 {
		t.Fatalf("应收尾 1 张,实际 %d %v", n, err)
	}
	wantRelease(t, s, stuck, "failed", "success", "failed", "skipped")
	wantRelease(t, s, parked, "waiting", "waiting", "pending")
	wantRelease(t, s, queued, "pending", "pending")
	if rel, _ := s.Repo.GetRelease(stuck); rel.ErrorRef != `{"code":"errRestart"}` {
		t.Errorf("重启原因没写上: %q", rel.ErrorRef)
	}
}
