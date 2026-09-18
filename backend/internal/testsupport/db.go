// Package testsupport 给测试提供一个真实 PostgreSQL 上的独占 schema。
//
// 隔离用 schema 而不是 database:CREATE DATABASE 在 PG 上要复制模板库,几百个
// 测试各建一个会把测试时间拖成分钟级;schema 的创建近乎免费,且允许并行。
package testsupport

import (
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"velagateway/migrations"
)

// defaultDSN 指向本机的测试库。CI 或换机器时用 VELA_TEST_PG_DSN 覆盖。
const defaultDSN = "host=127.0.0.1 port=5432 dbname=vela_test sslmode=disable"

var seq atomic.Int64

// DSN 返回测试库连接串。
func DSN() string {
	if v := os.Getenv("VELA_TEST_PG_DSN"); v != "" {
		return v
	}
	return defaultDSN
}

// NewDB 建一个独占 schema,在其中建好全部表,返回绑定到它的 *gorm.DB。
// 测试结束时 schema 连同其中的表一起删掉。
//
// 连不上就让测试失败,不 Skip:静默跳过的测试等于假绿,而这套测试的全部价值
// 就在于它们真的对 PostgreSQL 跑过。
func NewDB(t *testing.T) *gorm.DB {
	t.Helper()

	// 每一步都在**成功之后立刻**注册它自己的清理,而不是攒到最后一次性注册。
	// 中间任何一步 t.Fatalf,前面已经建好的东西都得有人收 —— 否则 baseline SQL
	// 在开发中写错一次,跑一遍 bootstrap 就在 vela_test 里留下上百个空 schema,
	// 每个还攥着一个没关的连接池;连跑几次撞上 max_connections,之后所有测试都报
	// "too many clients already",而那句报错跟真正的病因毫无关系。
	//
	// t.Cleanup 是 LIFO,所以注册顺序正好是执行顺序的倒序:
	//   注册 admin 关池 → 注册 drop schema → 注册 scoped 关池
	//   执行 scoped 关池 → 执行 drop schema → 执行 admin 关池
	// drop 要用 admin,所以 admin 的池必须最后才关;scoped 的连接要先放掉,
	// 否则它们攥着 schema 里的对象。
	dsn := DSN()
	admin, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("连不上测试库 (%s):%v\n建库:createdb vela_test", dsn, err)
	}
	t.Cleanup(func() {
		if sqlDB, derr := admin.DB(); derr == nil {
			_ = sqlDB.Close()
		}
	})

	schema := fmt.Sprintf("t_%d_%d", os.Getpid(), seq.Add(1))
	if err := admin.Exec(`CREATE SCHEMA ` + schema).Error; err != nil {
		t.Fatalf("create schema %s: %v", schema, err)
	}
	t.Cleanup(func() {
		if err := admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`).Error; err != nil {
			t.Logf("清理 schema %s 失败:%v", schema, err)
		}
	})

	scoped := openScoped(t, dsn, schema)

	// 直接执行 baseline,不走 bootstrap.RunSQLMigrations。两个理由:
	//
	// 1. bootstrap 的测试是内部测试(package bootstrap),它们要 import 这个包;
	//    这个包再 import bootstrap 就成了循环导入,编译不过。
	// 2. RunSQLMigrations 会抢 advisory lock。测试库里每个 schema 都是独占的,
	//    没有并发迁移可言,而那把锁会把所有测试的建库串成一条队。
	// 按文件名顺序应用**全部**迁移,而不是只跑 0001。原先这里硬编码了 baseline
	// 一个文件 —— 那在只有一个迁移时是对的,但第二个迁移加进来时,症状是用到新表的
	// 测试报 "relation does not exist",而指向的是被测代码,不是这里。
	entries, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		t.Fatalf("列出迁移: %v", err)
	}
	var names []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names) // 与 RunSQLMigrations 一样按字典序
	for _, n := range names {
		sqlBytes, err := fs.ReadFile(migrations.FS, n)
		if err != nil {
			t.Fatalf("读迁移 %s: %v", n, err)
		}
		if err := scoped.Exec(string(sqlBytes)).Error; err != nil {
			t.Fatalf("迁移 %s 建表失败 (schema %s):%v", n, schema, err)
		}
	}

	return scoped
}

// scopedDSN 把 search_path 写进连接串。
//
// 必须写进 DSN 而不是连上以后再 SET —— 池里的**每一条**连接都得带上它,否则第二条
// 连接落回 public,建出来的表就消失在另一个 schema 里。
func scopedDSN(dsn, schema string) string {
	if strings.Contains(dsn, "://") {
		sep := "&"
		if !strings.Contains(dsn, "?") {
			sep = "?"
		}
		return dsn + sep + "search_path=" + schema
	}
	return dsn + " search_path=" + schema
}

// openScoped 在指定 schema 上开一个连接池,并注册关池的清理。
func openScoped(t *testing.T, dsn, schema string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(postgres.Open(scopedDSN(dsn, schema)), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("连 schema %s: %v", schema, err)
	}
	t.Cleanup(func() {
		if sqlDB, derr := db.DB(); derr == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

// NewSession 在 db 所在的那个 schema 上再开一个**独立连接池**的 *gorm.DB —— 也就是
// 一个必然不同的 PostgreSQL 会话。
//
// 为什么需要专门有这么一个入口:同一个 *sql.DB 上连着取两次 `Conn`,池子按 LIFO 把刚
// 归还的那条**原样递回来**,两次拿到的是同一个后端进程。而 advisory lock 是按会话计数
// 且可重入的 —— 于是「上一次忘了解锁」在同一个池子里看起来和「解锁了」一模一样:
// pg_try_advisory_lock 照样返回 true。
//
// 任何想验证会话级锁的测试,第二个参与者都必须来自这里,否则那条用例是确定性的假绿:
// 它不可能因为它声称守护的那件事而失败。
func NewSession(t *testing.T, db *gorm.DB) *gorm.DB {
	t.Helper()
	var schema string
	if err := db.Raw(`SELECT current_schema()`).Scan(&schema).Error; err != nil {
		t.Fatalf("read current_schema: %v", err)
	}
	if schema == "" {
		t.Fatal("current_schema() 是空的 —— 这个 handle 没有绑定到任何 schema")
	}
	return openScoped(t, DSN(), schema)
}
