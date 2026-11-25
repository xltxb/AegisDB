package gateway

import "testing"

// 执行窗口按库开:放行前要确认语句只触及窗口那个库(2026-10 黑盒,工单 01 第 1 条)。
//
// 黑盒里在 A 库开窗,下面这些写法都免审批地改动了同实例的 B 库 —— 旧判定只认
// "关键字后面紧跟的那个名字",点号两侧有空格、JOIN / 逗号里的第二张表、动态 SQL、
// CTE 后面的 UPDATE 都看不见。
func TestStaysInDatabase_MySQLEscapes(t *testing.T) {
	const db = "A"
	for _, sql := range []string{
		// 黑盒原样复现的几种
		"UPDATE B . t2 SET v = 1",
		"UPDATE t1 a JOIN B.t2 b ON a.id = b.id SET b.v = 1",
		"UPDATE t1, B.t2 SET t2.v = 1",
		"DELETE b FROM t1 a JOIN B.t2 b ON a.id = b.id",
		"REPLACE INTO B.t2 VALUES (1, 2)",
		"RENAME TABLE mv TO B.moved",
		"DROP TABLE mv, B.dropme2",
		"PREPARE s FROM 'UPDATE B.t2 SET v = 1'",
		"EXECUTE s",
		"WITH x AS (SELECT 1 AS id) UPDATE B.t2 JOIN x USING(id) SET v = 1",
		// 读别库也不放行(工单选定:保守)
		"INSERT INTO t1 SELECT * FROM B.t2",
		"UPDATE t1 SET v = (SELECT v FROM B.t2 LIMIT 1)",
		// 原先已拦的几种,不能回退
		"UPDATE B.t2 SET v = 1",
		"DROP TABLE `B`.`t2`",
		"USE B",
		// 别名与别库同名:表的位置上 B.t2 仍是 B 库的表
		"UPDATE t1 AS B JOIN B.t2 c ON 1 = 1 SET c.v = 1",
		"UPDATE t1 B JOIN t3 ON 1 = 1, B.t2 c SET c.v = 1",
		"SELECT 1 FROM (SELECT 1) AS B, B.t2",
		// 多表删除的目标列表、USING 列表、LOCK 列表
		"DELETE t1, B.t2 FROM t1 JOIN B.t2 ON 1 = 1",
		"DELETE FROM x USING t1 x, B.t2 y",
		"LOCK TABLES t1 READ, B.t2 WRITE",
		// 别库的函数 / 过程:函数体在别库,能写别库
		"UPDATE t1 SET v = B.f(1)",
		"CALL B.p()",
		"DO B.f()",
		// DDL 的各种落点
		"CREATE TABLE t3 LIKE B.t2",
		"CREATE TABLE B.t3 (id INT)",
		"ALTER TABLE t1 RENAME TO B.t1",
		"ALTER TABLE t1 ADD CONSTRAINT fk FOREIGN KEY (x) REFERENCES B.t2 (id)",
		"ALTER TABLE t1 EXCHANGE PARTITION p0 WITH TABLE B.t2",
		"CREATE INDEX i ON B.t2 (c)",
		"CREATE TRIGGER trg AFTER INSERT ON t1 FOR EACH ROW UPDATE B.t2 SET v = NEW.v",
		"CREATE VIEW v AS SELECT * FROM B.t2",
		"TRUNCATE TABLE B.t2",
		"INSERT IGNORE INTO B.t2 VALUES (1)",
		"INSERT B.t2 VALUES (1)",
		"DROP TABLE IF EXISTS B.t2",
		"GRANT ALL ON B.* TO 'u'@'%'",
		// 三段名:schema.table.column
		"UPDATE t1 SET v = 1 WHERE B.t2.id = 1",
		// 库级 / 实例级语句不在"一个库里"
		"DROP DATABASE B",
		"CREATE DATABASE B",
		"ALTER DATABASE B CHARACTER SET utf8mb4",
		"CREATE USER x IDENTIFIED BY 'p'",
		"SET GLOBAL read_only = 0",
		// 注释:-- 后不跟空白在 MySQL 里不是注释;# 是注释,里面的引号不能吞掉后面的语句
		"UPDATE t1 JOIN t3 ON 1=1 --1\n JOIN B.t2 x SET x.v = 2",
		"UPDATE t1 SET v = 1 # it's\n, B.t2",
		// 可执行注释里的内容 MySQL 会执行
		"UPDATE t1 /*!, B.t2 */ SET v = 1",
		"UPDATE t1 /*!50000 JOIN B.t2 x ON 1=1 */ SET v = 1",
		// TiDB 的特性注释 /*T! … */ 同样会被 TiDB 执行(2026-10)
		"UPDATE t1 /*T! JOIN B.t2 x ON 1=1 */ SET v = 1",
		"/*T![auto_rand] UPDATE B.t2 SET v = 1 */",
		// 反斜杠转义:MySQL 默认模式下 'a\'' 是一个字符串,后面是真语句
		"UPDATE t1 SET v = 'a\\'', w = (SELECT 1 FROM B.t2) WHERE 'x' = 'x'",
		// 别库名在双引号里(ANSI_QUOTES 下是标识符)
		"UPDATE \"B\".t2 SET v = 1",
		// 批里夹了 USE
		"UPDATE t1 SET v = 1; USE B",
	} {
		if StaysInDatabase("MySQL 8.0", db, []string{sql}) {
			t.Errorf("越出窗口库 %s 却被判为不越库: %q", db, sql)
		}
	}
}

