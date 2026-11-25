package bootstrap

import (
	"testing"
	"time"

	"aegisdb/internal/model"
	"aegisdb/internal/repository"
	"aegisdb/internal/service"
)

// issue #115:#107 / #109 之前结束的发布单,阶段还停在待执行 / 等待 / 运行中 —— 启动时按现在的规则收尾。
func TestStartupSettlesStagesOfEndedReleases(t *testing.T) {
	app := newTestApp(t)
	db, err := OpenDB(app.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if sqlDB, derr := db.DB(); derr == nil {
		t.Cleanup(func() { sqlDB.Close() })
	}
	repo := repository.New(db)
	ended := time.Now().Add(-72 * time.Hour).Truncate(time.Second)
	earlier := ended.Add(-time.Hour)

	seed := func(no, status string, finished *time.Time, stages ...string) int64 {
		rel := model.Release{RelNo: no, Title: no, Status: status, FinishedAt: finished}
		mustCreate(t, db, &rel)
		for i, st := range stages {
			row := model.ReleaseStage{ReleaseID: rel.ID, StepOrder: i, Name: st, Type: model.StageNotify, Status: st}
			if st == "success" || st == "failed" || st == "skipped" {
				row.FinishedAt = &earlier
			}
			mustCreate(t, db, &row)
		}
		return rel.ID
	}
	check := func(what string, id int64, want ...string) []model.ReleaseStage {
		t.Helper()
		got, err := repo.StagesOfRelease(id)
		if err != nil || len(got) != len(want) {
			t.Fatalf("%s: %v %v", what, got, err)
		}
		for i := range got {
			eq(t, got[i].Status, want[i], what)
		}
		return got
	}

	failed := seed("REL-9f", "failed", &ended, "success", "running", "pending", "waiting")
	aborted := seed("REL-9a", "aborted", &ended, "success", "waiting", "pending")
	noEnd := seed("REL-9n", "failed", nil, "pending")
	succeeded := seed("REL-9s", "success", &ended, "running", "success")
	waiting := seed("REL-9w", "waiting", nil, "success", "waiting", "pending")

	for range 2 { // 可重复执行
		_ = service.New(repo, app.svc.Engine, app.svc.JWT)
	}

	got := check("失败的单", failed, "success", "failed", "skipped", "skipped")
	for i, st := range got {
		want := ended
		if i == 0 {
			want = earlier // 已有结论的阶段,时间不动
		}
		if st.FinishedAt == nil || !st.FinishedAt.Equal(want) {
			t.Errorf("第 %d 个阶段结束时间 %v,应为 %v", i+1, st.FinishedAt, want)
		}
	}
	check("终止的单", aborted, "success", "skipped", "skipped")
	for _, st := range check("没有结束时间的单", noEnd, "skipped") {
		if st.FinishedAt == nil {
			t.Error("没有结束时间可借时,用当前时间")
		}
	}
	check("成功的单不动", succeeded, "running", "success")
	check("等待中的单不动", waiting, "success", "waiting", "pending")
}
