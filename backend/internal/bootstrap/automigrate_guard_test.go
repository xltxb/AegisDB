package bootstrap

import (
	"bytes"
	"os"
	"testing"

	"velagateway/internal/testsupport"
)

// schema 只有一个权威来源:migrations/*.sql。
//
// 从前不是这样 —— dev 走 GORM AutoMigrate,prod 走 SQL 迁移,两份 schema 各自演进。
// db.go 里记着那次事故:tier_code 改名只做在一边,AutoMigrate 在已填充的 env 旁边
// 建了个空的 tier_code,而空值在两处查询里都读作「放行」,网关带着失效的管控上线,
// 健康检查全绿。这个测试是那条路被拆掉之后留下的钉子。
func TestSchemaHasOneSourceOfTruth(t *testing.T) {
	src, err := os.ReadFile("db.go")
	if err != nil {
		t.Fatalf("read db.go: %v", err)
	}
	for _, banned := range []string{"allModels", "autoMigrate", "shouldAutoMigrate"} {
		if bytes.Contains(src, []byte(banned)) {
			t.Errorf("db.go 又出现了 %q:schema 只能由 migrations/*.sql 定义", banned)
		}
	}
}

// Migrate 跑完,36 张表都在。
func TestMigrate_CreatesEverySchemaTable(t *testing.T) {
	db := testsupport.NewDB(t) // NewDB 已经跑过 baseline
	var n int64
	err := db.Raw(`SELECT count(*) FROM information_schema.tables
	               WHERE table_schema = current_schema() AND table_name LIKE 'tbl_%'`).Scan(&n).Error
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 36 {
		t.Fatalf("表数 = %d, want 36", n)
	}
}

// 跑第二次不炸 —— 账本挡住已应用的版本。
func TestMigrate_IsIdempotent(t *testing.T) {
	db := testsupport.NewDB(t)
	cfg := &Config{}
	cfg.Gateway.StrictMode = true
	if err := Migrate(cfg, db); err != nil {
		t.Fatalf("第二次 Migrate: %v", err)
	}
}