// 只触及窗口库的常见写法照常放行 —— 窗口在大多数 DML / DDL 上要可用。
func TestStaysInDatabase_MySQLInside(t *testing.T) {
	const db = "orders_db"
	for _, sql := range []string{
		"DROP TABLE orders_2024_q3",
		"DROP TABLE orders_db.orders_2024_q3",
		"DROP TABLE `orders_db`.`orders_2024_q3`",
		"UPDATE orders SET status = 'x' WHERE id = 1",
		"UPDATE t1 a JOIN t2 b ON a.id = b.id SET b.v = a.v WHERE a.x = 'B.t2'",
		"UPDATE t1, t2 SET t1.v = t2.v WHERE t1.id = t2.id",
		"DELETE b FROM t1 a JOIN t2 b ON a.id = b.id WHERE a.k > 1.5",
		"DELETE FROM t1 WHERE t1.id = 1",
		"INSERT INTO t1 (a, b) VALUES (1, 'x.y@z.com'), (2, '$.a')",
		"INSERT INTO t1 SELECT * FROM t2 WHERE t2.v > 0",
		"INSERT INTO t1 (a) VALUES (1) ON DUPLICATE KEY UPDATE t1.a = VALUES(a)",
		"REPLACE INTO orders_db.t2 VALUES (1, 2)",
		"WITH x AS (SELECT 1 AS id) UPDATE t2 JOIN x USING(id) SET t2.v = x.id",
		"ALTER TABLE t1 ADD COLUMN c INT, ADD INDEX idx_c (c)",
		"CREATE INDEX i ON t1 (c)",
		"CREATE TABLE t3 (id INT PRIMARY KEY, v VARCHAR(10) DEFAULT 'a.b')",
		"CREATE TABLE t3 LIKE t2",
		"CREATE TRIGGER trg BEFORE INSERT ON t1 FOR EACH ROW SET NEW.v = OLD.v + 1",
		"RENAME TABLE a TO b",
		"TRUNCATE TABLE t1",
		"SET @x = 1",
		"SET NAMES utf8mb4",
		"SET @@session.sql_mode = ''",
		"SELECT a.*, b.v FROM t1 a LEFT JOIN t2 b ON a.id = b.id",
		"(SELECT 1) UNION (SELECT 2)",
		"UPDATE t1 SET v = NOW(), w = CONCAT('a', 'b') -- B.t2 写在注释里\n WHERE id = 1",
		"UPDATE t1 SET v = 1 /* B.t2 */ WHERE id = 1",
		"UPDATE t1 SET v = 1 # B.t2\n WHERE id = 1",
		"CALL p_cleanup(1)",
		"BEGIN",
		"COMMIT",
		"LOCK TABLES t1 WRITE, t2 READ",
	} {
		if !StaysInDatabase("MySQL 8.0", db, []string{sql}) {
			t.Errorf("只触及 %s 的语句被判为越库: %q", db, sql)
		}
	}
}

