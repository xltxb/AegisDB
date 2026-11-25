package bootstrap

// 窗口越库(2026-10 黑盒,工单 01 第 1 条):在 A 库开窗,下面这些写法曾经免审批地真执行、
// 改动了同实例的 B 库。用真实 MySQL 证明:窗口照常放行 A 库里的语句,而这些越库写法一律
// 被拦成审批单,B 库的数据一行没动。

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

const (
	winDBA = "vela_win_a"
	winDBB = "vela_win_b"
)

// mysqlWindowFixture 在真实 MySQL 上备两个库,返回直连句柄与指向 A 库的 PROD 连接。
func (a *testApp) mysqlWindowFixture(token string) (*sql.DB, int64) {
	a.t.Helper()
	v := os.Getenv("VELA_TEST_MYSQL") // host:port/user/password
	if v == "" {
		a.t.Skip("未设置 VELA_TEST_MYSQL")
	}
	p := strings.SplitN(v, "/", 3)
	raw, err := sql.Open("mysql", fmt.Sprintf("%s:%s@tcp(%s)/", p[1], p[2], p[0]))
	if err != nil {
		a.t.Fatal(err)
	}
	a.t.Cleanup(func() { raw.Close() })
	for _, st := range []string{
		"DROP DATABASE IF EXISTS " + winDBA,
		"DROP DATABASE IF EXISTS " + winDBB,
		"CREATE DATABASE " + winDBA,
		"CREATE DATABASE " + winDBB,
		"CREATE TABLE " + winDBA + ".t1 (id INT PRIMARY KEY, v INT)",
		"INSERT INTO " + winDBA + ".t1 VALUES (1, 10)",
		"CREATE TABLE " + winDBA + ".mv (id INT)",
		"CREATE TABLE " + winDBB + ".t2 (id INT PRIMARY KEY, v INT)",
		"INSERT INTO " + winDBB + ".t2 VALUES (1, 100)",
		"CREATE TABLE " + winDBB + ".dropme2 (id INT)",
	} {
		if _, err := raw.Exec(st); err != nil {
			a.t.Fatalf("%s: %v", st, err)
		}
	}
	return raw, a.addConn(token, "win-escape-prod", "MySQL 8.0", "prod", p[0], winDBA, p[1], p[2])
}

