package bootstrap

// 审批单执行有自己的时限(gateway.approvalExecTimeout,默认 1 小时),与终端的执行超时分开。
//
// 终端是交互式的,默认 30 秒;审批单里常是大表上的 DDL,用终端那 30 秒会跑到一半被取消。
// 用真实 MySQL:SQLite 的聚合查询取消不掉,拿它测超时测不出东西。

import (
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func (a *testApp) mysqlProdConn(token string) int64 {
	a.t.Helper()
	v := os.Getenv("AEGISDB_TEST_MYSQL") // host:port/user/password
	if v == "" {
		a.t.Skip("未设置 AEGISDB_TEST_MYSQL")
	}
	p := strings.SplitN(v, "/", 3)
	raw, err := sql.Open("mysql", fmt.Sprintf("%s:%s@tcp(%s)/", p[1], p[2], p[0]))
	if err != nil {
		a.t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec("CREATE DATABASE IF NOT EXISTS aegisdb_aptimeout"); err != nil {
		a.t.Fatal(err)
	}
	return a.addConn(token, "aptimeout-prod", "MySQL 8.0", "prod", p[0], "aegisdb_aptimeout", p[1], p[2])
}

// 发起 → 通过 → 执行,返回工单最后的样子与执行花了多久。
func (a *testApp) runApprovedTimed(token string, connID int64, cmd string) (apOutcome, time.Duration) {
	a.t.Helper()
	ap := a.interceptedTicket(token, connID, cmd)
	approver := a.login("zhangwei@aegisdb.io", "aegisdb123")
	if r := a.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/approve", approver, nil); r.Code != 0 {
		a.t.Fatalf("审批通过: %s", r.Msg)
	}
	start := time.Now()
	if r := a.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/execute", token, nil); r.Code != 0 {
		a.t.Fatalf("执行: code=%d msg=%s", r.Code, r.Msg)
	}
	return a.ticketByNo(token, ap.ApNo), time.Since(start)
}

// DROP 让这一批被拦下走审批;SLEEP(3) 是那条"跑得久"的语句
const slowTicket = "DROP TABLE IF EXISTS aegisdb_aptimeout.t_never; SELECT SLEEP(3)"

func TestApprovalExecTimeout_AppliesToApprovedTickets(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	conn := app.mysqlProdConn(admin)
	// 终端时限放宽,只把审批时限收到 1 秒:超时必须是审批时限造成的
	app.setSettings(admin, map[string]any{"gateway.execTimeout": 30, "gateway.approvalExecTimeout": 1})

	after, took := app.runApprovedTimed(admin, conn, slowTicket)
	if after.ExecStatus != "failed" || !strings.Contains(after.Result, "超时") {
		t.Errorf("审批时限 1 秒,SLEEP(3) 应超时失败: status=%q result=%q", after.ExecStatus, after.Result)
	}
	if took > 2500*time.Millisecond {
		t.Errorf("应按审批时限(1 秒)取消,实际等了 %v", took)
	}
}

func TestApprovalExecTimeout_IndependentOfTerminalTimeout(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	conn := app.mysqlProdConn(admin)
	// 终端时限只有 1 秒,审批单照样能跑完 3 秒的语句
	app.setSettings(admin, map[string]any{"gateway.execTimeout": 1, "gateway.approvalExecTimeout": 30})

	after, took := app.runApprovedTimed(admin, conn, slowTicket)
	if after.ExecStatus != "success" {
		t.Errorf("审批时限 30 秒,不该受终端 1 秒时限影响: status=%q result=%q", after.ExecStatus, after.Result)
	}
	if took < 2500*time.Millisecond {
		t.Errorf("前提不成立:SLEEP(3) 应当真的跑了约 3 秒,实际 %v", took)
	}
}
