package gateway

// Web 命令行的每个标签页独占一条数据库连接(2026-10 用户要求)。
//
// 从前终端的单条语句一律走共享连接池(openConn,每台实例 3 条),同一台实例开两个标签,两边
// 的语句随机落在池里的几条连接上:标签 A 里 SET 的会话变量、建的临时表、BEGIN 开的事务,下一条
// 语句可能就不在那个会话里了,还可能"漏"到标签 B。真实客户端不是这样:每开一个窗口就是一个
// 新会话,窗口里的语句始终在同一个会话上。
//
// 现在:前端给每个标签页一个随机的会话标识,随每次执行带上来。网关按(用户, 标识)记一条
// 专用连接(ExecSession,只此一条物理连接),**第一次真正执行时**才连 —— 被拒、被拦截去审批的
// 语句不会为它开连接。之后这个标签的每条语句(含粘贴的一批、在终端里跑的脚本)都在这条连接
// 上。以下情形断开,断开即由数据库回滚未提交的事务:
//
//   - 关掉标签页(前端通知)
//   - 登出 / 空闲锁定(登出接口)
//   - 空闲超过空闲锁定的时长(SweepTermSessions;浏览器直接关掉、网络断了,前端来不及通知)
//
// 切库:MySQL 系在这条连接上发 USE —— 与 mysql 客户端的 USE 一样,会话(事务、变量)还在。
// PostgreSQL 的库是连接级的,换库只能重连(psql 的 \c 也是重连),会话状态随之重来。Oracle
// 换 schema 本来就是在同一会话里 ALTER SESSION(见 realRun)。
//
// 后台执行(exec-async)和审批单执行不走这里:它们本来就各开专用连接,而且会在请求结束之后
// 继续跑,占着标签的连接只会让这个标签后面的语句排队。

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"aegisdb/internal/apimsg"
	"aegisdb/internal/model"
)

// MaxTermSessionsPerUser 是一个用户同时能占用的终端连接数上限:防止一直开新标签(或者前端没来得及
// 通知关闭)把目标库的连接数耗光。到上限时拒绝新开,不挤掉旧的 —— 挤掉就是悄悄回滚别人的事务。
const MaxTermSessionsPerUser = 20

// MaxTermSessionsPerConn 是**一台实例**上所有人的终端标签连接加起来的上限(2026-10 审查)。
//
// 只按人限 20 条时,8 个人就能占满 MySQL 默认的 151 条连接 —— 一个只读账号开 20 个标签各跑一次
// SELECT 1,目标库就少了 20 条可用连接。到上限后新标签**不报错**:这条语句改走共享连接池,
// 回执里提示"本条语句不保留会话状态",人照样能查,只是没有独占会话。
const MaxTermSessionsPerConn = 30

// ErrTermSessionLimit:这个用户的终端连接数已经到上限。
var ErrTermSessionLimit = errors.New("term session limit")

// errTermConnFull:这台实例的终端独占连接已满 —— RealRun 据此报错(不退回共享连接池,见 RealRun)。
var errTermConnFull = errors.New("term session per-instance limit")

// ErrTermSessionClosed:标签已经关了(关标签 / 登出 / 空闲回收),排在它后面的语句不再执行。
var ErrTermSessionClosed = errors.New("term session closed")

type termKey struct {
	userID int64
	id     string
}

type termSession struct {
	sem      chan struct{} // 同一标签的执行排队:USE 与随后的语句之间不能插进别的语句;关闭也要先拿到它
	connID   int64
	key      string // 开连接用的 driver + dsn(MySQL 系不含库名,切库靠 USE)
	sess     *ExecSession
	opens    *atomic.Int64 // 这条会话上**物理连接**被建立过几次:变多了说明断线后被驱动悄悄换了一条
	lastUsed time.Time
	busy     int  // 正在执行或排队的请求数:清理时跳过,一条跑两小时的 ALTER 不算空闲
	closed   bool // 已关闭:在 termSessions 锁下设置;持有 sem 的人据此放弃,不再新建连接
}

var termSessions = struct {
	sync.Mutex
	m map[termKey]*termSession
}{m: map[termKey]*termSession{}}