func TestExecWindow_CannotWriteAnotherDatabaseOnRealMySQL(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	raw, conn := app.mysqlWindowFixture(admin)
	app.openWindow(admin, conn, winDBA, time.Now().Add(-time.Minute), time.Now().Add(time.Hour))

	exec := func(sqlText string) (approvalNo string, code int, msg string) {
		r := app.do(http.MethodPost, "/api/v1/terminal/exec", admin, map[string]any{
			"connectionId": conn, "sql": sqlText, "reason": "窗口越库用例", "database": winDBA,
		})
		var d struct {
			ApprovalNo string `json:"approvalNo"`
		}
		_ = json.Unmarshal(r.Data, &d)
		return d.ApprovalNo, r.Code, r.Msg
	}

	// 前提:窗口确实开着、而且是真执行 —— 否则下面"被拦"证明不了什么。
	if ap, code, msg := exec("UPDATE t1 SET v = 7 WHERE id = 1"); ap != "" || code != 0 {
		t.Fatalf("窗口库里的 UPDATE 应当免审批执行: ap=%q code=%d msg=%s", ap, code, msg)
	}
	var av int
	if err := raw.QueryRow("SELECT v FROM " + winDBA + ".t1 WHERE id = 1").Scan(&av); err != nil || av != 7 {
		t.Fatalf("前提不成立:窗口内的 UPDATE 应当真的改到了 A 库,v=%d err=%v", av, err)
	}

	for _, sqlText := range []string{
		"UPDATE " + winDBB + " . t2 SET v = 1",
		"UPDATE t1 a JOIN " + winDBB + ".t2 b ON a.id = b.id SET b.v = 2",
		"UPDATE t1, " + winDBB + ".t2 SET t2.v = 3",
		"DELETE b FROM t1 a JOIN " + winDBB + ".t2 b ON a.id = b.id",
		"REPLACE INTO " + winDBB + ".t2 VALUES (1, 4)",
		"RENAME TABLE mv TO " + winDBB + ".moved",
		"DROP TABLE mv, " + winDBB + ".dropme2",
		"PREPARE s FROM 'UPDATE " + winDBB + ".t2 SET v = 5'; EXECUTE s",
		"WITH x AS (SELECT 1 AS id) UPDATE " + winDBB + ".t2 JOIN x USING(id) SET v = 6",
		"INSERT INTO t1 SELECT id + 10, v FROM " + winDBB + ".t2",
	} {
		ap, code, msg := exec(sqlText)
		if ap == "" {
			t.Errorf("越库写法没有被拦成审批单: %q (code=%d msg=%s)", sqlText, code, msg)
		}
	}

	// B 库一行没动:t2 仍是 (1, 100),dropme2 还在,moved 没出现。
	var bid, bv int
	if err := raw.QueryRow("SELECT id, v FROM "+winDBB+".t2").Scan(&bid, &bv); err != nil || bid != 1 || bv != 100 {
		t.Errorf("B 库 t2 被改动了: id=%d v=%d err=%v", bid, bv, err)
	}
	var n int
	_ = raw.QueryRow("SELECT COUNT(*) FROM " + winDBB + ".t2").Scan(&n)
	eq(t, n, 1, "B 库 t2 行数")
	_ = raw.QueryRow("SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = ? AND table_name = 'dropme2'", winDBB).Scan(&n)
	eq(t, n, 1, "B 库 dropme2 应当还在")
	_ = raw.QueryRow("SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = ? AND table_name = 'moved'", winDBB).Scan(&n)
	eq(t, n, 0, "B 库不该出现 moved")
	_ = raw.QueryRow("SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = ? AND table_name = 'mv'", winDBA).Scan(&n)
	eq(t, n, 1, "A 库 mv 应当还在(RENAME / DROP 都被拦下)")
	_ = raw.QueryRow("SELECT COUNT(*) FROM " + winDBA + ".t1").Scan(&n)
	eq(t, n, 1, "A 库 t1 没有被灌进 B 库的数据")
}

// 同一组越库写法在 SQLite 夹具上也走一遍预检(不需要真库):窗口内,预检与执行同口径。
func TestExecWindow_RiskCheckRefusesCrossDatabaseForms(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	prod := app.connIDByEnv(token, "prod")
	const targetDB = "orders_db"
	app.openWindow(token, prod, targetDB, time.Now().Add(-time.Minute), time.Now().Add(time.Hour))

	for _, sqlText := range []string{
		"UPDATE some_other_db . t2 SET v = 1 WHERE id = 1",
		"UPDATE orders a JOIN some_other_db.t2 b ON a.id = b.id SET b.v = 2",
		"UPDATE orders, some_other_db.t2 SET t2.v = 3 WHERE orders.id = 1",
		"REPLACE INTO some_other_db.t2 VALUES (1, 4)",
		"DROP TABLE orders_2024_q3, some_other_db.dropme2",
		"PREPARE s FROM 'UPDATE some_other_db.t2 SET v = 5'; EXECUTE s",
	} {
		if !app.riskCheckIn(token, prod, sqlText, targetDB).RequiresApproval {
			t.Errorf("%q 越出窗口库,预检不该放行", sqlText)
		}
	}
	if app.riskCheckIn(token, prod, "UPDATE orders a JOIN order_items b ON a.id = b.order_id SET b.v = 1 WHERE a.id = 1", targetDB).RequiresApproval {
		t.Error("只触及窗口库的多表更新应当放行")
	}
}
