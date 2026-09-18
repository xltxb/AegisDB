package osc

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	_ "github.com/go-sql-driver/mysql"
)

// 这些用例**必须**对着真 MySQL 跑(ADR 0011)。拷贝与重放的交错、cut-over 的锁竞争
// 都只在真服务器上存在,拿 sqlite 或 mock 换来的绿色是假的。
//
// 连不上就**失败**,不是 skip —— 和后端其余测试对 vela_test 的规矩一致。静默跳过
// 会让一个谁也没跑过的 osc 包看起来是绿的,而它是会在生产库上改数据的东西。
const defaultDSN = "vela:velapass@tcp(127.0.0.1:3306)/osc_test?parseTime=true&multiStatements=true"

func mysqlDSN() string {
	if v := os.Getenv("VELA_OSC_MYSQL_DSN"); v != "" {
		return v
	}
	return defaultDSN
}

// 造某些夹具(触发器)需要特权连接,见用例里的说明。被测代码始终走普通账号。
func rootDSN() string {
	if v := os.Getenv("VELA_OSC_MYSQL_ROOT_DSN"); v != "" {
		return v
	}
	return "root@tcp(127.0.0.1:3306)/osc_test?parseTime=true&multiStatements=true"
}

func openMySQL(t *testing.T) *sql.DB { return openMySQLAs(t, mysqlDSN()) }

