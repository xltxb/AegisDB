package repository

import (
	"strings"
	"testing"

	"gorm.io/gorm"

	"aegisdb/internal/model"
)

// 设置一句写成:先 UPDATE、落空再 INSERT 的写法,在 MySQL 上对不存在的键会各持一把间隙锁,两次保存随后的
// INSERT 互相等待而死锁。
func TestSetSettings_SingleUpsert(t *testing.T) {
	r := newApprovalRepo(t)
	if err := r.db.AutoMigrate(&model.Setting{}); err != nil {
		t.Fatal(err)
	}
	updates := 0
	if err := r.db.Callback().Update().Before("gorm:update").Register("test:count", func(*gorm.DB) { updates++ }); err != nil {
		t.Fatal(err)
	}
	var sqls []string
	if err := r.db.Callback().Create().After("gorm:create").Register("test:sql", func(tx *gorm.DB) { sqls = append(sqls, tx.Statement.SQL.String()) }); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{`"1"`, `"2"`} { // 先是不存在的键,再是已有的
		if err := r.SetSettings(map[string]string{"a": v, "b": v}); err != nil {
			t.Fatal(err)
		}
		if err := r.SetSetting("c", v); err != nil {
			t.Fatal(err)
		}
		for _, k := range []string{"a", "b", "c"} {
			if got, err := r.GetSetting(k); err != nil || got != v {
				t.Errorf("%s = %q %v", k, got, err)
			}
		}
	}
	if updates != 0 {
		t.Errorf("跑了 %d 句 UPDATE", updates)
	}
	for _, q := range sqls {
		if !strings.Contains(q, "ON CONFLICT") {
			t.Errorf("不是 upsert: %s", q)
		}
	}
	if len(sqls) != 6 {
		t.Errorf("写了 %d 句", len(sqls))
	}
}
