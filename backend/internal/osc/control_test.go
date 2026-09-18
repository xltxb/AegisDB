package osc

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// 阶段五买的是 ADR 开篇列的第一件事:**可限流、可暂停、可中止**。原生 ALTER 一旦下去
// 就只能等,几小时里没有任何旋钮 —— 这一段就是那些旋钮。
//
// 其中最要紧的是中止,而"能停"只是一半:**停完要能接着跑**。做不到的话中止等于前功
// 尽弃,人就不会去用它 —— 一个没人敢按的中止键等于没有。

func TestCopy_CanBeStoppedAndResumedWithoutLosingOrDuplicatingRows(t *testing.T) {
	db := openMySQL(t)
	ctx := context.Background()
	name := makeTable(t, db, "CREATE TABLE `%s` (id BIGINT PRIMARY KEY, memo VARCHAR(64))")

	const total = 500
	for i := 1; i <= total; i++ {
		mustExec(t, db, fmt.Sprintf("INSERT INTO `%s` (id, memo) VALUES (%d, 'row-%d')", name, i, i))
	}
	sh, err := CreateShadow(ctx, db, "osc_test", name, "ADD INDEX idx_memo (memo)")
	if err != nil {
		t.Fatalf("建影子表失败: %v", err)
	}
	t.Cleanup(func() { _, _ = db.ExecContext(context.Background(), "DROP TABLE IF EXISTS `"+sh+"`") })

	// 跑满两块就叫停 —— 中途停,不是跑完了才停。
	stopCtx, stop := context.WithCancel(ctx)
	var chunks int
	res, err := CopyAll(stopCtx, db, "osc_test", name, sh, CopyOptions{
		ChunkSize: 50,
		OnProgress: func(p Progress) {
			chunks++
			if chunks == 2 {
				stop()
			}
		},
	})
	stop()
	if err == nil {
		t.Fatal("中途取消时应当返回错误,好让调用方知道这不是跑完了")
	}
	if !res.Interrupted {
		t.Error("结果里没有标记「被中断」—— 调用方分不清是跑完了还是被停了")
	}
	if res.Copied == 0 || res.Copied >= total {
		t.Fatalf("被停下时拷了 %d 行,应当是中间某个数(总共 %d)", res.Copied, total)
	}
	if res.Cursor == nil {
		t.Fatal("没有给出续跑游标 —— 中止就等于前功尽弃,那样的中止键没人敢按")
	}

	// 从停下的地方续跑。
	res2, err := CopyAll(ctx, db, "osc_test", name, sh, CopyOptions{
		ChunkSize: 50, Resume: res.Cursor,
	})
	if err != nil {
		t.Fatalf("续跑失败: %v", err)
	}
	if res2.Interrupted {
		t.Error("续跑跑到底了,不该标记为被中断")
	}

	// 不重不漏:两表逐行一致。
	var n int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM `"+sh+"`").Scan(&n); err != nil {
		t.Fatalf("数行数失败: %v", err)
	}
	if n != total {
		t.Errorf("续跑之后影子表 %d 行,原表 %d 行 —— 中止/续跑漏了或重了", n, total)
	}
	var mismatch int
	if err := db.QueryRowContext(ctx, fmt.Sprintf(
		"SELECT COUNT(*) FROM `%s` o LEFT JOIN `%s` s ON s.id = o.id WHERE s.id IS NULL OR s.memo <> o.memo",
		name, sh)).Scan(&mismatch); err != nil {
		t.Fatalf("对比失败: %v", err)
	}
	if mismatch != 0 {
		t.Errorf("有 %d 行与原表对不上", mismatch)
	}
}

