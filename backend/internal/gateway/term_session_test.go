package gateway

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"velagateway/internal/model"
)

// Web 命令行每个标签页独占一条连接:同一台实例开两个标签就是两个会话,标签里的语句始终在
// 自己那个会话上(见 term_session.go)。

func termRun(t *testing.T, userID int64, tab string, conn *model.Connection, q string) ExecResult {
	t.Helper()
	res, err := RealRun(WithTermSession(context.Background(), userID, tab), conn, q, 30*time.Second)
	if err != nil {
		t.Fatalf("[%s] %q: %v", tab, q, err)
	}
	return res
}

func TestTermSession_EachTabIsItsOwnSessionOnMySQL(t *testing.T) {
	base, admin := testMySQLConn(t)
	conn := base
	const uid = 900001
	t.Cleanup(func() { CloseTermSessionsOf(uid) })

	// 两个标签:连接号不同;同一个标签里多条语句:连接号不变
	a1 := firstCell(t, termRun(t, uid, "tab-a", conn, "SELECT CONNECTION_ID()"))
	b1 := firstCell(t, termRun(t, uid, "tab-b", conn, "SELECT CONNECTION_ID()"))
	a2 := firstCell(t, termRun(t, uid, "tab-a", conn, "SELECT CONNECTION_ID()"))
	if a1 == b1 {
		t.Fatalf("两个标签应是两条连接,实际都是 %s", a1)
	}
	if a1 != a2 {
		t.Fatalf("同一个标签应一直用同一条连接,实际 %s → %s", a1, a2)
	}

	// 会话变量只在自己的标签里
	termRun(t, uid, "tab-a", conn, "SET @vela_tab = 'A'")
	if got := firstCell(t, termRun(t, uid, "tab-a", conn, "SELECT COALESCE(@vela_tab, 'none')")); got != "A" {
		t.Errorf("标签 A 的会话变量应还在,实际 %q", got)
	}
	if got := firstCell(t, termRun(t, uid, "tab-b", conn, "SELECT COALESCE(@vela_tab, 'none')")); got != "none" {
		t.Errorf("会话变量不该漏到标签 B,实际 %q", got)
	}

	// 切库(USE)不换连接,会话还在
	other := *base
	other.Database = "information_schema"
	if got := firstCell(t, termRun(t, uid, "tab-a", &other, "SELECT CONCAT(DATABASE(), ':', CONNECTION_ID(), ':', COALESCE(@vela_tab, 'none'))")); got != "information_schema:"+a1+":A" {
		t.Errorf("切库应在同一条连接上 USE,会话保留;实际 %q", got)
	}
	if got := firstCell(t, termRun(t, uid, "tab-a", conn, "SELECT DATABASE()")); got != "mysql" {
		t.Errorf("切回来应回到 mysql 库,实际 %q", got)
	}

	// 事务:标签 A 没提交的写,标签 B 看不到;关掉标签 A,事务回滚
	schema := fmt.Sprintf("term_tab_%d", time.Now().UnixNano()%1_000_000)
	if _, err := admin.Exec("CREATE DATABASE " + schema); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec("DROP DATABASE " + schema)
	if _, err := admin.Exec("CREATE TABLE " + schema + ".t (id INT PRIMARY KEY) ENGINE=InnoDB"); err != nil {
		t.Fatal(err)
	}
	termRun(t, uid, "tab-a", conn, "BEGIN")
	termRun(t, uid, "tab-a", conn, "INSERT INTO "+schema+".t VALUES (1)")
	if got := firstCell(t, termRun(t, uid, "tab-a", conn, "SELECT COUNT(*) FROM "+schema+".t")); got != "1" {
		t.Errorf("标签 A 自己应看得到未提交的行,实际 %s", got)
	}
	if got := firstCell(t, termRun(t, uid, "tab-b", conn, "SELECT COUNT(*) FROM "+schema+".t")); got != "0" {
		t.Errorf("标签 B 不该看到标签 A 未提交的行,实际 %s", got)
	}
	CloseTermSession(uid, "tab-a")
	var n int
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := admin.QueryRow("SELECT COUNT(*) FROM " + schema + ".t").Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n == 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if n != 0 {
		t.Errorf("关掉标签后未提交的事务应回滚,表里还有 %d 行", n)
	}
	// 关掉之后再用同一个标识:是一条新连接
	if a3 := firstCell(t, termRun(t, uid, "tab-a", conn, "SELECT CONNECTION_ID()")); a3 == a1 {
		t.Errorf("关掉后再执行应是新连接,实际还是 %s", a3)
	}
}

