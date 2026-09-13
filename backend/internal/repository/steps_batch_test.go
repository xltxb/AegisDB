package repository

import (
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"velagateway/internal/model"
)

// 审批列表一页最多 500 张单,而每张单再查一次审批链 —— 一次翻页最多 501 次查询。
//
// 这条路径是**登录后第一屏**(待办、我的申请都读它),而 pageSize 由调用方给,上限 500。
// 每多一张单就多一次往返,在 MySQL 上那是实打实的 500 次网络请求;更糟的是它随数据量
// 增长,而没有任何地方会报错 —— 页面只是越来越慢。
//
// 一次查完:按 approval_id IN (…) 取回所有步骤,再按单分组。
func newStepsDB(t *testing.T) *Repo {
	t.Helper()
	path := filepath.Join(t.TempDir(), "steps.db")
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if sqlDB, derr := db.DB(); derr == nil {
		t.Cleanup(func() { sqlDB.Close() })
	}
	if err := db.AutoMigrate(&model.ApprovalStep{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return New(db)
}

func TestStepsOfMany_GroupsByApproval(t *testing.T) {
	repo := newStepsDB(t)
	db := repo.DB()

	// 三张单:两张各两步,一张没有步骤(审批链为空时会这样)。
	for _, s := range []model.ApprovalStep{
		{ApprovalID: 1, StepOrder: 2, Approver: "乙", Status: "waiting"},
		{ApprovalID: 1, StepOrder: 1, Approver: "甲", Status: "active"},
		{ApprovalID: 2, StepOrder: 1, Approver: "丙", Status: "approved"},
		{ApprovalID: 2, StepOrder: 2, Approver: "丁", Status: "waiting"},
	} {
		if err := db.Create(&s).Error; err != nil {
			t.Fatalf("seed step: %v", err)
		}
	}

	got, err := repo.StepsOfMany([]int64{1, 2, 3})
	if err != nil {
		t.Fatalf("StepsOfMany: %v", err)
	}

	if len(got[1]) != 2 || len(got[2]) != 2 {
		t.Fatalf("分组不对:单 1 有 %d 步、单 2 有 %d 步", len(got[1]), len(got[2]))
	}
	// 顺序要和逐张查时一样 —— 界面按它画审批链,乱序等于把流程画反了。
	if got[1][0].Approver != "甲" || got[1][1].Approver != "乙" {
		t.Errorf("单 1 的步骤没有按 step_order 排:%v", []string{got[1][0].Approver, got[1][1].Approver})
	}
	// 没有步骤的单不该凭空多出一个空条目。
	if _, ok := got[3]; ok {
		t.Error("没有步骤的单不该出现在结果里 —— 调用方按 map 取到 nil 切片就够了")
	}
	// 逐张查的结果必须一致。
	one, _ := repo.StepsOf(1)
	if len(one) != len(got[1]) {
		t.Errorf("一次查完和逐张查的结果不一致:%d vs %d", len(got[1]), len(one))
	}
}

// 空入参不该发出查询,也不该 panic。
func TestStepsOfMany_EmptyInput(t *testing.T) {
	got, err := newStepsDB(t).StepsOfMany(nil)
	if err != nil {
		t.Fatalf("StepsOfMany(nil): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("空入参应当得到空结果,实际 %d 组", len(got))
	}
}
