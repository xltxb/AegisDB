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

// Migrate 跑完,37 张表都在(baseline 36 张 + 0002 的 tbl_osc_job)。
//
// 这里守的是**确切的张数**,所以加一张表就要回来改这个数字 —— 那是故意的:
// 顺手多建一张表、或者少建一张,两种都会让它红。
func TestMigrate_CreatesEverySchemaTable(t *testing.T) {
	db := testsupport.NewDB(t) // NewDB 已经跑过 baseline
	var n int64
	err := db.Raw(`SELECT count(*) FROM information_schema.tables
	               WHERE table_schema = current_schema() AND table_name LIKE 'tbl_%'`).Scan(&n).Error
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	// 期望值只写一次。刚才改这个数字时,条件改了而报错文案没改,于是它红着却说
	// 「表数 = 37, want 37」—— 一句自相矛盾的话会让下一个人去怀疑数据库。
	const want = 37
	if n != want {
		t.Fatalf("表数 = %d, want %d", n, want)
	}
}

// 在一个已经建好表的库上跑 Migrate,不炸。
//
// 挡住重复执行的其实有两道闸,而这条用例走的是**第二道**:
//
//	一、账本(schema_migrations):记着哪个版本应用过,`migrate` 跑第二遍时整份文件都跳过。
//	二、每份迁移自身整体幂等:baseline 是 36 条 CREATE TABLE IF NOT EXISTS + 48 条
//	    CREATE [UNIQUE] INDEX IF NOT EXISTS,0002 再加 1 张表 2 个索引,重跑只出 NOTICE。
//
// testsupport.NewDB 直接 Exec baseline,**不写账本**。所以这里第一道闸是空的,Migrate
// 看到「0001 还没应用」,把整份 baseline 又完整跑了一遍 —— 于是这条用例实际压的是第二道闸。
//
// 那比注释原本声称的更有价值:账本那道闸由 TestRunSQLMigrations_AppliesAndIsIdempotent
// 直接盯着,而「baseline 重跑无害」没有别的用例在管,偏偏又是 newMigratedDB 每次建夹具
// 都要依赖的性质。下面顺带断言账本落了一行 —— 钉住「它真的跑了」,而不是被谁跳过了。
func TestMigrate_IsIdempotent(t *testing.T) {
	db := testsupport.NewDB(t)
	cfg := &Config{}
	cfg.Gateway.StrictMode = true
	if err := Migrate(cfg, db); err != nil {
		t.Fatalf("在已建好表的库上 Migrate: %v", err)
	}
	var applied int64
	if err := db.Raw(`SELECT count(*) FROM schema_migrations WHERE version = '0001_init.sql'`).
		Scan(&applied).Error; err != nil {
		t.Fatalf("count ledger: %v", err)
	}
	if applied != 1 {
		t.Fatalf("账本里 0001_init.sql 有 %d 行, want 1 —— 这一趟没有真的重跑 baseline,"+
			"那这条用例并没有验证到「重跑无害」", applied)
	}
}
