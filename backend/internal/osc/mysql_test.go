package osc

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

// 对真 MySQL 的测试。拷贝与重放的交错、cut-over 的锁竞争,拿 sqlite 或 mock 测出来
// 的绿色是假的(ADR 0011)。需要一个开了 ROW binlog 的 MySQL 8.0.13+,账号要有建库
// 与复制权限:
//
//	docker run -d --name vela-mysql-osc -e MYSQL_ROOT_PASSWORD=velaosc -p 127.0.0.1:13307:3306 mysql:8.0
//	VELA_OSC_TEST_DSN='root:velaosc@tcp(127.0.0.1:13307)/' go test ./internal/osc/
//
// 每个测试建一个自己的库,结束时删掉。
func mysqlDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	dsn := os.Getenv("VELA_OSC_TEST_DSN")
	if dsn == "" {
		t.Skip("未设置 VELA_OSC_TEST_DSN,跳过对真 MySQL 的测试")
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close() })
	schema := fmt.Sprintf("osc_%d", time.Now().UnixNano()%1_000_000_000)
	if _, err := admin.Exec("CREATE DATABASE " + schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Exec("DROP DATABASE " + schema) })

	cfg.DBName = schema
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db, schema
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

// fakeBinlog stands in for the binlog decoder: the test's writer commits a
// change and records the row images the decoder would have produced. 提交与
// 记录在同一把锁下完成,所以 Head 看到的永远是"已提交的都已记录"—— 这正是真 binlog
// 给的保证。
type fakeBinlog struct {
	mu     sync.Mutex
	events []Event
}

func (b *fakeBinlog) Head(context.Context) (Position, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return Position{File: "fake.000001", Pos: uint64(len(b.events))}, nil
}

func (b *fakeBinlog) Stream(_ context.Context, from Position) (Stream, error) {
	return &fakeStream{b: b, next: int(from.Pos)}, nil
}

// emitLocked appends an event; the caller holds b.mu.
func (b *fakeBinlog) emitLocked(ev Event) {
	ev.Pos = Position{File: "fake.000001", Pos: uint64(len(b.events) + 1)}
	b.events = append(b.events, ev)
}

type fakeStream struct {
	b    *fakeBinlog
	next int
}

func (s *fakeStream) Next(ctx context.Context) (Event, error) {
	for {
		s.b.mu.Lock()
		if s.next < len(s.b.events) {
			ev := s.b.events[s.next]
			s.next++
			s.b.mu.Unlock()
			return ev, nil
		}
		s.b.mu.Unlock()
		select {
		case <-ctx.Done():
			return Event{}, ctx.Err()
		case <-time.After(2 * time.Millisecond):
		}
	}
}

func (s *fakeStream) Close() error { return nil }

// ---- 一张覆盖各种编码陷阱的表 ----

const ordersDDL = `CREATE TABLE orders (
	id      BIGINT NOT NULL,
	name    VARCHAR(64) CHARACTER SET utf8mb4 NOT NULL,
	note    VARCHAR(32) CHARACTER SET latin1 NULL,
	qty     INT UNSIGNED NOT NULL,
	amount  DECIMAL(12,2) NOT NULL,
	created DATETIME(3) NOT NULL,
	doubled DECIMAL(13,2) AS (amount * 2) VIRTUAL,
	PRIMARY KEY (id),
	KEY idx_name (name)
) ENGINE=InnoDB`

type orderRow struct {
	id      int64
	name    string
	note    *string // 以 UTF-8 表示;写进 latin1 列
	qty     uint32
	amount  string
	created string
}

// image is the row as the binlog decoder hands it over: 有符号整数、DECIMAL 与
// 时间是文本、latin1 列是 latin1 的原始字节、生成列占位。
func (r orderRow) image() []any {
	var note any
	if r.note != nil {
		note = toLatin1(*r.note)
	}
	return []any{r.id, r.name, note, int32(r.qty), r.amount, r.created, nil}
}

func toLatin1(s string) string {
	b := make([]byte, 0, len(s))
	for _, r := range s {
		b = append(b, byte(r))
	}
	return string(b)
}