// 库名大小写:Linux 上的 MySQL 库名区分大小写,orders_db 与 ORDERS_DB 是两个库。
func TestStaysInDatabase_MySQLCaseIsExact(t *testing.T) {
	if StaysInDatabase("MySQL 8.0", "orders_db", []string{"UPDATE ORDERS_DB.t SET v = 1"}) {
		t.Error("大小写不同的库名不该被当成窗口库")
	}
}

// Oracle:窗口开给 TargetSchema;限定名、db link、动态 SQL、切 schema 都越得出去。
func TestStaysInDatabase_Oracle(t *testing.T) {
	const db = "SCOTT"
	for _, sql := range []string{
		"UPDATE hr.emp SET sal = 1",
		"UPDATE emp SET sal = (SELECT 1 FROM hr . t)",
		"INSERT INTO emp@remote VALUES (1)",
		"ALTER SESSION SET CURRENT_SCHEMA = HR",
		"BEGIN EXECUTE IMMEDIATE 'UPDATE hr.emp SET sal = 1'; END;",
		"BEGIN hr.pkg.run; END;",
		"CREATE SYNONYM s FOR hr.emp",
		"CREATE PUBLIC SYNONYM s FOR emp",
		"UPDATE \"scott\".emp SET sal = 1", // 带引号的小写是另一个 schema
		"UPDATE emp SET v = q'[it's]', w = (SELECT 1 FROM hr.t)",
	} {
		if StaysInDatabase("Oracle 19c", db, []string{sql}) {
			t.Errorf("越出 schema %s 却被判为不越库: %q", db, sql)
		}
	}
	for _, sql := range []string{
		"UPDATE emp SET sal = 1 WHERE empno = 7369",
		"UPDATE scott.emp SET sal = 1",
		"UPDATE \"SCOTT\".emp SET sal = 1",
		"INSERT INTO emp (id) VALUES (emp_seq.NEXTVAL)",
		"UPDATE emp SET note = q'[hr.emp it's]' WHERE 1 = 1",
		"BEGIN UPDATE emp SET sal = 1; COMMIT; END;",
		"CREATE OR REPLACE TRIGGER trg BEFORE INSERT ON emp FOR EACH ROW BEGIN :NEW.id := 1; END;",
	} {
		if !StaysInDatabase("Oracle 19c", db, []string{sql}) {
			t.Errorf("只触及 %s 的语句被判为越库: %q", db, sql)
		}
	}
}

// PostgreSQL 的限定名到不了别的库(schema 在库里面);但 dblink 能。
func TestStaysInDatabase_Postgres(t *testing.T) {
	if !StaysInDatabase("PostgreSQL 15", "app", []string{"UPDATE other_schema.t SET v = 1"}) {
		t.Error("PG 的 schema 限定名不越库")
	}
	for _, sql := range []string{
		"SELECT dblink_exec('dbname=b', 'UPDATE t SET v = 1')",
		"SELECT \"dblink_exec\"('dbname=b', 'x')",
	} {
		if StaysInDatabase("PostgreSQL 15", "app", []string{sql}) {
			t.Errorf("dblink 越库却被判为不越库: %q", sql)
		}
	}
}

// 说不清是哪个库就不放行。
func TestStaysInDatabase_EmptyDB(t *testing.T) {
	if StaysInDatabase("MySQL 8.0", "", []string{"DROP TABLE t"}) {
		t.Error("窗口库名为空时不该判为不越库")
	}
}

