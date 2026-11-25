// Package presence 记录"此刻谁在用网关":每个登录会话最近一次请求的时间、来源、在哪些实例上开着终端。
//
// 登录是无状态的 JWT,没有会话表 —— "谁在线"只能从请求里看出来。鉴权中间件每个请求顺手记一笔
// (Touch),终端 WebSocket 连上 / 执行 / 断开时各记一笔(TermOpen / TermExec / TermClose)。
//
// 只在内存里:进程重启就清空,几十秒之内各个开着的页面再次请求,又会出现在这里。多副本部署时
// 每个副本只看得到打到自己身上的请求。
//
// 两个时间分开记:
//   - LastSeen:最近一次任何请求。页面开着就会定时轮询(网关状态 5 秒、待办 20 秒),所以它回答
//     "页面开没开着";
//   - LastAction:最近一次**不是**自动轮询的请求,回答"人还在不在用" —— 一个挂了一整天没动的
//     标签页,LastSeen 一直是刚刚,LastAction 停在早上。
package presence

import (
	"sort"
	"strings"
	"sync"
	"time"
)

// OnlineWindow:这么久之内有过请求就算在线。后台标签页的定时器会被浏览器降到每分钟一次,
// 3 分钟留出余量。
const OnlineWindow = 3 * time.Minute

// persistEvery:同一个人的"最近活跃"多久写一次库(见 Touch 的返回值)。
const persistEvery = time.Minute

// pollPaths 是页面自己定时发的请求,不算"人在操作"。
var pollPaths = []string{"/gateway/stats", "/notifications", "/approvals/todo", "/auth/me"}

// Session 是一个登录会话(一次登录签发的 token,同一个浏览器的多个标签页共用)。
type Session struct {
	SessionID  string
	UserID     int64
	Name       string
	Email      string
	IP         string
	UserAgent  string
	LoginAt    time.Time
	LastSeen   time.Time
	LastAction time.Time
	LastPath   string
	terms      map[string]*Term
}

// Term 是一个开着的终端(一条 WebSocket,通常对应一个终端标签页)。
type Term struct {
	ID         string
	ConnID     int64
	Instance   string
	OpenedAt   time.Time
	LastExecAt time.Time
	Running    bool
}

// Snapshot 是一个在线会话此刻的样子(拷贝,调用方随便用)。
type Snapshot struct {
	Session
	Terms []Term
}

// Tracker 是在线记录。零值不可用,用 New。
type Tracker struct {
	mu          sync.Mutex
	sessions    map[string]*Session
	termOwner   map[string]string // 终端 id → 会话 id
	lastPersist map[int64]time.Time
	now         func() time.Time
}

func New() *Tracker {
	return &Tracker{sessions: map[string]*Session{}, termOwner: map[string]string{}, lastPersist: map[int64]time.Time{}, now: time.Now}
}

// Default 是进程里那一份。
var Default = New()

// Visit 是一次请求带来的信息。
type Visit struct {
	SessionID, Name, Email, IP, UserAgent, Path string
	UserID                                      int64
	LoginAt                                     time.Time
}

// Touch 记下一次请求。返回 true 表示该把这个人的"最近活跃"写回库了(每人每分钟至多一次)——
// 写库由调用方做,本包不碰数据库。自动轮询不算"活跃",不触发写库。
func (t *Tracker) Touch(v Visit) (persist bool) {
	if v.SessionID == "" || v.UserID == 0 {
		return false
	}
	now := t.now()
	action := !isPoll(v.Path)
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.sessions[v.SessionID]
	if s == nil {
		s = &Session{SessionID: v.SessionID, UserID: v.UserID, LoginAt: v.LoginAt, terms: map[string]*Term{}}
		t.sessions[v.SessionID] = s
	}
	s.Name, s.Email, s.IP, s.UserAgent = v.Name, v.Email, v.IP, v.UserAgent
	s.LastSeen = now
	if action {
		s.LastAction = now
		s.LastPath = v.Path
		if now.Sub(t.lastPersist[v.UserID]) >= persistEvery {
			t.lastPersist[v.UserID] = now
			persist = true
		}
	}
	return persist
}

