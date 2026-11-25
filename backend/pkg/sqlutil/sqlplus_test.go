package sqlutil

import (
	"strings"
	"testing"
)

func TestSplit_SQLPlusSetLinesEndAtNewline(t *testing.T) {
	got := SplitStatements("SET SERVEROUTPUT ON\nset linesize 200\nBEGIN DBMS_OUTPUT.PUT_LINE('a'); END;\n/\nSET DEFINE OFF\nSELECT 1 FROM DUAL;\n")
	want := []string{"SET SERVEROUTPUT ON", "set linesize 200", "BEGIN DBMS_OUTPUT.PUT_LINE('a'); END;", "SET DEFINE OFF", "SELECT 1 FROM DUAL"}
	if strings.Join(got, " | ") != strings.Join(want, " | ") {
		t.Fatalf("\n got %q\nwant %q", got, want)
	}
}

// MySQL / PostgreSQL 的 SET 不受影响:多行书写照旧按分号切。
func TestSplit_OtherEnginesSetUnchanged(t *testing.T) {
	cases := map[string][]string{
		"SET @sql = CONCAT('a',\n  'b');\nSELECT @sql;":       {"SET @sql = CONCAT('a',\n  'b')", "SELECT @sql"},
		"SET NAMES utf8mb4;\nSET search_path TO app;\nSELECT 1;": {"SET NAMES utf8mb4", "SET search_path TO app", "SELECT 1"},
		"SET autocommit =\n 0;\nSELECT 1;":                     {"SET autocommit =\n 0", "SELECT 1"},
		"SET TIME ZONE\n 'UTC';":                               {"SET TIME ZONE\n 'UTC'"},
		"SET SERVEROUTPUT ON;\nSELECT 1;":                      {"SET SERVEROUTPUT ON", "SELECT 1"},
	}
	for in, want := range cases {
		if got := SplitStatements(in); strings.Join(got, " | ") != strings.Join(want, " | ") {
			t.Errorf("%q:\n got %q\nwant %q", in, got, want)
		}
	}
	// 语句中间出现的 SET(UPDATE … SET)不算行首的 SQL*Plus 命令
	got := SplitStatements("UPDATE t\nSET linesize = 1\nWHERE id = 1;")
	if len(got) != 1 {
		t.Errorf("UPDATE … SET 被拆开了: %q", got)
	}
}

// SQL*Plus 的行命令(PROMPT / REM / DEFINE / COLUMN / SHOW ERRORS …)以换行结束(sqlplus-compat 01/03)。
func TestSplit_SQLPlusLineCommandsEndAtNewline(t *testing.T) {
	in := "PROMPT 开始建表; 别停\n" +
		"REM don't stop here\n" +
		"DEFINE owner = APP\n" +
		"col name format a20\n" +
		"CREATE TABLE t (id NUMBER);\n" +
		"CREATE OR REPLACE PROCEDURE p AS BEGIN NULL; END;\n/\n" +
		"SHOW ERRORS\n" +
		"PRO\n" +
		"SELECT 1 FROM DUAL;\n"
	want := []string{
		"PROMPT 开始建表; 别停",
		"REM don't stop here",
		"DEFINE owner = APP",
		"col name format a20",
		"CREATE TABLE t (id NUMBER)",
		"CREATE OR REPLACE PROCEDURE p AS BEGIN NULL; END;",
		"SHOW ERRORS",
		"PRO",
		"SELECT 1 FROM DUAL",
	}
	if got := SplitStatements(in); strings.Join(got, " | ") != strings.Join(want, " | ") {
		t.Fatalf("\n got %q\nwant %q", got, want)
	}
	if got, ex := SplitStatements(in), SplitStatementsExec(in); len(got) != len(ex) {
		t.Fatalf("判定与下发条数不一致: %d vs %d", len(got), len(ex))
	}
}