// 2026-10-26 生产:班车 AP-2638(prod-g04-game-h01 / G04)生效中,上包脚本(AP-2646)与建表批次
// (AP-2643)照样送审。a8d3768 把"认不出就放行"改成"认不出就送审"时,包体里的
// dbms_output.put_line(…) / pk_other.f(…) / EXECUTE IMMEDIATE,以及 COMMENT ON COLUMN t.col,
// 都被当成了别库。
func TestStaysInDatabase_OracleDDLInWindow(t *testing.T) {
	const db = "G04"
	stays := []string{
		`CREATE OR REPLACE PACKAGE "PK_AGENT_WALLET" is
  -- jackpot supervisor 校验
  procedure VerifyBingoSupervisor(p_guid in varchar2, p_res out varchar2);
end PK_AGENT_WALLET;`,
		`CREATE OR REPLACE PACKAGE BODY "PK_AGENT_WALLET" is
  procedure VerifyBingoSupervisor(p_guid in varchar2, p_res out varchar2) is
    v_r t_user%rowtype;
  begin
    select * into v_r from t_user where guid = p_guid;
    dbms_output.put_line(v_r.name);
    pk_other.do_it(p_guid);
    execute immediate 'truncate table t_tmp';
    p_res := sqlerrm;
  end;
end PK_AGENT_WALLET;`,
		`CREATE OR REPLACE PACKAGE BODY G04.pk is procedure p is begin hr.pkg.run; end; end;`,
		`CREATE OR REPLACE PROCEDURE p(a IN t.col%TYPE) IS BEGIN dbms_output.put_line(a); END;`,
		`CREATE OR REPLACE FUNCTION f(a NUMBER) RETURN NUMBER IS BEGIN RETURN pk_util.calc(a); END;`,
		`CREATE OR REPLACE TYPE t_obj AS OBJECT (a NUMBER)`,
		`create table T_BINGO_LUCKYNUM_DIY_LOG ( id NUMBER(16) not null, createtime DATE default sysdate not null )`,
		`comment on table T_BINGO_LUCKYNUM_DIY_LOG is 'Rush幸运球 DIY'`,
		`comment on column T_BINGO_LUCKYNUM_DIY_LOG.id is '主键（SEQ_BINGO_LUCKYNUM_DIY_LOG）'`,
		`comment on column G04.T_BINGO_LUCKYNUM_DIY_LOG.id is '主键'`,
		`alter table T_BINGO_LUCKYNUM_DIY_LOG add constraint PK_BINGO_LUCKYNUM_DIY_LOG primary key (ID)`,
		`create index I_LUCKYNUM_DIY_LV on T_BINGO_LUCKYNUM_DIY_LOG (LOGINNAME, VID, CREATETIME) tablespace ts_g04_idx01`,
	}
	for _, sql := range stays {
		if !StaysInDatabase("Oracle 19c", db, []string{sql}) {
			t.Errorf("只触及 %s 的语句被判为越库: %.80q", db, sql)
		}
	}
	// 放宽只到代码体:对象本身建在别的 schema、注释别 schema 的列,仍算越库
	for _, sql := range []string{
		`CREATE OR REPLACE PACKAGE BODY hr.pk is procedure p is begin null; end; end;`,
		`CREATE OR REPLACE PROCEDURE hr.p IS BEGIN NULL; END;`,
		`comment on column HR.T.id is 'x'`,
		// 触发器不放宽:它挂在表上,建好就会被别人的 DML 触发
		`CREATE OR REPLACE TRIGGER trg BEFORE INSERT ON emp FOR EACH ROW BEGIN hr.pkg.log(:NEW.id); END;`,
		// 匿名块里的动态 SQL 照旧看不见
		`BEGIN EXECUTE IMMEDIATE 'UPDATE hr.emp SET sal = 1'; END;`,
	} {
		if StaysInDatabase("Oracle 19c", db, []string{sql}) {
			t.Errorf("越出 schema %s 却被判为不越库: %.80q", db, sql)
		}
	}
	// MySQL 不受这次放宽影响:存储过程体照旧整句判
	if StaysInDatabase("MySQL 8.0", "a", []string{"CREATE PROCEDURE p() BEGIN UPDATE b.t SET v = 1; END"}) {
		t.Error("MySQL 过程体里写别库仍应判为越库")
	}
}