type termCtxKey struct{}

// WithTermSession 标记 ctx 下的执行属于某个终端标签页:RealRun 会把它落在这个标签的专用连接上。
// id 为空(老前端、非终端入口)时原样返回,行为与从前一样走连接池。
func WithTermSession(ctx context.Context, userID int64, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, termCtxKey{}, termKey{userID, id})
}

// HasTermSession:ctx 上带着终端标签。带着时,一批语句不再另开专用连接 —— 标签自己那条就是。
func HasTermSession(ctx context.Context) bool {
	_, ok := ctx.Value(termCtxKey{}).(termKey)
	return ok
}

// ValidTermSessionID:前端生成的标识(UUID)。只收短的字母数字和连字符,别的一律当没带。
func ValidTermSessionID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

// termDialKey 是开这个标签连接用的 driver + dsn。MySQL 系去掉库名:切库在同一条连接上 USE,
// 不必断开重连、丢掉会话。
func termDialKey(conn *model.Connection) (drv, dsn string, ok bool) {
	if engineFamily(conn.Engine) == familyMySQL {
		c := *conn
		c.Database = ""
		return engineDriver(&c)
	}
	return engineDriver(conn)
}

// termLease 是一次执行借到的标签连接。Release 必须调用;Reconnected 在执行之后问"这条语句期间
// 连接有没有被换过"(断线、超时取消后驱动丢弃了旧连接)。
type termLease struct {
	sess    *ExecSession
	release func()
	ts      *termSession
	before  int64
}

func (l *termLease) Release() { l.release() }

// Reconnected:从借到连接(切库之前)到现在,物理连接被重新建立过 —— 原来的会话已经没了,
// 没提交的事务被数据库回滚,会话变量、临时表也一起失效。
func (l *termLease) Reconnected() bool { return l.ts.opens.Load() > l.before }

// acquireTermDB 取 ctx 所属标签在这台实例上的专用连接,没有就开一条。ctx 上没有标签时返回 nil
// (调用方走连接池)。这台实例的独占连接已满时返回 errTermConnFull(调用方报错)。
func acquireTermDB(ctx context.Context, conn *model.Connection) (*termLease, error) {
	k, ok := ctx.Value(termCtxKey{}).(termKey)
	if !ok {
		return nil, nil
	}
	drv, dsn, ok := termDialKey(conn)
	if !ok {
		return nil, apimsg.Errorf("引擎 %q 未配置真实执行(需填写连接凭据)", conn.Engine)
	}
	key := drv + "\x00" + dsn

	termSessions.Lock()
	ts := termSessions.m[k]
	if ts == nil {
		mine, onConn := 0, 0
		for kk, other := range termSessions.m {
			if kk.userID == k.userID {
				mine++
			}
			if other.connID == conn.ID {
				onConn++
			}
		}
		if mine >= MaxTermSessionsPerUser {
			termSessions.Unlock()
			return nil, ErrTermSessionLimit
		}
		if onConn >= MaxTermSessionsPerConn {
			termSessions.Unlock()
			return nil, errTermConnFull
		}
		ts = &termSession{sem: make(chan struct{}, 1), connID: conn.ID, key: key, opens: new(atomic.Int64)}
		termSessions.m[k] = ts
	}
	ts.busy++
	termSessions.Unlock()

	release := func() {
		termSessions.Lock()
		ts.busy--
		ts.lastUsed = time.Now()
		termSessions.Unlock()
	}
	select {
	case ts.sem <- struct{}{}:
	case <-ctx.Done():
		release()
		return nil, ctx.Err()
	}
	unlock := func() { <-ts.sem; release() }

	// 排队的时候标签被关了:不再执行,也绝不再新建连接 —— 否则这条连接脱离了登记表,谁也关不掉,
	// 里面开着的事务会一直持锁(2026-10 审查)。
	termSessions.Lock()
	closed := ts.closed
	termSessions.Unlock()
	if closed {
		unlock()
		return nil, ErrTermSessionClosed
	}

	// 以下都在持有 sem 时做:同一标签并发的两次请求不会各开一条,也不会一边换连接一边在用。
	if ts.sess != nil && (ts.connID != conn.ID || ts.key != key) {
		// 换了实例,或者 PostgreSQL 换了库:旧连接不再适用,断开重来(这是人自己切的,不算"断线")。
		_ = ts.sess.Close()
		ts.sess = nil
	}
	ts.key = key
	termSessions.Lock() // connID 还被按实例计数的地方在登记表锁下读
	ts.connID = conn.ID
	termSessions.Unlock()
	if ts.sess == nil {
		ts.opens.Store(0)
		db, err := dialTermDB(drv, dsn, ts.opens)
		if err != nil {
			unlock()
			return nil, err
		}
		ts.sess = &ExecSession{key: key, db: db}
	}
	before := ts.opens.Load()
	// MySQL 系:切到这次执行的目标库。每次都发(一次往返):连接万一被驱动换过(断线重连),
	// 新连接上没有先前的 USE,只在库名变化时才发就会默默落到错误的库上。
	if engineFamily(conn.Engine) == familyMySQL && conn.Database != "" {
		if _, err := ts.sess.db.ExecContext(ctx, "USE `"+strings.ReplaceAll(conn.Database, "`", "``")+"`"); err != nil {
			unlock()
			return nil, err
		}
	}
	// 交给 RealRun 的视图:key 按这次执行的完整 dsn,sessionDB 才认得它。不负责关闭。
	full := ts.sess.key
	if d, s, ok := engineDriver(conn); ok {
		full = d + "\x00" + s
	}
	return &termLease{sess: &ExecSession{key: full, db: ts.sess.db}, release: unlock, ts: ts, before: before}, nil
}