func genOrder(id int64, i int) orderRow {
	notes := []string{"café", "naïve", "plain", "Ünïcödé"}
	var note *string
	if i%5 != 0 {
		n := notes[i%len(notes)]
		note = &n
	}
	return orderRow{
		id:      id,
		name:    fmt.Sprintf("订单-%d-🚀", i),
		note:    note,
		qty:     uint32(3_900_000_000 + i), // 超过 int32 —— 解码库会给出负数
		amount:  fmt.Sprintf("%d.%02d", i%100000, i%100),
		created: fmt.Sprintf("2024-01-%02d 10:%02d:%02d.%03d", 1+i%28, i%60, (i/60)%60, i%1000),
	}
}

func insertOrder(ctx context.Context, ex execer, r orderRow) error {
	_, err := ex.ExecContext(ctx, "INSERT INTO orders (id, name, note, qty, amount, created) VALUES (?, ?, ?, ?, ?, ?)",
		r.id, r.name, r.note, r.qty, r.amount, r.created)
	return err
}

func seedOrders(t *testing.T, db *sql.DB, n int) map[int64]orderRow {
	t.Helper()
	mustExec(t, db, ordersDDL)
	model := map[int64]orderRow{}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= n; i++ {
		r := genOrder(int64(i*10), i) // 键之间留空隙,写入方可以插在中间
		if err := insertOrder(context.Background(), tx, r); err != nil {
			t.Fatal(err)
		}
		model[r.id] = r
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return model
}

// readOrders reads the table back in the model's representation.
func readOrders(t *testing.T, db *sql.DB, table string) map[int64]orderRow {
	t.Helper()
	rows, err := db.Query(fmt.Sprintf("SELECT id, name, note, qty, amount, DATE_FORMAT(created, '%%Y-%%m-%%d %%H:%%i:%%s.%%f'), doubled = amount * 2 FROM %s", table))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[int64]orderRow{}
	for rows.Next() {
		var r orderRow
		var note sql.NullString
		var genOK bool
		if err := rows.Scan(&r.id, &r.name, &note, &r.qty, &r.amount, &r.created, &genOK); err != nil {
			t.Fatal(err)
		}
		r.created = r.created[:len(r.created)-3] // %f 是 6 位,列是 3 位
		if note.Valid {
			r.note = &note.String
		}
		if !genOK {
			t.Errorf("id=%d 的生成列没有按新值重算", r.id)
		}
		out[r.id] = r
	}
	return out
}

func diffOrders(t *testing.T, want, got map[int64]orderRow) {
	t.Helper()
	bad := 0
	for id, w := range want {
		g, ok := got[id]
		switch {
		case !ok:
			t.Errorf("缺行 id=%d", id)
		case g.name != w.name || g.qty != w.qty || g.amount != w.amount || g.created != w.created ||
			(g.note == nil) != (w.note == nil) || (g.note != nil && *g.note != *w.note):
			t.Errorf("id=%d 不一致\n  期望 %+v\n  实际 %+v", id, fmtRow(w), fmtRow(g))
		default:
			continue
		}
		if bad++; bad > 10 {
			t.Fatal("差异太多,不再列举")
		}
	}
	for id := range got {
		if _, ok := want[id]; !ok {
			t.Errorf("多出一行 id=%d(原表里已经不存在)", id)
		}
	}
}

func fmtRow(r orderRow) string {
	n := "<nil>"
	if r.note != nil {
		n = *r.note
	}
	return fmt.Sprintf("{%d %q %q %d %s %s}", r.id, r.name, n, r.qty, r.amount, r.created)
}

func indexNames(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.Query("SELECT DISTINCT INDEX_NAME FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? ", table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		rows.Scan(&s)
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func tableExists(t *testing.T, db *sql.DB, table string) bool {
	t.Helper()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?", table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n > 0
}

var addCreatedIdx = Change{Action: ActionAddIndex, Index: "idx_created", Columns: []IndexColumn{{Name: "created"}, {Name: "qty"}}}

// ---- 测试 ----

// 采集要如实:对着一张真表,每个事实都要和它的真实结构对上。
func TestMySQL_GatherReadsTheRealTable(t *testing.T) {
	db, schema := mysqlDB(t)
	ctx := context.Background()
	mustExec(t, db, ordersDDL)
	mustExec(t, db, "CREATE TABLE parent (id INT PRIMARY KEY)")
	mustExec(t, db, "CREATE TABLE child (id INT PRIMARY KEY, pid INT, FOREIGN KEY (pid) REFERENCES parent(id))")
	mustExec(t, db, "CREATE TABLE audit (id INT PRIMARY KEY)")
	mustExec(t, db, "CREATE TRIGGER trg AFTER INSERT ON audit FOR EACH ROW SET @x = 1")
	mustExec(t, db, "CREATE TABLE nokey (a INT, b VARCHAR(10))")
	mustExec(t, db, "CREATE TABLE ukey (code VARCHAR(20) NOT NULL, v INT, UNIQUE KEY uk_code (code))")
	mustExec(t, db, "CREATE TABLE fkey (id DOUBLE PRIMARY KEY)")
	mustExec(t, db, "CREATE TABLE geo (id INT PRIMARY KEY, g GEOMETRY)")
	mustExec(t, db, "CREATE TABLE _orders_gho (id INT)")

	f, err := Gather(ctx, db, schema, "orders")
	if err != nil {
		t.Fatal(err)
	}
	if !f.LogBin || f.BinlogFormat != "ROW" || f.BinlogRowImg != "FULL" || !f.HasReplSlave || !f.HasReplClient {
		t.Errorf("实例参数与权限没读对: %+v", f)
	}
	if f.VersionMajor != 8 {
		t.Errorf("版本没读对: %q -> %d.%d.%d", f.Version, f.VersionMajor, f.VersionMinor, f.VersionPatch)
	}
	if !f.TableExists || f.TableEngine != "InnoDB" || !f.HasPK || f.ChunkKey != "PRIMARY" || f.GeneratedCols != 1 {
		t.Errorf("表结构没读对: %+v", f)
	}
	if strings.Join(f.Indexes, ",") != "PRIMARY,idx_name" || len(f.Columns) != 7 {
		t.Errorf("索引 / 列没读对: %v %v", f.Indexes, f.Columns)
	}
	if !f.LeftoverGhost || f.LeftoverDel {
		t.Errorf("残留表没读对: gho=%v del=%v", f.LeftoverGhost, f.LeftoverDel)
	}
	if bs, _ := Preflight(f, addCreatedIdx); !codes(bs)["leftover_ghost"] || len(bs) != 1 {
		t.Errorf("只该因残留影子表被拒,实际: %+v", bs)
	}

	check := func(table string, test func(Facts) bool, what string) {
		t.Helper()
		f, err := Gather(ctx, db, schema, table)
		if err != nil {
			t.Fatal(err)
		}
		if !test(f) {
			t.Errorf("%s: %s,实际 %+v", table, what, f)
		}
	}
	check("parent", func(f Facts) bool { return f.ForeignKeysIn == 1 && f.ForeignKeysOut == 0 }, "应有 1 条入向外键")
	check("child", func(f Facts) bool { return f.ForeignKeysOut == 1 }, "应有 1 条出向外键")
	check("audit", func(f Facts) bool { return f.Triggers == 1 }, "应有 1 个触发器")
	check("nokey", func(f Facts) bool { return !f.HasPK && len(f.UniqueNotNull) == 0 && f.ChunkKey == "" }, "不应有可用键")
	check("ukey", func(f Facts) bool { return !f.HasPK && f.ChunkKey == "uk_code" }, "唯一非空索引应被选作分块键")
	check("fkey", func(f Facts) bool { return f.HasPK && f.ChunkKey == "" }, "浮点主键不能用来分块")
	check("geo", func(f Facts) bool { return len(f.UnsupportedCols) == 1 }, "空间列应被标为不支持")
	check("missing", func(f Facts) bool { return !f.TableExists }, "表不存在")
}

func TestMySQL_BinlogHeadAdvancesWithWrites(t *testing.T) {
	db, _ := mysqlDB(t)
	ctx := context.Background()
	mustExec(t, db, "CREATE TABLE h (id INT PRIMARY KEY)")
	p1, err := binlogHead(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, "INSERT INTO h VALUES (1)")
	p2, err := binlogHead(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if p2.Compare(p1) <= 0 {
		t.Errorf("写入之后位点应当前进: %s -> %s", p1, p2)
	}
}

// 核心:拷贝与重放交错进行,最后表里的每一行都必须等于"所有已提交写入之后的样子"。
// 写入方覆盖插入、改值、改键、删除四种动作,且持续到 cut-over 之后。
func TestMySQL_ConcurrentWritesConvergeThroughCutOver(t *testing.T) {
	db, schema := mysqlDB(t)
	ctx := context.Background()
	model := seedOrders(t, db, 20000)
	bl := &fakeBinlog{}

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
				// 挑一行已有的:按遍历顺序随便取一个
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
				var rc RowChange
				var apply func()
				switch i % 4 {
				case 0: // 插在已有键的空隙里 —— 可能落在拷贝还没走到的范围
					nextID += 7
					r := genOrder(nextID*10+3, i+100000)
					err = insertOrder(ctx, tx, r)
					rc = RowChange{Kind: RowInsert, After: r.image()}
					apply = func() { model[r.id] = r }
				case 1: // 改值
					r := pick
					r.amount, r.name = fmt.Sprintf("%d.99", i), fmt.Sprintf("改-%d-é", i)
					_, err = tx.ExecContext(ctx, "UPDATE orders SET amount = ?, name = ? WHERE id = ?", r.amount, r.name, r.id)
					rc = RowChange{Kind: RowUpdate, Before: pick.image(), After: r.image()}
					apply = func() { model[r.id] = r }
				case 2: // 改键
					r := pick
					r.id = 1_000_000_000 + int64(i)
					_, err = tx.ExecContext(ctx, "UPDATE orders SET id = ? WHERE id = ?", r.id, pick.id)
					rc = RowChange{Kind: RowUpdate, Before: pick.image(), After: r.image()}
					apply = func() { delete(model, pick.id); model[r.id] = r }
				case 3: // 删
					_, err = tx.ExecContext(ctx, "DELETE FROM orders WHERE id = ?", pick.id)
					rc = RowChange{Kind: RowDelete, Before: pick.image()}
					apply = func() { delete(model, pick.id) }
				}
				if err != nil {
					tx.Rollback()
					return err
				}
				bl.mu.Lock()
				if err := tx.Commit(); err != nil {
					bl.mu.Unlock()
					return err
				}
				bl.emitLocked(Event{Schema: schema, Table: "orders", Rows: []RowChange{rc}})
				// 别的表的事件也会出现在 binlog 里:位点前进,但不该被重放
				bl.emitLocked(Event{Schema: schema, Table: "other"})
				bl.mu.Unlock()
				modelMu.Lock()
				apply()
				ops++
				modelMu.Unlock()
			}
		}()
	}()

	m := New(db, bl, schema, "orders", addCreatedIdx, Config{ChunkSize: 150, CutOverRetryWait: 100 * time.Millisecond})
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
	if st.Phase != PhaseDone {
		t.Errorf("阶段应为 done,实际 %s", st.Phase)
	}

	diffOrders(t, model, readOrders(t, db, "orders"))
	if got := strings.Join(indexNames(t, db, "orders"), ","); got != "PRIMARY,idx_created,idx_name" {
		t.Errorf("新表的索引不对: %s", got)
	}
	if !tableExists(t, db, "_orders_del") || tableExists(t, db, "_orders_gho") {
		t.Error("切换后应当留下 _orders_del,且不再有 _orders_gho")
	}
}

// 长事务挡住 cut-over 时:拿锁超时、放手、重试,事务结束后成功。期间原表一直是原表。
func TestMySQL_CutOverRetriesPastALongTransaction(t *testing.T) {
	db, schema := mysqlDB(t)
	ctx := context.Background()
	model := seedOrders(t, db, 500)

	holder, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	if _, err := holder.ExecContext(ctx, "START TRANSACTION"); err != nil {
		t.Fatal(err)
	}
	// 一次普通读就够了:它不加行锁(拷贝照常进行),但事务不结束,表上的元数据锁
	// 就一直在 —— 这正是线上挡住 cut-over 的那种长事务。
	if _, err := holder.ExecContext(ctx, "SELECT * FROM orders WHERE id = 10"); err != nil {
		t.Fatal(err)
	}

	m := New(db, &fakeBinlog{}, schema, "orders", addCreatedIdx,
		Config{ChunkSize: 100, CutOverLockTimeout: time.Second, CutOverRetryWait: 100 * time.Millisecond, CutOverRetries: 20})
	done := make(chan error, 1)
	go func() { done <- m.Run(ctx) }()

	deadline := time.Now().Add(20 * time.Second)
	for m.Status().CutOverAttempts < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("cut-over 应当因长事务反复重试,状态: %+v", m.Status())
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !tableExists(t, db, "_orders_gho") || tableExists(t, db, "_orders_del") {
		t.Error("cut-over 没拿到锁时,什么都不该被改名")
	}
	holder.ExecContext(ctx, "COMMIT")

	if err := <-done; err != nil {
		t.Fatalf("长事务结束后应当成功: %v", err)
	}
	diffOrders(t, model, readOrders(t, db, "orders"))
}

// 中止:影子表被删,原表不受影响。
func TestMySQL_AbortDropsTheShadowAndLeavesTheTableAlone(t *testing.T) {
	db, schema := mysqlDB(t)
	ctx := context.Background()
	model := seedOrders(t, db, 300)

	m := New(db, &fakeBinlog{}, schema, "orders", addCreatedIdx, Config{ChunkSize: 50, ThrottleInterval: 20 * time.Millisecond})
	m.Pause()
	done := make(chan error, 1)
	go func() { done <- m.Run(ctx) }()
	waitPhase(t, m, PhaseCopying)
	if !tableExists(t, db, "_orders_gho") {
		t.Fatal("拷贝阶段应当已建好影子表")
	}
	m.Abort()
	if err := <-done; err != ErrAborted {
		t.Fatalf("应当返回 ErrAborted,实际 %v", err)
	}
	if st := m.Status(); st.Phase != PhaseAborted {
		t.Errorf("阶段应为 aborted,实际 %s", st.Phase)
	}
	if tableExists(t, db, "_orders_gho") {
		t.Error("中止后影子表应当被删掉,否则下一次会因残留被拒")
	}
	if got := strings.Join(indexNames(t, db, "orders"), ","); got != "PRIMARY,idx_name" {
		t.Errorf("原表不该被动过: %s", got)
	}
	diffOrders(t, model, readOrders(t, db, "orders"))
}

// 暂停时一块都不拷,限流时也一样;恢复后走完。
func TestMySQL_PauseAndThrottleHoldTheCopy(t *testing.T) {
	db, schema := mysqlDB(t)
	ctx := context.Background()
	model := seedOrders(t, db, 1000)

	m := New(db, &fakeBinlog{}, schema, "orders", addCreatedIdx, Config{ChunkSize: 100, ThrottleInterval: 20 * time.Millisecond})
	var busy sync.Mutex
	loaded := true
	m.loadProbe = func(context.Context) (string, error) {
		busy.Lock()
		defer busy.Unlock()
		if loaded {
			return "测试:负载过高", nil
		}
		return "", nil
	}
	m.Pause()
	done := make(chan error, 1)
	go func() { done <- m.Run(ctx) }()

	waitPhase(t, m, PhaseCopying)
	time.Sleep(200 * time.Millisecond)
	if st := m.Status(); st.Chunks != 0 || !st.Paused {
		t.Fatalf("暂停期间不该拷贝: %+v", st)
	}

	m.Resume()
	deadline := time.Now().Add(5 * time.Second)
	for m.Status().Throttled == "" {
		if time.Now().After(deadline) {
			t.Fatal("恢复后应当进入限流")
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	if st := m.Status(); st.Chunks != 0 {
		t.Fatalf("限流期间不该拷贝: %+v", st)
	}

	busy.Lock()
	loaded = false
	busy.Unlock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if st := m.Status(); st.RowsCopied != 1000 || st.Throttled != "" {
		t.Errorf("应当拷完 1000 行且不再限流: %+v", st)
	}
	diffOrders(t, model, readOrders(t, db, "orders"))
}

// binlog 里出现本表的 DDL:按下标对列的前提没了,必须中止并清理。
func TestMySQL_ADDLOnTheTableAbortsTheMigration(t *testing.T) {
	db, schema := mysqlDB(t)
	ctx := context.Background()
	seedOrders(t, db, 300)
	bl := &fakeBinlog{}
	m := New(db, bl, schema, "orders", addCreatedIdx, Config{ChunkSize: 50, ThrottleInterval: 20 * time.Millisecond})
	m.Pause()
	done := make(chan error, 1)
	go func() { done <- m.Run(ctx) }()
	waitPhase(t, m, PhaseCopying)
	bl.mu.Lock()
	bl.emitLocked(Event{Schema: schema, Table: "orders", DDL: true})
	bl.mu.Unlock()
	m.Resume()

	err := <-done
	if err == nil || !strings.Contains(err.Error(), "结构被改动") {
		t.Fatalf("应当因 DDL 中止,实际 %v", err)
	}
	if m.Status().Phase != PhaseFailed || tableExists(t, db, "_orders_gho") {
		t.Errorf("应当失败并删掉影子表: %+v", m.Status())
	}
}

// Run 自己再做一次前置检查,不信任调用方跑过。
func TestMySQL_RunRefusesWhatPreflightRefuses(t *testing.T) {
	db, schema := mysqlDB(t)
	mustExec(t, db, ordersDDL)
	mustExec(t, db, "CREATE TRIGGER trg AFTER INSERT ON orders FOR EACH ROW SET @x = 1")
	uniq := addCreatedIdx
	uniq.Unique = true
	err := New(db, &fakeBinlog{}, schema, "orders", uniq, Config{}).Run(context.Background())
	be, ok := err.(*BlockedError)
	if !ok {
		t.Fatalf("应当被前置检查拒绝,实际 %v", err)
	}
	if got := codes(be.Blockers); !got["triggers"] || !got["unique_index"] {
		t.Errorf("触发器与唯一索引都该被报出来: %+v", be.Blockers)
	}
	if tableExists(t, db, "_orders_gho") {
		t.Error("被拒时不该建出影子表")
	}
}

// 不是整数主键的分块:latin1 + 大小写不敏感排序规则的复合唯一键,以及超过 2^63 的
// 无符号主键。块大小取质数,让边界落在各种位置。
func TestMySQL_CopyHandlesAwkwardKeys(t *testing.T) {
	db, schema := mysqlDB(t)
	ctx := context.Background()

	mustExec(t, db, `CREATE TABLE words (
		code VARCHAR(20) CHARACTER SET latin1 COLLATE latin1_general_ci NOT NULL,
		seq INT NOT NULL, v INT, UNIQUE KEY uk (code, seq)) ENGINE=InnoDB`)
	words := []string{"apple", "Äpfel", "banana", "Béta", "cafe", "café", "zebra", "Zürich", "ñu", "o"}
	for i, w := range words {
		for s := 0; s < 13; s++ {
			mustExec(t, db, "INSERT INTO words VALUES (?, ?, ?)", w, s, i*100+s)
		}
	}
	mustExec(t, db, "CREATE TABLE big (id BIGINT UNSIGNED PRIMARY KEY, v VARCHAR(10))")
	for i := 0; i < 40; i++ {
		mustExec(t, db, "INSERT INTO big VALUES (?, ?)", uint64(18446744073709551000)+uint64(i*7), fmt.Sprint(i))
		mustExec(t, db, "INSERT INTO big VALUES (?, ?)", uint64(i), fmt.Sprint(i))
	}

	for _, tc := range []struct {
		table string
		chg   Change
		n     int
	}{
		{"words", Change{Action: ActionAddIndex, Index: "idx_v", Columns: []IndexColumn{{Name: "v"}}}, len(words) * 13},
		{"big", Change{Action: ActionAddIndex, Index: "idx_v", Columns: []IndexColumn{{Name: "v", Length: 3}}}, 80},
	} {
		m := New(db, &fakeBinlog{}, schema, tc.table, tc.chg, Config{ChunkSize: 7})
		if err := m.Run(ctx); err != nil {
			t.Fatalf("%s: %v", tc.table, err)
		}
		var a, b int
		db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s", tc.table)).Scan(&a)
		var cs1, cs2 sql.NullInt64
		var name string
		db.QueryRow(fmt.Sprintf("CHECKSUM TABLE %s", tc.table)).Scan(&name, &cs1)
		db.QueryRow(fmt.Sprintf("CHECKSUM TABLE _%s_del", tc.table)).Scan(&name, &cs2)
		db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM _%s_del", tc.table)).Scan(&b)
		if a != tc.n || b != tc.n || cs1 != cs2 {
			t.Errorf("%s: 新表 %d 行、旧表 %d 行(应为 %d),校验和 %v / %v", tc.table, a, b, tc.n, cs1, cs2)
		}
	}
}

// 删索引走同一条路。
func TestMySQL_DropIndex(t *testing.T) {
	db, schema := mysqlDB(t)
	model := seedOrders(t, db, 200)
	m := New(db, &fakeBinlog{}, schema, "orders", Change{Action: ActionDropIndex, Index: "idx_name"}, Config{ChunkSize: 64})
	if err := m.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(indexNames(t, db, "orders"), ","); got != "PRIMARY" {
		t.Errorf("idx_name 应当被删掉: %s", got)
	}
	diffOrders(t, model, readOrders(t, db, "orders"))
}

func waitPhase(t *testing.T, m *Migration, p Phase) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for m.Status().Phase != p {
		if time.Now().After(deadline) {
			t.Fatalf("等不到阶段 %s,当前 %+v", p, m.Status())
		}
		time.Sleep(10 * time.Millisecond)
	}
}
