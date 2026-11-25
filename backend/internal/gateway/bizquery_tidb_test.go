package gateway

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"velagateway/internal/model"
)

// 2026-10-27 UAT-TIDB:数据查询全部报 Error 1235 "function READ ONLY has only noop implementation in tidb now"。
// 数据查询把每条查询包在 START TRANSACTION READ ONLY 里,TiDB 默认不接受。TiDB 上改为普通事务(只读由判定层
// 保证、事务永远回滚);引擎标签写成 MySQL 的 TiDB 靠报错识别后重开。MySQL 照旧走只读事务(见
// TestMySQLBizQuery_ReadOnlyTransactionRefusesWrites)。
func TestTiDBNoopReadOnlyRecognised(t *testing.T) {
	if !tidbNoopReadOnly(errors.New("Error 1235 (42000): function READ ONLY has only noop implementation in tidb now, use tidb_enable_noop_functions to enable these functions")) {
		t.Error("TiDB 的 READ ONLY 报错应被识别")
	}
	for _, e := range []error{nil, errors.New("Error 1045: Access denied"), errors.New("Error 1235: This version of MySQL doesn't yet support 'X'")} {
		if tidbNoopReadOnly(e) {
			t.Errorf("别的报错不该被当成 TiDB READ ONLY: %v", e)
		}
	}
}

// 需要一台真实 TiDB:VELA_TEST_TIDB=host:port/user/password(本地可用 docker run -p 14000:4000 pingcap/tidb)
func TestTiDBBizQuery_RunsWithoutReadOnlyTransaction(t *testing.T) {
	v := os.Getenv("VELA_TEST_TIDB")
	if v == "" {
		t.Skip("未设置 VELA_TEST_TIDB")
	}
	p := strings.SplitN(v, "/", 3)
	hp := strings.SplitN(p[0], ":", 2)
	port, _ := strconv.Atoi(hp[1])
	pass := ""
	if len(p) > 2 {
		pass = p[2]
	}
	admin, err := sql.Open("mysql", fmt.Sprintf("%s:%s@tcp(%s)/", p[1], pass, p[0]))
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	for _, s := range []string{
		"CREATE DATABASE IF NOT EXISTS biz_tidb",
		"DROP TABLE IF EXISTS biz_tidb.orders",
		"CREATE TABLE biz_tidb.orders (id INT PRIMARY KEY, billtime DATETIME)",
		"INSERT INTO biz_tidb.orders VALUES (1, '2026-10-27 08:00:00'), (2, '2026-10-27 09:00:00'), (3, '2026-10-28 09:00:00')",
	} {
		if _, err := admin.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	q := "SELECT * FROM orders WHERE billtime > '2026-10-27 00:00:00' AND billtime < '2026-10-28 00:00:00'"
	for _, engine := range []string{"TiDB 7.5", "MySQL 8.0"} { // 后者:引擎标签写成 MySQL 的 TiDB
		conn := &model.Connection{Name: "tidb", Engine: engine, Host: hp[0], Port: port, Username: p[1], Password: pass, Database: "biz_tidb"}
		res, err := RealBizQuery(context.Background(), conn, q, 100, 10*time.Second)
		if err != nil {
			t.Fatalf("[%s] TiDB 上的只读查询应能执行,实际 %v", engine, err)
		}
		if len(res.Data) != 2 {
			t.Errorf("[%s] 应查到 2 行,实际 %d", engine, len(res.Data))
		}
		// 2026-10-29:SHOW 表结构白名单与 DESC 在 TiDB 上照样能跑
		for _, sq := range []string{"SHOW CREATE TABLE orders", "SHOW COLUMNS FROM orders", "SHOW INDEX FROM orders", "SHOW TABLES", "SHOW TABLE STATUS", "DESC orders"} {
			if r := BizReadOnlyCheck(engine, sq, "biz_tidb", []string{"biz_tidb", "mysql"}); r != nil {
				t.Fatalf("[%s] %q 判定应放行,实际 %s", engine, sq, model.RenderBizReason(r))
			}
			if res, err := RealBizQuery(context.Background(), conn, sq, 100, 10*time.Second); err != nil || len(res.Data) == 0 {
				t.Errorf("[%s] %q 应返回结果,实际 %d 行 err=%v", engine, sq, len(res.Data), err)
			}
		}
	}
}
