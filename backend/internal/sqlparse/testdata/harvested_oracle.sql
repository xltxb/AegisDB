-- 由 TestHarvestCorpus 从现有测试收集,勿手改。
-- @case gateway/oracle_create_errors_test.go
/* 头注释 */ CREATE OR REPLACE TRIGGER trg BEFORE INSERT ON t BEGIN NULL; END;
-- @case gateway/oracle_serveroutput_test.go
/* 头注释 */ SET FEEDBACK OFF
-- @case gateway/oracle_sqlplus_cmd_test.go
/*!40000 PROMPT */ DROP TABLE t
-- @case gateway/oracle_sqlplus_cmd_test.go
/*!PROMPT*/ DROP TABLE t
-- @case gateway/oracle_sqlplus_cmd_test.go
/*T! REM */ DROP TABLE t
-- @case gateway/oracle_compile_test.go
ALTER FUNCTION "APP"."APP_PKG" COMPILE
-- @case gateway/oracle_invalid_test.go
ALTER MATERIALIZED VIEW "APP"."MV_ORDER" COMPILE
-- @case gateway/oracle_compile_test.go
ALTER PACKAGE "APP"."APP_PKG" COMPILE BODY
-- @case gateway/oracle_compile_test.go
ALTER PACKAGE "APP"."APP_PKG" COMPILE PACKAGE
-- @case gateway/oracle_create_errors_test.go
ALTER PACKAGE p COMPILE
-- @case gateway/oracle_compile_test.go
ALTER PROCEDURE "APP"."APP_PKG" COMPILE
-- @case gateway/oracle_compile_test.go
ALTER PROCEDURE "P" COMPILE
-- @case gateway/oracle_create_errors_test.go
ALTER SESSION SET PLSQL_WARNINGS = 'ENABLE:ALL'
-- @case sqlutil/sqlplus_test.go
ALTER TABLE t ADD
COLUMN x INT
-- @case sqlutil/sqlplus_test.go
ALTER TABLE t ADD
COLUMN x INT;
SELECT 1;
-- @case gateway/oracle_sqlplus_cmd_test.go
ALTER TABLE t ADD c NUMBER
-- @case gateway/oracle_compile_test.go
ALTER TRIGGER "APP"."APP_PKG" COMPILE
-- @case gateway/oracle_compile_test.go
ALTER TYPE "APP"."APP_PKG" COMPILE BODY
-- @case gateway/oracle_compile_test.go
ALTER TYPE "APP"."APP_PKG" COMPILE TYPE
-- @case gateway/oracle_invalid_test.go
ALTER VIEW "APP"."V_ORDER" COMPILE
-- @case gateway/oracle_serveroutput_test.go
BEGIN
	  FOR i IN 1..5 LOOP DBMS_OUTPUT.PUT_LINE(RPAD(CHR(96 + i), 10000, CHR(96 + i))); END LOOP;
	  DBMS_OUTPUT.PUT_LINE('a' || CHR(10) || 'b');
	END;
-- @case review/plsql_body_test.go
BEGIN
  DROP TABLE a;
  DROP TABLE b;
END;
-- @case review/plsql_body_test.go
BEGIN
  DROP TABLE t_orders;
END;
-- @case sqlutil/plsql_begin_test.go
BEGIN
  FOR r IN (SELECT 1 FROM dual) LOOP
    NULL;
  END LOOP;
END;
-- @case sqlutil/plsql_begin_test.go
BEGIN
  UPDATE t SET x = 1 WHERE id = 2;
  COMMIT;
END;
-- @case review/plsql_body_test.go
BEGIN
  UPDATE t_orders SET flag = 1;
END;
-- @case sqlutil/plsql_begin_test.go
BEGIN
  my_proc();