// shutdownTerm 关掉一个已从登记表摘下、标成 closed 的标签:先拿到它的 sem(等手上那条语句跑完、
// 排队的请求看到 closed 自行放弃),再关连接。在后台做,关标签 / 登出的请求立刻返回。
func shutdownTerm(ts *termSession) {
	go func() {
		ts.sem <- struct{}{}
		if ts.sess != nil {
			_ = ts.sess.Close()
			ts.sess = nil
		}
		<-ts.sem
	}()
}

// CloseTermSession 关掉一个标签页的连接(未提交的事务由数据库回滚)。正在执行的那条语句跑完再关。
func CloseTermSession(userID int64, id string) {
	termSessions.Lock()
	ts := termSessions.m[termKey{userID, id}]
	delete(termSessions.m, termKey{userID, id})
	if ts != nil {
		ts.closed = true
	}
	termSessions.Unlock()
	if ts != nil {
		shutdownTerm(ts)
	}
}

// CloseTermSessionsOf 关掉一个用户全部标签页的连接(登出、空闲锁定)。返回关掉的标签数。
func CloseTermSessionsOf(userID int64) int {
	termSessions.Lock()
	var closing []*termSession
	for k, ts := range termSessions.m {
		if k.userID == userID {
			delete(termSessions.m, k)
			ts.closed = true
			closing = append(closing, ts)
		}
	}
	termSessions.Unlock()
	for _, ts := range closing {
		shutdownTerm(ts)
	}
	return len(closing)
}

// SweepTermSessions 关掉空闲超过 idle 的标签连接;正在执行或排队的跳过。返回关掉的标签数。
func SweepTermSessions(idle time.Duration, now time.Time) int {
	termSessions.Lock()
	var closing []*termSession
	for k, ts := range termSessions.m {
		if ts.busy == 0 && !ts.lastUsed.IsZero() && now.Sub(ts.lastUsed) > idle {
			delete(termSessions.m, k)
			ts.closed = true
			closing = append(closing, ts)
		}
	}
	termSessions.Unlock()
	for _, ts := range closing {
		shutdownTerm(ts)
	}
	return len(closing)
}

// TermSessionCount:当前登记的标签连接数(userID 为 0 表示全部)。测试与排查用。
func TermSessionCount(userID int64) int {
	termSessions.Lock()
	defer termSessions.Unlock()
	n := 0
	for k := range termSessions.m {
		if userID == 0 || k.userID == userID {
			n++
		}
	}
	return n
}
