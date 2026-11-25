package gateway

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"velagateway/internal/model"
)

func TestSQLPlusSet(t *testing.T) {
	yes := map[string]string{
		"SET SERVEROUTPUT ON":             "SERVEROUTPUT",
		"set serveroutput on size 100000": "SERVEROUTPUT",
		"set serverout on;":               "SERVEROUT",
		"SET LINESIZE 200":                "LINESIZE",
		"set pagesize 0":                  "PAGESIZE",
		"SET DEFINE OFF":                  "DEFINE",
		"set echo off":                    "ECHO",
		"SET TIMING ON":                   "TIMING",
		"/* 头注释 */ SET FEEDBACK OFF":      "FEEDBACK",
	}
	for in, want := range yes {
		opt, _, ok := SQLPlusSet("Oracle 19c", in)
		if !ok || opt != want {
			t.Errorf("%q 应识别为 SQL*Plus SET %s,实际 %q %v", in, want, opt, ok)
		}
	}
	// Oracle 自己的 SET 语句是 SQL,照常下发
	for _, in := range []string{"SET TRANSACTION READ ONLY", "set role all", "SET CONSTRAINTS ALL DEFERRED", "SET CONSTRAINT c1 IMMEDIATE"} {
		if _, _, ok := SQLPlusSet("Oracle 19c", in); ok {
			t.Errorf("%q 是 Oracle 的 SQL,不该被当成 SQL*Plus 命令", in)
		}
	}
	// 别的引擎的 SET 是真正的 SQL(MySQL 的 SET NAMES、PG 的 SET search_path)
	for _, e := range []string{"MySQL 8.0", "PostgreSQL 15", "DWS"} {
		if _, _, ok := SQLPlusSet(e, "SET NAMES utf8mb4"); ok {
			t.Errorf("%s 上的 SET 不该被拦下", e)
		}
	}
	// 不是 SET
	if _, _, ok := SQLPlusSet("Oracle 19c", "SELECT 'SET SERVEROUTPUT ON' FROM DUAL"); ok {
		t.Error("字符串里的 SET 不算")
	}
}

// 执行器收到 SQL*Plus 的 SET:不下发,回一句提示(以 · 开头)。连凭据都不需要 —— 它根本不碰库。
func TestExecutor_SQLPlusSetIsNotSent(t *testing.T) {
	x := &Executor{}
	conn := &model.Connection{Engine: "Oracle 19c", Host: "10.255.255.1", Port: 1521, Username: "u", Password: "p", Database: "ORCL"}
	start := time.Now()
	res := x.Run(context.Background(), conn, "SET SERVEROUTPUT ON", 5*time.Second)
	if time.Since(start) > time.Second {
		t.Fatal("SET 不该去连数据库")
	}
	if res.Err != nil || !strings.HasPrefix(res.Output, "·") || res.OutputRef == nil || res.OutputRef.Code != model.OutSQLPlusSetIgnored {
		t.Fatalf("应回一句已忽略的提示,实际 %+v", res)
	}
	res = x.Run(context.Background(), conn, "SET DEFINE OFF", 5*time.Second)
	if !strings.Contains(res.Output, "& ") {
		t.Errorf("SET DEFINE 要说清网关不做 & 替换:%q", res.Output)
	}
}

// ---------------------------------------------------------------- 真实 Oracle

// testOracle 从 VELA_TEST_ORACLE=host:port/user/password/service 取一台可用的 Oracle。
func testOracle(t *testing.T) *model.Connection {
	t.Helper()
	v := os.Getenv("VELA_TEST_ORACLE")
	if v == "" {
		t.Skip("未设置 VELA_TEST_ORACLE=host:port/user/password/service,跳过真实 Oracle 用例")
	}
	p := strings.SplitN(v, "/", 4)
	hp := strings.SplitN(p[0], ":", 2)
	port, _ := strconv.Atoi(hp[1])
	return &model.Connection{Name: "ora-it", Engine: "Oracle 23ai", Host: hp[0], Port: port,
		Username: p[1], Password: p[2], Database: p[3]}
}

