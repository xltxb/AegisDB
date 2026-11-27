package osc

import (
	"context"
	"strings"
	"testing"
	"time"
)

// 迁移暂停或被限流时重放停下,不再从流里取事件。整个实例的写入(不只是本表)照样涌进
// binlog:解码库的缓冲一满就不再读连接,库端写不出去,net_write_timeout 一到就断开
// —— 恢复后读到的是 "io.ReadFull ... EOF / connection was bad",迁移失败。
//
// 停着不读的这段时间里,流必须仍然活着,恢复后能读到所有本表的改动。
func TestRealBinlog_StreamSurvivesAStalledReader(t *testing.T) {
	db, schema := mysqlDB(t)
	mustExec(t, db, "CREATE TABLE orders (id INT PRIMARY KEY, v INT)")
	mustExec(t, db, "CREATE TABLE noise (id INT AUTO_INCREMENT PRIMARY KEY, pad VARCHAR(200))")

	// 只对之后新开的会话生效:binlog 连接要在改完之后再开
	var oldWT int
	if err := db.QueryRow("SELECT @@GLOBAL.net_write_timeout").Scan(&oldWT); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, "SET GLOBAL net_write_timeout = 2")
	t.Cleanup(func() { db.Exec("SET GLOBAL net_write_timeout = ?", oldWT) })

	ctx := context.Background()
	b := realBinlog(t, db, schema, "orders")
	from, err := b.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	s, err := b.Stream(ctx, from)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	mustExec(t, db, "INSERT INTO orders VALUES (1, 1)")
	// 远超解码库缓冲(10240 个事件)的别的表的写入;每行一个事务,一个事务约 4 个事件
	pad := strings.Repeat("x", 200)
	for i := 0; i < 400; i++ {
		var sb strings.Builder
		sb.WriteString("INSERT INTO noise (pad) VALUES ")
		for j := 0; j < 50; j++ {
			if j > 0 {
				sb.WriteByte(',')
			}
			sb.WriteString("('" + pad + "')")
		}
		mustExec(t, db, sb.String()) // 400 个事务 × 50 行
	}
	// 一次多行的事务只出一个行事件;要把事件数堆上去,还得有大量小事务
	tx := 0
	for tx < 12000 {
		mustExec(t, db, "INSERT INTO noise (pad) VALUES ('y')")
		tx++
	}
	mustExec(t, db, "INSERT INTO orders VALUES (2, 2)")
	end, err := b.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}

	time.Sleep(6 * time.Second) // 停着不读,远超 net_write_timeout

	seen := map[int32]bool{}
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		nctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		ev, err := s.Next(nctx)
		cancel()
		if err != nil {
			if err == context.DeadlineExceeded {
				continue
			}
			t.Fatalf("停读之后流断了: %v", err)
		}
		for _, r := range ev.Rows {
			seen[r.After[0].(int32)] = true
		}
		if ev.Pos.File == end.File && ev.Pos.Pos >= end.Pos {
			break
		}
	}
	if !seen[1] || !seen[2] {
		t.Fatalf("本表的改动没有全部读到: %v", seen)
	}
}

// 只推进位点的事件连续出现时合并成一个;本表的行事件按序保留;积压超限时,队列里已有的
// 事件照样交出,之后才是错误。
func TestMySQLStream_QueueCoalescesAndCaps(t *testing.T) {
	s := &mysqlStream{maxRows: 3, ready: make(chan struct{}, 1)}
	at := func(p uint64) Position { return Position{File: "bin.000001", Pos: p} }
	row := RowChange{Kind: RowInsert, After: []any{int32(1)}}
	for p := uint64(1); p <= 1000; p++ {
		s.push(Event{Pos: at(p), Schema: "other", Table: "noise"})
	}
	s.push(Event{Pos: at(1001), Schema: "s", Table: "t", Rows: []RowChange{row, row}})
	s.push(Event{Pos: at(1002)})
	s.push(Event{Pos: at(1003)})
	if ok := s.push(Event{Pos: at(1004), Schema: "s", Table: "t", Rows: []RowChange{row, row}}); ok {
		t.Fatal("积压 4 行超过上限 3,应当停下")
	}

	ctx := context.Background()
	want := []struct {
		pos  uint64
		rows int
	}{{1000, 0}, {1001, 2}, {1003, 0}, {1004, 2}}
	for _, w := range want {
		ev, err := s.Next(ctx)
		if err != nil {
			t.Fatalf("位点 %d 之前就报错: %v", w.pos, err)
		}
		if ev.Pos != at(w.pos) || len(ev.Rows) != w.rows {
			t.Fatalf("想要位点 %d、%d 行,拿到 %s、%d 行", w.pos, w.rows, ev.Pos, len(ev.Rows))
		}
	}
	if _, err := s.Next(ctx); err == nil || !strings.Contains(err.Error(), "积压") {
		t.Fatalf("队列取空后应报积压超限,拿到 %v", err)
	}
}
