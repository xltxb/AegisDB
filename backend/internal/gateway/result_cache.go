package gateway

// Web 命令行的结果分批显示(2026-10 用户要求:不再强限制 200 行,超出时可"加载更多")。
//
// 终端执行一条查询时,网关最多读 maxCachedRows 行(还有 maxCachedBytes 的总字节兜底);第一批
// (pageSize 行,系统设置可配)随回执返回,其余**打码之后**暂存在这里,按结果编号分批取。
//
// 为什么暂存、而不是"加载更多"时重新执行一遍带分页的 SQL:
//   - 重新执行就是再跑一次:要再判定、再审批、再审计,而且两次读到的数据可能已经不一样;
//   - 任意 SQL 改写成分页语句(LIMIT/OFFSET、ROWNUM、FETCH FIRST)各引擎写法不同,改写本身就是风险。
//
// 暂存只有本人能取(按用户隔离),10 分钟没人取就丢;每人同时最多留 maxCachedPerUser 份,
// 再多就挤掉最旧的。所有人加起来不超过 maxCacheTotalBytes,超了按时间挤掉最旧的(不论是谁的);
// 过期的每分钟清一次(SweepResultCache),不等下一次有人暂存(2026-10 审查)。只有终端带着 WithResultPaging 的执行才会走这条路,审批执行、导出等不受影响。

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sort"
	"sync"
	"time"
)

const (
	// maxCachedRows:一条查询最多读这么多行(含第一批);再多就提示改用数据导出。
	maxCachedRows = 50000
	// maxCachedBytes:一条查询读出的结果占用内存的上限(按 rowMemSize 估算,不只是文本),防宽表、
	// 长表把内存撑爆。
	maxCachedBytes = 64 << 20
	// maxCacheTotalBytes:所有人暂存加起来的上限。只按人限份数时,100 个人各暂存 3 份满额结果
	// 就是近 20GB(2026-10 审查)。
	maxCacheTotalBytes = 256 << 20
	resultCacheTTL = 10 * time.Minute
	// maxCachedPerUser:每人同时暂存的结果份数。
	maxCachedPerUser = 3

	// MinResultPageSize / MaxResultPageSize:每批行数设置的取值范围。
	MinResultPageSize = 100
	MaxResultPageSize = maxCachedRows
)

type pagingKey struct{}

type pagingOpt struct {
	userID   int64
	pageSize int
}

// WithResultPaging:这次执行来自 Web 命令行 —— 结果按 pageSize 分批,超出的部分暂存给本人"加载更多"。
func WithResultPaging(ctx context.Context, userID int64, pageSize int) context.Context {
	if pageSize < MinResultPageSize {
		pageSize = MinResultPageSize
	}
	if pageSize > MaxResultPageSize {
		pageSize = MaxResultPageSize
	}
	return context.WithValue(ctx, pagingKey{}, pagingOpt{userID: userID, pageSize: pageSize})
}

// WithoutResultPaging 撤掉 ctx 上的分批:之后的执行按一屏上限(maxResultRows)读,不暂存后续行。
// i 脚本用 —— 一份脚本可能有几十个结果集,不该各自在内存里占一份「加载更多」。
func WithoutResultPaging(ctx context.Context) context.Context {
	return context.WithValue(ctx, pagingKey{}, nil)
}

func pagingFrom(ctx context.Context) (pagingOpt, bool) {
	p, ok := ctx.Value(pagingKey{}).(pagingOpt)
	return p, ok
}

// 内存估算:Go 的 string 头 16 字节、切片头 24 字节。窄表(一两列短值)几万行时,这些头比文本
// 本身还大,只数文本会把 64MB 的上限放大成实际几百 MB(2026-10 审查)。
const (
	cellOverhead = 16
	rowOverhead  = 24
)

// rowMemSize:一行结果大约占多少内存。
func rowMemSize(rec []string) int {
	n := rowOverhead
	for _, s := range rec {
		n += cellOverhead + len(s)
	}
	return n
}

type cachedResult struct {
	userID  int64
	size    int // rowMemSize 之和
	columns []string
	rows    [][]string // 第一批之后的行
	shown   int        // 第一批的行数(编号从它之后接着算)
	expires time.Time
	created time.Time
}