func TestTermSession_IsScopedToTheUser(t *testing.T) {
	base, _ := testMySQLConn(t)
	conn := base
	t.Cleanup(func() { CloseTermSessionsOf(900002); CloseTermSessionsOf(900003) })
	x := firstCell(t, termRun(t, 900002, "same-id", conn, "SELECT CONNECTION_ID()"))
	y := firstCell(t, termRun(t, 900003, "same-id", conn, "SELECT CONNECTION_ID()"))
	if x == y {
		t.Fatal("不同用户用了同一个标签标识,也不能拿到同一条连接")
	}
	CloseTermSession(900003, "same-id") // 别人关不掉我的
	if again := firstCell(t, termRun(t, 900002, "same-id", conn, "SELECT CONNECTION_ID()")); again != x {
		t.Fatalf("别的用户关同名标签不该影响我的连接:%s → %s", x, again)
	}
}

func TestTermSession_SweepClosesOnlyIdleOnes(t *testing.T) {
	base, _ := testMySQLConn(t)
	conn := base
	const uid = 900004
	t.Cleanup(func() { CloseTermSessionsOf(uid) })
	termRun(t, uid, "idle", conn, "SELECT 1")
	if TermSessionCount(uid) != 1 {
		t.Fatalf("应登记一条,实际 %d", TermSessionCount(uid))
	}
	if n := SweepTermSessions(time.Hour, time.Now()); TermSessionCount(uid) != 1 {
		t.Fatalf("没到空闲时长不该清掉(清了 %d)", n)
	}
	SweepTermSessions(time.Minute, time.Now().Add(2*time.Minute))
	if TermSessionCount(uid) != 0 {
		t.Fatal("空闲超时的标签连接应被清掉")
	}
}

func TestTermSession_PerUserLimit(t *testing.T) {
	base, _ := testMySQLConn(t)
	conn := base
	const uid = 900005
	t.Cleanup(func() { CloseTermSessionsOf(uid) })
	for i := range MaxTermSessionsPerUser {
		termRun(t, uid, fmt.Sprintf("t%d", i), conn, "SELECT 1")
	}
	_, err := RealRun(WithTermSession(context.Background(), uid, "one-too-many"), conn, "SELECT 1", 10*time.Second)
	if err == nil || !strings.Contains(err.Error(), "上限") {
		t.Fatalf("超过上限应拒绝并说明原因,实际 %v", err)
	}
	// 已有的标签照常能用
	termRun(t, uid, "t0", conn, "SELECT 1")
}

func TestTermSession_PostgresSwitchingDatabaseReconnects(t *testing.T) {
	base := testPostgres(t)
	conn := base
	const uid = 900006
	t.Cleanup(func() { CloseTermSessionsOf(uid) })
	p1 := firstCell(t, termRun(t, uid, "pg", conn, "SELECT pg_backend_pid()"))
	p2 := firstCell(t, termRun(t, uid, "pg", conn, "SELECT pg_backend_pid()"))
	q := firstCell(t, termRun(t, uid, "pg-2", conn, "SELECT pg_backend_pid()"))
	if p1 != p2 || p1 == q {
		t.Fatalf("同标签应同一后端进程、不同标签不同:%s %s / %s", p1, p2, q)
	}
	other := *base
	other.Database = "template1"
	if got := firstCell(t, termRun(t, uid, "pg", &other, "SELECT current_database()")); got != "template1" {
		t.Errorf("换库后应连到 template1,实际 %q", got)
	}
}

func TestTermSession_WithoutATabUsesThePoolAsBefore(t *testing.T) {
	base, _ := testMySQLConn(t)
	if _, err := RealRun(context.Background(), base, "SELECT 1", 10*time.Second); err != nil {
		t.Fatal(err)
	}
	if HasTermSession(WithTermSession(context.Background(), 1, "")) {
		t.Fatal("空标识不该被当成终端标签")
	}
	for _, bad := range []string{"", "a b", strings.Repeat("x", 65), "../x", "id;drop"} {
		if ValidTermSessionID(bad) {
			t.Errorf("%q 不该是合法的标签标识", bad)
		}
	}
	if !ValidTermSessionID("3f2b8c1e-9d4a-4f6e-8b2a-1c3d5e7f9a0b") {
		t.Error("UUID 应是合法的标签标识")
	}
}