func openMySQLAs(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("打不开 MySQL 连接(DSN=%s): %v", dsn, err)
	}
	if err := db.Ping(); err != nil {
		t.Fatalf("连不上 MySQL(DSN=%s): %v\n"+
			"osc 的测试必须对着真 MySQL 跑,见 ADR 0011。本机装一个:\n"+
			"  brew install mysql && brew services start mysql\n"+
			"  mysql -u root -e \"CREATE DATABASE osc_test; CREATE USER 'vela'@'127.0.0.1' IDENTIFIED BY 'velapass';"+
			" GRANT ALL ON osc_test.* TO 'vela'@'127.0.0.1'; GRANT REPLICATION SLAVE, REPLICATION CLIENT ON *.* TO 'vela'@'127.0.0.1';\"\n"+
			"或用仓库根的 docker-compose.yml 里的 mysql-target(端口 3307)。", dsn, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// 每个用例自己的表,名字带上用例名 —— 并行跑时互不干扰,失败后残留的表也一眼看得出
// 是谁留下的。
//
// 同一个用例里要建多张表时必须各自唯一:光用 t.Name() 会让第二张表覆盖第一张,而
// 症状是"订阅收到了不该收到的事件" —— 指向的是过滤逻辑,而错在夹具。所以名字后面
// 挂一个序号。
var tableSeq atomic.Int64

func makeTable(t *testing.T, db *sql.DB, ddl string) string {
	t.Helper()
	name := fmt.Sprintf("t_%s_%d", t.Name(), tableSeq.Add(1))
	name = strings.NewReplacer("/", "_", " ", "_").Replace(name)
	// MySQL 的标识符上限是 64。影子表名还要在两头加 `_` 和 `_gho`(4 字符),
	// 所以夹具表名必须留出余量 —— 否则长用例名会在建影子表那一步才炸,
	// 而报错指向的是被测代码,不是夹具。
	if len(name) > 56 {
		name = name[:56]
	}
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, "DROP TABLE IF EXISTS `"+name+"`"); err != nil {
		t.Fatalf("清理旧表失败: %v", err)
	}
	if _, err := db.ExecContext(ctx, fmt.Sprintf(ddl, name)); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	t.Cleanup(func() { _, _ = db.ExecContext(context.Background(), "DROP TABLE IF EXISTS `"+name+"`") })
	return name
}

// 第一条:采集能认出一张普通的、有主键的表。
// 断言的是 Preflight 真正会读的那几个字段 —— 它们决定"这次变更能不能做"。
func TestGather_ReadsAPlainTableWithAPrimaryKey(t *testing.T) {
	db := openMySQL(t)
	name := makeTable(t, db, "CREATE TABLE `%s` (id BIGINT PRIMARY KEY, memo VARCHAR(64))")

	f, err := Gather(context.Background(), db, "osc_test", name)
	if err != nil {
		t.Fatalf("采集失败: %v", err)
	}
	if f.Engine != "mysql" {
		t.Errorf("Engine = %q,想要 mysql", f.Engine)
	}
	if !f.TableExists {
		t.Error("TableExists = false,但表是刚建出来的")
	}
	if !f.HasPK {
		t.Error("HasPK = false,但表上有 PRIMARY KEY")
	}
	if f.ForeignKeysOut != 0 || f.ForeignKeysIn != 0 || f.Triggers != 0 {
		t.Errorf("一张干净的表不该有外键或触发器,实际 out=%d in=%d trig=%d",
			f.ForeignKeysOut, f.ForeignKeysIn, f.Triggers)
	}
	// 实例参数来自这台真实例,而 docker-compose / 本机配置都要求它们是这几个值。
	if !f.LogBin || f.BinlogFormat != "ROW" || f.BinlogRowImg != "FULL" {
		t.Errorf("实例参数不满足 OSC 前提: log_bin=%v format=%s row_image=%s",
			f.LogBin, f.BinlogFormat, f.BinlogRowImg)
	}
	// 采集到的事实应当足以让前置检查放行。
	if bs, _ := Preflight(f, ActionAddIndex); len(bs) != 0 {
		t.Errorf("这张表本该可以做在线加索引,却被拒绝: %+v", bs)
	}
}

// 采集的价值不在读出一张干净的表,而在认出那些**会让影子表方案做不干净**的形态。
// 每一条都对着真库造出来,再看 Preflight 是否据此拒绝 —— 这是采集层与判定层之间
// 唯一要紧的契约:采集说的话,判定听得懂。
func TestGather_SeesTheShapesThatMustBeRefused(t *testing.T) {
	db := openMySQL(t)
	ctx := context.Background()

	t.Run("无主键无唯一键的表被拒", func(t *testing.T) {
		name := makeTable(t, db, "CREATE TABLE `%s` (id BIGINT, memo VARCHAR(64))")
		f, err := Gather(ctx, db, "osc_test", name)
		if err != nil {
			t.Fatalf("采集失败: %v", err)
		}
		if f.HasPK || len(f.UniqueNotNull) != 0 {
			t.Fatalf("这张表既没主键也没唯一键,实际 HasPK=%v unique=%v", f.HasPK, f.UniqueNotNull)
		}
		if !codes(firstOf(Preflight(f, ActionAddIndex)))["no_unique_key"] {
			t.Error("没有可分块的键,本该被拒")
		}
	})

	// 唯一非空索引可以代替主键分块 —— 一并拒掉会挡住一批本可以做的表。
	t.Run("只有唯一非空索引的表可以做", func(t *testing.T) {
		name := makeTable(t, db,
			"CREATE TABLE `%s` (id BIGINT NOT NULL, memo VARCHAR(64), UNIQUE KEY uk_id (id))")
		f, err := Gather(ctx, db, "osc_test", name)
		if err != nil {
			t.Fatalf("采集失败: %v", err)
		}
		if f.HasPK {
			t.Error("这张表没有主键")
		}
		if len(f.UniqueNotNull) == 0 {
			t.Fatal("uk_id 是唯一且非空的,应当被认作可分块的键")
		}
		if bs, _ := Preflight(f, ActionAddIndex); len(bs) != 0 {
			t.Errorf("有唯一非空键就该放行,却被拒: %+v", bs)
		}
	})

	// 可空列上的唯一索引允许多行 NULL,分不了块 —— 认错这一条会让拷贝漏行。
	t.Run("唯一但可空的索引不算可分块的键", func(t *testing.T) {
		name := makeTable(t, db,
			"CREATE TABLE `%s` (id BIGINT NULL, memo VARCHAR(64), UNIQUE KEY uk_id (id))")
		f, err := Gather(ctx, db, "osc_test", name)
		if err != nil {
			t.Fatalf("采集失败: %v", err)
		}
		if len(f.UniqueNotNull) != 0 {
			t.Errorf("可空列上的唯一索引不该被当成可分块的键,实际: %v", f.UniqueNotNull)
		}
	})

	t.Run("带触发器的表被拒", func(t *testing.T) {
		name := makeTable(t, db, "CREATE TABLE `%s` (id BIGINT PRIMARY KEY, memo VARCHAR(64))")
		trg := name + "_trg"
		// 开着 binlog 时,建触发器要 SUPER/SYSTEM_VARIABLES_ADMIN(错误 1419)——
		// 那是「谁能创建」的限制,与被测的采集代码无关。造夹具用特权连接,采集仍走
		// 普通账号,免得为了方便测试而放宽被测路径的权限。
		root := openMySQLAs(t, rootDSN())
		if _, err := root.ExecContext(ctx, fmt.Sprintf(
			"CREATE TRIGGER `%s` BEFORE INSERT ON `%s` FOR EACH ROW SET NEW.memo = 'x'", trg, name)); err != nil {
			t.Fatalf("建触发器失败(需要特权连接,见 rootDSN): %v", err)
		}
		t.Cleanup(func() { _, _ = root.ExecContext(context.Background(), "DROP TRIGGER IF EXISTS `"+trg+"`") })

		f, err := Gather(ctx, db, "osc_test", name)
		if err != nil {
			t.Fatalf("采集失败: %v", err)
		}
		if f.Triggers != 1 {
			t.Errorf("Triggers = %d,想要 1", f.Triggers)
		}
		if !codes(firstOf(Preflight(f, ActionAddIndex)))["triggers"] {
			t.Error("表上有触发器,本该被拒")
		}
	})

	// 外键两个方向都要看得见:指向影子表的约束会跟着 rename 走,指向原表的会在
	// cut-over 后指向被弃置的旧表。只认一个方向等于漏掉一半。
	t.Run("外键两个方向都认得出", func(t *testing.T) {
		parent := makeTable(t, db, "CREATE TABLE `%s` (id BIGINT PRIMARY KEY)")
		child := "t_fk_child"
		_, _ = db.ExecContext(ctx, "DROP TABLE IF EXISTS `"+child+"`")
		if _, err := db.ExecContext(ctx, fmt.Sprintf(
			"CREATE TABLE `%s` (id BIGINT PRIMARY KEY, pid BIGINT, FOREIGN KEY (pid) REFERENCES `%s`(id))",
			child, parent)); err != nil {
			t.Fatalf("建子表失败: %v", err)
		}
		t.Cleanup(func() { _, _ = db.ExecContext(context.Background(), "DROP TABLE IF EXISTS `"+child+"`") })

		cf, err := Gather(ctx, db, "osc_test", child)
		if err != nil {
			t.Fatalf("采集子表失败: %v", err)
		}
		if cf.ForeignKeysOut == 0 {
			t.Error("子表指向父表,ForeignKeysOut 应当 > 0")
		}
		pf, err := Gather(ctx, db, "osc_test", parent)
		if err != nil {
			t.Fatalf("采集父表失败: %v", err)
		}
		if pf.ForeignKeysIn == 0 {
			t.Error("父表被子表指向,ForeignKeysIn 应当 > 0")
		}
		for _, f := range []Facts{cf, pf} {
			if !codes(firstOf(Preflight(f, ActionAddIndex)))["foreign_keys"] {
				t.Errorf("涉及外键的表本该被拒,实际放行: %s", f.Table)
			}
		}
	})

	t.Run("不存在的表被拒", func(t *testing.T) {
		f, err := Gather(ctx, db, "osc_test", "t_definitely_not_here")
		if err != nil {
			t.Fatalf("采集不存在的表不该报错,而该如实说它不存在: %v", err)
		}
		if f.TableExists {
			t.Error("TableExists = true,但这张表没建过")
		}
		if !codes(firstOf(Preflight(f, ActionAddIndex)))["no_table"] {
			t.Error("表不存在,本该被拒")
		}
	})
}

// Preflight 返回两个值,这里只要阻塞项。
func firstOf(bs []Blocker, _ []Warning) []Blocker { return bs }

func mustExec(t *testing.T, db interface {
	ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error)
}, q string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), q); err != nil {
		t.Fatalf("执行失败 %q: %v", q, err)
	}
}