END;
-- @case gateway/oracle_binds_test.go
BEGIN :n := 1; END;
-- @case gateway/oracle_binds_test.go
BEGIN :n := :n + 1; END;
-- @case gateway/oracle_binds_test.go
BEGIN :s := :n || :s; END;
-- @case sqlutil/sqlplus_test.go
BEGIN DBMS_OUTPUT.PUT_LINE('a'); END;
-- @case gateway/oracle_serveroutput_test.go
BEGIN DBMS_OUTPUT.PUT_LINE('before error'); RAISE_APPLICATION_ERROR(-20001, 'boom'); END;
-- @case gateway/oracle_serveroutput_test.go
BEGIN DBMS_OUTPUT.PUT_LINE('hello'); DBMS_OUTPUT.PUT_LINE('中文 输出'); DBMS_OUTPUT.PUT_LINE(NULL); END;
-- @case gateway/oracle_serveroutput_test.go
BEGIN DBMS_OUTPUT.PUT_LINE('x'); RAISE_APPLICATION_ERROR(-20002, 'y'); END;
-- @case gateway/plsql_rows_test.go
BEGIN DELETE FROM
-- @case gateway/oracle_serveroutput_test.go
BEGIN FOR i IN 1..2500 LOOP DBMS_OUTPUT.PUT_LINE('line ' || i); END LOOP; END;
-- @case gateway/oracle_serveroutput_test.go
BEGIN NULL; END;
-- @case sqlutil/plsql_begin_test.go
BEGIN WORK; DELETE FROM t; COMMIT;
-- @case sqlutil/plsql_begin_test.go
BEGIN p1(); END;
/
SELECT 1 FROM dual;
-- @case sqlutil/plsql_begin_test.go
BEGIN p1(); END;
SELECT 1 FROM dual;
-- @case sqlutil/plsql_begin_test.go
BEGIN p1(); p2(); END;
-- @case gateway/oracle_sqlplus_cmd_test.go
CALL p()
-- @case gateway/oracle_tableddl_test.go
CREATE INDEX "APP"."IDX_ORDER_CREATED" ON "APP"."T_ORDER" ("CREATED_AT" DESC) TABLESPACE "TBS_IDX";
-- @case gateway/oracle_create_errors_test.go
CREATE INDEX i ON t(a)
-- @case gateway/oracle_create_errors_test.go
CREATE OR REPLACE EDITIONABLE PROCEDURE "MixedCase" IS BEGIN NULL; END;
-- @case sqlutil/split_plsql_test.go
CREATE OR REPLACE FUNCTION f RETURN NUMBER IS
BEGIN
  RETURN 1;
END;
-- @case sqlutil/split_plsql_test.go
CREATE OR REPLACE FUNCTION f RETURN NUMBER IS
BEGIN
  RETURN 1;
END;
/
-- @case gateway/bizquery_hidden_oracle_test.go
CREATE OR REPLACE FUNCTION vz_hid_auto RETURN NUMBER IS PRAGMA AUTONOMOUS_TRANSACTION; BEGIN INSERT INTO vz_hid_log VALUES (1); COMMIT; RETURN 1; END;
-- @case gateway/bizquery_hidden_oracle_test.go
CREATE OR REPLACE FUNCTION vz_hid_http RETURN VARCHAR2 IS BEGIN RETURN UTL_HTTP.REQUEST('http://127.0.0.1:1/'); END;
-- @case gateway/bizquery_hidden_oracle_test.go
CREATE OR REPLACE FUNCTION vz_hid_outer RETURN NUMBER IS BEGIN RETURN vz_hid_auto(); END;
-- @case gateway/bizquery_hidden_oracle_test.go
CREATE OR REPLACE FUNCTION vz_hid_pure(x NUMBER) RETURN NUMBER IS BEGIN RETURN x * 2; END;
-- @case gateway/oracle_create_errors_test.go
CREATE OR REPLACE NONEDITIONABLE FUNCTION f RETURN NUMBER IS BEGIN RETURN 1; END;
-- @case gateway/oracle_create_errors_test.go
CREATE OR REPLACE PACKAGE
-- @case gateway/oracle_create_errors_test.go
CREATE OR REPLACE PACKAGE BODY
-- @case sqlutil/split_plsql_test.go
CREATE OR REPLACE PACKAGE BODY app_pkg AS
  PROCEDURE greet(p_id NUMBER) IS
    v_n VARCHAR2(50);
  BEGIN
    SELECT name INTO v_n FROM users WHERE id = p_id;
    DBMS_OUTPUT.PUT_LINE(v_n);
  END greet;
