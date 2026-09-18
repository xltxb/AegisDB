package osc

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"gorm.io/gorm"

	"velagateway/internal/testsupport"
)

// Runner 把五个阶段串起来,并把每一步写进库。
//
// 它要守的不是"跑得完",而是**跑不完的时候留下的东西能被收拾**。ADR 0011 担心的
// 那个场景 —— 影子表和一段没追平的 binlog 留在库里,而人以为自己加了个索引 ——
// 正是这一层的职责所在。

func newRunner(t *testing.T) (*Runner, *testDeps) {
	t.Helper()
	store := testsupport.NewDB(t)
	target := openMySQL(t)
	return NewRunner(store, func(int64) (*sql.DB, string, error) {
		return target, mysqlDSN(), nil
	}), &testDeps{store: store, target: target}
}

func TestRunner_RunsAMigrationAndRecordsEveryStage(t *testing.T) {
	r, d := newRunner(t)
	ctx := context.Background()
	name := makeTable(t, d.target, "CREATE TABLE `%s` (id BIGINT PRIMARY KEY, memo VARCHAR(64))")
	for i := 1; i <= 50; i++ {
		mustExec(t, d.target, fmt.Sprintf("INSERT INTO `%s` (id, memo) VALUES (%d, 'x')", name, i))
	}

	job, err := r.Start(ctx, StartRequest{
		ConnectionID: 1, Schema: "osc_test", Table: name,
		Alter: "ADD INDEX idx_memo (memo)", CreatedBy: "linwei@vela.io",
	})
	if err != nil {
		t.Fatalf("发起失败: %v", err)
	}
	t.Cleanup(func() {
		for _, x := range []string{ShadowName(name), "_" + name + DelSuffix} {
			_, _ = d.target.ExecContext(context.Background(), "DROP TABLE IF EXISTS `"+x+"`")
		}
	})

	final := waitForFinish(t, r, job.ID, 60*time.Second)
	if final.Status != JobDone {
		t.Fatalf("最终状态 = %s(err=%q),想要 done", final.Status, final.Err)
	}
	// 落库的进度要对得上:拷了多少行不能是 0,否则进度条在说谎。
	if final.CopiedRows == 0 {
		t.Error("CopiedRows = 0 —— 进度没有被写库")
	}
	// 索引真的加上了。
	if idx := indexesOf(t, d.target, "osc_test", name); !contains(idx, "idx_memo") {
		t.Errorf("迁移报告完成,但索引不在: %v", idx)
	}
}

// 中止之后影子表必须被清掉。
//
// 「能中止」如果只是把状态改成 aborted,那它留下的仍然是一张影子表和一堆磁盘占用,
// 而下一次迁移会撞上自己上次留下的残留 —— 那不叫中止,叫放弃。
func TestRunner_AbortCleansUpTheShadowTable(t *testing.T) {
	r, d := newRunner(t)
	ctx := context.Background()
	name := makeTable(t, d.target, "CREATE TABLE `%s` (id BIGINT PRIMARY KEY, memo VARCHAR(64))")
	for i := 1; i <= 3000; i++ {
		mustExec(t, d.target, fmt.Sprintf("INSERT INTO `%s` (id, memo) VALUES (%d, 'x')", name, i))
	}
	t.Cleanup(func() {
		_, _ = d.target.ExecContext(context.Background(), "DROP TABLE IF EXISTS `"+ShadowName(name)+"`")
	})

	r.ChunkSize = 50
	job, err := r.Start(ctx, StartRequest{
		ConnectionID: 1, Schema: "osc_test", Table: name,
		Alter: "ADD INDEX idx_memo (memo)", CreatedBy: "linwei@vela.io",
	})
	if err != nil {
		t.Fatalf("发起失败: %v", err)
	}

	// 等它真的开始拷贝,再叫停 —— 停在 pending 上证明不了什么。
	//
	// 拷贝必须慢到能被停在半途:3000 行在默认块大小下一轮就搬完了,等状态轮询到
	// copying 时任务早已 done,而那时"中止"什么也没证明。Runner 把块大小做成可调
	// 就是为了这个 —— 生产上它也该可调(大表要小块才停得快)。
	waitForStatus(t, r, job.ID, JobCopying, 20*time.Second)
	if err := r.Abort(ctx, job.ID); err != nil {
		t.Fatalf("中止失败: %v", err)
	}

	final := waitForFinish(t, r, job.ID, 30*time.Second)
	if final.Status != JobAborted {
		t.Errorf("最终状态 = %s,想要 aborted", final.Status)
	}
	if ok, _ := tableExists(ctx, d.target, "osc_test", ShadowName(name)); ok {
		t.Error("中止之后影子表还在 —— 那不叫中止,叫放弃:它占着磁盘,还会挡住下一次迁移")
	}
	// 原表毫发无伤。
	var n int
	if err := d.target.QueryRowContext(ctx, "SELECT COUNT(*) FROM `"+name+"`").Scan(&n); err != nil {
		t.Fatalf("读原表失败: %v", err)
	}
	if n != 3000 {
		t.Errorf("原表 %d 行,应为 3000 —— 中止不该动原表", n)
	}
}

