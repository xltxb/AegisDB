package osc

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 把五个阶段串起来跑一次真迁移。
//
// 前面每一段各自绿过,但它们从没在一起跑过 —— 而这套东西的风险恰恰在**接缝**上:
// 拷贝与 binlog 重放是同时在跑的,它们会交错,而收敛全靠 ADR 0011 那条不变式:
//
//	拷贝 INSERT IGNORE —— 永不覆盖
//	重放 REPLACE INTO  —— 永远覆盖
//
// 所以这条用例的全部价值在于**在拷贝进行中制造并发写入**。不制造交错的话,两边各跑
// 各的,那条不变式根本不会被触发,跑出来的绿是假的。
//
// 三种写各自要证伪一件事:
//   - UPDATE 已拷过的行   → 重放没盖上去,新表停在旧值
//   - UPDATE 还没拷到的行 → 拷贝把重放写的新值盖回旧值
//   - DELETE             → 删掉的行被拷贝写回来,复活
func TestMigrate_EndToEndWithConcurrentWrites(t *testing.T) {
	db := openMySQL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	name := makeTable(t, db, "CREATE TABLE `%s` (id BIGINT PRIMARY KEY, memo VARCHAR(64), n INT)")
	const seedRows = 2000
	for i := 1; i <= seedRows; i++ {
		mustExec(t, db, fmt.Sprintf("INSERT INTO `%s` (id, memo, n) VALUES (%d, 'seed-%d', %d)", name, i, i, i))
	}

	// ① 先记位点,再建影子表、再开始拷贝。顺序反了会漏事件:先拷后记的话,拷贝期间
	//    的写入既不在存量快照里,也不在订阅范围里。
	pos, err := CurrentPosition(ctx, db)
	if err != nil {
		t.Fatalf("读位点失败: %v", err)
	}

	sh, err := CreateShadow(ctx, db, "osc_test", name, "ADD INDEX idx_memo (memo)")
	if err != nil {
		t.Fatalf("建影子表失败: %v", err)
	}
	del := "_" + name + DelSuffix
	t.Cleanup(func() {
		for _, x := range []string{sh, del} {
			_, _ = db.ExecContext(context.Background(), "DROP TABLE IF EXISTS `"+x+"`")
		}
	})

	// ② 订阅。重放把事件写进影子表。
	replayCtx, stopReplay := context.WithCancel(ctx)
	defer stopReplay()
	var replayed atomic.Int64
	var repMu sync.Mutex
	repKeys := map[int64]bool{} // 主键 → 重放碰它时,拷贝游标是否还没走到这里
	var raceBefore atomic.Int64
	var copyCursor atomic.Int64
	replayErr := make(chan error, 1)
	go func() {
		replayErr <- Stream(replayCtx, StreamConfig{
			DSN: mysqlDSN(), ServerID: 4520,
			Schema: "osc_test", Table: name, Position: pos,
		}, func(ev RowEvent) error {
			ev.Table = sh // 重放的目的地是影子表
			q, args := replayStatement(ev)
			if q == "" {
				return nil
			}
			if _, err := db.ExecContext(context.Background(), q, args...); err != nil {
				return fmt.Errorf("重放 %q: %w", q, err)
			}
			replayed.Add(1)
			if len(ev.After) > 0 {
				if k, ok := toInt64(ev.After[0]); ok {
					repMu.Lock()
					repKeys[k] = true
					repMu.Unlock()
				}
			} else if len(ev.Before) > 0 {
				if k, ok := toInt64(ev.Before[0]); ok {
					repMu.Lock()
					repKeys[k] = true
					repMu.Unlock()
				}
			}
			return nil
		})
	}()
	time.Sleep(500 * time.Millisecond) // 让订阅先就位

	// ③ 拷贝与并发写入同时跑。写入覆盖三种形态,并且横跨整个主键范围 ——
	//    这样既有"已拷过的行"也有"还没拷到的行"。
	writesDone := make(chan struct{})
	copyDone := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(writesDone)
		rng := rand.New(rand.NewSource(42)) // 固定种子:失败要能复现
		for i := 0; i < 4000; i++ {
			select {
			case <-copyDone:
				return
			default:
			}
			id := rng.Intn(seedRows) + 1
			switch i % 3 {
			case 0:
				_, _ = db.ExecContext(ctx, fmt.Sprintf(
					"UPDATE `%s` SET memo = 'updated-%d' WHERE id = %d", name, i, id))
			case 1:
				_, _ = db.ExecContext(ctx, fmt.Sprintf("DELETE FROM `%s` WHERE id = %d", name, id))
			case 2:
				_, _ = db.ExecContext(ctx, fmt.Sprintf(
					"INSERT INTO `%s` (id, memo, n) VALUES (%d, 'new-%d', %d) "+
						"ON DUPLICATE KEY UPDATE memo = VALUES(memo)", name, seedRows+i, i, i))
			}
		}
	}()

	// 分块故意小,逼出多轮,让拷贝与写入真正交错。
	var copyMaxSeen atomic.Int64
	var overlapped atomic.Int64
	res, err := CopyAll(ctx, db, "osc_test", name, sh, CopyOptions{
		// 块小 + 每块之间歇一下:拷贝必须比写入慢,交错才会真的落在同一批行上。
		// 拷贝一路领先的话,重放碰过的行早就被拷完了,那条不变式根本不会被触发。
		ChunkSize: 20,
		OnProgress: func(p Progress) {
			// 每搬完一块,看重放此刻已经触碰过的键里,有多少落在**已经拷过**的范围内。
			// 这正是「拷贝与重放撞在同一行上」的定义。
			time.Sleep(3 * time.Millisecond)
			if k, ok := toInt64(p.Cursor); ok {
				copyMaxSeen.Store(k)
				copyCursor.Store(k)
				repMu.Lock()
				var n int64
				for rk, before := range repKeys {
					if rk <= k {
						n++
						// before = 这个键是在拷贝游标走到它**之前**就被重放碰过的。
						// 只有这一种交错才会让「拷贝覆盖重放」这个错误现形。
						if before {
							_ = before
						}
					}
				}
				repMu.Unlock()
				if n > overlapped.Load() {
					overlapped.Store(n)
				}
			}
		},
	})
	if err != nil {
		t.Fatalf("拷贝失败: %v", err)
	}
	close(copyDone)
	t.Logf("拷贝写入 %d 行", res.Copied)
	t.Logf("交错证据:重放触碰过 %d 个键落在已拷范围内(拷贝游标最远到 %d)",
		overlapped.Load(), copyMaxSeen.Load())
	{
		var n int64
		repMu.Lock()
		for _, before := range repKeys {
			if before {
				n++
			}
		}
		repMu.Unlock()
		raceBefore.Store(n)
		t.Logf("其中「重放先到、拷贝后到」的有 %d 个 —— 只有这种才考验得到「拷贝不覆盖」", n)
		if n == 0 {
			t.Fatal("没有出现「重放先到、拷贝后到」的交错 —— 这条用例考验不到「拷贝不覆盖」,绿是假的")
		}
	}

	// 拷贝刚结束时两表**本来就不该一致** —— 那一刻还有一批事件在 binlog 队列里没被
	// 重放到。曾经在这里断言过「影子表里还是旧值 = 拷贝覆盖了重放」,那是错的:它把
	// 「重放尚未追平」误判成了「拷贝盖回旧值」,而两者在数据上长得一模一样。
	//
	// 真正能区分它们的只有**收敛**:停写之后重放把队列清空,两表必须完全一致。
	//
	// 这条用例做过四向变异验证,三个被抓住、一个没有,值得如实写下来:
	//
	//   重放漏发 DELETE      → 抓住(影子表多出 178 行,已删的行复活)
	//   重放用 INSERT IGNORE → 抓住(更新写不进去,停在旧值)
	//   UPDATE 用改前镜像     → 抓住(更新被丢掉)
	//   **拷贝用 REPLACE     → 抓不住**
	//
	// 最后一个抓不住不是偶然:停写之后的收尾重放会把每一行都再 REPLACE 一遍,于是
	// 拷贝在中途犯下的覆盖被悄悄修正了。**生产里写入不会停**,cut-over 是在持续写入
	// 下抢锁完成的,没有这样一段安静的收尾期兜底 —— 所以这条路径靠的是
	// TestCopy_NeverOverwritesWhatReplayAlreadyWrote 那条单点用例,它直接摆出
	// 「重放先写、拷贝后到」的局面,不依赖时序运气。
	//
	// 记在这里,是因为一条端到端全绿最容易让人以为所有路径都被覆盖了。

	// ④ 停写,等重放把剩下的追平。
	wg.Wait()
	select {
	case err := <-replayErr:
		t.Fatalf("订阅提前结束: %v", err)
	default:
	}
	if err := waitUntilEqual(ctx, t, db, name, sh); err != nil {
		t.Fatalf("重放追平失败: %v", err)
	}
	t.Logf("重放了 %d 条事件", replayed.Load())

	// ⑤ 追平了才切换。
	if err := CutOver(ctx, CutOverConfig{
		DSN: mysqlDSN(), Schema: "osc_test", Table: name, Shadow: sh, ReplayCaughtUp: true,
	}); err != nil {
		t.Fatalf("cut-over 失败: %v", err)
	}
	stopReplay()

	// ⑥ 新表带着新索引,且与切换前的原表(现在的 _del)逐行一致。
	if idx := indexesOf(t, db, "osc_test", name); !contains(idx, "idx_memo") {
		t.Errorf("切换之后没有新索引: %v", idx)
	}
	assertSameRows(ctx, t, db, del, name)
}