END app_pkg;
-- @case gateway/oracle_create_errors_test.go
CREATE OR REPLACE PACKAGE BODY pk_agent AS ... END;
-- @case sqlutil/split_plsql_test.go
CREATE OR REPLACE PACKAGE BODY pk_y AS
  n NUMBER := 0;
  PROCEDURE p IS BEGIN NULL; END;
END pk_y;
-- @case gateway/bizquery_hidden_oracle_test.go
CREATE OR REPLACE PACKAGE BODY vz_hid_pkg AS FUNCTION f RETURN NUMBER IS PRAGMA AUTONOMOUS_TRANSACTION; BEGIN INSERT INTO vz_hid_log VALUES (3); COMMIT; RETURN 1; END; END;
-- @case sqlutil/split_plsql_test.go
CREATE OR REPLACE PACKAGE app_pkg AS
  PROCEDURE greet(p_id NUMBER);
END app_pkg;
-- @case sqlutil/split_plsql_test.go
CREATE OR REPLACE PACKAGE app_pkg AS
  PROCEDURE greet(p_id NUMBER);
END app_pkg;
/
-- @case sqlutil/split_plsql_test.go
CREATE OR REPLACE PACKAGE pk_y AS
  PROCEDURE p;
END;
-- @case gateway/bizquery_hidden_oracle_test.go
CREATE OR REPLACE PACKAGE vz_hid_pkg AS FUNCTION f RETURN NUMBER; END;
-- @case gateway/oracle_create_errors_test.go
CREATE OR REPLACE PROCEDURE
-- @case gateway/oracle_create_errors_test.go
CREATE OR REPLACE PROCEDURE "S"."P" IS BEGIN NULL; END;
-- @case sqlutil/split_plsql_test.go
CREATE OR REPLACE PROCEDURE p AS
BEGIN
  EXECUTE IMMEDIATE 'DROP TABLE t';
END;
/
-- @case sqlutil/split_plsql_test.go
CREATE OR REPLACE PROCEDURE p AS
BEGIN
  NULL;
END;
-- @case sqlutil/split_plsql_test.go
CREATE OR REPLACE PROCEDURE p AS
BEGIN
  NULL;
END;
/
DROP TABLE victim;
-- @case sqlutil/sqlplus_test.go
CREATE OR REPLACE PROCEDURE p AS BEGIN NULL; END;
-- @case sqlutil/sqlplus_test.go
CREATE OR REPLACE PROCEDURE p AS BEGIN NULL; END;
/
-- @case review/plsql_body_test.go
CREATE OR REPLACE PROCEDURE p IS BEGIN
  DROP TABLE t_orders;
END;
-- @case review/plsql_body_test.go
CREATE OR REPLACE PROCEDURE p IS BEGIN
  TRUNCATE TABLE t_orders;
END;
-- @case gateway/oracle_create_errors_test.go
CREATE OR REPLACE PROCEDURE zz_in_schema IS BEGIN still_missing; END;
-- @case gateway/oracle_create_errors_test.go
CREATE OR REPLACE TRIGGER
-- @case gateway/oracle_binds_test.go
CREATE OR REPLACE TRIGGER t BEFORE INSERT ON x FOR EACH ROW BEGIN :NEW.id := 1; END;
-- @case gateway/oracle_create_errors_test.go
CREATE OR REPLACE TYPE BODY ty AS END;
-- @case gateway/oracle_create_errors_test.go
CREATE OR REPLACE VIEW v AS SELECT 1 FROM dual
-- @case gateway/bizquery_hidden_oracle_test.go
CREATE OR REPLACE VIEW vz_hid_clean_v AS SELECT vz_hid_pure(n) x FROM vz_hid_log
-- @case gateway/bizquery_hidden_oracle_test.go
CREATE OR REPLACE VIEW vz_hid_v AS SELECT vz_hid_auto() x FROM dual
-- @case sqlutil/split_plsql_test.go
CREATE OR REPLACE package PK_X as
  procedure p;