// 2026-10-26 回归排查:a8d3768 之后,Oracle 班车里调用包的 DML / 匿名块、EXEC 过程、
// ALTER SESSION SET NLS_*,以及 TiDB 的 BATCH / SPLIT / ADMIN CHECK、大小写库名,都丢了窗口。
func TestStaysInDatabase_RestoredEverydayStatements(t *testing.T) {
	for _, sql := range []string{
		"UPDATE t SET v = pk_util.f(1) WHERE id = 2",
		"UPDATE t SET v = pk_const.c_max WHERE id = 1",
		"BEGIN pk_util.do_it(1); END;",
		"BEGIN pk_util.g_flag := 1; END;",
		"DECLARE v_cnt NUMBER; BEGIN v_cnt := pk_util.cnt; pk_log.info('x'); END;",
		"DECLARE CURSOR c IS SELECT id FROM t; BEGIN FOR r IN c LOOP pk_util.p(r.id); END LOOP; END;",
		"BEGIN DBMS_STATS.GATHER_TABLE_STATS(ownname => 'APP', tabname => 'T'); END;",
		"BEGIN DBMS_OUTPUT.PUT_LINE('x'); END;",
		"UPDATE t SET v = DBMS_RANDOM.VALUE(1, 10)",
		"UPDATE t SET v = (SELECT COUNT(*) FROM TABLE(pk_util.list_ids))",
		"CALL pk_util.do_it(1)",
		"EXEC pk_util.do_it(1)",
		"EXECUTE pk_util.do_it(1)",
		"EXEC p(1)",
		"BEGIN APP.pk_util.do_it(1); END;",
		"ALTER SESSION SET NLS_DATE_FORMAT = 'YYYY-MM-DD HH24:MI:SS'",
		"ALTER SESSION SET NLS_LANGUAGE = AMERICAN NLS_TERRITORY = AMERICA",
		"ALTER SESSION ENABLE PARALLEL DML",
	} {
		if !StaysInDatabase("Oracle 19c", "APP", []string{sql}) {
			t.Errorf("只触及 APP 的语句被判为越库: %q", sql)
		}
	}
	for _, sql := range []string{
		"UPDATE hr.emp SET v = pk_util.f(1)",              // 表位置照旧
		"BEGIN hr.pkg.run; END;",                          // 三段名照旧
		"EXECUTE IMMEDIATE 'UPDATE hr.emp SET sal = 1'",   // 动态 SQL
		"ALTER SESSION SET CURRENT_SCHEMA = HR",           // 换 schema
		"ALTER SESSION SET CONTAINER = PDB2",              // 换 PDB
		"ALTER SESSION SET NLS_DATE_FORMAT = 'x' CURRENT_SCHEMA = HR",
		"EXEC hr.pkg.run(1)",
		"SELECT * FROM hr.emp e WHERE e.id = pk_util.f(1)",
		// 能拿字符串执行任意语句 / 建作业的系统包:与 EXECUTE IMMEDIATE 同样看不见真正执行的语句
		"BEGIN DBMS_UTILITY.EXEC_DDL_STATEMENT('DROP TABLE hr.emp'); END;",
		"DECLARE c INTEGER; n INTEGER; BEGIN c := DBMS_SQL.OPEN_CURSOR; DBMS_SQL.PARSE(c, 'UPDATE hr.emp SET sal = 1', DBMS_SQL.NATIVE); n := DBMS_SQL.EXECUTE(c); END;",
		"BEGIN DBMS_SCHEDULER.CREATE_JOB(job_name => 'j', job_type => 'PLSQL_BLOCK', job_action => 'UPDATE hr.emp SET sal = 1;', enabled => TRUE); END;",
		"BEGIN dbms_job.submit(:j, 'UPDATE hr.emp SET sal = 1;'); END;",
	} {
		if StaysInDatabase("Oracle 19c", "APP", []string{sql}) {
			t.Errorf("越出 APP 却被判为不越库: %q", sql)
		}
	}

	for _, sql := range []string{
		"BATCH ON id LIMIT 1000 DELETE FROM t WHERE created < '2026-01-01'",
		"BATCH LIMIT 500 UPDATE t SET v = 1 WHERE v = 0",
		"SPLIT TABLE t BETWEEN (0) AND (1000000) REGIONS 16",
		"ADMIN CHECK TABLE t",
		"FLASHBACK TABLE t TO t_restored",
		"RECOVER TABLE t",
		"UPDATE APP.t SET v = 1", // TiDB 库名不分大小写
	} {
		if !StaysInDatabase("TiDB 7.5", "app", []string{sql}) {
			t.Errorf("TiDB 只触及 app 的语句被判为越库: %q", sql)
		}
	}
	for _, sql := range []string{
		"BATCH ON id LIMIT 1000 DELETE FROM other.t WHERE 1 = 1",
		"SPLIT TABLE other.t BETWEEN (0) AND (10) REGIONS 2",
		"ADMIN CANCEL DDL JOBS 1",
		"FLASHBACK TABLE other.t",
	} {
		if StaysInDatabase("TiDB 7.5", "app", []string{sql}) {
			t.Errorf("TiDB 越出 app 却被判为不越库: %q", sql)
		}
	}
	// MySQL 保持原样:库名区分大小写,两段函数调用的第一段仍按库判
	if StaysInDatabase("MySQL 8.0", "app", []string{"UPDATE APP.t SET v = 1"}) {
		t.Error("MySQL 库名大小写不同仍应判为越库")
	}
	if StaysInDatabase("MySQL 8.0", "app", []string{"UPDATE t SET v = other.f(1)"}) {
		t.Error("MySQL 调用别库函数仍应判为越库")
	}
}

