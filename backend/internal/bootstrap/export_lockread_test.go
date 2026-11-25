package bootstrap

// 2026-10 黑盒测试(.scratch/blackbox-fixes/issues/04-data-tools.md 第 4 条):导出原先放行
// SELECT … FOR UPDATE / LOCK IN SHARE MODE / SLEEP() / GET_LOCK(),在生产库上实打实持住了行锁。
// 导出与业务查询同口径(gateway.ReadSideEffect)拒绝;正常导出不受影响。

import (
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"

	"velagateway/pkg/resp"
)

// 加锁读、睡眠 / 加锁函数(含反引号 / 双引号包着的函数名)
var exportSideEffectSQL = []string{
	"SELECT * FROM orders FOR UPDATE",
	"SELECT * FROM orders WHERE id = 1 FOR SHARE",
	"SELECT * FROM orders LOCK IN SHARE MODE",
	"SELECT * FROM orders /*!50000 FOR UPDATE */",
	"SELECT SLEEP(5)",
	"SELECT id, SLEEP(1) FROM orders",
	"SELECT `sleep`(3)",
	"SELECT GET_LOCK('vela', 10)",
	"SELECT `get_lock`('vela', 10)",
	`SELECT "pg_sleep"(2)`,
	"SELECT BENCHMARK(100000000, MD5('a'))",
}

// 典型的正常导出:JOIN、子查询、CTE、聚合与常用函数
var exportNormalSQL = []string{
	"SELECT o.id, u.name FROM orders o JOIN users u ON u.id = o.user_id WHERE o.status = 'paid'",
	"SELECT REPLACE(name, ' ', ''), name || '-' || tier FROM users",
	"SELECT * FROM orders WHERE user_id IN (SELECT id FROM users WHERE tier = 'vip')",
	"WITH t AS (SELECT user_id, SUM(amount) s FROM orders GROUP BY user_id) SELECT * FROM t ORDER BY s DESC",
	"SELECT * FROM orders WHERE status = 'for update sleep(1)'",
}

func TestExport_RejectsLockingReadsAndSideEffectFuncs(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	dev := app.connIDByEnv(token, "dev")
	eq(t, app.do(http.MethodPut, "/api/v1/settings", token, map[string]any{"export.savePath": t.TempDir()}).Code, 0, "set path")

	for _, s := range exportSideEffectSQL {
		r := app.do(http.MethodPost, "/api/v1/export", token, map[string]any{"connectionId": dev, "sql": s, "name": "evil"})
		if r.Code != resp.CodeForbidden {
			t.Errorf("导出 %q 应当拒绝(CodeForbidden),实际 code=%d msg=%s", s, r.Code, r.Msg)
		}
	}
	for _, s := range exportNormalSQL {
		r := app.do(http.MethodPost, "/api/v1/export", token, map[string]any{"connectionId": dev, "sql": s, "name": "rpt"})
		if r.Code != 0 {
			t.Errorf("正常导出 %q 应当接受,实际 code=%d msg=%s", s, r.Code, r.Msg)
		}
	}
}

// 真实 MySQL:加锁读与睡眠函数在提交时就被拒;含 JOIN / DATE_FORMAT / REPLACE / CONCAT / 子查询 /
// CTE 的正常导出真跑完。
func TestExport_MySQLLockingReadRefusedNormalRuns(t *testing.T) {
	v := os.Getenv("VELA_TEST_MYSQL")
	if v == "" {
		t.Skip("未设置 VELA_TEST_MYSQL")
	}
	p := strings.SplitN(v, "/", 3)
	raw, err := sql.Open("mysql", fmt.Sprintf("%s:%s@tcp(%s)/", p[1], p[2], p[0]))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	for _, s := range []string{
		"CREATE DATABASE IF NOT EXISTS vela_bbexport",
		"CREATE TABLE IF NOT EXISTS vela_bbexport.users (id INT PRIMARY KEY, name VARCHAR(32), tier VARCHAR(8))",
		"CREATE TABLE IF NOT EXISTS vela_bbexport.orders (id INT PRIMARY KEY, user_id INT, amount INT, status VARCHAR(8), created_at DATETIME)",
		"REPLACE INTO vela_bbexport.users VALUES (1, 'a b', 'vip'), (2, 'c', 'std')",
		"REPLACE INTO vela_bbexport.orders VALUES (1, 1, 10, 'paid', '2026-10-01 10:00:00'), (2, 2, 20, 'paid', '2026-10-02 11:00:00')",
	} {
		if _, err := raw.Exec(s); err != nil {
			t.Fatal(err)
		}
	}

	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	conn := app.addConn(token, "bbexport-mysql", "MySQL 8.0", "dev", p[0], "vela_bbexport", p[1], p[2])
	eq(t, app.do(http.MethodPut, "/api/v1/settings", token, map[string]any{"export.savePath": t.TempDir()}).Code, 0, "set path")

	for _, s := range []string{
		"SELECT * FROM orders FOR UPDATE",
		"SELECT * FROM orders LOCK IN SHARE MODE",
		"SELECT * FROM orders FOR SHARE",
		"SELECT SLEEP(5)",
		"SELECT `sleep`(5)",
		"SELECT GET_LOCK('vela_bb', 10)",
	} {
		r := app.do(http.MethodPost, "/api/v1/export", token, map[string]any{"connectionId": conn, "sql": s, "name": "evil"})
		if r.Code != resp.CodeForbidden {
			t.Errorf("导出 %q 应当拒绝,实际 code=%d msg=%s", s, r.Code, r.Msg)
		}
	}
	for _, s := range []string{
		"SELECT o.id, u.name, DATE_FORMAT(o.created_at, '%Y-%m-%d') AS d FROM orders o JOIN users u ON u.id = o.user_id",
		"SELECT REPLACE(name, ' ', ''), CONCAT(name, '-', tier) FROM users",
		"SELECT * FROM orders WHERE user_id IN (SELECT id FROM users WHERE tier = 'vip')",
		"WITH t AS (SELECT user_id, SUM(amount) s FROM orders GROUP BY user_id) SELECT * FROM t ORDER BY s DESC",
	} {
		j := app.waitJob(token, app.submitExport(token, conn, s, "rpt"))
		if j.Status != "done" || j.Rows == 0 {
			t.Errorf("正常导出 %q 应当跑完,实际 status=%s rows=%d err=%s", s, j.Status, j.Rows, j.Error)
		}
	}
}
