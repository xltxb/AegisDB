// Package testsupport 给测试提供一个真实 PostgreSQL 上的独占 schema。
//
// 隔离用 schema 而不是 database:CREATE DATABASE 在 PG 上要复制模板库,几百个
// 测试各建一个会把测试时间拖成分钟级;schema 的创建近乎免费,且允许并行。
package testsupport

import (
	"fmt"
	"io/fs"
	"os"
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

	dsn := DSN()
	admin, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("连不上测试库 (%s):%v\n建库:createdb vela_test", dsn, err)
	}

	schema := fmt.Sprintf("t_%d_%d", os.Getpid(), seq.Add(1))
	if err := admin.Exec(`CREATE SCHEMA ` + schema).Error; err != nil {
		t.Fatalf("create schema %s: %v", schema, err)
	}

	// search_path 要写进 DSN 重新连一次 —— 池里的每条连接都得带上它,否则第二条
	// 连接落回 public,建出来的表就消失在另一个 schema 里。
	scopedDSN := dsn + " search_path=" + schema
	if strings.Contains(dsn, "://") {
		sep := "&"
		if !strings.Contains(dsn, "?") {
			sep = "?"
		}
		scopedDSN = dsn + sep + "search_path=" + schema
	}

	scoped, err := gorm.Open(postgres.Open(scopedDSN), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("连 schema %s: %v", schema, err)
	}

	// 直接执行 baseline,不走 bootstrap.RunSQLMigrations。两个理由:
	//
	// 1. bootstrap 的测试是内部测试(package bootstrap),它们要 import 这个包;
	//    这个包再 import bootstrap 就成了循环导入,编译不过。
	// 2. RunSQLMigrations 会抢 advisory lock。测试库里每个 schema 都是独占的,
	//    没有并发迁移可言,而那把锁会把所有测试的建库串成一条队。
	sqlBytes, err := fs.ReadFile(migrations.FS, "0001_init.sql")
	if err != nil {
		t.Fatalf("read baseline: %v", err)
	}
	if err := scoped.Exec(string(sqlBytes)).Error; err != nil {
		t.Fatalf("baseline 建表失败 (schema %s):%v", schema, err)
	}

	t.Cleanup(func() {
		if sqlDB, derr := scoped.DB(); derr == nil {
			_ = sqlDB.Close()
		}
		if err := admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`).Error; err != nil {
			t.Logf("清理 schema %s 失败:%v", schema, err)
		}
		if sqlDB, derr := admin.DB(); derr == nil {
			_ = sqlDB.Close()
		}
	})

	return scoped
}
