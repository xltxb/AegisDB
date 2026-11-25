package gateway

import (
	"context"
	"strings"
	"testing"

	"velagateway/internal/model"
)

// SQL*Plus 客户端命令表(sqlplus-compat 工单 01 / 03)。

const ora = "Oracle 19c"

func TestOracleClientCommand_Classify(t *testing.T) {
	cases := map[string]SQLPlusKind{
		"conn scott/tiger@orcl":       SQLPlusRefused,
		"CONNECT / AS SYSDBA":         SQLPlusRefused,
		"disc":                        SQLPlusRefused,
		"host ls -l":                  SQLPlusRefused,
		"ho":                          SQLPlusRefused,
		"!ls":                         SQLPlusRefused,
		"$dir":                        SQLPlusRefused,
		"startup":                     SQLPlusRefused,
		"SHUTDOWN IMMEDIATE":          SQLPlusRefused,
		"recover database":            SQLPlusRefused,
		"archive log list":            SQLPlusRefused,
		"passw scott":                 SQLPlusRefused,
		"copy from a@x to b@y create t using select * from t": SQLPlusRefused,
		"ed":                          SQLPlusRefused,
		"exit":                        SQLPlusExit,
		"EXIT;":                       SQLPlusExit,
		"exit sql.sqlcode rollback":   SQLPlusExit,
		"quit":                        SQLPlusExit,
		"whenever sqlerror exit failure": SQLPlusWhenever,
		"WHENEVER SQLERROR CONTINUE":  SQLPlusWhenever,
		"spool /tmp/x.log":            SQLPlusIgnored,
		"spo off":                     SQLPlusIgnored,
		"l":                           SQLPlusIgnored,
		"list 2 3":                    SQLPlusIgnored,
		"a  where 1=1":                SQLPlusIgnored,
		"c /emp/dept/":                SQLPlusIgnored,
		"del 2":                       SQLPlusIgnored,
		"i order by 1":                SQLPlusIgnored,
		"save x.sql":                  SQLPlusIgnored,
		"get x.sql":                   SQLPlusIgnored,
		"help index":                  SQLPlusIgnored,
		"@deploy.sql":                 SQLPlusRefused,
		"@@sub/b.sql arg1":            SQLPlusRefused,
		"start deploy.sql 1 2":        SQLPlusRefused,
		"sta deploy":                  SQLPlusRefused,
		"break on deptno":             SQLPlusIgnored,
		"comp sum of sal on deptno":   SQLPlusIgnored,
		"ttitle 'Report'":             SQLPlusIgnored,
		"pause":                       SQLPlusIgnored,
		"col ename format a20":        SQLPlusIgnored,
		"COLUMN":                      SQLPlusIgnored,
		"clear columns":               SQLPlusIgnored,
		"define owner = APP":          SQLPlusIgnored,
		"undef owner":                 SQLPlusIgnored,
		"accept pwd prompt 'pwd:' hide": SQLPlusIgnored,
		"timing start t1":             SQLPlusIgnored,
		"SHOW ERRORS":                 SQLPlusIgnored,
		"show err":                    SQLPlusIgnored,
		"prompt 开始建表":                 SQLPlusPrompt,
		"PRO":                         SQLPlusPrompt,
		"rem don't":                   SQLPlusRemark,
		"REMARK x":                    SQLPlusRemark,
	}
	for in, want := range cases {
		c, ok := OracleClientCommand(ora, in)
		if !ok || c.Kind != want {
			t.Errorf("%q: got ok=%v kind=%v, want %v", in, ok, c.Kind, want)
		}
	}
	if c, _ := OracleClientCommand(ora, "prompt  建表 a; b"); c.Text != "建表 a; b" {
		t.Errorf("PROMPT 文字 = %q", c.Text)
	}
}

// SQL 语句、长得像的词、其他引擎:都不是这里管的。
func TestOracleClientCommand_NotSQL(t *testing.T) {
	for _, in := range []string{
		"SELECT host FROM t",
		"select * from emp connect by prior empno = mgr",
		"COMMENT ON TABLE t IS 'x'",
		"COMMIT",
		"CALL p()",
		"CLOSE c",
		"hostname",
		"CREATE TABLE copy_of_t AS SELECT * FROM t",
		"SHOW ERRORS PROCEDURE p", // 带对象的由 OracleSQLPlus 翻译
		"SHOW USER",
		"DECLARE x NUMBER; BEGIN NULL; END;",
		"RENAME a TO b",
		"EXPLAIN PLAN FOR SELECT 1 FROM dual",
		"DELETE FROM t WHERE 1=0",
		"INSERT INTO t VALUES (1)",
		"ALTER TABLE t ADD c NUMBER",
		"LOCK TABLE t IN EXCLUSIVE MODE",
		"SAVEPOINT s1",
		"GRANT SELECT ON t TO u",
		"AUDIT SELECT ON t",
		"ANALYZE TABLE t COMPUTE STATISTICS",
	} {
		if c, ok := OracleClientCommand(ora, in); ok {
			t.Errorf("%q 不该被当成 SQL*Plus 命令: %+v", in, c)
		}
	}
	for _, eng := range []string{"MySQL 8.0", "PostgreSQL 15", "TiDB"} {
		for _, in := range []string{"exit", "prompt x", "copy t from stdin", "define x"} {
			if _, ok := OracleClientCommand(eng, in); ok {
				t.Errorf("%s 上 %q 不该被识别", eng, in)
			}
		}
	}
}