func TestOracleServerOutput(t *testing.T) {
	conn := testOracle(t)
	on := WithServerOutput(context.Background(), true)
	block := "BEGIN DBMS_OUTPUT.PUT_LINE('hello'); DBMS_OUTPUT.PUT_LINE('中文 输出'); DBMS_OUTPUT.PUT_LINE(NULL); END;"

	res, err := RealRun(on, conn, block, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(res.ServerOutput, "|") != "hello|中文 输出|" {
		t.Fatalf("应读回三行(含空行),实际 %q", res.ServerOutput)
	}

	// 不开:不读,也不残留到下一次
	res, err = RealRun(context.Background(), conn, block, 30*time.Second)
	if err != nil || len(res.ServerOutput) != 0 {
		t.Fatalf("没开 SERVEROUTPUT 不该有输出,实际 %q %v", res.ServerOutput, err)
	}
	for i := 0; i < 4; i++ { // 连接池里的连接不带着上一次的缓冲区
		res, _ = RealRun(on, conn, "BEGIN NULL; END;", 30*time.Second)
		if len(res.ServerOutput) != 0 {
			t.Fatalf("第 %d 次:空块不该读到残留输出 %q", i+1, res.ServerOutput)
		}
	}

	// EXEC 简写(翻译成 BEGIN … END)同样有输出
	res, err = RealRun(on, conn, "EXEC DBMS_OUTPUT.PUT_LINE('via exec')", 30*time.Second)
	if err != nil || len(res.ServerOutput) != 1 || res.ServerOutput[0] != "via exec" {
		t.Fatalf("EXEC 应有输出,实际 %q %v", res.ServerOutput, err)
	}

	// 报错:报错之前打印的内容照样带回来
	res, err = RealRun(on, conn, "BEGIN DBMS_OUTPUT.PUT_LINE('before error'); RAISE_APPLICATION_ERROR(-20001, 'boom'); END;", 30*time.Second)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("应报错,实际 %v", err)
	}
	if len(res.ServerOutput) != 1 || res.ServerOutput[0] != "before error" {
		t.Errorf("报错时也要带回之前的输出,实际 %q", res.ServerOutput)
	}

	// 执行器:报错时输出也一路带到结果里
	x := (&Executor{}).Run(on, conn, "BEGIN DBMS_OUTPUT.PUT_LINE('x'); RAISE_APPLICATION_ERROR(-20002, 'y'); END;", 30*time.Second)
	if x.Err == nil || len(x.ServerOutput) != 1 {
		t.Errorf("执行器报错时应带着输出,实际 %+v", x)
	}

	// 查询里调用会 PUT_LINE 的函数:结果集与输出都在
	res, err = RealRun(on, conn, "SELECT 1 AS n FROM DUAL", 30*time.Second)
	if err != nil || len(res.Data) != 1 {
		t.Fatalf("查询照常返回结果集: %v %v", res.Data, err)
	}

	// 上限:超过 2000 行截断
	start := time.Now()
	res, err = RealRun(on, conn, "BEGIN FOR i IN 1..2500 LOOP DBMS_OUTPUT.PUT_LINE('line ' || i); END LOOP; END;", 60*time.Second)
	if err != nil || len(res.ServerOutput) != maxServerOutputLines || !res.ServerOutputTruncated {
		t.Fatalf("超过上限应截断到 %d 行,实际 %d truncated=%v err=%v", maxServerOutputLines, len(res.ServerOutput), res.ServerOutputTruncated, err)
	}
	t.Logf("读回 2000 行耗时 %v", time.Since(start))

	// 长行:一批装不下,跨批次(carry)读回,内容一个字节不能坏;行内的换行原样保留
	res, err = RealRun(on, conn, `BEGIN
	  FOR i IN 1..5 LOOP DBMS_OUTPUT.PUT_LINE(RPAD(CHR(96 + i), 10000, CHR(96 + i))); END LOOP;
	  DBMS_OUTPUT.PUT_LINE('a' || CHR(10) || 'b');
	END;`, 60*time.Second)
	if err != nil || len(res.ServerOutput) != 6 || res.ServerOutputTruncated {
		t.Fatalf("5 个长行 + 1 行带换行:实际 %d 行 truncated=%v err=%v", len(res.ServerOutput), res.ServerOutputTruncated, err)
	}
	for i := 0; i < 5; i++ {
		if want := strings.Repeat(string(rune('a'+i)), 10000); res.ServerOutput[i] != want {
			t.Errorf("第 %d 个长行被截坏:长度 %d", i+1, len(res.ServerOutput[i]))
		}
	}
	if res.ServerOutput[5] != "a\nb" {
		t.Errorf("行内换行应原样保留,实际 %q", res.ServerOutput[5])
	}
}
