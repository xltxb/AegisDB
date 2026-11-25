package gateway

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"velagateway/internal/model"
)

// Oracle 建存储程序编译失败也返回成功 —— 网关要回读 all_errors,把它报成失败(2026-10 生产:
// \i PK_AGENT.pck 显示"脚本执行成功",查回来包体是 INVALID)。

func TestOracleCreatedUnit_RecognisesStoredProgramCreates(t *testing.T) {
	cases := []struct{ in, owner, name, typ string }{
		{"CREATE OR REPLACE PACKAGE BODY pk_agent AS ... END;", "", "PK_AGENT", "PACKAGE BODY"},
		{"create or replace package  body\n  UAT_P_G04.PK_AGENT is ...", "UAT_P_G04", "PK_AGENT", "PACKAGE BODY"},
		{"CREATE PACKAGE pk_a AS END;", "", "PK_A", "PACKAGE"},
		{"CREATE OR REPLACE EDITIONABLE PROCEDURE \"MixedCase\" IS BEGIN NULL; END;", "", "MixedCase", "PROCEDURE"},
		{"CREATE OR REPLACE NONEDITIONABLE FUNCTION f RETURN NUMBER IS BEGIN RETURN 1; END;", "", "F", "FUNCTION"},
		{"/* 头注释 */ CREATE OR REPLACE TRIGGER trg BEFORE INSERT ON t BEGIN NULL; END;", "", "TRG", "TRIGGER"},
		{"CREATE OR REPLACE TYPE BODY ty AS END;", "", "TY", "TYPE BODY"},
		{"CREATE TYPE ty AS OBJECT (a NUMBER);", "", "TY", "TYPE"},
		{"CREATE OR REPLACE PROCEDURE \"S\".\"P\" IS BEGIN NULL; END;", "S", "P", "PROCEDURE"},
	}
	for _, c := range cases {
		u, ok := oracleCreatedUnit(c.in)
		if !ok || u.Owner != c.owner || u.Name != c.name || u.Type != c.typ {
			t.Errorf("%q: 应认出 %s %s.%s,实际 %+v ok=%v", c.in, c.typ, c.owner, c.name, u, ok)
		}
	}
	for _, in := range []string{"CREATE TABLE t (a NUMBER)", "CREATE INDEX i ON t(a)", "CREATE OR REPLACE VIEW v AS SELECT 1 FROM dual", "ALTER PACKAGE p COMPILE", "SELECT 1 FROM dual"} {
		if u, ok := oracleCreatedUnit(in); ok {
			t.Errorf("%q 不是建存储程序,不该认出 %+v", in, u)
		}
	}
}

func TestRealRun_OracleCreateWithCompileErrorsIsAFailure(t *testing.T) {
	conn := testOracle(t)
	pkg := fmt.Sprintf("ZZ_CE_%d", time.Now().UnixNano()%1_000_000)
	run := func(q string) (ExecResult, error) {
		return RealRun(context.Background(), conn, q, 30*time.Second)
	}
	t.Cleanup(func() {
		run("DROP PACKAGE " + pkg)
		run("DROP PROCEDURE " + pkg + "_P")
		run("DROP PROCEDURE " + pkg + "_W")
	})

	if _, err := run("CREATE OR REPLACE PACKAGE " + pkg + " AS PROCEDURE p; END " + pkg + ";"); err != nil {
		t.Fatalf("包头没有错误,应成功: %v", err)
	}
	// 包体引用一个不存在的包:Oracle 建出 INVALID 的包体,驱动不报错
	_, err := run("CREATE OR REPLACE PACKAGE BODY " + pkg + " AS PROCEDURE p IS BEGIN zz_no_such_pkg.x; END; END " + pkg + ";")
	var ce *PLSQLCompileError
	if !errors.As(err, &ce) {
		t.Fatalf("编译失败的包体必须报失败,实际 err=%v", err)
	}
	if ce.Unit.Type != "PACKAGE BODY" || len(ce.Errors) == 0 || !strings.Contains(err.Error(), "PLS-00201") || !strings.Contains(err.Error(), "INVALID") {
		t.Errorf("报错要说清对象、INVALID 和 PLS 错误与行列: %v", err)
	}

	// 改好之后再建:成功,不残留上一次的错误
	if _, err := run("CREATE OR REPLACE PACKAGE BODY " + pkg + " AS PROCEDURE p IS BEGIN NULL; END; END " + pkg + ";"); err != nil {
		t.Fatalf("改好的包体应成功: %v", err)
	}

	// 带 schema 前缀的写法同样回读得到
	var user string
	if r, err := run("SELECT USER FROM dual"); err == nil && len(r.Data) > 0 {
		user = r.Data[0][0]
	}
	_, err = run("CREATE OR REPLACE PROCEDURE " + user + "." + pkg + "_P IS BEGIN nope_nope; END;")
	if !errors.As(err, &ce) {
		t.Fatalf("schema.name 的过程编译失败也要报出来,实际 %v", err)
	}

	// 只有 PLW 警告(不让对象失效)不算失败
	if _, err := run("ALTER SESSION SET PLSQL_WARNINGS = 'ENABLE:ALL'"); err != nil {
		t.Fatal(err)
	}
	if _, err := run("CREATE OR REPLACE PROCEDURE " + pkg + "_W IS v NUMBER; BEGIN NULL; END;"); err != nil {
		t.Errorf("只有编译警告的过程是 VALID,不该报失败: %v", err)
	}
}

