package gateway

import (
	"context"
	"strings"
	"testing"
	"time"

	"aegisdb/internal/model"
)

// 2026-10-29:数据查询放开的 SHOW 表结构白名单与 DESC,在真实 MySQL 的只读事务里能跑出结果。
func TestMySQLBizQuery_ShowAndDesc(t *testing.T) {
	_, done := bizSetup(t)
	defer done()
	conn := testMySQL(t)
	conn.Database = "biztest"
	for _, q := range []string{
		"SHOW CREATE TABLE nums",
		"SHOW FULL COLUMNS FROM nums",
		"SHOW INDEX FROM nums",
		"SHOW TABLES",
		"SHOW TABLES FROM biztest LIKE 'nu%'",
		"SHOW TABLE STATUS LIKE 'nums'",
		"DESC nums",
		"DESCRIBE biztest.nums n",
	} {
		if r := BizReadOnlyCheck(conn.Engine, q, "biztest", []string{"biztest", "mysql"}); r != nil {
			t.Fatalf("%q 判定应放行,实际 %s", q, model.RenderBizReason(r))
		}
		res, err := RealBizQuery(context.Background(), conn, q, 100, 5*time.Second)
		if err != nil || len(res.Data) == 0 {
			t.Errorf("%q 应返回结果,实际 %d 行 err=%v", q, len(res.Data), err)
		}
		if q == "SHOW CREATE TABLE nums" && (len(res.Data) != 1 || !strings.Contains(res.Data[0][1], "CREATE TABLE")) {
			t.Errorf("SHOW CREATE TABLE 应返回建表语句,实际 %v", res.Data)
		}
	}
}
