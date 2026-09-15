package repository

import (
	"testing"

	"velagateway/internal/model"
	"velagateway/internal/testsupport"
)

// 自定义 SQL 规范规则的 code 查重不分大小写,和邮箱同源同因:MySQL 的
// utf8mb4_unicode_ci 白送了这个语义,PG 不会。SaveReviewRule
// (internal/service/sqlreview.go) 靠 GetSQLReviewRuleByCode 判断编码是否已被占用
// 来拒绝重复,这个函数如果按原样比较,"custom.Foo" 和 "custom.foo" 就会被判成两个
// 不同的编码 —— 运营能建出两条看着同名、只是大小写不同的自定义规则,查重形同虚设。
//
// 跟邮箱不同的是:这里**不**在入库时 ToLower —— 运营输入的编码原样保留(存储层不
// 替他做主),只让"是否已存在"这个判断折叠大小写。
func TestGetSQLReviewRuleByCode_IsCaseInsensitive(t *testing.T) {
	db := testsupport.NewDB(t)
	r := New(db)

	if err := r.CreateSQLReviewRule(&model.SQLReviewRule{
		Code: "custom.Foo", Name: "运营自定义规则", Category: "dml",
	}); err != nil {
		t.Fatalf("seed rule: %v", err)
	}

	for _, in := range []string{"custom.Foo", "custom.foo", "CUSTOM.FOO"} {
		row, err := r.GetSQLReviewRuleByCode(in)
		if err != nil {
			t.Errorf("GetSQLReviewRuleByCode(%q): %v", in, err)
			continue
		}
		// 存储原样保留,不像邮箱那样被 ToLower 改写。
		if row.Code != "custom.Foo" {
			t.Errorf("GetSQLReviewRuleByCode(%q) 存储形态被改写: got %q, want %q", in, row.Code, "custom.Foo")
		}
	}
}

// DB 层的唯一索引也必须折叠大小写,不能只靠应用层查重 —— 两个并发请求都在
// GetSQLReviewRuleByCode 查重通过之后才各自 INSERT,应用层拦不住那个竞态窗口,
// 真正兜底的是唯一约束本身。这里绕开应用层直接 INSERT,验证 DB 层独立生效。
func TestSQLReviewRuleCodeUniqueIndexIsCaseInsensitive(t *testing.T) {
	db := testsupport.NewDB(t)

	if err := db.Exec(`INSERT INTO tbl_sql_review_rule (code, name, category)
	                   VALUES ('custom.Foo', '运营自定义规则', 'dml')`).Error; err != nil {
		t.Fatalf("seed rule: %v", err)
	}

	err := db.Exec(`INSERT INTO tbl_sql_review_rule (code, name, category)
	                VALUES ('custom.foo', '同名大小写不同', 'dml')`).Error
	if err == nil {
		t.Fatal("插入 code 仅大小写不同的第二条规则本应被唯一约束拒绝,却成功了")
	}
}
