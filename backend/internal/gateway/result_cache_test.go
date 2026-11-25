package gateway

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Web 命令行的结果分批(不再强限制 200 行):第一批随回执返回,其余暂存给本人"加载更多"。
func TestRealRun_TerminalResultPaging(t *testing.T) {
	conn, _ := testMySQLConn(t)
	const q = "WITH RECURSIVE s(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM s WHERE n < 2500) SELECT n FROM s"
	const uid = 700001

	// 不是终端(没有 WithResultPaging):照旧最多 200 行
	plain, err := RealRun(context.Background(), conn, "SET SESSION cte_max_recursion_depth = 10000", 30*time.Second)
	_ = plain
	if err != nil {
		t.Skipf("测试库不支持调递归深度: %v", err)
	}
	ctxDepth := WithTermSession(context.Background(), uid, "paging-tab") // 同一连接上先调深度
	t.Cleanup(func() { CloseTermSessionsOf(uid) })
	if _, err := RealRun(ctxDepth, conn, "SET SESSION cte_max_recursion_depth = 10000", 30*time.Second); err != nil {
		t.Fatal(err)
	}
	r0, err := RealRun(ctxDepth, conn, q, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(r0.Data) != maxResultRows || !r0.Truncated || r0.ResultID != "" {
		t.Errorf("非终端执行应照旧截在 %d 行、不暂存: rows=%d id=%q", maxResultRows, len(r0.Data), r0.ResultID)
	}

	// 终端:每批 1000 行
	ctx := WithResultPaging(ctxDepth, uid, 1000)
	r, err := RealRun(ctx, conn, q, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Data) != 1000 || r.TotalRows != 2500 || r.ResultID == "" || r.BeyondCap || !r.Truncated {
		t.Fatalf("第一批应是 1000 行、共 2500 行、有暂存: rows=%d total=%d id=%q beyond=%v", len(r.Data), r.TotalRows, r.ResultID, r.BeyondCap)
	}
	p2, ok := FetchResultPage(uid, r.ResultID, 1000, 1000)
	if !ok || len(p2.Data) != 1000 || !p2.More || p2.Offset != 1000 || p2.Data[0][0] != "1001" {
		t.Fatalf("第二批应是 1001-2000: ok=%v rows=%d more=%v offset=%d first=%v", ok, len(p2.Data), p2.More, p2.Offset, p2.Data)
	}
	p3, _ := FetchResultPage(uid, r.ResultID, 2000, 1000)
	if len(p3.Data) != 500 || p3.More || p3.Data[499][0] != "2500" {
		t.Fatalf("最后一批应是 2001-2500: rows=%d more=%v", len(p3.Data), p3.More)
	}
	if _, ok := FetchResultPage(uid+1, r.ResultID, 1000, 1000); ok {
		t.Error("别人不能取我暂存的结果")
	}

	// 一批就装得下:不暂存
	small, _ := RealRun(ctx, conn, "SELECT 1 FROM dual", 30*time.Second)
	if small.ResultID != "" || small.Truncated {
		t.Errorf("一批装得下就不该暂存: %+v", small)
	}
}

func TestResultCache_KeepsAtMostThreePerUser(t *testing.T) {
	ids := []string{}
	for i := 0; i < 4; i++ {
		ids = append(ids, storeResult(700009, []string{"n"}, [][]string{{"x"}}, 100))
		time.Sleep(2 * time.Millisecond)
	}
	if _, ok := FetchResultPage(700009, ids[0], 100, 100); ok {
		t.Error("超过 3 份时最旧的应被挤掉")
	}
	if _, ok := FetchResultPage(700009, ids[3], 100, 100); !ok {
		t.Error("最新的一份应还在")
	}
}

func resetResultCacheForTest(t *testing.T) {
	t.Helper()
	resultCache.Lock()
	resultCache.m = map[string]*cachedResult{}
	resultCache.total = 0
	resultCache.Unlock()
}

// 每份按实际占用计数(文本 + 字符串头 + 行切片头),不只是文本(2026-10 审查)。
func TestResultCache_CountsRealMemory(t *testing.T) {
	resetResultCacheForTest(t)
	rows := [][]string{{"a", "bc"}, {"", "d"}}
	storeResult(700010, []string{"x", "y"}, rows, 100)
	want := 2*rowOverhead + 4*cellOverhead + 4
	if got := ResultCacheBytes(); got != want {
		t.Fatalf("计数应含字符串头与行切片头:想要 %d,实际 %d", want, got)
	}
}

// 所有人加起来不超过 maxCacheTotalBytes:超了从最旧的挤起,不论是谁的。
func TestResultCache_GlobalCapEvictsOldestAcrossUsers(t *testing.T) {
	resetResultCacheForTest(t)
	big := strings.Repeat("x", 1<<20) // 同一个串反复引用:计数 1MB,实际不多占内存
	rows := make([][]string, 60)
	for i := range rows {
		rows[i] = []string{big}
	}
	var ids []string
	for u := 0; u < 5; u++ { // 5 个人各一份约 60MB,总共约 300MB > 256MB
		ids = append(ids, storeResult(int64(710000+u), []string{"c"}, rows, 100))
		time.Sleep(2 * time.Millisecond)
	}
	if got := ResultCacheBytes(); got > maxCacheTotalBytes {
		t.Fatalf("总占用不该超过上限:%d > %d", got, maxCacheTotalBytes)
	}
	if _, ok := FetchResultPage(710000, ids[0], 100, 100); ok {
		t.Error("超过总上限时最旧的一份应被挤掉(哪怕是别人的)")
	}
	if _, ok := FetchResultPage(710004, ids[4], 100, 100); !ok {
		t.Error("刚存的一份应还在")
	}
	if _, ok := FetchResultPage(710001, ids[1], 100, 100); !ok {
		t.Error("只挤掉放得下所需的份数,第二旧的应还在")
	}
}

// 过期的由定时清理丢掉,不等下一次有人暂存;占用随之归零。
func TestResultCache_SweepDropsExpired(t *testing.T) {
	resetResultCacheForTest(t)
	id := storeResult(700011, []string{"n"}, [][]string{{"x"}}, 100)
	if n := SweepResultCache(time.Now()); n != 0 {
		t.Fatalf("没过期不该清掉,清了 %d", n)
	}
	if n := SweepResultCache(time.Now().Add(resultCacheTTL + time.Second)); n != 1 {
		t.Fatalf("过期的应被清掉 1 份,实际 %d", n)
	}
	if ResultCacheBytes() != 0 {
		t.Fatalf("清掉之后占用应归零,实际 %d", ResultCacheBytes())
	}
	if _, ok := FetchResultPage(700011, id, 100, 100); ok {
		t.Error("清掉之后取不到")
	}
}