// 重启之后要能把残局挑出来。
//
// 分不清「还在跑」和「死在半路」的话,那条记录会一直显示为进行中,而它的影子表
// 没有任何人去收。
func TestRunner_UnfinishedJobsAreVisibleAfterRestart(t *testing.T) {
	r, d := newRunner(t)
	ctx := context.Background()
	_ = d

	// 直接往库里塞一条「死在 copying」的记录,模拟上一个进程没来得及收尾就退出了。
	stale := &Job{
		ConnectionID: 1, Schema: "osc_test", Table: "t_gone",
		Alter: "ADD INDEX i (c)", Status: JobCopying, Shadow: "_t_gone_gho",
	}
	if err := r.store.Create(stale).Error; err != nil {
		t.Fatalf("造残局失败: %v", err)
	}

	// 新进程起来 —— 用同一个库新建一个 Runner。
	r2 := NewRunner(r.store, r.connect)
	orphans, err := r2.Unfinished(ctx)
	if err != nil {
		t.Fatalf("查残局失败: %v", err)
	}
	var found bool
	for _, j := range orphans {
		if j.ID == stale.ID {
			found = true
			if j.Shadow == "" {
				t.Error("残局记录里没有影子表名 —— 收拾的人无从下手")
			}
		}
	}
	if !found {
		t.Error("重启之后看不到那条死在 copying 的记录")
	}
}

type testDeps struct {
	store  *gorm.DB
	target *sql.DB
}

// waitForStatus 等任务走到某个状态。等不到就失败并报出它卡在哪 ——
// 只说"超时"的话,下一个人还要自己去查库才知道发生了什么。
func waitForStatus(t *testing.T, r *Runner, id int64, want JobStatus, d time.Duration) *Job {
	t.Helper()
	deadline := time.Now().Add(d)
	var last *Job
	for time.Now().Before(deadline) {
		j, err := r.Get(context.Background(), id)
		if err == nil {
			last = j
			if j.Status == want {
				return j
			}
			if !j.Status.Unfinished() {
				t.Fatalf("任务已经结束在 %s(err=%q),等不到 %s", j.Status, j.Err, want)
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if last != nil {
		t.Fatalf("%v 内没等到 %s,卡在 %s(err=%q)", d, want, last.Status, last.Err)
	}
	t.Fatalf("%v 内读不到任务 %d", d, id)
	return nil
}

// waitForFinish 等任务走到任一终态。
func waitForFinish(t *testing.T, r *Runner, id int64, d time.Duration) *Job {
	t.Helper()
	deadline := time.Now().Add(d)
	var last *Job
	for time.Now().Before(deadline) {
		j, err := r.Get(context.Background(), id)
		if err == nil {
			last = j
			if !j.Status.Unfinished() {
				return j
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if last != nil {
		t.Fatalf("%v 内没有结束,停在 %s(err=%q)", d, last.Status, last.Err)
	}
	t.Fatalf("%v 内读不到任务 %d", d, id)
	return nil
}