func isPoll(path string) bool {
	for _, p := range pollPaths {
		if strings.HasSuffix(path, p) {
			return true
		}
	}
	return false
}

// TermOpen 记下一个开着的终端。会话要已经 Touch 过(WebSocket 握手前页面总是先请求过别的)。
func (t *Tracker) TermOpen(sessionID, termID string) {
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.sessions[sessionID]
	if s == nil {
		return
	}
	s.terms[termID] = &Term{ID: termID, OpenedAt: now}
	t.termOwner[termID] = sessionID
	s.LastSeen = now
}

// TermExec 记下终端上的一次执行开始(running=true)或结束(running=false)。
func (t *Tracker) TermExec(termID string, connID int64, instance string, running bool) {
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.sessions[t.termOwner[termID]]
	if s == nil {
		return
	}
	tm := s.terms[termID]
	if tm == nil {
		return
	}
	tm.ConnID, tm.Instance, tm.Running = connID, instance, running
	tm.LastExecAt = now
	s.LastSeen, s.LastAction = now, now
}

// TermClose 记下终端关闭。
func (t *Tracker) TermClose(termID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if s := t.sessions[t.termOwner[termID]]; s != nil {
		delete(s.terms, termID)
	}
	delete(t.termOwner, termID)
}

// DropUser 去掉一个人的全部会话:退出登录、停用、重置口令都会让他的 token 全部作废。
func (t *Tracker) DropUser(userID int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.lastPersist, userID) // 再登录进来的第一次操作立刻写"最近活跃",不等上一轮的节流
	for id, s := range t.sessions {
		if s.UserID == userID {
			for tid := range s.terms {
				delete(t.termOwner, tid)
			}
			delete(t.sessions, id)
		}
	}
}

// Online 返回此刻在线的会话:OnlineWindow 之内有过请求的,或者还开着终端的(终端的 WebSocket
// 连着就是在线,哪怕页面别的请求停了)。顺手清掉过期的记录。按最近操作倒序。
func (t *Tracker) Online() []Snapshot {
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	out := []Snapshot{}
	for id, s := range t.sessions {
		if now.Sub(s.LastSeen) > OnlineWindow && len(s.terms) == 0 {
			delete(t.sessions, id)
			continue
		}
		snap := Snapshot{Session: *s}
		snap.terms = nil
		for _, tm := range s.terms {
			snap.Terms = append(snap.Terms, *tm)
		}
		sort.Slice(snap.Terms, func(i, j int) bool { return snap.Terms[i].OpenedAt.Before(snap.Terms[j].OpenedAt) })
		out = append(out, snap)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i].LastAction, out[j].LastAction
		if !a.Equal(b) {
			return a.After(b)
		}
		return out[i].SessionID < out[j].SessionID
	})
	return out
}

// ShortUA 把 User-Agent 压成"浏览器 · 系统",给人看的,认不出就原样截短。
func ShortUA(ua string) string {
	browser := ""
	switch {
	case strings.Contains(ua, "Edg/"):
		browser = "Edge"
	case strings.Contains(ua, "Firefox/"):
		browser = "Firefox"
	case strings.Contains(ua, "Chrome/"):
		browser = "Chrome"
	case strings.Contains(ua, "Safari/"):
		browser = "Safari"
	}
	os := ""
	switch {
	case strings.Contains(ua, "Windows"):
		os = "Windows"
	case strings.Contains(ua, "Mac OS X"), strings.Contains(ua, "Macintosh"):
		os = "macOS"
	case strings.Contains(ua, "Android"):
		os = "Android"
	case strings.Contains(ua, "iPhone"), strings.Contains(ua, "iPad"):
		os = "iOS"
	case strings.Contains(ua, "Linux"):
		os = "Linux"
	}
	switch {
	case browser != "" && os != "":
		return browser + " · " + os
	case browser != "" || os != "":
		return browser + os
	}
	if r := []rune(ua); len(r) > 40 {
		return string(r[:40]) + "…"
	}
	return ua
}
