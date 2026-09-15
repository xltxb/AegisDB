package bootstrap

import (
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"gorm.io/gorm"

	"velagateway/internal/testsupport"
)

func TestRunSQLMigrations_AppliesAndIsIdempotent(t *testing.T) {
	fsys := fstest.MapFS{
		"0001_a.sql": {Data: []byte(`-- comment
CREATE DATABASE IF NOT EXISTS ignored;
USE ignored;
CREATE TABLE IF NOT EXISTS t_alpha (id INTEGER PRIMARY KEY);`)},
		"0002_b.sql": {Data: []byte(`CREATE TABLE IF NOT EXISTS t_beta (id INTEGER PRIMARY KEY);`)},
	}

	// A dedicated schema, so the runner's own ledger table and the two toy
	// tables below cannot collide with another test's.
	db := testsupport.NewDB(t)
	if err := RunSQLMigrations(db, fsys); err != nil {
		t.Fatalf("first run: %v", err)
	}
	// Both migrations recorded.
	var count int64
	db.Table("schema_migrations").Count(&count)
	if count != 2 {
		t.Fatalf("expected 2 applied migrations, got %d", count)
	}
	// Tables exist (CREATE DATABASE/USE were skipped, so this ran against the
	// connected schema, not a phantom "ignored" database).
	if !db.Migrator().HasTable("t_alpha") || !db.Migrator().HasTable("t_beta") {
		t.Fatal("expected t_alpha and t_beta to exist")
	}

	// Second run is a no-op (idempotent) — no error, still exactly 2 rows.
	if err := RunSQLMigrations(db, fsys); err != nil {
		t.Fatalf("second run: %v", err)
	}
	db.Table("schema_migrations").Count(&count)
	if count != 2 {
		t.Fatalf("expected still 2 applied migrations after re-run, got %d", count)
	}
}

func TestSplitSQLStatements_SkipsCommentsAndScoping(t *testing.T) {
	stmts := splitSQLStatements(`-- header comment
CREATE DATABASE IF NOT EXISTS vela_gateway;
USE vela_gateway;
CREATE TABLE a (id INT);
CREATE TABLE b (id INT);`)
	if len(stmts) != 2 {
		t.Fatalf("expected 2 statements (DATABASE/USE/comments skipped), got %d: %v", len(stmts), stmts)
	}
}

// 两个进程同时跑迁移,只能有一个真的跑。
//
// 这是 advisory lock 存在的唯一理由,而它守的是一个只在**多副本部署钩子**上出现的
// 场景:两台实例同时启动、同时跑 `migrate`。没有锁的话,两边都读到「0001 还没应用」,
// 于是同一份 DDL 被执行两次 —— 第一个症状是 schema_migrations 主键冲突(R14),
// 而更坏的情况是迁移里有非幂等语句(未来的 ALTER / INSERT),那就是数据被写了两遍。
//
// 所以这条用例刻意用一份**非幂等**的迁移:CREATE TABLE 不带 IF NOT EXISTS,后面跟一条
// INSERT。锁没生效的话三种失败至少中一种 —— CREATE TABLE 报 already exists、
// schema_migrations 主键冲突、或者探针表里出现两行。
//
// 两个 goroutine 必须落在**同一个 schema** 上:testsupport.NewDB 每调一次就是一个新
// schema(t_<pid>_<seq>),两个 schema 之间不存在并发可言。所以这里共用一个 *gorm.DB;
// 它自带连接池,两次 RunSQLMigrations 各自 Pin 一条 *sql.Conn,也就是两个真实的
// PG 会话 —— 而 advisory lock 正是按会话算的。
func TestRunSQLMigrations_ConcurrentRunnersDoNotCollide(t *testing.T) {
	fsys := fstest.MapFS{
		"0001_probe.sql": {Data: []byte(
			"CREATE TABLE t_lock_probe (n INT);\n" +
				"INSERT INTO t_lock_probe (n) VALUES (1);")},
	}

	db := testsupport.NewDB(t)

	const runners = 2
	start := make(chan struct{})
	errs := make([]error, runners)
	var wg sync.WaitGroup
	for i := 0; i < runners; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // 一起出发,把两次抢锁挤进同一个瞬间
			errs[i] = RunSQLMigrations(db, fsys)
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("runner %d: %v", i, err)
		}
	}

	// 账本恰好一行 —— 两个 runner 谁也没在别人写过之后再写一次。
	var applied int64
	if err := db.Raw(`SELECT count(*) FROM schema_migrations WHERE version = '0001_probe.sql'`).
		Scan(&applied).Error; err != nil {
		t.Fatalf("count ledger: %v", err)
	}
	if applied != 1 {
		t.Errorf("schema_migrations 里 0001_probe.sql 有 %d 行, want 1", applied)
	}

	// 迁移体恰好执行一次。
	var rows int64
	if err := db.Raw(`SELECT count(*) FROM t_lock_probe`).Scan(&rows).Error; err != nil {
		t.Fatalf("count probe rows: %v", err)
	}
	if rows != 1 {
		t.Errorf("探针表里有 %d 行, want 1 —— 迁移被执行了不止一次", rows)
	}
}

// 锁用完必须还回去 —— 下一个**进程**才拿得到。
//
// pg_advisory_lock 是会话级的,而 *sql.Conn 用完会放回连接池。忘了 unlock 的话,那条
// 连接带着锁回到池里,锁一直活到进程退出;表现出来就是下一台实例的迁移原地等满 60 秒,
// 然后报「另一个迁移正在跑」,而那时根本没有别的迁移在跑。
//
// 第二个迁移者必须来自 testsupport.NewSession,也就是一个**必然不同的会话**。用同一个
// *gorm.DB 跑第二次是测不出东西的:池子按 LIFO 把刚归还的那条连接原样递回来,而 advisory
// lock 同会话可重入 —— 即便上一次真的漏了解锁,pg_try_advisory_lock 也照样返回 true。
// 那样写出来的用例是确定性的假绿,它不可能因为它声称守护的那件事而失败。
func TestRunSQLMigrations_ReleasesTheLock(t *testing.T) {
	db := testsupport.NewDB(t)
	other := testsupport.NewSession(t, db)

	// 先把「两个会话」这个前提本身钉住。这条断言一旦不成立,下面那个 10 秒断言就会因为
	// 可重入而无条件通过,整条用例随之失去牙齿 —— 而它失去牙齿的样子和通过一模一样。
	pid := func(h *gorm.DB) int {
		t.Helper()
		var n int
		if err := h.Raw(`SELECT pg_backend_pid()`).Scan(&n).Error; err != nil {
			t.Fatalf("pg_backend_pid: %v", err)
		}
		return n
	}
	if p1, p2 := pid(db), pid(other); p1 == p2 {
		t.Fatalf("两个 handle 落在同一个后端进程上(pid=%d) —— 这条用例测不了会话级的锁", p1)
	}

	if err := RunSQLMigrations(db, fstest.MapFS{
		"0001_a.sql": {Data: []byte(`CREATE TABLE IF NOT EXISTS t_relock_a (id INT);`)},
	}); err != nil {
		t.Fatalf("first run: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- RunSQLMigrations(other, fstest.MapFS{
			"0002_b.sql": {Data: []byte(`CREATE TABLE IF NOT EXISTS t_relock_b (id INT);`)},
		})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("second run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("另一个会话拿不到锁 —— 上一次迁移没有释放,锁被带回了连接池")
	}
}
