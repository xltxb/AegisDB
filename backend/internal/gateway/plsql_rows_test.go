package gateway

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// Oracle 的 PL/SQL 匿名块不报影响行数(驱动永远给 0):标成未知,而不是说"影响 0 行"。
// 普通 DML 照旧报真实行数。(2026-10 生产 AP-2559)
func TestRealRun_OraclePLSQLBlockRowsAreUnknown(t *testing.T) {
	conn := testOracle(t)
	tb := fmt.Sprintf("ZZ_ROWS_%d", time.Now().UnixNano()%1_000_000)
	run := func(q string) ExecResult {
		t.Helper()
		r, err := RealRun(context.Background(), conn, q, 30*time.Second)
		if err != nil {
			t.Fatalf("%q: %v", q, err)
		}
		return r
	}
	run("CREATE TABLE " + tb + " (id NUMBER)")
	t.Cleanup(func() { RealRun(context.Background(), conn, "DROP TABLE "+tb+" PURGE", 30*time.Second) })

	if r := run("INSERT INTO " + tb + " SELECT level FROM dual CONNECT BY level <= 3"); r.RowsUnknown || r.Rows != 3 {
		t.Errorf("普通 INSERT 应报 3 行且不是未知,实际 rows=%d unknown=%v", r.Rows, r.RowsUnknown)
	}
	for _, q := range []string{
		"BEGIN DELETE FROM " + tb + " WHERE id <= 2; INSERT INTO " + tb + " VALUES (9); COMMIT; END;",
		"DECLARE n NUMBER; BEGIN UPDATE " + tb + " SET id = id + 1; END;",
	} {
		if r := run(q); !r.RowsUnknown {
			t.Errorf("%.30q: PL/SQL 块的影响行数应标成未知,实际 rows=%d", q, r.Rows)
		}
	}
}