// 进度要能看出"还要多久",所以它得单调地往前走,并且最终走到总数。
func TestCopy_ReportsProgressThatMovesForward(t *testing.T) {
	db := openMySQL(t)
	ctx := context.Background()
	name := makeTable(t, db, "CREATE TABLE `%s` (id BIGINT PRIMARY KEY, memo VARCHAR(64))")
	const total = 200
	for i := 1; i <= total; i++ {
		mustExec(t, db, fmt.Sprintf("INSERT INTO `%s` (id, memo) VALUES (%d, 'x')", name, i))
	}
	sh, err := CreateShadow(ctx, db, "osc_test", name, "ADD INDEX idx_memo (memo)")
	if err != nil {
		t.Fatalf("建影子表失败: %v", err)
	}
	t.Cleanup(func() { _, _ = db.ExecContext(context.Background(), "DROP TABLE IF EXISTS `"+sh+"`") })

	var seen []int64
	res, err := CopyAll(ctx, db, "osc_test", name, sh, CopyOptions{
		ChunkSize:  40,
		OnProgress: func(p Progress) { seen = append(seen, p.Copied) },
	})
	if err != nil {
		t.Fatalf("拷贝失败: %v", err)
	}
	if len(seen) < 2 {
		t.Fatalf("只报了 %d 次进度,分块显然没生效", len(seen))
	}
	for i := 1; i < len(seen); i++ {
		if seen[i] < seen[i-1] {
			t.Errorf("进度倒退了: %v", seen)
			break
		}
	}
	if seen[len(seen)-1] != res.Copied {
		t.Errorf("最后一次进度 %d 与实际拷贝数 %d 对不上", seen[len(seen)-1], res.Copied)
	}
}

// 限流:两块之间该不该等,是一个纯判断。把它单独拎出来,才能逐格测透而不必搭一套
// 主从复制 —— 那测的是 MySQL 的复制,不是我们的逻辑。
func TestThrottle_WaitsOnlyWhenTheReplicaIsBehind(t *testing.T) {
	for _, tc := range []struct {
		name      string
		lag, max  time.Duration
		wantPause bool
	}{
		{"没有延迟就不等", 0, 5 * time.Second, false},
		{"延迟在阈值以内不等", 3 * time.Second, 5 * time.Second, false},
		{"正好到阈值不等", 5 * time.Second, 5 * time.Second, false},
		{"超过阈值就等", 6 * time.Second, 5 * time.Second, true},
		// 阈值为 0 = 没开限流。把它当成"一有延迟就停"会让拷贝永远动不了。
		{"没配阈值就永远不等", 9999 * time.Second, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldPause(tc.lag, tc.max); got != tc.wantPause {
				t.Errorf("shouldPause(%v, %v) = %v,想要 %v", tc.lag, tc.max, got, tc.wantPause)
			}
		})
	}
}

// 限流真的能让拷贝停下来等 —— 用注入的假延迟源测,不搭真从库。
func TestCopy_ThrottleActuallyHoldsTheCopyBack(t *testing.T) {
	db := openMySQL(t)
	ctx := context.Background()
	name := makeTable(t, db, "CREATE TABLE `%s` (id BIGINT PRIMARY KEY, memo VARCHAR(64))")
	for i := 1; i <= 120; i++ {
		mustExec(t, db, fmt.Sprintf("INSERT INTO `%s` (id, memo) VALUES (%d, 'x')", name, i))
	}
	sh, err := CreateShadow(ctx, db, "osc_test", name, "ADD INDEX idx_memo (memo)")
	if err != nil {
		t.Fatalf("建影子表失败: %v", err)
	}
	t.Cleanup(func() { _, _ = db.ExecContext(context.Background(), "DROP TABLE IF EXISTS `"+sh+"`") })

	// 前两次询问报"从库落后了",之后恢复正常。
	var asked int
	lag := func(context.Context) (time.Duration, error) {
		asked++
		if asked <= 2 {
			return 30 * time.Second, nil
		}
		return 0, nil
	}

	start := time.Now()
	res, err := CopyAll(ctx, db, "osc_test", name, sh, CopyOptions{
		ChunkSize: 40, MaxLag: 5 * time.Second, ReplicaLag: lag,
		ThrottleInterval: 120 * time.Millisecond,
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("拷贝失败: %v", err)
	}
	if asked < 2 {
		t.Errorf("只问了 %d 次延迟,限流没起作用", asked)
	}
	// 两次"落后"各等一个间隔,所以至少等了两个间隔。
	if elapsed < 240*time.Millisecond {
		t.Errorf("总共只花了 %v —— 限流应当让它至少等两轮", elapsed)
	}
	if res.Copied != 120 {
		t.Errorf("限流之后拷了 %d 行,应当仍是 120 —— 限流只该让它慢,不该让它漏", res.Copied)
	}
}
