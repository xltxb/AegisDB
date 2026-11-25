package gateway

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"aegisdb/internal/model"
)

// 2026-10-29 实测:带 PRAGMA AUTONOMOUS_TRANSACTION 的函数在 SET TRANSACTION READ ONLY 里照样写库,
// 直接调用、经视图调用都写进去了。修复后这些查询在下发前被拒绝,日志表一行都不多。
func TestOracleBizQuery_AutonomousFunctionRefused_RealOracle(t *testing.T) {
	conn := testOracle(t)
	ctx := context.Background()
	run := func(s string) {
		t.Helper()
		if _, err := RealRun(ctx, conn, s, 30*time.Second); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	drop := func() {
		for _, s := range []string{
			"DROP PUBLIC SYNONYM vz_hid_syn", "DROP VIEW vz_hid_v", "DROP VIEW vz_hid_clean_v", "DROP PACKAGE vz_hid_pkg",
			"DROP FUNCTION vz_hid_auto", "DROP FUNCTION vz_hid_outer", "DROP FUNCTION vz_hid_http", "DROP FUNCTION vz_hid_pure",
			"DROP TABLE vz_hid_log",
		} {
			_, _ = RealRun(ctx, conn, s, 30*time.Second)
		}
	}
	drop()
	t.Cleanup(drop)
	run("CREATE TABLE vz_hid_log (n NUMBER)")
	run(`CREATE OR REPLACE FUNCTION vz_hid_auto RETURN NUMBER IS PRAGMA AUTONOMOUS_TRANSACTION; BEGIN INSERT INTO vz_hid_log VALUES (1); COMMIT; RETURN 1; END;`)
	run(`CREATE OR REPLACE FUNCTION vz_hid_outer RETURN NUMBER IS BEGIN RETURN vz_hid_auto(); END;`)
	run(`CREATE OR REPLACE FUNCTION vz_hid_pure(x NUMBER) RETURN NUMBER IS BEGIN RETURN x * 2; END;`)
	run(`CREATE OR REPLACE FUNCTION vz_hid_http RETURN VARCHAR2 IS BEGIN RETURN UTL_HTTP.REQUEST('http://127.0.0.1:1/'); END;`)
	run(`CREATE OR REPLACE PACKAGE vz_hid_pkg AS FUNCTION f RETURN NUMBER; END;`)
	run(`CREATE OR REPLACE PACKAGE BODY vz_hid_pkg AS FUNCTION f RETURN NUMBER IS PRAGMA AUTONOMOUS_TRANSACTION; BEGIN INSERT INTO vz_hid_log VALUES (3); COMMIT; RETURN 1; END; END;`)
	run(`CREATE OR REPLACE VIEW vz_hid_v AS SELECT vz_hid_auto() x FROM dual`)
	run(`CREATE OR REPLACE VIEW vz_hid_clean_v AS SELECT vz_hid_pure(n) x FROM vz_hid_log`)
	run(`CREATE PUBLIC SYNONYM vz_hid_syn FOR vz_hid_auto`)
	// 数据查询在 SET TRANSACTION READ ONLY 里跑:表刚建好的一两秒内读它报 ORA-01466(表定义晚于
	// 事务的读一致性时间点)。生产上不会刚建就查,用例里等一下。
	time.Sleep(3 * time.Second)

	refused := []struct{ q, code string }{
		{"SELECT vz_hid_auto() FROM dual", model.BizAutonomous},
		{"select * from VZ_HID_V", model.BizAutonomous},
		{"SELECT vz_hid_outer() FROM dual", model.BizAutonomous},
		{"SELECT vz_hid_pkg.f() FROM dual", model.BizAutonomous},
		{"SELECT vz_hid_syn() FROM dual", model.BizAutonomous},
		{"SELECT x FROM (SELECT vz_hid_auto() x FROM dual) WHERE 1 = 1", model.BizAutonomous},
		{"SELECT vz_hid_http() FROM dual", model.BizFunc},
	}
	for _, c := range refused {
		_, err := RealBizQuery(ctx, conn, c.q, 10, 20*time.Second)
		var r *BizRefused
		if !errors.As(err, &r) || r.Ref.Code != c.code {
			t.Errorf("%q:应在下发前以 %s 拒绝,实际 %v", c.q, c.code, err)
		}
	}
	res, err := RealRun(ctx, conn, "SELECT COUNT(*) FROM vz_hid_log", 10*time.Second)
	if err != nil || len(res.Data) != 1 || res.Data[0][0] != "0" {
		t.Fatalf("被拒的查询不该写进任何一行,实际 %v %v", res.Data, err)
	}

	// 日常查询照旧:普通表、DUAL、纯函数、调纯函数的视图、内置函数
	for _, q := range []string{
		"SELECT COUNT(*) FROM vz_hid_log",
		"SELECT SYSDATE, USERENV('LANG') FROM dual",
		"SELECT vz_hid_pure(21) FROM dual",
		"SELECT * FROM vz_hid_clean_v",
		"SELECT NVL(MAX(n), 0), TO_CHAR(SYSDATE, 'YYYY') FROM vz_hid_log",
		"WITH t AS (SELECT 1 n FROM dual) SELECT * FROM t",
	} {
		if _, err := RealBizQuery(ctx, conn, q, 10, 20*time.Second); err != nil {
			t.Errorf("%q:应正常执行,实际 %v", q, err)
		}
	}

	// DESC:翻成当前 schema 的 ALL_TAB_COLUMNS
	d, err := RealBizQuery(ctx, conn, "desc vz_hid_log", 10, 20*time.Second)
	if err != nil || len(d.Data) != 1 || d.Data[0][0] != "N" || d.Data[0][1] != "NUMBER" {
		t.Fatalf("DESC 应列出 N NUMBER,实际 %v %v %v", d.Columns, d.Data, err)
	}
	if _, err := RealBizQuery(ctx, conn, "DESC vz_hid_no_such", 10, 20*time.Second); err == nil || !strings.Contains(err.Error(), "不存在") {
		t.Errorf("DESC 不存在的表应报不存在,实际 %v", err)
	}
}
