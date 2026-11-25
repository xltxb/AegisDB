package repository

import (
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"velagateway/internal/model"
)

// AI 巡检报告的元数据(.scratch/inspection-report 工单 01)。

func newInspectionRepo(t *testing.T) *Repo {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "insp.db")), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if sqlDB, derr := db.DB(); derr == nil {
		t.Cleanup(func() { sqlDB.Close() })
	}
	if err := db.AutoMigrate(&model.InspectionReport{}); err != nil {
		t.Fatal(err)
	}
	return New(db)
}

func rep(conn int64, date, level string) *model.InspectionReport {
	return &model.InspectionReport{ConnectionID: conn, InstanceName: "inst", ReportDate: date, Level: level, FilePath: "x", SHA256: "h"}
}

// 同一实例同一天再推一次:覆盖,只剩一条,保留 ID。
func TestInspection_UpsertReplacesSameInstanceSameDay(t *testing.T) {
	r := newInspectionRepo(t)
	first := rep(1, "2026-10-15", model.InspectionWarning)
	if replaced, err := r.UpsertInspectionReport(first); err != nil || replaced {
		t.Fatalf("第一次应是新建: replaced=%v err=%v", replaced, err)
	}
	again := rep(1, "2026-10-15", model.InspectionCritical)
	if replaced, err := r.UpsertInspectionReport(again); err != nil || !replaced {
		t.Fatalf("同实例同一天应覆盖: replaced=%v err=%v", replaced, err)
	}
	if again.ID != first.ID {
		t.Errorf("覆盖应保留原 ID: %d → %d", first.ID, again.ID)
	}
	list, total, err := r.ListInspectionReports(1, 0, 10)
	if err != nil || total != 1 || list[0].Level != model.InspectionCritical {
		t.Fatalf("应只剩一条、内容是新的: total=%d list=%+v err=%v", total, list, err)
	}
	// 另一天是另一份
	if replaced, _ := r.UpsertInspectionReport(rep(1, "2026-10-16", model.InspectionOK)); replaced {
		t.Error("不同日期不该覆盖")
	}
}

// 每个实例只取最新一份;历史按日期倒序。
func TestInspection_LatestPerInstanceAndHistoryOrder(t *testing.T) {
	r := newInspectionRepo(t)
	for _, x := range []*model.InspectionReport{
		rep(1, "2026-10-13", model.InspectionOK), rep(1, "2026-10-15", model.InspectionCritical), rep(1, "2026-10-14", model.InspectionWarning),
		rep(2, "2026-10-14", model.InspectionOK),
	} {
		if _, err := r.UpsertInspectionReport(x); err != nil {
			t.Fatal(err)
		}
	}
	latest, err := r.LatestInspectionReports()
	if err != nil || len(latest) != 2 {
		t.Fatalf("两个实例各取一份: %+v err=%v", latest, err)
	}
	for _, x := range latest {
		want := map[int64]string{1: "2026-10-15", 2: "2026-10-14"}[x.ConnectionID]
		if x.ReportDate != want {
			t.Errorf("实例 %d 最新应是 %s,实际 %s", x.ConnectionID, want, x.ReportDate)
		}
	}
	hist, total, _ := r.ListInspectionReports(1, 0, 2)
	if total != 3 || len(hist) != 2 || hist[0].ReportDate != "2026-10-15" || hist[1].ReportDate != "2026-10-14" {
		t.Errorf("历史按日期倒序、分页: total=%d %+v", total, hist)
	}
}
