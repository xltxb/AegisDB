package bootstrap

// 在线变更任务的运行器(.scratch/osc-product 工单 02):跑完、同表互斥、暂停后中止、重启收尾。
// 对着真 MySQL(AEGISDB_OSC_TEST_DSN)。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"aegisdb/internal/model"
	"aegisdb/internal/osc"
	"aegisdb/internal/service"
)

func oscRaw(t *testing.T, schema string) *sql.DB {
	t.Helper()
	cfg, _ := mysql.ParseDSN(os.Getenv("AEGISDB_OSC_TEST_DSN"))
	cfg.DBName = schema
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func oscHasTable(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var n int
	db.QueryRow("SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?", name).Scan(&n)
	return n > 0
}

func oscHasIndex(t *testing.T, db *sql.DB, table, index string) bool {
	t.Helper()
	var n int
	db.QueryRow("SELECT COUNT(*) FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND INDEX_NAME = ?", table, index).Scan(&n)
	return n > 0
}

// newOSCJob 照终端的路子建一个任务:判定 → 前置检查 → 记下。
func (a *testApp) newOSCJob(connID int64, stmt string) *model.OSCJob {
	a.t.Helper()
	conn, _ := a.repo.GetConnection(connID)
	d := a.svc.OSCRoute(context.Background(), conn, stmt)
	if d.Kind != service.OSCOnline {
		a.t.Fatalf("%q 应判为在线变更: %+v", stmt, d)
	}
	blockers, warnings, err := a.svc.OSCPreflight(context.Background(), conn, d.Schema, d.Table, d.Parsed.Change)
	if err != nil || len(blockers) > 0 {
		a.t.Fatalf("前置检查: err=%v blockers=%+v", err, blockers)
	}
	u, _ := a.repo.GetUserByEmail("linwei@aegisdb.io")
	j, err := a.svc.NewOSCJob(u, conn, stmt, d, warnings)
	if err != nil {
		a.t.Fatal(err)
	}
	return j
}

func (a *testApp) waitOSC(id int64, done func(*model.OSCJob) bool) *model.OSCJob {
	a.t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		j, _ := a.repo.GetOSCJob(id)
		if done(j) {
			return j
		}
		if time.Now().After(deadline) {
			a.t.Fatalf("等任务超时: %+v", j)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func oscFinished(j *model.OSCJob) bool {
	return j.Status == model.OSCDone || j.Status == model.OSCFailed || j.Status == model.OSCAborted
}

func (a *testApp) oscSetup(chunk int) (prod int64, raw *sql.DB) {
	a.t.Helper()
	admin := a.login("linwei@aegisdb.io", "aegisdb123")
	prod, _, schema := a.oscTestConns(admin)
	eq(a.t, a.do(http.MethodPut, "/api/v1/settings", admin, map[string]any{
		"osc.minRows": 100, "osc.minBytes": 1 << 40, "osc.chunkSize": chunk}).Code, 0, "设阈值")
	return prod, oscRaw(a.t, schema)
}

func TestOSCJob_RunsToCompletion(t *testing.T) {
	app := newTestApp(t)
	prod, raw := app.oscSetup(50)
	j := app.newOSCJob(prod, "ALTER TABLE big ADD INDEX idx_a (a)")

	finished := make(chan *model.OSCJob, 1)
	if err := app.svc.StartOSCJob(j.ID, func(f *model.OSCJob, _ error) { finished <- f }); err != nil {
		t.Fatal(err)
	}
	var f *model.OSCJob
	select {
	case f = <-finished:
	case <-time.After(90 * time.Second):
		t.Fatal("迁移没有结束")
	}
	if f.Status != model.OSCDone || f.OldTable != "_big_del" || f.RowsCopied < 300 || f.Phase != "done" || f.FinishedAt == nil {
		t.Fatalf("应成功并落下进度与旧表名: %+v", f)
	}
	if !oscHasIndex(t, raw, "big", "idx_a") || !oscHasTable(t, raw, "_big_del") || oscHasTable(t, raw, "_big_gho") {
		t.Error("新索引应在原表上,旧表 _big_del 保留,影子表不在")
	}
	if err := app.svc.StartOSCJob(j.ID, nil); err == nil || !strings.Contains(err.Error(), "执行过") {
		t.Errorf("一个任务只能执行一次: %v", err)
	}
}

func TestOSCJob_OneMigrationPerTable(t *testing.T) {
	app := newTestApp(t)
	prod, _ := app.oscSetup(50)
	a := app.newOSCJob(prod, "ALTER TABLE big ADD INDEX idx_a (a)")
	b := app.newOSCJob(prod, "ALTER TABLE big ADD INDEX idx_b (b)")
	// 装出 a 正在跑
	if err := app.repo.UpdateOSCJob(a.ID, map[string]any{"status": model.OSCRunning}); err != nil {
		t.Fatal(err)
	}
	if err := app.svc.StartOSCJob(b.ID, nil); !errors.Is(err, service.ErrOSCBusy) {
		t.Errorf("同一张表上已有在跑的,应拒绝: %v", err)
	}
	app.repo.UpdateOSCJob(a.ID, map[string]any{"status": model.OSCFailed})
}

func TestOSCJob_PauseThenAbortLeavesTheTableAlone(t *testing.T) {
	app := newTestApp(t)
	prod, raw := app.oscSetup(1) // 一行一块:拷贝慢到来得及暂停
	j := app.newOSCJob(prod, "ALTER TABLE big ADD INDEX idx_a (a)")
	if err := app.svc.StartOSCJob(j.ID, nil); err != nil {
		t.Fatal(err)
	}
	app.waitOSC(j.ID, func(x *model.OSCJob) bool { return x.Phase == "copying" })
	if err := app.svc.PauseOSCJob(j.ID); err != nil {
		t.Fatal(err)
	}
	app.waitOSC(j.ID, func(x *model.OSCJob) bool { return x.Paused })
	if err := app.svc.AbortOSCJob(j.ID); err != nil {
		t.Fatal(err)
	}
	f := app.waitOSC(j.ID, oscFinished)
	if f.Status != model.OSCAborted || f.OldTable != "" {
		t.Fatalf("应为已中止: %+v", f)
	}
	if oscHasIndex(t, raw, "big", "idx_a") || oscHasTable(t, raw, "_big_gho") || oscHasTable(t, raw, "_big_del") {
		t.Error("中止后原表不动、影子表已清理")
	}
}

func TestOSCJob_RestartFailsRunningAndDropsTheGhost(t *testing.T) {
	app := newTestApp(t)
	prod, raw := app.oscSetup(50)
	j := app.newOSCJob(prod, "ALTER TABLE big ADD INDEX idx_a (a)")
	// 装出"上一个进程跑到一半":任务 running、影子表在
	app.repo.UpdateOSCJob(j.ID, map[string]any{"status": model.OSCRunning, "phase": "copying"})
	if _, err := raw.Exec("CREATE TABLE _big_gho LIKE big"); err != nil {
		t.Fatal(err)
	}
	app.svc.ReconcileOSCJobs()
	f := app.waitOSC(j.ID, func(x *model.OSCJob) bool { return strings.Contains(x.Error, "残留的影子表已删除") })
	if f.Status != model.OSCFailed || !strings.Contains(f.Error, "网关重启") {
		t.Fatalf("重启后应判失败并说明: %+v", f)
	}
	if oscHasTable(t, raw, "_big_gho") {
		t.Error("残留的影子表应被删掉")
	}
}

// 前置检查的警告要以界面认得的形状存下来:{code, reason} 小写。曾经直接存 osc.Warning,
// 落库成大写的 Code / Reason,审批页的进度面板只剩一个没有字的 ⚠。
func TestOSCJob_WarningsAreStoredInTheShapeTheUIReads(t *testing.T) {
	app := newTestApp(t)
	prod, _ := app.oscSetup(50)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	conn, _ := app.repo.GetConnection(prod)
	stmt := "ALTER TABLE big ADD INDEX idx_a (a)"
	d := app.svc.OSCRoute(context.Background(), conn, stmt)
	u, _ := app.repo.GetUserByEmail("linwei@aegisdb.io")
	j, err := app.svc.NewOSCJob(u, conn, stmt, d, []osc.Warning{{Code: "long_transaction", Reason: "当前有已运行 60 秒的事务"}})
	if err != nil {
		t.Fatal(err)
	}
	var view struct {
		Warnings string `json:"warnings"`
	}
	_ = json.Unmarshal(app.do(http.MethodGet, "/api/v1/osc/jobs/"+itoa(j.ID), admin, nil).Data, &view)
	var items []struct {
		Code   string `json:"code"`
		Reason string `json:"reason"`
	}
	// Go 的 json.Unmarshal 认字段名不分大小写,前端的 JSON.parse 分 —— 只解出来不够,要看原文的键名
	if !strings.Contains(view.Warnings, `"code":`) || !strings.Contains(view.Warnings, `"reason":`) {
		t.Errorf("键名必须是小写 code / reason(界面按它取字),实际 %q", view.Warnings)
	}
	if err := json.Unmarshal([]byte(view.Warnings), &items); err != nil || len(items) != 1 ||
		items[0].Code != "long_transaction" || items[0].Reason != "当前有已运行 60 秒的事务" {
		t.Errorf("警告应存成 [{code, reason}],实际 %q", view.Warnings)
	}
	app.repo.UpdateOSCJob(j.ID, map[string]any{"status": model.OSCFailed})
}