func TestEvaluate_SQLPlusClientCommands(t *testing.T) {
	e := metaReadEngine(map[string]string{"write|prod": model.LevelDeny})
	for _, in := range []string{"conn scott/tiger", "host rm -rf /", "shutdown abort"} {
		v := e.EvaluateFor([]int64{1}, ora, "prod", in)
		if v.Action != ActionDeny || v.Ref == nil || v.Ref.Code != model.RuleSQLPlusRefused {
			t.Errorf("%q 应以 sqlplusRefused 拒绝,实际 %+v", in, v)
		}
	}
	// 只读角色(write 被拒)贴 PROMPT / REM / DEFINE / COLUMN 也要放行:它们从不下发
	for _, in := range []string{"prompt drop old table", "rem drop", "define t = DROP", "col x format a10", "SHOW ERRORS"} {
		if v := e.EvaluateFor([]int64{1}, ora, "prod", in); v.Action != ActionAllow {
			t.Errorf("%q 应放行,实际 %+v", in, v)
		}
		if _, risk, _ := e.ScanStatementFor(ora, "prod", in); risk != "safe" {
			t.Errorf("脚本扫描 %q 应为 safe,实际 %s", in, risk)
		}
	}
	if _, risk, _ := e.ScanStatementFor(ora, "prod", "conn a/b"); risk != "high" {
		t.Errorf("脚本扫描 CONNECT 应为 high,实际 %s", risk)
	}
}

// 可执行注释包住 SQL*Plus 命令的写法:判定与执行器看到的必须是同一个东西。
// 批量执行时判定拿去标记的文本、执行器拿保留标记的文本 —— 两边都按剥了注释的文本认。
func TestSQLPlusClientCommand_ExecCommentNotABypass(t *testing.T) {
	x := NewExecutor()
	conn := &model.Connection{Engine: ora}
	for _, exec := range []string{"/*!PROMPT*/ DROP TABLE t", "/*T! REM */ DROP TABLE t", "/*!40000 PROMPT */ DROP TABLE t"} {
		judged := StripComments(exec)
		jc, jok := OracleClientCommand(ora, judged)
		xc, xok := OracleClientCommand(ora, exec)
		if jok != xok || jc.Kind != xc.Kind {
			t.Errorf("%q: 判定认成 %v/%v,执行器认成 %v/%v —— 两边必须一致", exec, jok, jc.Kind, xok, xc.Kind)
		}
		// 判定按 PROMPT / REM 放行的,执行器也必须拦下(回 PROMPT 文字或"已忽略"),不能下发
		if jok {
			res := x.Run(context.Background(), conn, exec, 0)
			if res.OutputRef != nil && res.OutputRef.Code == model.OutExecAffected {
				t.Errorf("%q: 判定放行为 %s,执行器却把它当 SQL 执行了: %+v", exec, jc.Name, res)
			}
			if jc.Kind == SQLPlusPrompt && res.Output != "DROP TABLE t" {
				t.Errorf("%q: 执行器应回 PROMPT 文字,实际 %+v", exec, res)
			}
		}
	}
}

func TestExecutor_SQLPlusClientCommands(t *testing.T) {
	x := NewExecutor()
	conn := &model.Connection{Engine: ora}
	res := x.Run(context.Background(), conn, "conn scott/tiger", 0)
	if res.Err == nil || res.OutputRef == nil || res.OutputRef.Code != model.OutSQLPlusRefused {
		t.Fatalf("CONNECT 应失败并带 sqlplusRefused: %+v", res)
	}
	if !strings.Contains(res.Output, "绕过平台的授权与审计") {
		t.Errorf("拒绝要说清原因: %q", res.Output)
	}
	res = x.Run(context.Background(), conn, "PROMPT 开始建表", 0)
	if res.Err != nil || res.Output != "开始建表" {
		t.Errorf("PROMPT 应原样回话: %+v", res)
	}
	res = x.Run(context.Background(), conn, "define owner = APP", 0)
	if res.Err != nil || res.OutputRef == nil || res.OutputRef.Code != model.OutSQLPlusIgnored {
		t.Errorf("DEFINE 在脚本里应忽略: %+v", res)
	}
}

