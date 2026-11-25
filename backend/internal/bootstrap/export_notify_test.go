package bootstrap

// 异步导出跑完要通知提交它的人。
//
// 提交完导出,人就离开那个页面了 —— 结果什么时候出来他并不知道。不主动告诉他,
// 他只能靠自己想起来回去刷一下,而"想起来"这件事在忙起来的时候是不发生的。
//
// 失败尤其不能只写进库里:成功还能靠"文件出现了"发现,失败没有任何外部信号,
// 人会一直等一个永远不会来的文件。

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// waitForNotif 等一条指定类型的通知出现。导出是后台跑的,断言必须等它跑完 ——
// 直接断言会稳定地在"还没跑完"那一刻失败。
func (a *testApp) waitForNotif(token, typ string, within time.Duration) (notifItem, bool) {
	a.t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		for _, n := range a.notifications(token).Items {
			if n.Type == typ {
				return n, true
			}
		}
		time.Sleep(120 * time.Millisecond)
	}
	return notifItem{}, false
}

func TestExport_NotifiesOnCompletion(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	conn := app.realSQLiteConn(admin)
	if r := app.execSQL(admin, conn, `CREATE TABLE t_rows (id INTEGER PRIMARY KEY, v TEXT)`); r.Code != 0 {
		t.Fatalf("建表: %s", r.Msg)
	}
	if r := app.execSQL(admin, conn, `INSERT INTO t_rows (v) VALUES ('a'), ('b')`); r.Code != 0 {
		t.Fatalf("插入: %s", r.Msg)
	}

	r := app.do(http.MethodPost, "/api/v1/export", admin, map[string]any{
		"connectionId": conn, "sql": "SELECT * FROM t_rows", "name": "行数据",
	})
	if r.Code != 0 {
		t.Fatalf("提交导出: code=%d msg=%s", r.Code, r.Msg)
	}

	n, ok := app.waitForNotif(admin, "export-done", 15*time.Second)
	if !ok {
		t.Fatal("导出跑完了却没有通知 —— 提交的人已经离开页面,不告诉他就等于没完成")
	}
	if !strings.Contains(n.Body, "行数据") {
		t.Errorf("通知里应说清是哪个任务,实际: %q", n.Body)
	}
	if n.Read {
		t.Error("刚产生的通知不该是已读 —— 已读的不会弹给人看")
	}
}

func TestExport_NotifiesOnFailure(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	conn := app.realSQLiteConn(admin)

	// 查一张不存在的表:数据库会拒,任务落到失败分支。
	r := app.do(http.MethodPost, "/api/v1/export", admin, map[string]any{
		"connectionId": conn, "sql": "SELECT * FROM t_does_not_exist", "name": "注定失败",
	})
	if r.Code != 0 {
		t.Skipf("这条用例要求导出能被提交(提交阶段就拒了): %s", r.Msg)
	}

	n, ok := app.waitForNotif(admin, "export-failed", 15*time.Second)
	if !ok {
		t.Fatal("导出失败了却没有通知 —— 失败没有任何外部信号,人会一直等一个不会来的文件")
	}
	if !strings.Contains(n.Body, "注定失败") {
		t.Errorf("通知里应说清是哪个任务,实际: %q", n.Body)
	}
}

// 审批出结果时,发起人要收到通知(这条本来就有,这里把它钉住 —— 它和导出是同一类
// "人已经走开了,结果要追上去告诉他")。
func TestApproval_NotifiesTheInitiator(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	ap := app.submitProdHighRisk(admin) // linwei 发起
	approver := app.login("zhangwei@vela.io", "vela123")

	eq(t, app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/reject", approver, nil).Code, 0, "驳回")

	if _, ok := app.waitForNotif(admin, "approval-rejected", 5*time.Second); !ok {
		t.Error("审批被驳回后,发起人应收到通知")
	}
}