var resultCache = struct {
	sync.Mutex
	m     map[string]*cachedResult
	total int // 所有暂存的 size 之和
}{m: map[string]*cachedResult{}}

// dropCached 在 resultCache 锁内删一份暂存并扣掉它的大小。
func dropCached(id string) {
	if c := resultCache.m[id]; c != nil {
		resultCache.total -= c.size
		delete(resultCache.m, id)
	}
}

func sweepExpiredLocked(now time.Time) int {
	n := 0
	for k, v := range resultCache.m {
		if now.After(v.expires) {
			dropCached(k)
			n++
		}
	}
	return n
}

// SweepResultCache 丢掉过期的暂存,返回丢掉的份数。主程序每分钟调一次。
func SweepResultCache(now time.Time) int {
	resultCache.Lock()
	defer resultCache.Unlock()
	return sweepExpiredLocked(now)
}

// ResultCacheBytes:当前暂存占用的内存估算(测试与监控用)。
func ResultCacheBytes() int {
	resultCache.Lock()
	defer resultCache.Unlock()
	return resultCache.total
}

// storeResult 暂存第一批之后的行,返回结果编号。
func storeResult(userID int64, columns []string, rest [][]string, shown int) string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	id := hex.EncodeToString(b[:])
	now := time.Now()
	size := 0
	for _, r := range rest {
		size += rowMemSize(r)
	}
	resultCache.Lock()
	defer resultCache.Unlock()
	sweepExpiredLocked(now)
	// 每人最多留 maxCachedPerUser 份:挤掉最旧的
	var mine []string
	for k, v := range resultCache.m {
		if v.userID == userID {
			mine = append(mine, k)
		}
	}
	if len(mine) >= maxCachedPerUser {
		sort.Slice(mine, func(i, j int) bool { return resultCache.m[mine[i]].created.Before(resultCache.m[mine[j]].created) })
		for _, k := range mine[:len(mine)-maxCachedPerUser+1] {
			dropCached(k)
		}
	}
	// 全局上限:放不下就从最旧的挤起(不论是谁的)。单份最大 maxCachedBytes,远小于总上限,总放得下。
	if resultCache.total+size > maxCacheTotalBytes {
		all := make([]string, 0, len(resultCache.m))
		for k := range resultCache.m {
			all = append(all, k)
		}
		sort.Slice(all, func(i, j int) bool { return resultCache.m[all[i]].created.Before(resultCache.m[all[j]].created) })
		for _, k := range all {
			if resultCache.total+size <= maxCacheTotalBytes {
				break
			}
			dropCached(k)
		}
	}
	resultCache.m[id] = &cachedResult{userID: userID, size: size, columns: columns, rows: rest, shown: shown, expires: now.Add(resultCacheTTL), created: now}
	resultCache.total += size
	return id
}

// ResultPage 是"加载更多"取回的一批。
type ResultPage struct {
	Columns []string   `json:"columns"`
	Data    [][]string `json:"data"`
	// Offset:这一批第一行在整个结果里的位置(0 起)
	Offset int  `json:"offset"`
	More   bool `json:"more"`
}

// FetchResultPage 取暂存结果的一批。offset 是整个结果里的行位置(第一批之后从 shown 起)。
// 结果不存在、已过期或不是本人的,ok=false。取一次就续期。
func FetchResultPage(userID int64, id string, offset, pageSize int) (ResultPage, bool) {
	if pageSize < MinResultPageSize {
		pageSize = MinResultPageSize
	}
	resultCache.Lock()
	defer resultCache.Unlock()
	c := resultCache.m[id]
	if c == nil || c.userID != userID || time.Now().After(c.expires) {
		return ResultPage{}, false
	}
	c.expires = time.Now().Add(resultCacheTTL)
	start := offset - c.shown
	if start < 0 {
		start = 0
	}
	if start > len(c.rows) {
		start = len(c.rows)
	}
	end := start + pageSize
	if end > len(c.rows) {
		end = len(c.rows)
	}
	return ResultPage{Columns: c.columns, Data: c.rows[start:end], Offset: c.shown + start, More: end < len(c.rows)}, true
}