// SHOW ERRORS <类型> <名字>(工单 02)。
func TestOracleSQLPlus_ShowErrors(t *testing.T) {
	q, ok := OracleSQLPlus("show errors package body app.pk_agent")
	if !ok || !strings.Contains(q, "FROM ALL_ERRORS") || !strings.Contains(q, "OWNER = 'APP'") ||
		!strings.Contains(q, "NAME = 'PK_AGENT'") || !strings.Contains(q, "TYPE = 'PACKAGE BODY'") {
		t.Fatalf("got %q ok=%v", q, ok)
	}
	q, ok = OracleSQLPlus("SHOW ERR PROCEDURE p_x;")
	if !ok || !strings.Contains(q, "CURRENT_SCHEMA") || !strings.Contains(q, "TYPE = 'PROCEDURE'") {
		t.Fatalf("got %q ok=%v", q, ok)
	}
	// 名字里有引号 / 怪字符:不翻译(拼进字面量的安全边界)
	for _, in := range []string{"SHOW ERRORS PROCEDURE p'x", "SHOW ERRORS PROCEDURE \"p x\"", "SHOW ERRORS TABLE t"} {
		if _, ok := OracleSQLPlus(in); ok {
			t.Errorf("%q 不该被翻译", in)
		}
	}
}

// WHENEVER / EXIT 对脚本流程的影响(sqlplus-compat 07)。
func TestScriptFlow(t *testing.T) {
	f := NewScriptFlow(ora)
	if f.ContinueOnError() {
		t.Fatal("默认遇错即停")
	}
	f.Observe("whenever sqlerror continue none")
	if !f.ContinueOnError() {
		t.Fatal("WHENEVER SQLERROR CONTINUE 之后应继续")
	}
	f.Observe("WHENEVER OSERROR EXIT") // OSERROR 不改 SQL 错误的走法
	if !f.ContinueOnError() {
		t.Fatal("OSERROR 不该影响 SQLERROR")
	}
	f.Observe("WHENEVER SQLERROR EXIT SQL.SQLCODE ROLLBACK")
	if f.ContinueOnError() {
		t.Fatal("WHENEVER SQLERROR EXIT 之后应停下")
	}
	if !f.IsExit("exit") || !f.IsExit("QUIT;") || f.IsExit("SELECT 1 FROM dual") {
		t.Fatal("IsExit 认错了")
	}
	// 其他引擎:什么都不改变
	m := NewScriptFlow("MySQL 8.0")
	m.Observe("WHENEVER SQLERROR CONTINUE")
	if m.ContinueOnError() || m.IsExit("exit") {
		t.Fatal("非 Oracle 不该受影响")
	}
}

// 判定:脚本末尾的 EXIT、WHENEVER 放行(不能让整份脚本因为它们被拒);@ / START 拒绝并说怎么做。
func TestEvaluate_ExitWheneverAt(t *testing.T) {
	e := metaReadEngine(map[string]string{"write|prod": model.LevelDeny})
	for _, in := range []string{"exit", "QUIT", "whenever sqlerror exit failure", "spool x.log"} {
		if v := e.EvaluateFor([]int64{1}, ora, "prod", in); v.Action != ActionAllow {
			t.Errorf("%q 应放行,实际 %+v", in, v)
		}
	}
	for _, in := range []string{"@deploy.sql", "start deploy.sql"} {
		if v := e.EvaluateFor([]int64{1}, ora, "prod", in); v.Action != ActionDeny {
			t.Errorf("%q 应拒绝,实际 %+v", in, v)
		}
	}
	x := NewExecutor()
	conn := &model.Connection{Engine: ora}
	if res := x.Run(context.Background(), conn, "@deploy.sql", 0); res.Err == nil || !strings.Contains(res.Output, "@ 命令") {
		t.Errorf("@ 应失败并说明在终端里用 @ 命令: %+v", res)
	}
	if res := x.Run(context.Background(), conn, "exit", 0); res.Err != nil || res.OutputRef == nil || res.OutputRef.Code != model.OutSQLPlusExit {
		t.Errorf("EXIT 应回'脚本在此结束': %+v", res)
	}
}

// 执行器只认单行 SET:粘上来的下一条语句不会被当成 SET 的一部分悄悄跳过(2026-10-28)。
func TestSQLPlusSet_SingleLineOnly(t *testing.T) {
	if _, _, ok := SQLPlusSet(ora, "SET HEAD OFF"); !ok {
		t.Fatal("单行 SET HEAD OFF 应认出")
	}
	if _, _, ok := SQLPlusSet(ora, "SET HEAD OFF\nSELECT 1 FROM dual"); ok {
		t.Fatal("多行文本不能整段当 SET —— 下一条会被悄悄跳过")
	}
	// 执行器不再把它吞成"已忽略":整段交给数据库,Oracle 把它当一条语句、报语法错(ORA-00922),
	// 人看得见出了什么事。(里面就算写了 DROP 也不会执行 —— Oracle 一次调用只执行一条语句。)
	res := NewExecutor().Run(context.Background(), &model.Connection{Engine: ora}, "SET HEAD OFF\nSELECT 1 FROM dual", 0)
	if res.OutputRef != nil && res.OutputRef.Code == model.OutSQLPlusSetIgnored {
		t.Fatalf("多行文本不该被当 SET 忽略: %+v", res)
	}
}
