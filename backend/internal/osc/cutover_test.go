package osc

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// cut-over 是影子表顶替生产表的那一刻,整件事里最危险的一步。
//
// 它要买的不是"换得快",而是**换的过程中这张表一次都没有消失过**。朴素的写法
//
//	DROP TABLE t; RENAME _t_gho TO t;
//
// 在两条语句之间留下一个窗口:那一瞬间访问这张表的每一个请求都会拿到
// "table doesn't exist"。表面上是"加了个索引",实际是全站 500。
//
// 所以下面第一条用例开一个并发读循环一直查这张表,断言它**一次都没有**收到
// 表不存在 —— 只断言最终状态是看不出这个窗口的。

func TestCutOver_TheTableIsNeverMissing(t *testing.T) {
	db := openMySQL(t)
	ctx := context.Background()
	name := makeTable(t, db, "CREATE TABLE `%s` (id BIGINT PRIMARY KEY, memo VARCHAR(64))")
	mustExec(t, db, "INSERT INTO `"+name+"` (id, memo) VALUES (1, '甲')")

	sh, err := CreateShadow(ctx, db, "osc_test", name, "ADD INDEX idx_memo (memo)")
	if err != nil {
		t.Fatalf("建影子表失败: %v", err)
	}
	if _, err := CopyAll(ctx, db, "osc_test", name, sh, CopyOptions{ChunkSize: 100}); err != nil {
		t.Fatalf("拷贝失败: %v", err)
	}
	t.Cleanup(func() {
		for _, x := range []string{sh, "_" + name + DelSuffix} {
			_, _ = db.ExecContext(context.Background(), "DROP TABLE IF EXISTS `"+x+"`")
		}
	})

	// 一直读这张表,直到 cut-over 结束。
	stop := make(chan struct{})
	var wg sync.WaitGroup
	var missing, reads int
	var mu sync.Mutex
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			var n int
			err := db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM `"+name+"`").Scan(&n)
			mu.Lock()
			reads++
			// 1146 = ER_NO_SUCH_TABLE。这是那个致命窗口的样子。
			if err != nil && (strings.Contains(err.Error(), "1146") ||
				strings.Contains(strings.ToLower(err.Error()), "doesn't exist")) {
				missing++
			}
			mu.Unlock()
		}
	}()

	// 让读循环先跑起来,确保它真的覆盖了切换那一瞬间。
	time.Sleep(300 * time.Millisecond)
	cutErr := CutOver(ctx, CutOverConfig{
		DSN: mysqlDSN(), Schema: "osc_test", Table: name, Shadow: sh, ReplayCaughtUp: true,
	})
	close(stop)
	wg.Wait()

	if cutErr != nil {
		t.Fatalf("cut-over 失败: %v", cutErr)
	}
	mu.Lock()
	defer mu.Unlock()
	if reads < 5 {
		t.Fatalf("读循环只跑了 %d 次,覆盖不到切换那一瞬间,这条用例说明不了问题", reads)
	}
	if missing != 0 {
		t.Errorf("%d/%d 次读到了「表不存在」—— 切换过程中原表消失过,那一瞬间全站访问这张表都会 500",
			missing, reads)
	}
}

// 换完之后的三件事:新表带着新索引、旧表留在 _del 里、影子表不见了。
func TestCutOver_SwapsTheTablesAndKeepsTheOldOne(t *testing.T) {
	db := openMySQL(t)
	ctx := context.Background()
	name := makeTable(t, db, "CREATE TABLE `%s` (id BIGINT PRIMARY KEY, memo VARCHAR(64))")
	mustExec(t, db, "INSERT INTO `"+name+"` (id, memo) VALUES (1, '甲')")

	sh, err := CreateShadow(ctx, db, "osc_test", name, "ADD INDEX idx_memo (memo)")
	if err != nil {
		t.Fatalf("建影子表失败: %v", err)
	}
	if _, err := CopyAll(ctx, db, "osc_test", name, sh, CopyOptions{ChunkSize: 100}); err != nil {
		t.Fatalf("拷贝失败: %v", err)
	}
	del := "_" + name + DelSuffix
	t.Cleanup(func() {
		for _, x := range []string{sh, del} {
			_, _ = db.ExecContext(context.Background(), "DROP TABLE IF EXISTS `"+x+"`")
		}
	})

	if err := CutOver(ctx, CutOverConfig{
		DSN: mysqlDSN(), Schema: "osc_test", Table: name, Shadow: sh, ReplayCaughtUp: true,
	}); err != nil {
		t.Fatalf("cut-over 失败: %v", err)
	}

	// 原名下现在是那张新表 —— 带着这次加的索引,数据也在。
	idx := indexesOf(t, db, "osc_test", name)
	if !contains(idx, "idx_memo") {
		t.Errorf("换过来之后 %s 上没有新索引,实际: %v", name, idx)
	}
	var n int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM `"+name+"`").Scan(&n); err != nil {
		t.Fatalf("读新表失败: %v", err)
	}
	if n != 1 {
		t.Errorf("新表 %d 行,想要 1", n)
	}

	// 旧表留着而不是删掉 —— 出了事要能换回去,这是唯一的退路。
	if ok, _ := tableExists(ctx, db, "osc_test", del); !ok {
		t.Errorf("旧表没有留在 %s 里 —— 出事之后没有任何退路", del)
	}
	// 影子表已经顶上去了,不该还留着一张同名的。
	if ok, _ := tableExists(ctx, db, "osc_test", sh); ok {
		t.Errorf("影子表 %s 还在 —— 它应当已经改名顶上去了", sh)
	}
}

// 重放没追平就换,等于把那段还没重放的写入直接丢掉,而且不会有任何报错。
func TestCutOver_RefusesWhenReplayHasNotCaughtUp(t *testing.T) {
	db := openMySQL(t)
	ctx := context.Background()
	name := makeTable(t, db, "CREATE TABLE `%s` (id BIGINT PRIMARY KEY, memo VARCHAR(64))")
	sh, err := CreateShadow(ctx, db, "osc_test", name, "ADD INDEX idx_memo (memo)")
	if err != nil {
		t.Fatalf("建影子表失败: %v", err)
	}
	t.Cleanup(func() { _, _ = db.ExecContext(context.Background(), "DROP TABLE IF EXISTS `"+sh+"`") })

	if err := CutOver(ctx, CutOverConfig{
		DSN: mysqlDSN(), Schema: "osc_test", Table: name, Shadow: sh, ReplayCaughtUp: false,
	}); err == nil {
		t.Fatal("重放没追平时必须拒绝换 —— 换过去就丢掉了那段写入")
	}

	// 拒绝之后一切照旧:原表还在原地,影子表也还在,可以追平之后再来一次。
	if ok, _ := tableExists(ctx, db, "osc_test", name); !ok {
		t.Error("拒绝之后原表不见了")
	}
	if ok, _ := tableExists(ctx, db, "osc_test", sh); !ok {
		t.Error("拒绝之后影子表不见了 —— 追平后就没法重来了")
	}
}

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
