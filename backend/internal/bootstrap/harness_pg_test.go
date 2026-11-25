package bootstrap

// 测试存储库跑真 PostgreSQL(AEGISDB_TEST_STORE_PG=libpq 键值串,不含 dbname)。
//
// 模板库只建一次:跑 pg 迁移、按开发环境播种、种测试夹具;之后每个测试用
// CREATE DATABASE … TEMPLATE 复制一份,测试结束 DROP … WITH (FORCE)。

import (
	"database/sql"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"aegisdb/internal/repository"
)

var (
	pgTplOnce sync.Once
	pgTplName string
	pgTplErr  error
	pgCreateMu sync.Mutex // 同一模板上的并发 CREATE DATABASE 会撞"模板正被访问"
	pgStoreSeq int64
)

func pgAdmin(base string) (*sql.DB, error) { return sql.Open("pgx", base+" dbname=postgres") }

// pgTestStore 给当前测试建一个独立的 PG 存储库,返回它的 DSN。
func pgTestStore(t *testing.T, base string, cfg *Config) string {
	t.Helper()
	tpl := pgTemplate(t, base, cfg)
	name := fmt.Sprintf("vt_%d_%d", os.Getpid(), atomic.AddInt64(&pgStoreSeq, 1))
	admin, err := pgAdmin(base)
	if err != nil {
		t.Fatal(err)
	}
	pgCreateMu.Lock()
	_, err = admin.Exec("CREATE DATABASE " + name + " TEMPLATE " + tpl)
	pgCreateMu.Unlock()
	if err != nil {
		admin.Close()
		t.Fatalf("create pg store: %v", err)
	}
	// 注册在 OpenDB 之前 → 清理时最后执行(LIFO),连接池已关。
	t.Cleanup(func() { _, _ = admin.Exec("DROP DATABASE IF EXISTS " + name + " WITH (FORCE)"); admin.Close() })
	return base + " dbname=" + name
}

func pgTemplate(t *testing.T, base string, cfg *Config) string {
	pgTplOnce.Do(func() {
		pgTplName = fmt.Sprintf("vt_tpl_%d", os.Getpid())
		admin, err := pgAdmin(base)
		if err != nil {
			pgTplErr = err
			return
		}
		defer admin.Close()
		_, _ = admin.Exec("DROP DATABASE IF EXISTS " + pgTplName + " WITH (FORCE)")
		if _, err := admin.Exec("CREATE DATABASE " + pgTplName); err != nil {
			pgTplErr = err
			return
		}
		c := *cfg
		// 与 SQLite 那条路一致:迁移后的一次性回填不把"全局严格模式关"折进分层
		// (SQLite 走 AutoMigrate,根本不跑那一步)。
		c.Gateway.StrictMode = true
		c.Database.PostgresDSN = base + " dbname=" + pgTplName
		db, err := OpenDB(&c)
		if err != nil {
			pgTplErr = err
			return
		}
		defer func() {
			if s, e := db.DB(); e == nil {
				s.Close()
			}
		}()
		if pgTplErr = Migrate(&c, db); pgTplErr != nil {
			return
		}
		repo := repository.New(db)
		sc := c
		sc.EnvRaw = ""
		if pgTplErr = Seed(repo, &sc); pgTplErr != nil {
			return
		}
		seedTestFixtures(t, repo)
	})
	if pgTplErr != nil {
		t.Fatalf("pg store template: %v", pgTplErr)
	}
	return pgTplName
}