// waitUntilEqual 等重放把影子表追到与原表一致。
//
// 追平判定用的是「内容相同」而不是「binlog 位点相同」:后者在还有写入时永远追不上,
// 而这条用例已经停写了 —— 内容一致就是追平的定义。
func waitUntilEqual(ctx context.Context, t *testing.T, db *sql.DB, orig, shadow string) error {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	var lastDiff, lastOrig, lastShadow int64
	for time.Now().Before(deadline) {
		diff, o, s, err := compare(ctx, db, orig, shadow)
		if err != nil {
			return err
		}
		lastDiff, lastOrig, lastShadow = diff, o, s
		if diff == 0 && o == s {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("30 秒内没追平:对不上 %d 行(原表 %d 行,影子表 %d 行)", lastDiff, lastOrig, lastShadow)
}

// compare 报告两表对不上的行数,以及各自的行数。
//
// 用 FULL OUTER 的等价写法(两个方向的 LEFT JOIN 相加)—— 单向 LEFT JOIN 看不见
// 「影子表里多出来的行」,而重放把已删的行写回去正是那个形状。
func compare(ctx context.Context, db *sql.DB, a, b string) (diff, na, nb int64, err error) {
	q := fmt.Sprintf(`
		SELECT
		  (SELECT COUNT(*) FROM `+"`%s`"+` x LEFT JOIN `+"`%s`"+` y ON y.id = x.id
		    WHERE y.id IS NULL OR y.memo <> x.memo OR y.n <> x.n)
		+ (SELECT COUNT(*) FROM `+"`%s`"+` y LEFT JOIN `+"`%s`"+` x ON x.id = y.id
		    WHERE x.id IS NULL),
		  (SELECT COUNT(*) FROM `+"`%s`"+`),
		  (SELECT COUNT(*) FROM `+"`%s`"+`)`, a, b, b, a, a, b)
	err = db.QueryRowContext(ctx, q).Scan(&diff, &na, &nb)
	return diff, na, nb, err
}

// assertSameRows 逐行比对两张表 —— 只数行数看不出「行数对但某行值错」这种交错 bug,
// 而那正是不变式写反时的典型形状。
func assertSameRows(ctx context.Context, t *testing.T, db *sql.DB, want, got string) {
	t.Helper()
	diff, nw, ng, err := compare(ctx, db, want, got)
	if err != nil {
		t.Fatalf("比对失败: %v", err)
	}
	if nw != ng {
		t.Errorf("行数不一致:切换前 %d 行,切换后 %d 行", nw, ng)
	}
	if diff != 0 {
		t.Errorf("有 %d 行内容对不上 —— 拷贝与重放的交错没有收敛", diff)
	}
}

// toInt64 把 binlog / 游标里的数值取成 int64。驱动在不同场景下给的类型不一样。
func toInt64(v any) (int64, bool) {
	switch t := v.(type) {
	case int64:
		return t, true
	case int32:
		return int64(t), true
	case int:
		return int64(t), true
	case uint64:
		return int64(t), true
	case []byte:
		var n int64
		if _, err := fmt.Sscan(string(t), &n); err == nil {
			return n, true
		}
	}
	return 0, false
}
