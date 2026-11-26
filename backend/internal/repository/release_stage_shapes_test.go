package repository

import (
	"reflect"
	"strings"
	"testing"

	"gorm.io/gorm"

	"aegisdb/internal/model"
)

// 发布单列表的阶段批量读取:一条查询、不读日志等大列、按单分组且单内有序。
// 见 StageShapesOfReleases —— 逐单查(N+1)曾把发布页轮询拖成存储库连接的排队源头。

type sqlCounter struct {
	n    int
	sqls []string
}

func newStageShapesRepo(t *testing.T) (*Repo, *gorm.DB) {
	t.Helper()
	db := openStore(t)
	return New(db), db
}

func TestStageShapesOfReleases_OneQueryGroupedOrderedWithoutOutput(t *testing.T) {
	r, db := newStageShapesRepo(t)
	for _, s := range []model.ReleaseStage{
		{ReleaseID: 2, StepOrder: 2, Name: "b2"},
		{ReleaseID: 1, StepOrder: 3, Name: "a3"},
		{ReleaseID: 1, StepOrder: 1, Name: "a1"},
		{ReleaseID: 9, StepOrder: 1, Name: "not-asked"},
		{ReleaseID: 2, StepOrder: 1, Name: "b1"},
		{ReleaseID: 1, StepOrder: 1, Name: "a1bis"},
	} {
		s.Type, s.Status = "review", "success"
		s.Log, s.LogRefs, s.Findings, s.Config = "LOG", "[]", "[]", "{}"
		if err := db.Create(&s).Error; err != nil {
			t.Fatal(err)
		}
	}

	c := &sqlCounter{}
	_ = db.Callback().Query().After("gorm:query").Register("test:count", func(tx *gorm.DB) {
		c.n++
		c.sqls = append(c.sqls, tx.Statement.SQL.String())
	})

	got, err := r.StageShapesOfReleases([]int64{1, 2, 3})
	if err != nil {
		t.Fatal(err)
	}
	if c.n != 1 {
		t.Errorf("应只发一条查询,实际 %d 条:%v", c.n, c.sqls)
	}
	if len(c.sqls) > 0 && strings.Contains(c.sqls[0], "*") {
		t.Errorf("不该 SELECT *:%s", c.sqls[0])
	}
	for _, col := range []string{"`log`", "log_refs", "findings", "config"} {
		if len(c.sqls) > 0 && strings.Contains(c.sqls[0], col) {
			t.Errorf("不该读大列 %s:%s", col, c.sqls[0])
		}
	}
	names := func(id int64) []string {
		out := []string{}
		for _, s := range got[id] {
			out = append(out, s.Name)
			if s.Log != "" || s.LogRefs != "" || s.Findings != "" || s.Config != "" {
				t.Errorf("阶段 %s 不该带输出", s.Name)
			}
		}
		return out
	}
	if n := names(1); !reflect.DeepEqual(n, []string{"a1", "a1bis", "a3"}) {
		t.Errorf("单 1 阶段 %v", n)
	}
	if n := names(2); !reflect.DeepEqual(n, []string{"b1", "b2"}) {
		t.Errorf("单 2 阶段 %v", n)
	}
	if _, ok := got[9]; ok {
		t.Error("没问的单不该出现")
	}
	if len(got[3]) != 0 {
		t.Error("没有阶段的单应为空")
	}

	empty, err := r.StageShapesOfReleases(nil)
	if err != nil || len(empty) != 0 || c.n != 1 {
		t.Errorf("空 id 列表不该查库:%v %v 查询数=%d", empty, err, c.n)
	}
}