// 2026-10 审查 #8:标签关掉时,排在后面的语句不再执行,也不会在关闭之后再新建一条连接 ——
// 那条连接脱离了登记表,谁也关不掉,里面的事务一直持锁。
func TestTermSession_CloseWhileQueuedRefusesAndLeavesNoConnection(t *testing.T) {
	base, _ := testMySQLConn(t)
	conn := base
	const uid = 900007
	t.Cleanup(func() { CloseTermSessionsOf(uid) })
	termRun(t, uid, "q", conn, "SELECT 1")
	termSessions.Lock()
	ts := termSessions.m[termKey{uid, "q"}]
	termSessions.Unlock()

	slow := make(chan error, 1)
	go func() {
		_, err := RealRun(WithTermSession(context.Background(), uid, "q"), conn, "SELECT SLEEP(1.5)", 30*time.Second)
		slow <- err
	}()
	time.Sleep(300 * time.Millisecond) // 让 SLEEP 先拿到标签
	queued := make(chan error, 1)
	go func() {
		_, err := RealRun(WithTermSession(context.Background(), uid, "q"), conn, "SELECT 2", 30*time.Second)
		queued <- err
	}()
	time.Sleep(200 * time.Millisecond) // 第二条在排队
	CloseTermSession(uid, "q")

	if err := <-slow; err != nil {
		t.Fatalf("正在执行的那条应跑完,实际 %v", err)
	}
	if err := <-queued; err == nil || !strings.Contains(err.Error(), "已关闭") {
		t.Fatalf("排队中的语句应因标签已关闭被拒,实际 %v", err)
	}
	// 后台关闭完成后,这个标签手上不该还有连接
	deadline := time.Now().Add(5 * time.Second)
	for {
		ts.sem <- struct{}{}
		gone := ts.sess == nil
		<-ts.sem
		if gone {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("标签关闭后连接没有被关掉")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if TermSessionCount(uid) != 0 {
		t.Fatalf("关掉的标签不该再登记,实际 %d", TermSessionCount(uid))
	}
}

// 2026-10 审查 #9:一台实例上所有人的独占连接加起来有上限。2026-10-26 讨论 11:满了之后新标签**报错**,
// 不再退回共享连接(那样它开的事务 / 锁 / 会话变量会漏给借到这条连接的别人)。
func TestTermSession_PerInstanceCapRefusesInsteadOfPool(t *testing.T) {
	base, _ := testMySQLConn(t)
	conn := base
	const firstUID = 910000
	t.Cleanup(func() {
		for i := range MaxTermSessionsPerConn + 1 {
			CloseTermSessionsOf(int64(firstUID + i))
		}
	})
	// 先把这台实例上别的测试留下的标签算进来
	termSessions.Lock()
	already := 0
	for _, ts := range termSessions.m {
		if ts.connID == conn.ID {
			already++
		}
	}
	termSessions.Unlock()
	for i := range MaxTermSessionsPerConn - already {
		res := termRun(t, int64(firstUID+i), "x", conn, "SELECT 1")
		if res.NoticeRef != nil {
			t.Fatalf("没满之前不该有提示,第 %d 条得到 %v", i, res.Notice)
		}
	}
	extra := int64(firstUID + MaxTermSessionsPerConn)
	if _, err := RealRun(WithTermSession(context.Background(), extra, "x"), conn, "SELECT 1", 30*time.Second); err == nil || !strings.Contains(err.Error(), "终端会话已满") {
		t.Fatalf("满了之后应报错并说明,实际 err=%v", err)
	}
	if TermSessionCount(extra) != 0 {
		t.Fatal("被拒的不该登记成独占标签")
	}
	// 腾出一个位置,新标签就又能拿到独占连接
	CloseTermSessionsOf(firstUID)
	if res := termRun(t, extra, "x", conn, "SELECT 1"); res.NoticeRef != nil {
		t.Fatalf("有空位后应拿到独占连接,实际提示 %q", res.Notice)
	}
	if TermSessionCount(extra) != 1 {
		t.Fatal("有空位后应登记成独占标签")
	}
}

// 2026-10 审查 #10:连接断了被驱动悄悄换成新的时,那条语句的回执要明说会话已重连(事务已回滚)。
func TestTermSession_ReconnectIsReported(t *testing.T) {
	base, admin := testMySQLConn(t)
	conn := base
	const uid = 900008
	t.Cleanup(func() { CloseTermSessionsOf(uid) })
	id := firstCell(t, termRun(t, uid, "k", conn, "SELECT CONNECTION_ID()"))
	if res := termRun(t, uid, "k", conn, "SELECT 1"); res.NoticeRef != nil {
		t.Fatalf("连接没断时不该提示,实际 %q", res.Notice)
	}
	if _, err := admin.Exec("KILL " + id); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	// 驱动可能先把断掉的连接报成一次错误,下一条才换新连接;两种都接受,但换了连接的那条必须带提示
	var res ExecResult
	var err error
	for range 2 {
		res, err = RealRun(WithTermSession(context.Background(), uid, "k"), conn, "SELECT CONNECTION_ID()", 30*time.Second)
		if err == nil {
			break
		}
	}
	if err != nil {
		t.Fatalf("重连后应能执行,实际 %v", err)
	}
	if got := firstCell(t, res); got == id {
		t.Fatalf("被 KILL 后应是新连接,实际还是 %s", got)
	}
	if res.NoticeRef == nil || res.NoticeRef.Code != "termSessionReset" {
		t.Fatalf("换了连接的那条应提示会话已重连,实际 notice=%q ref=%v", res.Notice, res.NoticeRef)
	}
	if res := termRun(t, uid, "k", conn, "SELECT 1"); res.NoticeRef != nil {
		t.Fatalf("重连之后的语句不该再提示,实际 %q", res.Notice)
	}
}
