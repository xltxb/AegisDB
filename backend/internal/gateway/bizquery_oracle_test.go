package gateway

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

// testOracle 见 oracle_serveroutput_test.go(AEGISDB_TEST_ORACLE=host:port/user/password/service)。

// Oracle 不改写行数(11g 不支持 FETCH FIRST),只靠读取时截断。要确认的是:读满 n 行之后关闭
// 结果集,驱动不会把剩下的行也拉完 —— 否则一条 5000 万行的查询会一直跑到授权时限。
func TestOracleBizQuery_RowCapStopsReading(t *testing.T) {
	conn := testOracle(t)
	start := time.Now()
	res, err := RealBizQuery(context.Background(), conn,
		"SELECT LEVEL AS n FROM dual CONNECT BY LEVEL <= 50000000", 10, 20*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Data) != 10 || !res.Truncated {
		t.Fatalf("应读 10 行并标记截断,实际 %d 行 truncated=%v", len(res.Data), res.Truncated)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("读满 10 行后应立即返回,实际耗时 %v(关闭结果集时驱动把剩下的行也拉了)", d)
	}
}

// 只读事务:哪怕一条写语句漏过了判定,Oracle 自己也会拒绝(ORA-01456)。表是真的,
// 失败只可能来自只读事务。
func TestOracleBizQuery_ReadOnlyTransaction(t *testing.T) {
	conn := testOracle(t)
	drv, dsn, _ := engineDriver(conn)
	db, err := sql.Open(drv, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, _ = db.Exec("DROP TABLE biz_ro_t")
	if _, err := db.Exec("CREATE TABLE biz_ro_t (n NUMBER)"); err != nil {
		t.Fatal(err)
	}
	defer db.Exec("DROP TABLE biz_ro_t") //nolint:errcheck

	_, err = RealBizQuery(context.Background(), conn, "INSERT INTO biz_ro_t VALUES (1)", 10, 5*time.Second)
	if err == nil || !strings.Contains(err.Error(), "ORA-01456") {
		t.Fatalf("只读事务应以 ORA-01456 拒绝写入,实际 %v", err)
	}
	var n int
	_ = db.QueryRow("SELECT COUNT(*) FROM biz_ro_t").Scan(&n)
	if n != 0 {
		t.Errorf("写入不该生效,表里有 %d 行", n)
	}
}

// Oracle 没有语句级时限,超时只能靠网关取消(go-ora 在 context 结束时发 break)。
func TestOracleBizQuery_TimeoutCancels(t *testing.T) {
	conn := testOracle(t)
	start := time.Now()
	_, err := RealBizQuery(context.Background(), conn,
		"SELECT COUNT(*) FROM all_objects a, all_objects b, all_objects c", 10, 2*time.Second)
	if err == nil {
		t.Fatal("超过时限应当报错")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("应当是超时取消,实际 %v", err)
	}
	if d := time.Since(start); d > 8*time.Second {
		t.Errorf("时限 2 秒,实际 %v 才返回:go-ora 没有按 context 取消", d)
	}
	t.Logf("超时返回耗时 %v,错误:%v", time.Since(start), err)
}

// 超时被取消的那条连接不能还回池子:go-ora 的 break 会在连接上留下 ORA-01013,下一个借到它的
// 人(可能是别人的终端语句)第一条就报"用户请求取消"。
func TestOracleBizQuery_TimeoutDoesNotPoisonThePool(t *testing.T) {
	conn := testOracle(t)
	if _, err := RealBizQuery(context.Background(), conn,
		"SELECT COUNT(*) FROM all_objects a, all_objects b, all_objects c", 10, time.Second); err == nil {
		t.Fatal("超过时限应当报错")
	}
	for i := 0; i < 3; i++ {
		if _, err := RealRun(context.Background(), conn, "SELECT 1 FROM dual", 5*time.Second); err != nil {
			t.Fatalf("超时之后第 %d 条普通语句不该失败,实际 %v", i+1, err)
		}
	}
}
