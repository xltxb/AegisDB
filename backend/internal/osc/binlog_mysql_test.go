package osc

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

// 阶段三:真实 binlog。写入方只执行 SQL,迁移从 MySQL 的 binlog 里读到这些写入 ——
// 与 mysql_test.go 里假解码器的用例对照,拷贝 / 重放 / cut-over 的逻辑是同一套。

func realBinlog(t *testing.T, _ *sql.DB, schema, table string) *MySQLBinlog {
	t.Helper()
	cfg, err := mysql.ParseDSN(os.Getenv("AEGISDB_OSC_TEST_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	host, port, _ := strings.Cut(cfg.Addr, ":")
	p, _ := strconv.Atoi(port)
	b := NewMySQLBinlog(MySQLSource{Host: host, Port: uint16(p), User: cfg.User, Password: cfg.Passwd}, schema, table)
	t.Cleanup(func() { b.Close() })
	return b
}

// 和假解码器版本同一个场景:边拷贝边插入、改值、改键、删除,另一张表也在写。
func TestRealBinlog_ConcurrentWritesConvergeThroughCutOver(t *testing.T) {
	db, schema := mysqlDB(t)
	ctx := context.Background()
	model := seedOrders(t, db, 20000)
	mustExec(t, db, "CREATE TABLE other (id INT PRIMARY KEY, v INT)")

	var modelMu sync.Mutex
	stop := make(chan struct{})
	writerDone := make(chan error, 1)
	var ops int
	go func() {
		writerDone <- func() error {
			nextID := int64(1)
			for i := 0; ; i++ {
				select {
				case <-stop:
					return nil
				default:
				}
				modelMu.Lock()
				var pick orderRow
				for _, r := range model {
					pick = r
					break
				}
				modelMu.Unlock()
				tx, err := db.BeginTx(ctx, nil)
				if err != nil {
					return err
				}
				var apply func()
				switch i % 4 {
				case 0:
					nextID += 7
					r := genOrder(nextID*10+3, i+100000)
					err = insertOrder(ctx, tx, r)
					apply = func() { model[r.id] = r }
				case 1:
					r := pick
					r.amount, r.name = fmt.Sprintf("%d.99", i), fmt.Sprintf("改-%d-é", i)
					_, err = tx.ExecContext(ctx, "UPDATE orders SET amount = ?, name = ? WHERE id = ?", r.amount, r.name, r.id)
					apply = func() { model[r.id] = r }
				case 2:
					r := pick
					r.id = 1_000_000_000 + int64(i)
					_, err = tx.ExecContext(ctx, "UPDATE orders SET id = ? WHERE id = ?", r.id, pick.id)
					apply = func() { delete(model, pick.id); model[r.id] = r }
				case 3:
					_, err = tx.ExecContext(ctx, "DELETE FROM orders WHERE id = ?", pick.id)
					apply = func() { delete(model, pick.id) }
				}
				if err == nil { // 别的表也在写:位点前进,不该被重放
					_, err = tx.ExecContext(ctx, "REPLACE INTO other VALUES (?, ?)", i%50, i)
				}
				if err != nil {
					tx.Rollback()
					return err
				}
				if err := tx.Commit(); err != nil {
					return err
				}
				modelMu.Lock()
				apply()
				ops++
				modelMu.Unlock()
			}
		}()
	}()

	m := New(db, realBinlog(t, db, schema, "orders"), schema, "orders", addCreatedIdx,
		Config{ChunkSize: 150, CutOverRetryWait: 100 * time.Millisecond})
	err := m.Run(ctx)
	close(stop)
	if werr := <-writerDone; werr != nil {
		t.Fatalf("写入方出错: %v", werr)
	}
	if err != nil {
		t.Fatalf("迁移失败: %v\n状态: %+v", err, m.Status())
	}
	st := m.Status()
	t.Logf("写入 %d 次,拷贝 %d 行 / %d 块,重放 %d 行,cut-over 尝试 %d 次", ops, st.RowsCopied, st.Chunks, st.RowsReplayed, st.CutOverAttempts)
	if st.RowsReplayed == 0 {
		t.Fatal("没有发生任何重放 —— 这个用例没有测到交错")
	}
	diffOrders(t, model, readOrders(t, db, "orders"))
	if got := strings.Join(indexNames(t, db, "orders"), ","); got != "PRIMARY,idx_created,idx_name" {
		t.Errorf("新表的索引不对: %s", got)
	}
}

// 各种列类型经过真实 binlog 解码、重放之后,和不迁移的对照表逐字节一致。
const typesDDL = `CREATE TABLE %s (
	id     BIGINT UNSIGNED NOT NULL,
	ti     TINYINT UNSIGNED NULL,
	mi     MEDIUMINT UNSIGNED NULL,
	si     SMALLINT NULL,
	bu     BIGINT UNSIGNED NULL,
	dec_   DECIMAL(30,10) NULL,
	fl     FLOAT NULL,
	db     DOUBLE NULL,
	dt     DATETIME(6) NULL,
	ts     TIMESTAMP(3) NULL,
	d      DATE NULL,
	tm     TIME(2) NULL,
	yr     YEAR NULL,
	lat    VARCHAR(40) CHARACTER SET latin1 NULL,
	emo    VARCHAR(40) CHARACTER SET utf8mb4 NULL,
	ch     CHAR(8) CHARACTER SET utf8mb4 NULL,
	bin    VARBINARY(16) NULL,
	bl     BLOB NULL,
	tx     TEXT CHARACTER SET utf8mb4 NULL,
	en     ENUM('红','green','blue') NULL,
	st     SET('a','b','c','d') NULL,
	bt     BIT(10) NULL,
	js     JSON NULL,
	gen    VARCHAR(50) AS (CONCAT(IFNULL(emo,''), '#', id)) VIRTUAL,
	PRIMARY KEY (id),
	KEY idx_ti (ti)
) ENGINE=InnoDB`

func typesRow(r *rand.Rand, id uint64) []any {
	pick := func(xs ...any) any { return xs[r.Intn(len(xs))] }
	return []any{
		id,
		pick(nil, 0, 255, 128),
		pick(nil, 0, 16777215, 8388608),
		pick(nil, -32768, 32767, 0),
		pick(nil, uint64(18446744073709551615), uint64(9223372036854775808), uint64(1)),
		pick(nil, "12345678901234567890.0123456789", "-0.0000000001", "0.5000000000"),
		pick(nil, 1.5, -3.25, 0),
		pick(nil, 3.141592653589793, -1e300, 0),
		pick(nil, "2026-10-01 12:34:56.123456", "1000-01-01 00:00:00.000001", "9999-12-31 23:59:59.999999"),
		pick(nil, "2026-10-01 12:34:56.789", "1970-01-02 00:00:00.001", "2038-01-19 03:14:07.999"),
		pick(nil, "2026-10-01", "1000-01-01", "9999-12-31"),
		pick(nil, "12:34:56.78", "-838:59:59.00", "838:59:59.00"),
		pick(nil, 2026, 1901, 2155),
		pick(nil, "café crème", "plain", ""), // 存进 latin1 列是 0xE9 这类单字节
		pick(nil, "表情😀🚀", "中文", ""),
		pick(nil, "ab", "中", ""),
		pick(nil, []byte{0, 1, 2, 255}, []byte{}, []byte("x")),
		pick(nil, []byte{0xde, 0xad, 0xbe, 0xef}, bytes.Repeat([]byte{7}, 300)),
		pick(nil, strings.Repeat("长文本", 100), "t"),
		pick(nil, "红", "green", "blue"),
		pick(nil, "a,c", "", "b,c,d"),
		pick(nil, 0, 1023, 512),
		pick(nil, `{"a": [1, 2.5, "中"], "b": null}`, `[]`, `"str"`, `123`),
	}
}

const typesCols = "id, ti, mi, si, bu, dec_, fl, db, dt, ts, d, tm, yr, lat, emo, ch, bin, bl, tx, en, st, bt, js"

func insertTypes(ctx context.Context, ex execer, table string, row []any) error {
	ph := strings.TrimSuffix(strings.Repeat("?, ", len(row)), ", ")
	_, err := ex.ExecContext(ctx, fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", table, typesCols, ph), row...)
	return err
}

// rawTable reads every column (including the virtual one) as raw bytes, by id.
func rawTable(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.Query(fmt.Sprintf("SELECT *, HEX(bt) FROM %s ORDER BY id", table))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	var out []string
	for rows.Next() {
		vals := make([]sql.RawBytes, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		parts := make([]string, len(vals))
		for i, v := range vals {
			if v == nil {
				parts[i] = "NULL"
			} else {
				parts[i] = fmt.Sprintf("%x", []byte(v))
			}
		}
		out = append(out, strings.Join(parts, "|"))
	}
	return out
}

func TestRealBinlog_EveryColumnTypeSurvivesReplay(t *testing.T) {
	db, schema := mysqlDB(t)
	ctx := context.Background()
	// 池子故意开小:cut-over 持锁读位点时,被锁挡住的写入占满了池子 —— 读位点若也从这个池子
	// 取连接就会互相等死(见 MySQLBinlog.Head)
	db.SetMaxOpenConns(4)
	// 会话时区不是 UTC:TIMESTAMP 的文本若按本地时区解出来,这里就会对不上
	mustExec(t, db, "SET GLOBAL time_zone = '+08:00'")
	t.Cleanup(func() { db.Exec("SET GLOBAL time_zone = 'SYSTEM'") })
	mustExec(t, db, fmt.Sprintf(typesDDL, "types"))
	mustExec(t, db, fmt.Sprintf(typesDDL, "mirror")) // 对照:同样的写入,不迁移

	r := rand.New(rand.NewSource(42))
	for id := uint64(1); id <= 3000; id++ {
		row := typesRow(r, id*10)
		tx, _ := db.BeginTx(ctx, nil)
		if err := insertTypes(ctx, tx, "types", row); err != nil {
			t.Fatal(err)
		}
		if err := insertTypes(ctx, tx, "mirror", row); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}

	stop := make(chan struct{})
	writerDone := make(chan error, 1)
	go func() {
		writerDone <- func() error {
			wr := rand.New(rand.NewSource(7))
			for i := 0; ; i++ {
				select {
				case <-stop:
					return nil
				default:
				}
				tx, err := db.BeginTx(ctx, nil)
				if err != nil {
					return err
				}
				id := uint64(wr.Intn(3000)+1) * 10
				for _, tb := range []string{"types", "mirror"} {
					switch i % 3 {
					case 0: // 新行:落在已有键的空隙里
						err = insertTypes(ctx, tx, tb, typesRow(rand.New(rand.NewSource(int64(i))), uint64(i)*10+5))
					case 1: // 整行改成另一组值
						row := typesRow(rand.New(rand.NewSource(int64(i))), id)
						set := []string{}
						for _, c := range strings.Split(typesCols, ", ")[1:] {
							set = append(set, c+" = ?")
						}
						_, err = tx.ExecContext(ctx, fmt.Sprintf("UPDATE %s SET %s WHERE id = ?", tb, strings.Join(set, ", ")), append(row[1:], id)...)
					case 2:
						_, err = tx.ExecContext(ctx, fmt.Sprintf("DELETE FROM %s WHERE id = ?", tb), id)
					}
					if err != nil {
						break
					}
				}
				if err != nil {
					tx.Rollback()
					return err
				}
				if err := tx.Commit(); err != nil {
					return err
				}
			}
		}()
	}()

	m := New(db, realBinlog(t, db, schema, "types"), schema, "types",
		Change{Action: ActionAddIndex, Index: "idx_dt", Columns: []IndexColumn{{Name: "dt"}}},
		Config{ChunkSize: 100, CutOverRetryWait: 100 * time.Millisecond})
	err := m.Run(ctx)
	close(stop)
	if werr := <-writerDone; werr != nil {
		t.Fatalf("写入方出错: %v", werr)
	}
	if err != nil {
		t.Fatalf("迁移失败: %v\n状态: %+v", err, m.Status())
	}
	if m.Status().RowsReplayed == 0 {
		t.Fatal("没有发生任何重放 —— 这个用例没有测到交错")
	}
	got, want := rawTable(t, db, "types"), rawTable(t, db, "mirror")
	if len(got) != len(want) {
		t.Fatalf("行数不同: 迁移后 %d,对照 %d", len(got), len(want))
	}
	bad := 0
	for i := range got {
		if got[i] != want[i] {
			bad++
			if bad <= 3 {
				t.Errorf("第 %d 行不同\n迁移后: %s\n对照:   %s", i, got[i], want[i])
			}
		}
	}
	if bad > 0 {
		t.Fatalf("共 %d 行不同", bad)
	}
	t.Logf("%d 行逐字节一致,重放 %d 行", len(got), m.Status().RowsReplayed)
}

// 迁移期间表被 TRUNCATE(改了数据却没有行镜像):从真实 binlog 认出来,中止并清理。
func TestRealBinlog_TruncateDuringMigrationAborts(t *testing.T) {
	db, schema := mysqlDB(t)
	ctx := context.Background()
	seedOrders(t, db, 3000)
	m := New(db, realBinlog(t, db, schema, "orders"), schema, "orders", addCreatedIdx,
		Config{ChunkSize: 50, ThrottleInterval: 20 * time.Millisecond})
	m.Pause()
	done := make(chan error, 1)
	go func() { done <- m.Run(ctx) }()
	waitPhase(t, m, PhaseCopying)
	mustExec(t, db, "TRUNCATE TABLE orders")
	m.Resume()
	err := <-done
	if err == nil || !strings.Contains(err.Error(), "结构被改动") {
		t.Fatalf("应当因 TRUNCATE 中止,实际 %v", err)
	}
	if tableExists(t, db, "_orders_gho") {
		t.Error("中止后应删掉影子表")
	}
}

func TestTouchesTable(t *testing.T) {
	cases := []struct {
		q, def string
		want   bool
	}{
		{"ALTER TABLE orders ADD COLUMN x INT", "app", true},
		{"alter table `app`.`orders` drop index i", "other", true},
		{"TRUNCATE orders", "app", true},
		{"RENAME TABLE orders TO orders_old", "app", true},
		{"UPDATE orders SET a = 1", "app", true},  // 语句格式的 DML:没有行镜像
		{"DROP TABLE other.orders", "app", false}, // 别的库的同名表
		{"ALTER TABLE orders_archive ADD x INT", "app", false},
		{"ALTER TABLE my_orders ADD x INT", "app", false},
		{"ALTER TABLE orders ADD x INT", "other", false}, // 默认库不是本库、也没限定
		{"BEGIN", "app", false},
		{"COMMIT", "app", false},
		{"CREATE TABLE t2 (a INT) /* orders */", "app", false},
		{"/*!80000 ALTER TABLE orders ADD x INT */", "app", true},
		{"CREATE INDEX i ON `orders` (a)", "app", true},
	}
	for _, c := range cases {
		if got := touchesTable(c.q, c.def, "app", "orders"); got != c.want {
			t.Errorf("%q (默认库 %s): 应为 %v", c.q, c.def, c.want)
		}
	}
}

// 多个写入方全速并发写单行事务,持续整个迁移:重放必须追得上。
//
// 逐事件提交时,重放每行一次提交、一次刷盘,而并发写入方的提交是并行刷的 —— 重放
// 的吞吐只有写入的几分之一,迁移会永远停在"追平中",直到 cut-over 重试耗尽而失败
// (2026-10 端到端验收:120 万行的表、每秒约 100 行写入,重放每秒只有三四十行)。
func TestRealBinlog_ReplayKeepsUpWithConcurrentWriters(t *testing.T) {
	db, schema := mysqlDB(t)
	db.SetMaxOpenConns(16)
	ctx := context.Background()
	model := seedOrders(t, db, 20000)

	const writers = 6
	var modelMu sync.Mutex
	stop := make(chan struct{})
	errs := make(chan error, writers)
	var ops int64
	for w := 0; w < writers; w++ {
		go func(w int) {
			errs <- func() error {
				for i := 0; ; i++ {
					select {
					case <-stop:
						return nil
					default:
					}
					r := genOrder(int64(2_000_000_000+w*10_000_000+i), i)
					if err := insertOrder(ctx, db, r); err != nil { // 自动提交:每行一个事务
						return err
					}
					r.amount = fmt.Sprintf("%d.50", i%1000)
					if _, err := db.ExecContext(ctx, "UPDATE orders SET amount = ? WHERE id = ?", r.amount, r.id); err != nil {
						return err
					}
					modelMu.Lock()
					model[r.id] = r
					ops += 2
					modelMu.Unlock()
				}
			}()
		}(w)
	}

	m := New(db, realBinlog(t, db, schema, "orders"), schema, "orders", addCreatedIdx,
		// 重试次数与生产默认(10)一致:起点锁表也用这组参数(见 Migration.startPoint),持续并发写入下
		// 3 次常常拿不到读锁,测试约三次失败一次(2026-10)。
		Config{ChunkSize: 500, CatchUpTimeout: 10 * time.Second, CutOverRetries: 10, CutOverRetryWait: 100 * time.Millisecond})
	start := time.Now()
	err := m.Run(ctx)
	close(stop)
	for w := 0; w < writers; w++ {
		if werr := <-errs; werr != nil {
			t.Fatalf("写入方出错: %v", werr)
		}
	}
	st := m.Status()
	t.Logf("用时 %s,写入 %d 次,重放 %d 行,cut-over 尝试 %d 次", time.Since(start).Round(time.Millisecond), ops, st.RowsReplayed, st.CutOverAttempts)
	if err != nil {
		t.Fatalf("并发写入下迁移应能追平并完成: %v\n状态: %+v", err, st)
	}
	diffOrders(t, model, readOrders(t, db, "orders"))
}

// 断点续跑:跑到一半取检查点、再多跑一会儿后中断(检查点之后的块与事件因此会被处理两次),
// 另起一个迁移从检查点续跑,期间写入不停。终态必须与模型逐行一致。
func TestRealBinlog_ResumeFromCheckpointConverges(t *testing.T) {
	db, schema := mysqlDB(t)
	db.SetMaxOpenConns(16)
	ctx := context.Background()
	model := seedOrders(t, db, 20000)

	const writers = 3
	var modelMu sync.Mutex
	stop := make(chan struct{})
	errs := make(chan error, writers)
	for w := 0; w < writers; w++ {
		go func(w int) {
			errs <- func() error {
				r := rand.New(rand.NewSource(int64(w)))
				for i := 0; ; i++ {
					select {
					case <-stop:
						return nil
					default:
					}
					// 挑行、写库、记进 model 在同一把锁里做完。从前挑完就放锁:两个写入方挑中同一行,
					// 库里是「A 改 → B 删」(行没了),model 里却可能记成「B 删 → A 改」(行又回来了),
					// 最后比对报「缺行」—— 是测试自己的 model 记乱了,不是在线变更丢了行。负载越高越容易
					// 撞上,全量并行时约几十次一次(2026-10)。写入方之间串行了,但它们和拷贝、binlog 重放
					// 照样并发,这条测要验证的东西没变。
					modelMu.Lock()
					var pick orderRow
					for _, x := range model {
						pick = x
						break
					}
					var apply func()
					var err error
					switch r.Intn(3) {
					case 0:
						o := genOrder(int64(3_000_000_000+w*10_000_000+i), i)
						err = insertOrder(ctx, db, o)
						apply = func() { model[o.id] = o }
					case 1:
						o := pick
						o.amount = fmt.Sprintf("%d.25", i%1000)
						_, err = db.ExecContext(ctx, "UPDATE orders SET amount = ? WHERE id = ?", o.amount, o.id)
						apply = func() { model[o.id] = o }
					default:
						_, err = db.ExecContext(ctx, "DELETE FROM orders WHERE id = ?", pick.id)
						apply = func() { delete(model, pick.id) }
					}
					if err != nil {
						modelMu.Unlock()
						return err
					}
					apply()
					modelMu.Unlock()
					time.Sleep(2 * time.Millisecond)
				}
			}()
		}(w)
	}
	stopWriters := func() {
		close(stop)
		for w := 0; w < writers; w++ {
			if err := <-errs; err != nil {
				t.Fatalf("写入方出错: %v", err)
			}
		}
	}

	cfg := Config{ChunkSize: 100, CutOverRetryWait: 100 * time.Millisecond}
	m1 := New(db, realBinlog(t, db, schema, "orders"), schema, "orders", addCreatedIdx, cfg)
	done1 := make(chan error, 1)
	go func() { done1 <- m1.Run(ctx) }()
	deadline := time.Now().Add(60 * time.Second)
	for m1.Status().RowsCopied < 6000 {
		if time.Now().After(deadline) {
			t.Fatalf("拷贝太慢,没等到中途: %+v", m1.Status())
		}
		time.Sleep(20 * time.Millisecond)
	}
	ck := m1.Checkpoint()
	if ck == nil || len(ck.Lower) == 0 || ck.CopyDone {
		t.Fatalf("拷贝中途应有检查点: %+v", ck)
	}
	time.Sleep(300 * time.Millisecond) // 检查点之后再走一段:续跑时这一段会被再处理一次
	m1.Interrupt()
	if err := <-done1; !errors.Is(err, ErrInterrupted) {
		t.Fatalf("中断应返回 ErrInterrupted: %v", err)
	}
	var n int
	db.QueryRow("SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? AND TABLE_NAME = '_orders_gho'", schema).Scan(&n)
	if n != 1 {
		t.Fatal("中断后影子表应保留")
	}
	time.Sleep(300 * time.Millisecond) // "网关不在"的这段时间写入照常

	cfg.Resume = ck
	m2 := New(db, realBinlog(t, db, schema, "orders"), schema, "orders", addCreatedIdx, cfg)
	err := m2.Run(ctx)
	stopWriters()
	if err != nil {
		t.Fatalf("续跑失败: %v\n状态: %+v", err, m2.Status())
	}
	st := m2.Status()
	if st.RowsCopied < ck.RowsCopied {
		t.Errorf("进度应接着检查点计: 检查点 %d,续跑后 %d", ck.RowsCopied, st.RowsCopied)
	}
	t.Logf("检查点 拷贝 %d 行 / 位点 %s;续跑后 拷贝 %d 行、重放 %d 行", ck.RowsCopied, ck.Applied, st.RowsCopied, st.RowsReplayed)
	diffOrders(t, model, readOrders(t, db, "orders"))
}

// 续跑的前提不在了:binlog 被清理、影子表被删 —— 都要说清楚地拒绝,不能从头悄悄重来。
func TestRealBinlog_ResumeRefusesWithoutItsPrerequisites(t *testing.T) {
	db, schema := mysqlDB(t)
	ctx := context.Background()
	seedOrders(t, db, 500)
	mustExec(t, db, "CREATE TABLE _orders_gho LIKE orders")
	mustExec(t, db, "ALTER TABLE _orders_gho ADD INDEX idx_created (created, qty)")
	k := int64(100)

	gone := &Checkpoint{Lower: []KeyPart{{I: &k}}, Applied: Position{File: "binlog.999999", Pos: 4}}
	err := New(db, realBinlog(t, db, schema, "orders"), schema, "orders", addCreatedIdx, Config{Resume: gone}).Run(ctx)
	if !errors.Is(err, ErrBinlogPurged) {
		t.Errorf("binlog 已清理应报 ErrBinlogPurged: %v", err)
	}

	mustExec(t, db, "DROP TABLE IF EXISTS _orders_gho")
	head, _ := binlogHead(ctx, db)
	ok := &Checkpoint{Lower: []KeyPart{{I: &k}}, Applied: head}
	err = New(db, realBinlog(t, db, schema, "orders"), schema, "orders", addCreatedIdx, Config{Resume: ok}).Run(ctx)
	if err == nil || !strings.Contains(err.Error(), "影子表") {
		t.Errorf("影子表不在应拒绝续跑: %v", err)
	}
}

// 起点与拷贝范围要在"这张表上没有在途写事务"的那一刻定(startPoint)。开了 binlog 的提交分两步,
// binlog 已写、InnoDB 还没提交的事务若插了一行新的最大键,先读起点再读最大键会两头都漏掉它
// (2026-10 压测下实测丢行)。这里用一个故意不提交的写事务把"在途"拉长:迁移必须等它结束才
// 定起点,而那一行最终要出现在结果里。
func TestRealBinlog_StartWaitsForInFlightWrites(t *testing.T) {
	db, schema := mysqlDB(t)
	ctx := context.Background()
	model := seedOrders(t, db, 500)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	late := genOrder(9_000_000, 1)
	if err := insertOrder(ctx, tx, late); err != nil {
		t.Fatal(err)
	}

	m := New(db, realBinlog(t, db, schema, "orders"), schema, "orders", addCreatedIdx,
		Config{ChunkSize: 100, CutOverLockTimeout: time.Second, CutOverRetries: 30, CutOverRetryWait: 50 * time.Millisecond})
	done := make(chan error, 1)
	go func() { done <- m.Run(ctx) }()
	time.Sleep(1500 * time.Millisecond)
	st := m.Status()
	if err := tx.Commit(); err != nil { // 先放掉事务,失败时也不把清理卡住
		t.Fatal(err)
	}
	if st.Phase != PhasePreparing {
		m.Abort()
		<-done
		t.Fatalf("有在途写事务时不该定下起点开始拷贝: %+v", st)
	}
	model[late.id] = late
	if err := <-done; err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	diffOrders(t, model, readOrders(t, db, "orders"))
}