end;
-- @case sqlutil/split_plsql_test.go
CREATE OR REPLACE package body "PK_X" as
  flag constant number := 0;
  procedure p is begin null; end;
end;
-- @case gateway/oracle_create_errors_test.go
CREATE PACKAGE pk_a AS END;
-- @case sqlutil/split_plsql_test.go
CREATE PROCEDURE p() BEGIN SELECT 1
-- @case sqlutil/split_plsql_test.go
CREATE PROCEDURE p() BEGIN SELECT 1; END; SELECT 2;
-- @case gateway/oracle_desc_test.go
CREATE PUBLIC SYNONYM VDESC_GIFT FOR VDESC_B.VDESC_GIFT
-- @case gateway/bizquery_hidden_oracle_test.go
CREATE PUBLIC SYNONYM vz_hid_syn FOR vz_hid_auto
-- @case gateway/oracle_desc_test.go
CREATE SYNONYM VDESC_C.VDESC_GIFT FOR VDESC_A.VDESC_GIFT
-- @case gateway/oracle_tableddl_test.go
CREATE TABLE
-- @case gateway/oracle_tableddl_test.go
CREATE TABLE "APP"."T_MIN" (
-- @case gateway/oracle_tableddl_test.go
CREATE TABLE "APP"."T_ORDER" (
-- @case gateway/oracle_desc_test.go
CREATE TABLE VDESC_A.VDESC_GIFT (GMTYPE VARCHAR2(10) NOT NULL, GIFT_ID NUMBER(10) NOT NULL, PRICE NUMBER(10,2))
-- @case gateway/oracle_desc_test.go
CREATE TABLE VDESC_B.VDESC_GIFT (GMTYPE VARCHAR2(10) NOT NULL, GIFT_ID NUMBER(10) NOT NULL, PRICE NUMBER(10,2), B_ONLY DATE)
-- @case gateway/oracle_desc_test.go
CREATE TABLE VDESC_B.VDESC_ONLY_B (Y NUMBER)
-- @case gateway/bizquery_oracle_test.go
CREATE TABLE biz_ro_t (n NUMBER)
-- @case gateway/oracle_sqlplus_cmd_test.go
CREATE TABLE copy_of_t AS SELECT * FROM t
-- @case gateway/oracle_create_errors_test.go
CREATE TABLE t (a NUMBER)
-- @case sqlutil/sqlplus_test.go
CREATE TABLE t (id NUMBER)
-- @case sqlutil/sqlplus_test.go
CREATE TABLE t (id NUMBER);
-- @case gateway/bizquery_hidden_oracle_test.go
CREATE TABLE vz_hid_log (n NUMBER)
-- @case gateway/oracle_create_errors_test.go
CREATE TYPE ty AS OBJECT (a NUMBER);
-- @case gateway/oracle_tableddl_test.go
CREATE UNIQUE INDEX "APP"."UK_ORDER_TITLE"
-- @case gateway/oracle_create_errors_test.go
CREATE USER
-- @case gateway/oracle_desc_test.go
CREATE USER VDESC_A IDENTIFIED BY x QUOTA UNLIMITED ON USERS
-- @case gateway/oracle_desc_test.go
CREATE USER VDESC_B IDENTIFIED BY x QUOTA UNLIMITED ON USERS
-- @case gateway/oracle_desc_test.go
CREATE USER VDESC_C IDENTIFIED BY x QUOTA UNLIMITED ON USERS
-- @case gateway/oracle_binds_test.go
CREATE 语句不该绑定
-- @case sqlutil/split_plsql_test.go
DECLARE
  v NUMBER;
BEGIN
  v := 1;
  UPDATE t SET x = v;
END;
-- @case sqlutil/split_plsql_test.go
DECLARE
  v NUMBER;
BEGIN
  v := 1;
  UPDATE t SET x = v;
END;
/
-- @case review/plsql_body_test.go
DECLARE
  v_cnt NUMBER;
BEGIN
  v_cnt := 0;
  SELECT COUNT(1) INTO v_cnt FROM t_orders WHERE id = 1;
  IF v_cnt > 0 THEN
    UPDATE t_orders SET flag = 1 WHERE id = 1;
  END IF;
END;
-- @case gateway/plsql_rows_test.go
DECLARE n NUMBER; BEGIN UPDATE
-- @case review/plsql_body_test.go
DECLARE v NUMBER;
BEGIN
  DELETE FROM t_orders;
END;
-- @case gateway/oracle_sqlplus_cmd_test.go
DECLARE x NUMBER; BEGIN NULL; END;
-- @case review/plsql_body_test.go
DECLARE 块里的无 WHERE DELETE
-- @case gateway/oracle_sqlplus_cmd_test.go
DELETE FROM t WHERE 1=0
-- @case gateway/oracle_sqlplus_test.go
DESC "Weird Name"
-- @case gateway/oracle_sqlplus_test.go
DESC (SELECT 1 FROM DUAL)
-- @case gateway/oracle_desc_test.go
DESC colorgame_uat.t_bingo_gift_setup
-- @case gateway/oracle_sqlplus_test.go
DESC t' OR '1'='1
-- @case gateway/oracle_sqlplus_test.go
DESC t_user
-- @case gateway/oracle_sqlplus_test.go
DESC t_user; DROP TABLE t_x
-- @case gateway/bizquery_hidden_oracle_test.go
DESC vz_hid_no_such
-- @case gateway/oracle_desc_test.go
DESC 应当被翻译
-- @case gateway/oracle_sqlplus_test.go
DESC 应当被翻译 —— 它和 SHOW 一样是客户端命令
-- @case gateway/oracle_desc_test.go
DESCRIBE vdesc_gift
-- @case gateway/bizquery_hidden_oracle_test.go
DROP FUNCTION vz_hid_auto
-- @case gateway/bizquery_hidden_oracle_test.go
DROP FUNCTION vz_hid_http
-- @case gateway/bizquery_hidden_oracle_test.go
DROP FUNCTION vz_hid_outer
-- @case gateway/bizquery_hidden_oracle_test.go
DROP FUNCTION vz_hid_pure
-- @case gateway/oracle_create_errors_test.go
DROP PACKAGE
-- @case gateway/bizquery_hidden_oracle_test.go
DROP PACKAGE vz_hid_pkg
-- @case gateway/oracle_create_errors_test.go
DROP PROCEDURE
-- @case gateway/oracle_desc_test.go
DROP PUBLIC SYNONYM VDESC_GIFT
-- @case gateway/bizquery_hidden_oracle_test.go
DROP PUBLIC SYNONYM vz_hid_syn
-- @case gateway/plsql_rows_test.go
DROP TABLE
-- @case gateway/bizquery_oracle_test.go
DROP TABLE biz_ro_t
-- @case gateway/oracle_sqlplus_cmd_test.go
DROP TABLE t
-- @case review/plsql_body_test.go
DROP TABLE t_orders;
-- @case sqlutil/split_plsql_test.go
DROP TABLE victim
-- @case gateway/bizquery_hidden_oracle_test.go
DROP TABLE vz_hid_log
-- @case gateway/oracle_create_errors_test.go
DROP USER
-- @case gateway/oracle_desc_test.go
DROP USER VDESC_A CASCADE
-- @case gateway/oracle_desc_test.go
DROP USER VDESC_B CASCADE
-- @case gateway/oracle_desc_test.go
DROP USER VDESC_C CASCADE
-- @case gateway/bizquery_hidden_oracle_test.go
DROP VIEW vz_hid_clean_v
-- @case gateway/bizquery_hidden_oracle_test.go
DROP VIEW vz_hid_v
-- @case gateway/oracle_sqlplus_cmd_test.go
EXPLAIN PLAN FOR SELECT 1 FROM dual
-- @case gateway/oracle_create_errors_test.go
GRANT CREATE SESSION TO
-- @case sqlutil/split_plsql_test.go
GRANT EXECUTE ON app_pkg TO reporter
-- @case gateway/oracle_sqlplus_cmd_test.go
GRANT SELECT ON t TO u
-- @case gateway/plsql_rows_test.go
INSERT INTO
-- @case gateway/bizquery_oracle_test.go
INSERT INTO biz_ro_t VALUES (1)
-- @case gateway/oracle_sqlplus_cmd_test.go
INSERT INTO t VALUES (1)
-- @case gateway/oracle_sqlplus_cmd_test.go
RENAME a TO b
-- @case gateway/oracle_binds_test.go
SELECT ":n" FROM dual
-- @case gateway/oracle_binds_test.go
SELECT ':n' FROM dual
-- @case gateway/oracle_serveroutput_test.go
SELECT 'SET SERVEROUTPUT ON' FROM DUAL
-- @case gateway/oracle_sqlplus_test.go
SELECT * FROM t_user
-- @case gateway/oracle_sqlplus_test.go
SELECT * FROM t_user ORDER BY id DESC
-- @case gateway/bizquery_hidden_oracle_test.go
SELECT * FROM vz_hid_clean_v
-- @case gateway/oracle_binds_test.go
SELECT /* :s */ :N FROM dual
-- @case sqlutil/sqlplus_test.go
SELECT 1
-- @case gateway/oracle_serveroutput_test.go
SELECT 1 AS n FROM DUAL
-- @case sqlutil/sqlplus_test.go
SELECT 1 FROM DUAL
-- @case sqlutil/sqlplus_test.go
SELECT 1 FROM DUAL;
-- @case gateway/bizquery_oracle_test.go
SELECT 1 FROM dual
-- @case gateway/oracle_binds_test.go
SELECT 1 FROM dual -- :n
-- @case sqlutil/sqlplus_test.go
SELECT 1 FROM dual;
-- @case review/plsql_body_test.go
SELECT 1 FROM dual;

BEGIN
  DROP TABLE t_orders;
END;
/
-- @case sqlutil/split_plsql_test.go
SELECT 2
-- @case gateway/oracle_binds_test.go
SELECT :n
-- @case gateway/oracle_binds_test.go
SELECT :n + 1 FROM dual
-- @case sqlutil/sqlplus_test.go
SELECT :n FROM dual
-- @case gateway/oracle_binds_test.go
SELECT :undeclared FROM dual
-- @case sqlutil/sqlplus_test.go
SELECT @sql
-- @case gateway/bizquery_oracle_test.go
SELECT COUNT(*) FROM all_objects a, all_objects b, all_objects c
-- @case gateway/bizquery_oracle_test.go
SELECT COUNT(*) FROM biz_ro_t
-- @case gateway/bizquery_hidden_oracle_test.go
SELECT COUNT(*) FROM vz_hid_log
-- @case gateway/bizquery_oracle_test.go
SELECT LEVEL AS n FROM dual CONNECT BY LEVEL <= 50000000
-- @case gateway/bizquery_hidden_oracle_test.go
SELECT NVL(MAX(n), 0), TO_CHAR(SYSDATE, 'YYYY') FROM vz_hid_log
-- @case gateway/bizquery_hidden_oracle_test.go
SELECT SYSDATE, USERENV('LANG') FROM dual
-- @case gateway/oracle_create_errors_test.go
SELECT SYS_CONTEXT('USERENV', 'CURRENT_SCHEMA') FROM dual
-- @case gateway/oracle_binds_test.go
SELECT TO_CHAR(SYSDATE, 'HH24:MI') FROM dual
-- @case gateway/oracle_create_errors_test.go
SELECT USER FROM dual
-- @case gateway/oracle_binds_test.go
SELECT a:n FROM t
-- @case gateway/oracle_sqlplus_cmd_test.go
SELECT host FROM t
-- @case gateway/plsql_rows_test.go
SELECT level FROM dual CONNECT BY level <= 3
-- @case gateway/bizquery_hidden_oracle_test.go
SELECT vz_hid_auto() FROM dual
-- @case gateway/bizquery_hidden_oracle_test.go
SELECT vz_hid_http() FROM dual
-- @case gateway/bizquery_hidden_oracle_test.go
SELECT vz_hid_outer() FROM dual
-- @case gateway/bizquery_hidden_oracle_test.go
SELECT vz_hid_pkg.f() FROM dual
-- @case gateway/bizquery_hidden_oracle_test.go
SELECT vz_hid_pure(21) FROM dual
-- @case gateway/bizquery_hidden_oracle_test.go
SELECT vz_hid_syn() FROM dual
-- @case gateway/bizquery_hidden_oracle_test.go
SELECT x FROM (SELECT vz_hid_auto() x FROM dual) WHERE 1 = 1
-- @case gateway/oracle_binds_test.go
SELECT x::int FROM t
-- @case sqlutil/sqlplus_test.go
SET @sql = CONCAT('a',
  'b')
-- @case sqlutil/sqlplus_test.go
SET @sql = CONCAT('a',
  'b');
SELECT @sql;
-- @case gateway/oracle_serveroutput_test.go
SET CONSTRAINT c1 IMMEDIATE
-- @case sqlutil/sqlplus_test.go
SET CONSTRAINTS ALL
 DEFERRED;
-- @case gateway/oracle_serveroutput_test.go
SET CONSTRAINTS ALL DEFERRED
-- @case gateway/oracle_serveroutput_test.go
SET DEFINE OFF
-- @case sqlutil/sqlplus_test.go
SET EMB ON
-- @case gateway/oracle_sqlplus_cmd_test.go
SET HEAD OFF
-- @case gateway/oracle_sqlplus_cmd_test.go
SET HEAD OFF
SELECT 1 FROM dual
-- @case gateway/oracle_serveroutput_test.go
SET LINESIZE 200
-- @case gateway/oracle_serveroutput_test.go
SET NAMES utf8mb4
-- @case sqlutil/sqlplus_test.go
SET NAMES utf8mb4;
SET search_path TO app;
SELECT 1;
-- @case sqlutil/sqlplus_test.go
SET NUMF 999
-- @case sqlutil/sqlplus_test.go
SET PAGES 0
-- @case sqlutil/sqlplus_test.go
SET SERVEROUT ON
-- @case gateway/oracle_serveroutput_test.go
SET SERVEROUTPUT ON
-- @case sqlutil/sqlplus_test.go
SET SERVEROUTPUT ON
set linesize 200
BEGIN DBMS_OUTPUT.PUT_LINE('a'); END;
/
SET DEFINE OFF
SELECT 1 FROM DUAL;
-- @case sqlutil/sqlplus_test.go
SET SERVEROUTPUT ON;
SELECT 1;
-- @case sqlutil/sqlplus_test.go
SET TIME ZONE
 'UTC'
-- @case sqlutil/sqlplus_test.go
SET TIME ZONE
 'UTC';
-- @case gateway/oracle_serveroutput_test.go
SET TIMING ON
-- @case gateway/oracle_serveroutput_test.go
SET TRANSACTION READ ONLY
-- @case sqlutil/sqlplus_test.go
SET TRIMS ON
-- @case sqlutil/sqlplus_test.go
SET WRA OFF
-- @case sqlutil/sqlplus_test.go
SET autocommit =
 0
-- @case sqlutil/sqlplus_test.go
SET autocommit =
 0;
-- @case sqlutil/sqlplus_test.go
SET autocommit =
 0;
SELECT 1;
-- @case gateway/plsql_rows_test.go
SET id = id + 1; END;
-- @case sqlutil/sqlplus_test.go
SET long_query_time =
 1;
-- @case sqlutil/sqlplus_test.go
SET search_path TO
 app;
-- @case sqlutil/sqlplus_test.go
SET search_path TO app
-- @case sqlutil/sqlplus_test.go
SET sql_mode =
 'STRICT';
-- @case sqlutil/sqlplus_test.go
SET statement_timeout =
 1000;
-- @case sqlutil/sqlplus_test.go
SET time_zone =
 '+8:00';
-- @case gateway/oracle_serveroutput_test.go
SET 不该去连数据库
-- @case gateway/oracle_sqlplus_test.go
SHOW CON_NAME
-- @case gateway/oracle_sqlplus_cmd_test.go
SHOW ERR PROCEDURE p_x;
-- @case gateway/oracle_sqlplus_cmd_test.go
SHOW ERRORS
-- @case gateway/oracle_sqlplus_cmd_test.go
SHOW ERRORS PROCEDURE "p x"
-- @case gateway/oracle_sqlplus_cmd_test.go
SHOW ERRORS PROCEDURE p
-- @case gateway/oracle_sqlplus_cmd_test.go
SHOW ERRORS PROCEDURE p'x
-- @case gateway/oracle_sqlplus_cmd_test.go
SHOW ERRORS TABLE t
-- @case sqlutil/sqlplus_test.go
SHOW ERRORS;
SELECT 1;
-- @case gateway/oracle_sqlplus_test.go
SHOW PARAMETER cursor
-- @case gateway/oracle_sqlplus_test.go
SHOW PARAMETER open_cursors
-- @case gateway/oracle_sqlplus_test.go
SHOW RELEASE
-- @case gateway/oracle_sqlplus_test.go
SHOW SQLCODE
-- @case gateway/oracle_sqlplus_cmd_test.go
SHOW USER
-- @case gateway/oracle_sqlplus_test.go
SHOW USER 应当被翻译 —— 否则原样发给 Oracle 就是 ORA-00900
-- @case gateway/oracle_sqlplus_test.go
SHOW USER;
-- @case gateway/oracle_sqlplus_test.go
SHOW VERSION
-- @case gateway/oracle_sqlplus_test.go
Show   User
-- @case sqlutil/sqlplus_test.go
UPDATE t
SET linesize = 1
WHERE id = 1;
-- @case sqlutil/split_plsql_test.go
UPDATE t SET x = 1
-- @case gateway/bizquery_hidden_oracle_test.go
WITH t AS (SELECT 1 n FROM dual) SELECT * FROM t
-- @case sqlutil/plsql_begin_test.go
begin
  null;
end;
-- @case gateway/oracle_create_errors_test.go
create or replace package  body
  UAT_P_G04.PK_AGENT is ...
-- @case gateway/oracle_desc_test.go
desc T_BINGO_GIFT_SETUP;
-- @case gateway/oracle_desc_test.go
desc colorgame_uat.t_bingo_gift_setup
-- @case gateway/oracle_desc_test.go
desc t_bingo_gift_setup
-- @case gateway/oracle_desc_test.go
desc vdesc_a.vdesc_only_b
-- @case gateway/oracle_desc_test.go
desc vdesc_b.vdesc_gift
-- @case gateway/oracle_desc_test.go
desc vdesc_c.vdesc_gift
-- @case gateway/oracle_desc_test.go
desc vdesc_gift
-- @case gateway/oracle_desc_test.go
desc vdesc_gift;
-- @case gateway/oracle_desc_test.go
desc vdesc_only_b
-- @case gateway/bizquery_hidden_oracle_test.go
desc vz_hid_log
-- @case gateway/oracle_sqlplus_test.go
describe scott.emp
-- @case gateway/bizquery_hidden_oracle_test.go
select * from VZ_HID_V
-- @case gateway/oracle_sqlplus_cmd_test.go
select * from emp connect by prior empno = mgr
-- @case gateway/oracle_serveroutput_test.go
set echo off
-- @case sqlutil/sqlplus_test.go
set feed off
-- @case sqlutil/sqlplus_test.go
set heads off
-- @case sqlutil/sqlplus_test.go
set lin 120
-- @case sqlutil/sqlplus_test.go
set linesize 200
-- @case sqlutil/sqlplus_test.go
set newp none
-- @case gateway/oracle_serveroutput_test.go
set pagesize 0
-- @case gateway/oracle_serveroutput_test.go
set role all
-- @case gateway/oracle_serveroutput_test.go
set serverout on;
-- @case gateway/oracle_serveroutput_test.go
set serveroutput on size 100000
-- @case sqlutil/sqlplus_test.go
set sqlbl on
-- @case sqlutil/sqlplus_test.go
set und off
-- @case sqlutil/sqlplus_test.go
set ver off
-- @case gateway/oracle_sqlplus_cmd_test.go
show err
-- @case gateway/oracle_sqlplus_cmd_test.go
show errors package body app.pk_agent
-- @case gateway/oracle_sqlplus_test.go
show user
-- @case gateway/oracle_sqlplus_test.go
show user ;
