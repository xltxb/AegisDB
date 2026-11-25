package presence

import (
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time      { return c.t }
func (c *clock) add(d time.Duration) { c.t = c.t.Add(d) }
func newAt(c *clock) *Tracker        { tr := New(); tr.now = c.now; return tr }
func visit(sid string, uid int64, path string) Visit {
	return Visit{SessionID: sid, UserID: uid, Name: "u", Path: path}
}

func TestPresence_PollKeepsOnlineButIsNotAnAction(t *testing.T) {
	c := &clock{time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)}
	tr := newAt(c)
	if !tr.Touch(visit("s1", 1, "/api/v1/connections")) {
		t.Error("第一次真实操作应当触发写库")
	}
	actionAt := c.t
	c.add(2 * time.Minute)
	if tr.Touch(visit("s1", 1, "/api/v1/gateway/stats")) {
		t.Error("自动轮询不算活跃,不写库")
	}
	on := tr.Online()
	if len(on) != 1 || !on[0].LastAction.Equal(actionAt) || !on[0].LastSeen.Equal(c.t) {
		t.Fatalf("轮询刷新 LastSeen,不动 LastAction: %+v", on)
	}
}

func TestPresence_PersistAtMostOncePerMinute(t *testing.T) {
	c := &clock{time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)}
	tr := newAt(c)
	tr.Touch(visit("s1", 1, "/a"))
	c.add(30 * time.Second)
	if tr.Touch(visit("s2", 1, "/b")) { // 同一个人另一个会话
		t.Error("同一个人一分钟内不该再写库")
	}
	c.add(31 * time.Second)
	if !tr.Touch(visit("s1", 1, "/c")) {
		t.Error("过了一分钟应当再写一次")
	}
}

func TestPresence_ExpiresButOpenTerminalKeepsOnline(t *testing.T) {
	c := &clock{time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)}
	tr := newAt(c)
	tr.Touch(visit("idle", 1, "/a"))
	tr.Touch(visit("term", 2, "/a"))
	tr.TermOpen("term", "ws1")
	tr.TermExec("ws1", 7, "prod-order", true)
	c.add(OnlineWindow + time.Second)
	on := tr.Online()
	if len(on) != 1 || on[0].SessionID != "term" {
		t.Fatalf("超过窗口的会话应当下线,开着终端的仍在线: %+v", on)
	}
	if tm := on[0].Terms; len(tm) != 1 || tm[0].Instance != "prod-order" || !tm[0].Running {
		t.Errorf("终端应带着实例与执行中: %+v", tm)
	}
	tr.TermClose("ws1")
	if len(tr.Online()) != 0 {
		t.Error("终端关了、也过了窗口,应当下线")
	}
}

func TestPresence_DropUserRemovesAllSessions(t *testing.T) {
	c := &clock{time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)}
	tr := newAt(c)
	tr.Touch(visit("a1", 1, "/x"))
	tr.Touch(visit("a2", 1, "/x"))
	tr.Touch(visit("b1", 2, "/x"))
	tr.TermOpen("a1", "ws")
	tr.DropUser(1)
	on := tr.Online()
	if len(on) != 1 || on[0].UserID != 2 {
		t.Fatalf("退出登录后这个人的会话应全部去掉: %+v", on)
	}
	tr.TermExec("ws", 1, "x", true) // 已去掉的终端,不该把会话复活
	if len(tr.Online()) != 1 {
		t.Error("去掉的会话不该被终端事件复活")
	}
}

func TestPresence_DropUserResetsPersistThrottle(t *testing.T) {
	c := &clock{time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)}
	tr := newAt(c)
	tr.Touch(visit("s1", 1, "/a"))
	tr.DropUser(1) // 退出登录
	c.add(10 * time.Second)
	if !tr.Touch(visit("s2", 1, "/a")) {
		t.Error("重新登录后的第一次操作应立刻写库,不受上一轮节流影响")
	}
}