// 2026-10-26 讨论 06 / 14 的结论:
//   - 06-1 Oracle 存储代码体查"写明了的越库"(表位置上的别 schema、db link、动态 SQL 系统包),
//     EXECUTE IMMEDIATE 与普通包调用不查;
//   - 06-2 EXECUTE IMMEDIATE '写死的语句' 再判一次,拼接的照旧送审;
//   - 14 MySQL 建存储过程 / 函数 / 事件时过程体里的 PREPARE / EXECUTE 不按越库判,写明的别库照判。
func TestStaysInDatabase_DecisionsOf20261026(t *testing.T) {
	const db = "APP"
	stays := []string{
		// 06-1:正常包体照常放行
		`CREATE OR REPLACE PACKAGE BODY pk IS
  PROCEDURE p(p_id NUMBER) IS
    v_r t%ROWTYPE;
    v_n NUMBER;
  BEGIN
    SELECT * INTO v_r FROM t WHERE id = p_id;
    SELECT COUNT(*) INTO v_n FROM app.t2;
    FETCH c INTO v_r.a, v_r.b;
    UPDATE t SET v = pk_util.f(v_r.a) WHERE id = p_id RETURNING v INTO v_r.v;
    INSERT INTO t_log (id) VALUES (seq_log.NEXTVAL);
    DELETE FROM t_tmp;
    EXECUTE IMMEDIATE 'truncate table t_stage';
    EXECUTE IMMEDIATE 'insert into ' || v_tab || ' values (1)';
    hr.pkg.notify(p_id);
    dbms_output.put_line('ok');
  END;
END pk;`,
		// 06-2:写死的语句、只动自己 schema
		"BEGIN EXECUTE IMMEDIATE 'truncate table t'; END;",
		"BEGIN EXECUTE IMMEDIATE 'ALTER TABLE t ADD c NUMBER'; END;",
		"BEGIN EXECUTE IMMEDIATE 'UPDATE t SET v = :1 WHERE id = :2' USING 1, 2; END;",
		"BEGIN EXECUTE IMMEDIATE 'UPDATE app.t SET v = 1'; END;",
	}
	for _, sql := range stays {
		if !StaysInDatabase("Oracle 19c", db, []string{sql}) {
			t.Errorf("应放行: %.90q", sql)
		}
	}
	for _, sql := range []string{
		// 06-1:代码体里写明的越库
		"CREATE OR REPLACE PROCEDURE p AS BEGIN UPDATE hr.emp SET sal = 1; END;",
		"CREATE OR REPLACE PROCEDURE p AS BEGIN DELETE FROM hr.emp; END;",
		"CREATE OR REPLACE PROCEDURE p AS BEGIN INSERT INTO hr.emp (id) VALUES (1); END;",
		"CREATE OR REPLACE PROCEDURE p AS CURSOR c IS SELECT * FROM hr.emp; BEGIN NULL; END;",
		"CREATE OR REPLACE PROCEDURE p AS BEGIN MERGE INTO hr.emp e USING t s ON (e.id = s.id) WHEN MATCHED THEN UPDATE SET e.v = s.v; END;",
		"CREATE OR REPLACE PROCEDURE p AS BEGIN INSERT INTO t SELECT * FROM t2@remote; END;",
		"CREATE OR REPLACE PACKAGE BODY pk IS PROCEDURE p IS c INTEGER; BEGIN c := DBMS_SQL.OPEN_CURSOR; END; END;",
		"CREATE OR REPLACE PROCEDURE p AS BEGIN SYS.DBMS_SQL.PARSE(1, 'x', 1); END;",
		"CREATE OR REPLACE PROCEDURE hr.p AS BEGIN NULL; END;",
		// 06-2:越库的、拼接的、变量的照旧送审
		"BEGIN EXECUTE IMMEDIATE 'drop table hr.emp'; END;",
		"BEGIN EXECUTE IMMEDIATE 'truncate table ' || v_tab; END;",
		"BEGIN EXECUTE IMMEDIATE v_sql; END;",
		"BEGIN EXECUTE IMMEDIATE 'GRANT SELECT ON t TO u'; END;",
		"BEGIN EXECUTE IMMEDIATE q'[truncate table t]'; END;",
	} {
		if StaysInDatabase("Oracle 19c", db, []string{sql}) {
			t.Errorf("应判越库: %.90q", sql)
		}
	}

	// 14:MySQL 建含动态 SQL 的存储过程 / 事件,不跨库就放行
	for _, sql := range []string{
		"CREATE PROCEDURE p_part(IN d VARCHAR(8)) BEGIN SET @s = CONCAT('ALTER TABLE t_log ADD PARTITION (PARTITION p', d, ' VALUES LESS THAN (', d, '))'); PREPARE st FROM @s; EXECUTE st; DEALLOCATE PREPARE st; END",
		"CREATE DEFINER=`root`@`%` EVENT ev ON SCHEDULE EVERY 1 DAY DO BEGIN PREPARE st FROM @s; EXECUTE st; END",
	} {
		if !StaysInDatabase("MySQL 8.0", "app", []string{sql}) {
			t.Errorf("MySQL 建过程不跨库应放行: %.90q", sql)
		}
	}
	for _, sql := range []string{
		"CREATE PROCEDURE p() BEGIN PREPARE st FROM @s; EXECUTE st; UPDATE other.t SET v = 1; END", // 写明的别库照判
		"PREPARE st FROM 'DELETE FROM t'",                                                          // 直接执行照旧
		"EXECUTE st",
		"CREATE TRIGGER trg BEFORE INSERT ON t FOR EACH ROW BEGIN PREPARE st FROM @s; END", // 只放过程 / 函数 / 事件
	} {
		if StaysInDatabase("MySQL 8.0", "app", []string{sql}) {
			t.Errorf("MySQL 应判越库: %.90q", sql)
		}
	}
}