// 选了 schema(终端里切到别的 schema)时,没写 schema 的单元建在那个 schema 下 —— 回读要按
// 同一会话的 CURRENT_SCHEMA 找,不能拿登录用户去查。
func TestRealRun_OracleCreateErrorsFollowTheCurrentSchema(t *testing.T) {
	conn := testOracle(t)
	schema := fmt.Sprintf("ZZCE%d", time.Now().UnixNano()%1_000_000)
	admin := *conn
	if _, err := RealRun(context.Background(), &admin, "CREATE USER "+schema+" IDENTIFIED BY \"Zz_pw_123\" QUOTA UNLIMITED ON USERS", 30*time.Second); err != nil {
		t.Skipf("建测试 schema 失败(账号权限不足?): %v", err)
	}
	t.Cleanup(func() { RealRun(context.Background(), &admin, "DROP USER "+schema+" CASCADE", 60*time.Second) })

	in := *conn
	in.TargetSchema = schema
	_, err := RealRun(context.Background(), &in, "CREATE OR REPLACE PROCEDURE zz_in_schema IS BEGIN still_missing; END;", 30*time.Second)
	var ce *PLSQLCompileError
	if !errors.As(err, &ce) {
		t.Fatalf("建在 %s 下的过程编译失败应报出来,实际 %v", schema, err)
	}
}

// 切过 schema 的连接归还连接池前必须切回来:否则下一个没选 schema 的请求(别人的终端、导出、
// 审批执行)借到它,不带 owner 的语句就落在上一个人选的 schema 上。
func TestRealRun_OracleSchemaDoesNotLeakThroughThePool(t *testing.T) {
	conn := testOracle(t)
	schema := fmt.Sprintf("ZZLK%d", time.Now().UnixNano()%1_000_000)
	if _, err := RealRun(context.Background(), conn, "CREATE USER "+schema+" IDENTIFIED BY \"Zz_pw_123\"", 30*time.Second); err != nil {
		t.Skipf("建测试 schema 失败: %v", err)
	}
	t.Cleanup(func() { RealRun(context.Background(), conn, "DROP USER "+schema+" CASCADE", 60*time.Second) })

	in := *conn
	in.TargetSchema = schema
	for range 3 { // 把池里的连接都切一遍
		r, err := RealRun(context.Background(), &in, "SELECT SYS_CONTEXT('USERENV', 'CURRENT_SCHEMA') FROM dual", 30*time.Second)
		if err != nil || len(r.Data) == 0 || r.Data[0][0] != schema {
			t.Fatalf("选了 schema 时应在 %s 下执行: %v %v", schema, r.Data, err)
		}
	}
	want := strings.ToUpper(conn.Username)
	for i := range 6 {
		r, err := RealRun(context.Background(), conn, "SELECT SYS_CONTEXT('USERENV', 'CURRENT_SCHEMA') FROM dual", 30*time.Second)
		if err != nil || len(r.Data) == 0 {
			t.Fatalf("第 %d 次: %v", i+1, err)
		}
		if got := r.Data[0][0]; got != want {
			t.Fatalf("没选 schema 的请求借到了上一个人切过的连接:在 %s 下执行,应是 %s", got, want)
		}
	}
}

// 切回的是连接**原来的** schema,不是登录用户(2026-10 审查):账号配了登录触发器、登录时就切到
// 业务属主的库,切回 SESSION_USER 会让这条连接回到池里之后落在一个空 schema 上。
func TestRealRun_OracleSchemaRestoresTheOriginalNotTheLoginUser(t *testing.T) {
	admin := testOracle(t)
	n := time.Now().UnixNano() % 1_000_000
	login, owner, other := fmt.Sprintf("ZZLG%d", n), fmt.Sprintf("ZZOW%d", n), fmt.Sprintf("ZZOT%d", n)
	run := func(q string) error {
		_, err := RealRun(context.Background(), admin, q, 60*time.Second)
		return err
	}
	for _, u := range []string{login, owner, other} {
		if err := run("CREATE USER " + u + " IDENTIFIED BY \"Zz_pw_123\""); err != nil {
			t.Skipf("建测试用户失败: %v", err)
		}
		u := u
		t.Cleanup(func() { _ = run("DROP USER " + u + " CASCADE") })
	}
	if err := run("GRANT CREATE SESSION TO " + login); err != nil {
		t.Skipf("授权失败: %v", err)
	}
	// 登录触发器:这个账号一登录,CURRENT_SCHEMA 就是业务属主
	if err := run("CREATE OR REPLACE TRIGGER " + login + ".zz_logon AFTER LOGON ON " + login + ".SCHEMA BEGIN " +
		"EXECUTE IMMEDIATE 'ALTER SESSION SET CURRENT_SCHEMA = " + owner + "'; END;"); err != nil {
		t.Skipf("建登录触发器失败: %v", err)
	}

	conn := *admin
	conn.ID = 0
	conn.Username, conn.Password = login, "Zz_pw_123"
	current := func(c *model.Connection) string {
		t.Helper()
		r, err := RealRun(context.Background(), c, "SELECT SYS_CONTEXT('USERENV', 'CURRENT_SCHEMA') FROM dual", 30*time.Second)
		if err != nil || len(r.Data) == 0 {
			t.Fatalf("查当前 schema 失败: %v", err)
		}
		return r.Data[0][0]
	}
	if got := current(&conn); got != owner {
		t.Skipf("登录触发器没生效(当前 schema %s),跳过", got)
	}
	in := conn
	in.TargetSchema = other
	for range 3 { // 把池里的连接都切一遍
		if got := current(&in); got != other {
			t.Fatalf("选了 schema 时应在 %s 下执行,实际 %s", other, got)
		}
	}
	for i := range 6 {
		if got := current(&conn); got != owner {
			t.Fatalf("第 %d 次:切过 schema 的连接归还后应回到原来的 %s(登录触发器设的),实际 %s", i+1, owner, got)
		}
	}
}