// 这些词只在语句边界的行首才算;语句中间、或带分号的普通写法照旧。
func TestSplit_SQLPlusLineCommandsOnlyAtBoundary(t *testing.T) {
	cases := map[string][]string{
		// ALTER … ADD 换行后的 COLUMN 是同一条语句的一部分
		"ALTER TABLE t ADD\nCOLUMN x INT;\nSELECT 1;": {"ALTER TABLE t ADD\nCOLUMN x INT", "SELECT 1"},
		// MySQL 的 SHOW ERRORS 写了分号:照常按分号切
		"SHOW ERRORS;\nSELECT 1;": {"SHOW ERRORS", "SELECT 1"},
		// 不是这些词(CLOSE / COMMIT / PREPARE / COPY)不受影响
		"COMMIT;\nSELECT 1;":                  {"COMMIT", "SELECT 1"},
		"COPY t FROM STDIN\n WITH CSV;":        {"COPY t FROM STDIN\n WITH CSV"},
		"PREPARE s FROM\n 'SELECT 1';\nEXECUTE s;": {"PREPARE s FROM\n 'SELECT 1'", "EXECUTE s"},
	}
	for in, want := range cases {
		if got := SplitStatements(in); strings.Join(got, " | ") != strings.Join(want, " | ") {
			t.Errorf("%q:\n got %q\nwant %q", in, got, want)
		}
	}
}

// 第二批:WHENEVER / SPOOL / EXIT / @ 行同样按换行结束;START TRANSACTION 不受影响。
func TestSplit_SQLPlusBatch2LineCommands(t *testing.T) {
	in := "WHENEVER SQLERROR EXIT FAILURE\nspool deploy.log\n@@sub/b.sql 1\nINSERT INTO t VALUES (1);\nspool off\nexit\n"
	want := []string{"WHENEVER SQLERROR EXIT FAILURE", "spool deploy.log", "@@sub/b.sql 1", "INSERT INTO t VALUES (1)", "spool off", "exit"}
	if got := SplitStatements(in); strings.Join(got, " | ") != strings.Join(want, " | ") {
		t.Fatalf("\n got %q\nwant %q", got, want)
	}
	if got := SplitStatements("START TRANSACTION\n;\nSELECT 1;"); len(got) != 2 || got[0] != "START TRANSACTION" {
		t.Fatalf("START TRANSACTION 应照常按分号切: %q", got)
	}
}

func TestSplit_SQLPlusBindLines(t *testing.T) {
	in := "VARIABLE n NUMBER\nEXEC :n := 42;\nprint n\nSELECT :n FROM dual;\n"
	want := []string{"VARIABLE n NUMBER", "EXEC :n := 42", "print n", "SELECT :n FROM dual"}
	if got := SplitStatements(in); strings.Join(got, " | ") != strings.Join(want, " | ") {
		t.Fatalf("\n got %q\nwant %q", got, want)
	}
}

// 2026-10-28:SET HEAD OFF 不在旧白名单里,和下一行粘成一条,执行器把整段当 SET 忽略 —— 下一条被悄悄跳过。
// 现在 SQL*Plus 的全部 SET 选项、从最短缩写到全称都按行结束。
func TestSplit_SQLPlusSetAllAbbreviations(t *testing.T) {
	for _, line := range []string{"SET HEAD OFF", "set lin 120", "SET PAGES 0", "set feed off", "SET NUMF 999", "set und off",
		"SET TRIMS ON", "set ver off", "SET SERVEROUT ON", "set newp none", "SET EMB ON", "set sqlbl on", "SET WRA OFF", "set heads off"} {
		got := SplitStatements(line + "\nSELECT 1 FROM dual;")
		if len(got) != 2 || got[0] != line || got[1] != "SELECT 1 FROM dual" {
			t.Errorf("%q 应按行结束,实际 %q", line, got)
		}
	}
	// MySQL / PostgreSQL 的 SET 目标不受影响(多行照旧按分号切)
	for _, in := range []string{
		"SET search_path TO\n app;", "SET sql_mode =\n 'STRICT';", "SET long_query_time =\n 1;", "SET time_zone =\n '+8:00';",
		"SET CONSTRAINTS ALL\n DEFERRED;", "SET statement_timeout =\n 1000;", "SET autocommit =\n 0;", "SET TIME ZONE\n 'UTC';",
	} {
		if got := SplitStatements(in); len(got) != 1 {
			t.Errorf("%q 不该被按行拆开: %q", in, got)
		}
	}
}
