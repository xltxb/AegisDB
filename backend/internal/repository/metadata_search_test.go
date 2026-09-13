package repository

import (
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"velagateway/internal/model"
)

// 元数据搜索里的 `%` 和 `_` 要当字面量,不当通配符。
//
// 审批搜索早就这么做了,理由写在那里:表名里到处都是下划线(t_order),不转义的话搜
// `t_order` 会连 `tXorder` 一起命中。元数据搜索漏了这一步,于是:
//
//   · 输入 `%`  → 命中全表(这一条尤其糟:人以为自己在搜,拿回来的是整个库的清单)
//   · 输入 `t_order` → 连 `tXorder` 一起回来
//
// ESCAPE 子句也必须显式写:MySQL 默认拿反斜杠当转义,而 **SQLite 默认一个转义字符都
// 没有** —— 只转义不写 ESCAPE,在 SQLite 上反而什么都搜不到。
func newMetaDB(t *testing.T) *Repo {
	t.Helper()
	path := filepath.Join(t.TempDir(), "meta.db")
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if sqlDB, derr := db.DB(); derr == nil {
		t.Cleanup(func() { sqlDB.Close() })
	}
	if err := db.AutoMigrate(&model.MetaTable{}, &model.MetaColumn{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	repo := New(db)
	for _, tb := range []model.MetaTable{
		{ConnectionID: 1, DBName: "orders_db", Name: "t_order"},
		{ConnectionID: 1, DBName: "orders_db", Name: "tXorder"},
		{ConnectionID: 1, DBName: "orders_db", Name: "customer"},
	} {
		if err := db.Create(&tb).Error; err != nil {
			t.Fatalf("seed table: %v", err)
		}
	}
	for _, col := range []model.MetaColumn{
		{ConnectionID: 1, DBName: "orders_db", TableName_: "t_order", Name: "id_card"},
		{ConnectionID: 1, DBName: "orders_db", TableName_: "t_order", Name: "idXcard"},
		{ConnectionID: 1, DBName: "orders_db", TableName_: "t_order", Name: "amount"},
	} {
		if err := db.Create(&col).Error; err != nil {
			t.Fatalf("seed column: %v", err)
		}
	}
	return repo
}

func TestSearchMetaTables_WildcardsAreLiteral(t *testing.T) {
	repo := newMetaDB(t)

	rows, err := repo.SearchMetaTables([]int64{1}, "%", 100)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("输入一个 %% 命中了 %d 张表 —— 人以为自己在搜,拿回来的是整个库的清单", len(rows))
	}

	rows, err = repo.SearchMetaTables([]int64{1}, "t_order", 100)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(rows) != 1 || rows[0].Name != "t_order" {
		names := []string{}
		for _, r := range rows {
			names = append(names, r.Name)
		}
		t.Errorf("搜 t_order 应当只命中 t_order,实际 %v —— 下划线被当成了通配符", names)
	}
}

func TestSearchMetaColumns_WildcardsAreLiteral(t *testing.T) {
	repo := newMetaDB(t)

	if rows, _ := repo.SearchMetaColumns([]int64{1}, "%", 100); len(rows) != 0 {
		t.Errorf("输入一个 %% 命中了 %d 列", len(rows))
	}
	rows, _ := repo.SearchMetaColumns([]int64{1}, "id_card", 100)
	if len(rows) != 1 || rows[0].Name != "id_card" {
		names := []string{}
		for _, r := range rows {
			names = append(names, r.Name)
		}
		t.Errorf("搜 id_card 应当只命中 id_card,实际 %v", names)
	}
}
