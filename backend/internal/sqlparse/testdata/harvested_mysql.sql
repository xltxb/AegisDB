-- 由 TestHarvestCorpus 从现有测试收集,勿手改。
-- @case gateway/split_judge_test.go
# a
/* b */ -- c
# d
DROP TABLE t
-- @case gateway/split_judge_test.go
# it's
UPDATE t SET a = 1
-- @case gateway/split_judge_test.go
# note
DELETE FROM t
-- @case gateway/split_judge_test.go
# x
DELETE FROM t
-- @case gateway/hash_comment_test.go
# 备注
SELECT * FROM t
-- @case gateway/hash_comment_test.go
# 开头的注释
SELECT 1
-- @case gateway/split_judge_test.go
#a
#b
  DELETE FROM t
-- @case gateway/window_scope_test.go
(SELECT 1) UNION (SELECT 2)
-- @case gateway/explain_plan_test.go
(SELECT a FROM t LIMIT 10) UNION (SELECT a FROM t2 LIMIT 10) LIMIT 10
-- @case gateway/explain_plan_test.go
(SELECT a FROM t) UNION (SELECT a FROM t2)
-- @case gateway/bizquery_forms_test.go
-- c
DESCRIBE t_user
-- @case gateway/risk_metadata_read_test.go
-- 看看建表语句
SHOW CREATE TABLE t
-- @case gateway/bizquery_explain_test.go
-- 看计划
EXPLAIN PLAN FOR SELECT 1 FROM dual ;
-- @case gateway/mariadb_exec_comment_test.go
/* M! 这不是可执行注释 */ SELECT 1
-- @case gateway/explain_test.go
/* comment */ EXPLAIN DELETE FROM t
-- @case review/checks_spec_test.go
/* crm_order_sync_step1 */ SELECT a FROM s.t_a;
-- @case review/checks_spec_test.go
/* j */ SELECT 1 FROM s.t0
-- @case gateway/mask_evade_test.go
/* nightly */ SELECT x FROM (SELECT id_card AS x FROM t_user) s
-- @case gateway/session_scope_escape_test.go
/* nothing to see */ SET @@GLOBAL.read_only = 0
-- @case gateway/split_judge_test.go
/* x */ # note
DELETE FROM t
-- @case gateway/explain_plan_test.go
/* 注释 */ EXPLAIN DELETE FROM t
-- @case gateway/realrun_routing_test.go
/* 注释 */ explain DELETE FROM t_demo WHERE id = 1
-- @case gateway/stripcomments_test.go
/*!40000 /* c */ DROP TABLE y */
-- @case gateway/stripcomments_test.go
/*!40000 /*x*/ GRANT ALL ON db.* TO u */
-- @case gateway/stripcomments_test.go
/*!40000/**/DROP TABLE y */
-- @case gateway/mariadb_exec_comment_test.go
/*M 没有感叹号 */ SELECT 1
-- @case gateway/window_scope_test.go
ALTER DATABASE B CHARACTER SET utf8mb4
-- @case sqlutil/alter_test.go
ALTER INDEX idx REBUILD
-- @case sqlutil/redact_test.go
ALTER ROLE r ENCRYPTED PASSWORD 'encpw'
-- @case gateway/window_scope_test.go
ALTER SESSION ENABLE PARALLEL DML
-- @case gateway/window_scope_test.go
ALTER SESSION SET CONTAINER = PDB2
-- @case gateway/dialect_audit_test.go
ALTER SESSION SET CURRENT_SCHEMA = G04
-- @case gateway/window_scope_test.go
ALTER SESSION SET CURRENT_SCHEMA = HR
-- @case gateway/session_scope_escape_test.go
ALTER SESSION SET CURRENT_SCHEMA = app
-- @case gateway/readonly_guard_test.go
ALTER SESSION SET NLS_DATE_FORMAT = 'YYYY'
-- @case gateway/window_scope_test.go
ALTER SESSION SET NLS_DATE_FORMAT = 'YYYY-MM-DD HH24:MI:SS'
-- @case gateway/dialect_audit_test.go
ALTER SESSION SET NLS_DATE_FORMAT = 'YYYY-MM-DD'
-- @case gateway/window_scope_test.go
ALTER SESSION SET NLS_DATE_FORMAT = 'x' CURRENT_SCHEMA = HR
-- @case gateway/set_statement_test.go
ALTER SESSION SET NLS_DATE_FORMAT='YYYY-MM-DD'
-- @case gateway/window_scope_test.go
ALTER SESSION SET NLS_LANGUAGE = AMERICAN NLS_TERRITORY = AMERICA
-- @case sqlutil/alter_test.go
ALTER SESSION SET current_schema = app
-- @case gateway/session_scope_escape_test.go
ALTER SYSTEM SET max_connections = 1000
-- @case gateway/dialect_audit_test.go
ALTER SYSTEM SET processes = 500
-- @case gateway/dialect_audit_test.go
ALTER SYSTEM SET shared_buffers = '8GB'
-- @case gateway/session_scope_escape_test.go
ALTER SYSTEM 改的是整个实例,不是会话
-- @case osc/binlog_mysql_test.go
ALTER TABLE _orders_gho ADD INDEX idx_created (created, qty)
-- @case osc/parse_test.go
ALTER TABLE `app`.orders CHANGE note remark VARCHAR(64), RENAME COLUMN a TO b, MODIFY c BIGINT, ALGORITHM=INPLACE, LOCK=NONE
-- @case sqlutil/alter_test.go
ALTER TABLE `t order` ADD COLUMN `my col` INT
-- @case review/checks_spec_test.go
ALTER TABLE `t_risk_black_associated_request`
  ADD INDEX `idx_pbc_id_status` (`pbc_id`, `status`) COMMENT 'PBC去重,合并';
-- @case review/checks_spec_test.go
ALTER TABLE app.t_a ADD KEY idx_a (a);
-- @case gateway/rule_identity_test.go
ALTER TABLE customers ADD COLUMN c INT
-- @case review/checks_notnull_temporal_test.go
ALTER TABLE g_ods_a ADD COLUMN amt numeric(18,2);
-- @case review/checks_notnull_temporal_test.go
ALTER TABLE g_ods_a ADD COLUMN etl_time timestamp with time zone;
-- @case osc/binlog_mysql_test.go
ALTER TABLE my_orders ADD x INT
-- @case osc/parse_test.go
ALTER TABLE orders ADD COLUMN c INT NOT NULL DEFAULT 0
-- @case osc/parse_test.go
ALTER TABLE orders ADD COLUMN c INT, ADD INDEX ic (c)
-- @case osc/parse_test.go
ALTER TABLE orders ADD COLUMN c INT, ALGORITHM=INSTANT
-- @case gateway/data_action_test.go
ALTER TABLE orders ADD COLUMN m int
-- @case osc/parse_test.go
ALTER TABLE orders ADD COLUMN s INT AS (a * 2) STORED
-- @case osc/parse_test.go
ALTER TABLE orders ADD COLUMN seq BIGINT NOT NULL AUTO_INCREMENT, ADD UNIQUE KEY (seq)
-- @case osc/parse_test.go
ALTER TABLE orders ADD COLUMN v INT AS (a + 1) VIRTUAL
-- @case osc/binlog_mysql_test.go
ALTER TABLE orders ADD COLUMN x INT
-- @case osc/parse_test.go
ALTER TABLE orders ADD CONSTRAINT ck CHECK (amount >= 0)
-- @case osc/parse_test.go
ALTER TABLE orders ADD CONSTRAINT fk FOREIGN KEY (uid) REFERENCES users (id)
-- @case osc/parse_test.go
ALTER TABLE orders ADD CONSTRAINT uk UNIQUE (a)
-- @case osc/parse_test.go
ALTER TABLE orders ADD INDEX a (x), ADD INDEX b (y)
-- @case osc/parse_test.go
ALTER TABLE orders ADD INDEX d (a DESC)
-- @case osc/parse_test.go
ALTER TABLE orders ADD INDEX f ((lower(name)))
-- @case osc/parse_test.go
ALTER TABLE orders ADD INDEX i (a), ADD CONSTRAINT fk FOREIGN KEY (u) REFERENCES u (id)
-- @case osc/parse_test.go
ALTER TABLE orders ADD INDEX i (a), RENAME TO o2
-- @case osc/parse_test.go
ALTER TABLE orders ADD INDEX idx_c (created_at) COMMENT 'a,b', ADD COLUMN x VARCHAR(8) DEFAULT 'p,q'
-- @case osc/parse_test.go
ALTER TABLE orders ADD INDEX idx_c (created_at);
-- @case osc/parse_test.go
ALTER TABLE orders ADD INDEX idx_c USING BTREE (created_at) COMMENT 'x', ALGORITHM=INPLACE
-- @case osc/parse_test.go
ALTER TABLE orders ADD PRIMARY KEY (id)
-- @case osc/parse_test.go
ALTER TABLE orders ADD SPATIAL INDEX g (pt)
-- @case osc/parse_test.go
ALTER TABLE orders ADD UNIQUE INDEX u (a)
-- @case osc/parse_test.go
ALTER TABLE orders ADD UNIQUE KEY u (a)
-- @case osc/parse_test.go
ALTER TABLE orders ADD c INT AFTER id
-- @case osc/binlog_mysql_test.go
ALTER TABLE orders ADD x INT
-- @case osc/parse_test.go
ALTER TABLE orders ALTER COLUMN st SET DEFAULT 'new'
-- @case osc/parse_test.go
ALTER TABLE orders ALTER INDEX a INVISIBLE
-- @case osc/parse_test.go
ALTER TABLE orders ALTER st DROP DEFAULT
-- @case osc/parse_test.go
ALTER TABLE orders AUTO_INCREMENT = 1000
-- @case osc/parse_test.go
ALTER TABLE orders CHANGE note remark VARCHAR(64)
-- @case osc/parse_test.go
ALTER TABLE orders COMMENT = '订单'
-- @case osc/parse_test.go
ALTER TABLE orders CONVERT TO CHARACTER SET utf8mb4
-- @case osc/parse_test.go
ALTER TABLE orders DEFAULT CHARSET = utf8mb4
-- @case osc/parse_test.go
ALTER TABLE orders DISCARD TABLESPACE
-- @case osc/parse_test.go
ALTER TABLE orders DROP COLUMN c
-- @case osc/parse_test.go
ALTER TABLE orders DROP FOREIGN KEY fk_a
-- @case osc/parse_test.go
ALTER TABLE orders DROP INDEX `PRIMARY`
-- @case osc/parse_test.go
ALTER TABLE orders DROP INDEX idx_c
-- @case osc/parse_test.go
ALTER TABLE orders DROP KEY `idx_c`
-- @case osc/parse_test.go
ALTER TABLE orders DROP PARTITION p0
-- @case osc/parse_test.go
ALTER TABLE orders DROP PRIMARY KEY
-- @case osc/parse_test.go
ALTER TABLE orders DROP PRIMARY KEY, ADD PRIMARY KEY (id, a)
-- @case osc/parse_test.go
ALTER TABLE orders DROP c
-- @case osc/parse_test.go
ALTER TABLE orders ENGINE=InnoDB
-- @case osc/parse_test.go
ALTER TABLE orders ENGINE=MyISAM
-- @case osc/parse_test.go
ALTER TABLE orders FORCE
-- @case osc/parse_test.go
ALTER TABLE orders FROBNICATE
-- @case osc/parse_test.go
ALTER TABLE orders MODIFY COLUMN amount BIGINT NOT NULL
-- @case osc/parse_test.go
ALTER TABLE orders MODIFY name VARCHAR(200)
-- @case osc/parse_test.go
ALTER TABLE orders ORDER BY created_at
-- @case osc/parse_test.go
ALTER TABLE orders PARTITION BY HASH(id) PARTITIONS 4
-- @case osc/parse_test.go
ALTER TABLE orders REMOVE PARTITIONING
-- @case osc/parse_test.go
ALTER TABLE orders RENAME COLUMN a TO b
-- @case osc/parse_test.go
ALTER TABLE orders RENAME INDEX a TO b
-- @case osc/parse_test.go
ALTER TABLE orders RENAME TO orders_old
-- @case osc/parse_test.go
ALTER TABLE orders ROW_FORMAT=DYNAMIC
-- @case osc/binlog_mysql_test.go
ALTER TABLE orders_archive ADD x INT
-- @case osc/alter_mysql_test.go
ALTER TABLE ref
-- @case gateway/schemasnapshot_test.go
ALTER TABLE snap_diff.t_order MODIFY note VARCHAR(200) DEFAULT 'n/a'
-- @case osc/alter_mysql_test.go
ALTER TABLE t
-- @case sqlutil/alter_test.go
ALTER TABLE t ADD (a NUMBER, b NUMBER)
-- @case gateway/dialect_audit_test.go
ALTER TABLE t ADD (c NUMBER)
-- @case sqlutil/alter_test.go
ALTER TABLE t ADD COLUMN IF NOT EXISTS a INT
-- @case sqlutil/alter_test.go
ALTER TABLE t ADD COLUMN a INT, ADD COLUMN b INT NOT NULL, DROP COLUMN c
-- @case sqlutil/alter_test.go
ALTER TABLE t ADD COLUMN a INT, ADD INDEX idx (a)
-- @case osc/parse_test.go
ALTER TABLE t ADD COLUMN c INT
-- @case osc/parse_test.go
ALTER TABLE t ADD COLUMN c INT AFTER id
-- @case osc/parse_test.go
ALTER TABLE t ADD COLUMN c INT FIRST
-- @case sqlutil/alter_test.go
ALTER TABLE t ADD CONSTRAINT fk FOREIGN KEY (a) REFERENCES p(id)
-- @case sqlutil/alter_test.go
ALTER TABLE t ADD CONSTRAINT uk_ab UNIQUE (a, b)
-- @case sqlutil/alter_test.go
ALTER TABLE t ADD INDEX idx_a (a)
-- @case sqlutil/alter_test.go
ALTER TABLE t ADD PRIMARY KEY (a)
-- @case sqlutil/alter_test.go
ALTER TABLE t ADD UNIQUE (a, b)
-- @case sqlutil/alter_test.go
ALTER TABLE t ADD UNIQUE KEY uk_ab (a, b)
-- @case gateway/risk_metadata_read_test.go
ALTER TABLE t ADD c int
-- @case sqlutil/alter_test.go
ALTER TABLE t ALTER COLUMN a DROP NOT NULL
-- @case sqlutil/alter_test.go
ALTER TABLE t ALTER COLUMN a TYPE bigint
-- @case sqlutil/alter_test.go
ALTER TABLE t ALTER COLUMN n SET NOT NULL
-- @case sqlutil/alter_test.go
ALTER TABLE t ALTER n SET NOT NULL
-- @case sqlutil/alter_test.go
ALTER TABLE t CHANGE COLUMN old_n new_n INT
-- @case sqlutil/alter_test.go
ALTER TABLE t DROP COLUMN IF EXISTS a
-- @case osc/parse_test.go
ALTER TABLE t DROP COLUMN c
-- @case sqlutil/alter_test.go
ALTER TABLE t DROP COLUMN memo
-- @case sqlutil/alter_test.go
ALTER TABLE t DROP PARTITION p1
-- @case sqlutil/alter_test.go
ALTER TABLE t DROP memo
-- @case sqlutil/alter_test.go
ALTER TABLE t MODIFY (n NUMBER NOT NULL)
-- @case sqlutil/alter_test.go
ALTER TABLE t MODIFY COLUMN n INT NOT NULL
-- @case sqlutil/alter_test.go
ALTER TABLE t RENAME TO t2
-- @case gateway/window_scope_test.go
ALTER TABLE t1 ADD COLUMN c INT, ADD INDEX idx_c (c)
-- @case gateway/window_scope_test.go
ALTER TABLE t1 ADD CONSTRAINT fk FOREIGN KEY (x) REFERENCES B.t2 (id)
-- @case gateway/window_scope_test.go
ALTER TABLE t1 EXCHANGE PARTITION p0 WITH TABLE B.t2
-- @case gateway/window_scope_test.go
ALTER TABLE t1 RENAME TO B.t1
-- @case review/checks_notnull_temporal_test.go
ALTER TABLE t_a ADD (expire_at TIMESTAMP(3) WITH LOCAL TIME ZONE, born DATE);
-- @case review/checks_notnull_temporal_test.go
ALTER TABLE t_a ADD (qty NUMBER(10));
-- @case review/checks_spec_test.go
ALTER TABLE t_a ADD COLUMN `index` INT;
-- @case review/checks_notnull_temporal_test.go
ALTER TABLE t_a ADD COLUMN amount DECIMAL(10,2);
-- @case review/checks_spec_test.go
ALTER TABLE t_a ADD COLUMN bank_card_no VARCHAR(32);
-- @case review/checks_spec_test.go
ALTER TABLE t_a ADD COLUMN c1 INT, ADD INDEX idx_a (a);
-- @case review/checks_notnull_temporal_test.go
ALTER TABLE t_a ADD COLUMN deleted_at DATETIME(3) NULL COMMENT 'd', ADD d2 DATE;
-- @case review/checks_notnull_temporal_test.go
ALTER TABLE t_a ADD COLUMN deleted_at DATETIME(3), ADD COLUMN remark VARCHAR(255);
-- @case review/checks_spec_test.go
ALTER TABLE t_a ADD COLUMN key_name INT;
-- @case review/checks_notnull_temporal_test.go
ALTER TABLE t_a ADD COLUMN remark VARCHAR(255);
-- @case review/checks_notnull_temporal_test.go
ALTER TABLE t_a ADD COLUMN updated_at TIMESTAMP(6) NULL;
-- @case review/checks_spec_test.go
ALTER TABLE t_a ADD FOREIGN KEY (a) REFERENCES t_b (id);
-- @case review/checks_spec_test.go
ALTER TABLE t_a ADD FULLTEXT INDEX ft_a (a);
-- @case review/checks_spec_test.go
ALTER TABLE t_a ADD INDEX idx_a (a), ADD COLUMN c1 INT;
-- @case review/checks_spec_test.go
ALTER TABLE t_a ADD INDEX idx_a (a), MODIFY COLUMN c1 BIGINT;
-- @case review/checks_spec_test.go
ALTER TABLE t_a ADD PRIMARY KEY (id);
-- @case review/checks_spec_test.go
ALTER TABLE t_a ADD SPATIAL INDEX sp_a (a);
-- @case review/checks_spec_test.go
ALTER TABLE t_a ADD UNIQUE INDEX uk_a (a);
-- @case review/checks_spec_test.go
ALTER TABLE t_a DROP COLUMN c1;
-- @case review/checks_spec_test.go
ALTER TABLE t_a DROP INDEX idx_a, ADD INDEX idx_b (a, b), ADD KEY idx_c (c);
-- @case review/checks_spec_test.go
ALTER TABLE t_a DROP INDEX idx_a, DROP PRIMARY KEY, ADD PRIMARY KEY (id);
-- @case review/checks_spec_test.go
ALTER TABLE t_a DROP INDEX idx_a;
-- @case review/checks_spec_test.go
ALTER TABLE t_a DROP KEY idx_a;
-- @case review/checks_spec_test.go
ALTER TABLE t_a DROP PRIMARY KEY;
-- @case review/checks_spec_test.go
ALTER TABLE t_a ENGINE=InnoDB;
-- @case review/checks_spec_test.go
ALTER TABLE t_a RENAME TO t_b;
-- @case sqlutil/alter_test.go
ALTER TABLE t_order ADD (n NUMBER NOT NULL)
-- @case sqlutil/alter_test.go
ALTER TABLE t_order ADD COLUMN memo VARCHAR(64)
-- @case sqlutil/refs_test.go
ALTER TABLE t_order ADD COLUMN n INT
-- @case sqlutil/alter_test.go
ALTER TABLE t_order ADD COLUMN n INT NOT NULL
-- @case sqlutil/alter_test.go
ALTER TABLE t_order ADD COLUMN n INT NOT NULL DEFAULT 0
-- @case sqlutil/alter_test.go
ALTER TABLE t_order ADD memo VARCHAR(64)
-- @case review/checks_spec_test.go
ALTER TABLE t_tests ADD COLUMN c1 VARCHAR(20), ALGORITHM=INSTANT;
-- @case review/checks_spec_test.go
ALTER TABLE t_tests ADD COLUMN c1 VARCHAR(20);
-- @case review/checks_spec_test.go
ALTER TABLE t_tests DROP PRIMARY KEY, ADD PRIMARY KEY (id), ALGORITHM=INPLACE, LOCK=NONE;
-- @case review/checks_spec_test.go
ALTER TABLE t_tests add INDEX idx_us_un(column_name) using btree;
-- @case gateway/sensitive_error_test.go
ALTER TABLE t_user ADD INDEX idx_phone(phone)
-- @case review/checks_spec_test.go
ALTER TABLE t_users ADD COLUMN nickname VARCHAR(45);
-- @case review/checks_spec_test.go
ALTER TABLE t_users DROP COLUMN nickname;
-- @case review/checks_spec_test.go
ALTER TABLE t_users DROP CONSTRAINT fk_user_org;
-- @case review/checks_spec_test.go
ALTER TABLE t_users DROP INDEX idx_us_un;
-- @case review/review_test.go
ALTER TABLE tbl_order ADD COLUMN a INT NOT NULL DEFAULT 0 COMMENT 'a';
-- @case review/review_test.go
ALTER TABLE tbl_order ADD COLUMN b INT NOT NULL DEFAULT 0 COMMENT 'b';
-- @case review/review_test.go
ALTER TABLE tbl_order ADD COLUMN memo VARCHAR(64) NOT NULL COMMENT '备注';
-- @case review/review_test.go
ALTER TABLE tbl_order ADD COLUMN memo VARCHAR(64) NOT NULL DEFAULT '' COMMENT '备注';
-- @case gateway/dialect_audit_test.go
ALTER TABLE 在生产不该放行 —— 会话设置的放宽不能顺带把它带过去
-- @case sqlutil/redact_test.go
ALTER USER 'u' IDENTIFIED WITH caching_sha2_password BY 'topsecret'
-- @case gateway/schemasnapshot_test.go
ALTER USER SNAP_IDX_OWNER QUOTA UNLIMITED ON USERS
-- @case sqlutil/redact_audit_test.go
ALTER USER u IDENTIFIED BY 's3cr3t' REPLACE 'old1'
-- @case sqlutil/redact_audit_test.go
ALTER USER u IDENTIFIED WITH 'mysql_native_password' BY 's3cr3t'
-- @case sqlutil/delimiter_test.go
BEGIN
-- @case gateway/window_scope_test.go
BEGIN APP.pk_util.do_it(1); END;
-- @case gateway/window_scope_test.go
BEGIN DBMS_OUTPUT.PUT_LINE('x'); END;
-- @case gateway/window_scope_test.go
BEGIN DBMS_SCHEDULER.CREATE_JOB(job_name => 'j', job_type => 'PLSQL_BLOCK', job_action => 'UPDATE hr.emp SET sal = 1;', enabled => TRUE); END;
-- @case gateway/window_scope_test.go
BEGIN DBMS_STATS.GATHER_TABLE_STATS(ownname => 'APP', tabname => 'T'); END;
-- @case gateway/window_scope_test.go
BEGIN DBMS_UTILITY.EXEC_DDL_STATEMENT('DROP TABLE hr.emp'); END;
-- @case gateway/window_scope_test.go
BEGIN EXECUTE IMMEDIATE 'ALTER TABLE t ADD c NUMBER'; END;
-- @case gateway/sqldump_roundtrip_test.go
BEGIN EXECUTE IMMEDIATE 'DROP TABLE
-- @case gateway/schemasnapshot_test.go
BEGIN EXECUTE IMMEDIATE 'DROP TABLE SNAP_T_ORDER PURGE'; EXCEPTION WHEN OTHERS THEN NULL; END;
-- @case gateway/schemasnapshot_test.go
BEGIN EXECUTE IMMEDIATE 'DROP TABLE SNAP_T_OWN PURGE'; EXCEPTION WHEN OTHERS THEN NULL; END;
-- @case gateway/dictionary_literals_test.go
BEGIN EXECUTE IMMEDIATE 'DROP TABLE t'; END;
-- @case gateway/schemasnapshot_test.go
BEGIN EXECUTE IMMEDIATE 'DROP USER SNAP_IDX_OWNER CASCADE'; EXCEPTION WHEN OTHERS THEN NULL; END;
-- @case gateway/schemasnapshot_test.go
BEGIN EXECUTE IMMEDIATE 'DROP VIEW SNAP_V_ORDER'; EXCEPTION WHEN OTHERS THEN NULL; END;
-- @case gateway/window_scope_test.go
BEGIN EXECUTE IMMEDIATE 'GRANT SELECT ON t TO u'; END;
-- @case gateway/window_scope_test.go
BEGIN EXECUTE IMMEDIATE 'UPDATE app.t SET v = 1'; END;
-- @case gateway/window_scope_test.go
BEGIN EXECUTE IMMEDIATE 'UPDATE hr.emp SET sal = 1'; END;
-- @case gateway/window_scope_test.go
BEGIN EXECUTE IMMEDIATE 'UPDATE t SET v = :1 WHERE id = :2' USING 1, 2; END;
-- @case gateway/window_scope_test.go
BEGIN EXECUTE IMMEDIATE 'drop table hr.emp'; END;
-- @case gateway/window_scope_test.go
BEGIN EXECUTE IMMEDIATE 'truncate table ' || v_tab; END;
-- @case gateway/window_scope_test.go
BEGIN EXECUTE IMMEDIATE 'truncate table t'; END;
-- @case gateway/window_scope_test.go
BEGIN EXECUTE IMMEDIATE q'[truncate table t]'; END;
-- @case gateway/window_scope_test.go
BEGIN EXECUTE IMMEDIATE v_sql; END;
-- @case gateway/window_scope_test.go
BEGIN UPDATE emp SET sal = 1; COMMIT; END;
-- @case gateway/window_scope_test.go
BEGIN dbms_job.submit(:j, 'UPDATE hr.emp SET sal = 1;'); END;
-- @case gateway/window_scope_test.go
BEGIN hr.pkg.run; END;
-- @case gateway/dialect_audit_test.go
BEGIN my_proc(); END;
-- @case gateway/client_commands_test.go
BEGIN my_proc; END;
-- @case gateway/readonly_guard_test.go
BEGIN p; END;
-- @case gateway/window_scope_test.go
BEGIN pk_util.do_it(1); END;
-- @case gateway/window_scope_test.go
BEGIN pk_util.g_flag := 1; END;
-- @case gateway/client_commands_test.go
BEGIN pkg.my_proc(1, 'a'); END;
-- @case gateway/txn_capability_test.go
BEGIN 被放宽成了 select —— Oracle 的匿名 PL/SQL 块以它开头,块里可以改数据
-- @case gateway/window_scope_test.go
CALL B.p()
-- @case gateway/sensitive_star_align_test.go
CALL get_user(1)
-- @case gateway/dialect_audit_test.go
CALL my_proc()
-- @case gateway/readonly_guard_test.go
CALL p()
-- @case gateway/window_scope_test.go
CALL p_cleanup(1)
-- @case gateway/window_scope_test.go
CALL pk_util.do_it(1)
-- @case gateway/brokenconn_test.go
CREATE DATABASE
-- @case gateway/window_scope_test.go
CREATE DATABASE B
-- @case gateway/bizquery_test.go
CREATE DATABASE IF NOT EXISTS biz_empty_db
-- @case gateway/bizquery_tidb_test.go
CREATE DATABASE IF NOT EXISTS biz_tidb
-- @case gateway/bizquery_test.go
CREATE DATABASE IF NOT EXISTS biztest
-- @case gateway/schemasnapshot_test.go
CREATE DATABASE IF NOT EXISTS dbpick_test
-- @case gateway/columns_test.go
CREATE DATABASE IF NOT EXISTS tc_a
-- @case gateway/columns_test.go
CREATE DATABASE IF NOT EXISTS tc_b
-- @case gateway/readonly_guard_test.go
CREATE DATABASE IF NOT EXISTS vz_guard
-- @case gateway/schemasnapshot_test.go
CREATE DATABASE schpick_test
-- @case gateway/schemasnapshot_test.go
CREATE DATABASE snap_diff
-- @case gateway/schemasnapshot_test.go
CREATE DATABASE snap_pair
-- @case gateway/schemasnapshot_test.go
CREATE DATABASE snap_test
-- @case gateway/schema_emptydb_test.go
CREATE DATABASE tc_empty_db
-- @case sqlutil/delimiter_test.go
CREATE DEFINER = CURRENT_USER TRIGGER trg BEFORE INSERT ON t FOR EACH ROW
BEGIN
  SET NEW.a = 1;
  SET NEW.b = 2;
END
-- @case sqlutil/delimiter_test.go
CREATE EVENT e ON SCHEDULE EVERY 1 DAY DO
BEGIN
  DELETE FROM t WHERE d < NOW();
  OPTIMIZE TABLE t;
END
-- @case sqlutil/split_definer_test.go
CREATE EVENT ev ON SCHEDULE EVERY 1 DAY DO BEGIN DELETE FROM log; DELETE FROM log2; END
-- @case sqlutil/split_definer_test.go
CREATE EVENT ev ON SCHEDULE EVERY 1 DAY DO DELETE FROM log WHERE ts < NOW();
UPDATE t SET a = CASE WHEN b = 1 THEN 2 ELSE 3 END;
-- @case sqlutil/redact_audit_test.go
CREATE FOREIGN TABLE t (...) SERVER obs OPTIONS (ACCESS_KEY 'AK123', SECRET_ACCESS_KEY 'SK456')
-- @case osc/parse_test.go
CREATE FULLTEXT INDEX f ON orders (note)
-- @case sqlutil/split_definer_test.go
CREATE FUNCTION f(a INT) RETURNS INT DETERMINISTIC RETURN a + 1;
SELECT CASE WHEN 1 = 1 THEN 1 END
-- @case sqlutil/delimiter_test.go
CREATE FUNCTION f(a INT) RETURNS INT NO SQL
BEGIN
  DECLARE x INT;
  SET x = a + 1;
  RETURN x;
END
-- @case gateway/readonly_guard_test.go
CREATE FUNCTION vz_guard.f() RETURNS INT DETERMINISTIC MODIFIES SQL DATA BEGIN INSERT INTO vz_guard.lg VALUES (1); RETURN 1; END
-- @case gateway/schemasnapshot_test.go
CREATE INDEX SNAP_IDX_OWNER.SNAP_IDX_UP ON SNAP_T_OWN (UPPER(CODE))
-- @case gateway/schemasnapshot_test.go
CREATE INDEX SNAP_IDX_STATUS ON SNAP_T_ORDER (STATUS, CREATED_AT)
-- @case gateway/schemasnapshot_test.go
CREATE INDEX SNAP_IDX_UPPER ON SNAP_T_ORDER (UPPER(NOTE))
-- @case gateway/window_scope_test.go
CREATE INDEX i ON B.t2 (c)
-- @case osc/binlog_mysql_test.go
CREATE INDEX i ON `orders` (a)
-- @case gateway/window_scope_test.go
CREATE INDEX i ON t1 (c)
-- @case osc/parse_test.go
CREATE INDEX idx_c ON orders (created_at ASC)
-- @case gateway/schemasnapshot_test.go
CREATE INDEX idx_code ON
-- @case gateway/schemasnapshot_test.go
CREATE INDEX idx_lower_code ON t_order (lower((code)::text))
-- @case sqlutil/refs_test.go
CREATE INDEX idx_n ON t_order (n)
-- @case gateway/pg_tableddl_test.go
CREATE INDEX idx_order_created ON public.t_order USING btree (created_at DESC)
-- @case gateway/pg_tableddl_test.go
CREATE INDEX idx_order_created ON public.t_order USING btree (created_at DESC);
-- @case gateway/schemasnapshot_test.go
CREATE INDEX idx_status ON snap_diff.t_order (status)
-- @case gateway/schemasnapshot_test.go
CREATE INDEX idx_status ON t_order (status, amount)
-- @case gateway/schemasnapshot_test.go
CREATE INDEX idx_status ON t_order (status, created_at)
-- @case review/checks_spec_test.go
CREATE INDEX idx_this_index_name_is_definitely_far_too_long ON t_a(b);
-- @case review/checks_spec_test.go
CREATE INDEX idx_us_un ON t_users(username);
-- @case gateway/window_scope_test.go
CREATE OR REPLACE FUNCTION f(a NUMBER) RETURN NUMBER IS BEGIN RETURN pk_util.calc(a); END;
-- @case gateway/readonly_guard_test.go
CREATE OR REPLACE FUNCTION vz_grd_auto RETURN NUMBER IS PRAGMA AUTONOMOUS_TRANSACTION; BEGIN INSERT INTO vz_grd_log VALUES (1); COMMIT; RETURN 1; END;
-- @case gateway/readonly_guard_test.go
CREATE OR REPLACE FUNCTION vz_guard_f() RETURNS int LANGUAGE plpgsql AS $$ BEGIN INSERT INTO vz_guard_lg VALUES (1); RETURN 1; END $$
-- @case gateway/window_scope_test.go
CREATE OR REPLACE PACKAGE "PK_AGENT_WALLET" is
  -- jackpot supervisor 校验
  procedure VerifyBingoSupervisor(p_guid in varchar2, p_res out varchar2);
end PK_AGENT_WALLET;
-- @case gateway/window_scope_test.go
CREATE OR REPLACE PACKAGE BODY G04.pk is procedure p is begin hr.pkg.run; end; end;
-- @case gateway/window_scope_test.go
CREATE OR REPLACE PACKAGE BODY hr.pk is procedure p is begin null; end; end;
-- @case gateway/sensitive_error_test.go
CREATE OR REPLACE PACKAGE BODY pk IS PROCEDURE p IS BEGIN SELECT phone INTO v FROM t; pk_util.f; END; END;
-- @case gateway/window_scope_test.go
CREATE OR REPLACE PACKAGE BODY pk IS PROCEDURE p IS c INTEGER; BEGIN c := DBMS_SQL.OPEN_CURSOR; END; END;
-- @case sqlutil/split_definer_test.go
CREATE OR REPLACE PACKAGE pk IS
  PROCEDURE p;
  c CONSTANT NUMBER := 1;
END pk;
-- @case sqlutil/split_definer_test.go
CREATE OR REPLACE PACKAGE pk IS
  PROCEDURE p;
END pk;
/
CREATE OR REPLACE PACKAGE BODY pk IS
  PROCEDURE p IS BEGIN NULL; END;
END pk;
/
-- @case gateway/window_scope_test.go
CREATE OR REPLACE PROCEDURE hr.p AS BEGIN NULL; END;
-- @case gateway/window_scope_test.go
CREATE OR REPLACE PROCEDURE hr.p IS BEGIN NULL; END;
-- @case sqlutil/split_definer_test.go
CREATE OR REPLACE PROCEDURE p AS
  v NUMBER;
BEGIN
  v := 1;
  UPDATE t SET a = v;
END;
-- @case gateway/window_scope_test.go
CREATE OR REPLACE PROCEDURE p AS BEGIN DELETE FROM hr.emp; END;
-- @case gateway/window_scope_test.go
CREATE OR REPLACE PROCEDURE p AS BEGIN INSERT INTO hr.emp (id) VALUES (1); END;
-- @case gateway/window_scope_test.go
CREATE OR REPLACE PROCEDURE p AS BEGIN INSERT INTO t SELECT * FROM t2@remote; END;
-- @case gateway/window_scope_test.go
CREATE OR REPLACE PROCEDURE p AS BEGIN MERGE INTO hr.emp e USING t s ON (e.id = s.id) WHEN MATCHED THEN UPDATE SET e.v = s.v; END;
-- @case gateway/window_scope_test.go
CREATE OR REPLACE PROCEDURE p AS BEGIN SYS.DBMS_SQL.PARSE(1, 'x', 1); END;
-- @case gateway/window_scope_test.go
CREATE OR REPLACE PROCEDURE p AS BEGIN UPDATE hr.emp SET sal = 1; END;
-- @case gateway/window_scope_test.go
CREATE OR REPLACE PROCEDURE p AS CURSOR c IS SELECT * FROM hr.emp; BEGIN NULL; END;
-- @case gateway/window_scope_test.go
CREATE OR REPLACE TRIGGER trg BEFORE INSERT ON emp FOR EACH ROW BEGIN :NEW.id := 1; END;
-- @case gateway/window_scope_test.go
CREATE OR REPLACE TRIGGER trg BEFORE INSERT ON emp FOR EACH ROW BEGIN hr.pkg.log(:NEW.id); END;
-- @case sqlutil/split_definer_test.go
CREATE OR REPLACE TRIGGER trg BEFORE INSERT ON t FOR EACH ROW
BEGIN
  :NEW.id := 1;
END;
-- @case gateway/window_scope_test.go
CREATE OR REPLACE TYPE t_obj AS OBJECT (a NUMBER)
-- @case sqlutil/delimiter_test.go
CREATE PROCEDURE
-- @case sqlutil/delimiter_test.go
CREATE PROCEDURE a() BEGIN SELECT 1; END$$
-- @case sqlutil/delimiter_test.go
CREATE PROCEDURE b
-- @case sqlutil/delimiter_test.go
CREATE PROCEDURE b() BEGIN SELECT 2; END$$
-- @case sqlutil/delimiter_test.go
CREATE PROCEDURE c() BEGIN SELECT 3; END;
-- @case sqlutil/delimiter_test.go
CREATE PROCEDURE p()
-- @case gateway/hash_comment_test.go
CREATE PROCEDURE p()
BEGIN
  # 备注
  UPDATE t SET a=1 WHERE id=1;
END
-- @case sqlutil/delimiter_test.go
CREATE PROCEDURE p() BEGIN DROP TABLE t_secret; END //
-- @case gateway/window_scope_test.go
CREATE PROCEDURE p() BEGIN PREPARE st FROM @s; EXECUTE st; UPDATE other.t SET v = 1; END
-- @case sqlutil/delimiter_test.go
CREATE PROCEDURE p() BEGIN SELECT 1; END //
-- @case gateway/window_scope_test.go
CREATE PROCEDURE p() BEGIN UPDATE b.t SET v = 1; END
-- @case sqlutil/split_definer_test.go
CREATE PROCEDURE p() BEGIN UPDATE t SET a = CASE WHEN b = 1 THEN 2 END; DELETE FROM t2; END;
-- @case gateway/window_scope_test.go
CREATE PROCEDURE p_part(IN d VARCHAR(8)) BEGIN SET @s = CONCAT('ALTER TABLE t_log ADD PARTITION (PARTITION p', d, ' VALUES LESS THAN (', d, '))'); PREPARE st FROM @s; EXECUTE st; DEALLOCATE PREPARE st; END
-- @case gateway/window_scope_test.go
CREATE PUBLIC SYNONYM s FOR emp
-- @case sqlutil/redact_test.go
CREATE ROLE r LOGIN PASSWORD 'pgsecret'
-- @case sqlutil/redact_test.go
CREATE ROLE r WITH ENCRYPTED PASSWORD 'hunter2'
-- @case sqlutil/redact_audit_test.go
CREATE ROLE r WITH ENCRYPTED PASSWORD 's3cr3t'
-- @case gateway/schemasnapshot_test.go
CREATE SCHEMA
-- @case gateway/columns_test.go
CREATE SCHEMA IF NOT EXISTS tc_s
-- @case gateway/schemasnapshot_test.go
CREATE SCHEMA app
-- @case gateway/schemasnapshot_test.go
CREATE SCHEMA app_uat
-- @case sqlutil/redact_audit_test.go
CREATE SUBSCRIPTION s CONNECTION 'host=db1 user=rep password=s3cr3t dbname=app' PUBLICATION p
-- @case sqlutil/redact_audit_test.go
CREATE SUBSCRIPTION s CONNECTION 'host=h password=p'
-- @case gateway/window_scope_test.go
CREATE SYNONYM s FOR hr.emp
-- @case gateway/bizquery_show_mysql_test.go
CREATE TABLE
-- @case review/checks_notnull_temporal_test.go
CREATE TABLE "APP"."T_A" ("ID" NUMBER(19) PRIMARY KEY, "CREATED_AT" TIMESTAMP (6) WITH LOCAL TIME ZONE, "BORN" DATE, "MY COL" INTERVAL DAY (2) TO SECOND (6));
-- @case review/checks_notnull_temporal_test.go
CREATE TABLE "T_A" ("ID" NUMBER(19) PRIMARY KEY, "NAME" VARCHAR2(64), "CREATED_AT" DATE);
-- @case review/checks_notnull_temporal_test.go
CREATE TABLE "g_ods_a" ("id" BIGINT NOT NULL, "etl_time" timestamp(6) with time zone) DISTRIBUTE BY HASH("id");
-- @case gateway/pg_tableddl_test.go
CREATE TABLE "public"."t_min" (
-- @case gateway/pg_tableddl_test.go
CREATE TABLE "public"."t_order" (
-- @case gateway/window_scope_test.go
CREATE TABLE B.t3 (id INT)
-- @case gateway/backslash_test.go
CREATE TABLE IF NOT EXISTS biztest.bs_victim (n INT)
-- @case gateway/bizquery_test.go
CREATE TABLE IF NOT EXISTS biztest.nums (n INT PRIMARY KEY)
-- @case sqlutil/refs_test.go
CREATE TABLE IF NOT EXISTS t_new (id INT)
-- @case review/review_test.go
CREATE TABLE Orders (id INT, amount FLOAT) ENGINE=MyISAM DEFAULT CHARSET=utf8;
-- @case gateway/schemasnapshot_test.go
CREATE TABLE SNAP_T_ORDER (ID NUMBER(19) PRIMARY KEY, CODE VARCHAR2(32 CHAR) NOT NULL UNIQUE,
-- @case gateway/schemasnapshot_test.go
CREATE TABLE SNAP_T_OWN (ID NUMBER PRIMARY KEY, CODE VARCHAR2(20))
-- @case review/review_test.go
CREATE TABLE T1 (ID NUMBER(10) NOT NULL, NAME VARCHAR(20), PRIMARY KEY(ID));
-- @case osc/mysql_test.go
CREATE TABLE _orders_gho (id INT)
-- @case osc/binlog_mysql_test.go
CREATE TABLE _orders_gho LIKE orders
-- @case osc/alter_mysql_test.go
CREATE TABLE _t_ocp (keep_me INT)
-- @case sqlutil/split_exec_test.go
CREATE TABLE `t_a` (
-- @case gateway/schemasnapshot_test.go
CREATE TABLE app.t_log (id INT PRIMARY KEY)
-- @case osc/mysql_test.go
CREATE TABLE audit (id INT PRIMARY KEY)
-- @case osc/mysql_test.go
CREATE TABLE big (id BIGINT UNSIGNED PRIMARY KEY, v VARCHAR(10))
-- @case gateway/bizquery_tidb_test.go
CREATE TABLE biz_tidb.orders (id INT PRIMARY KEY, billtime DATETIME)
-- @case osc/mysql_test.go
CREATE TABLE child (id INT PRIMARY KEY, pid INT, FOREIGN KEY (pid) REFERENCES parent(id))
-- @case gateway/schema_test.go
CREATE TABLE customers(id INTEGER PRIMARY KEY, name TEXT)
-- @case osc/mysql_test.go
CREATE TABLE fkey (id DOUBLE PRIMARY KEY)
-- @case review/checks_notnull_temporal_test.go
CREATE TABLE g_ods_a (
		id BIGINT NOT NULL,
		d date,
		ts timestamp(6),
		tz timestamp with time zone,
		tz2 timestamptz,
		ntz timestamp(3) without time zone,
		tm time,
		ttz time with time zone,
		ttz2 timetz,
		iv interval,
		ivd interval day to second(3),
		sdt smalldatetime
	) DISTRIBUTE BY HASH(id);
-- @case review/checks_notnull_temporal_test.go
CREATE TABLE g_ods_a (id BIGINT NOT NULL, memo text, etl_time timestamptz, biz_date date) DISTRIBUTE BY HASH(id);
-- @case review/checks_notnull_temporal_test.go
CREATE TABLE g_ods_a (id BIGINT NOT NULL, name varchar(64)) DISTRIBUTE BY HASH(id);
-- @case osc/mysql_test.go
CREATE TABLE geo (id INT PRIMARY KEY, g GEOMETRY)
-- @case osc/mysql_test.go
CREATE TABLE h (id INT PRIMARY KEY)
-- @case osc/mysql_test.go
CREATE TABLE nokey (a INT, b VARCHAR(10))
-- @case osc/mysql_test.go
CREATE TABLE orders (
	id      BIGINT NOT NULL,
	name    VARCHAR(64) CHARACTER SET utf8mb4 NOT NULL,
	note    VARCHAR(32) CHARACTER SET latin1 NULL,
	qty     INT UNSIGNED NOT NULL,
	amount  DECIMAL(12,2) NOT NULL,
	created DATETIME(3) NOT NULL,
	doubled DECIMAL(13,2) AS (amount * 2) VIRTUAL,
	PRIMARY KEY (id),
	KEY idx_name (name)
) ENGINE=InnoDB
-- @case gateway/schema_test.go
CREATE TABLE orders(id INTEGER PRIMARY KEY, amount REAL)
-- @case osc/binlog_mysql_test.go
CREATE TABLE other (id INT PRIMARY KEY, v INT)
-- @case osc/mysql_test.go
CREATE TABLE parent (id INT PRIMARY KEY)
-- @case review/checks_spec_test.go
CREATE TABLE s.t_a (a BIGINT, b INT) DISTRIBUTE BY HASH(a);
-- @case review/checks_spec_test.go
CREATE TABLE s.t_a (a FLOAT, b INT) DISTRIBUTE BY HASH(a);
-- @case review/checks_spec_test.go
CREATE TABLE s.t_a (a INT) DISTRIBUTE BY HASH(a);
-- @case review/checks_spec_test.go
CREATE TABLE s.t_a (a INT, b INT) DISTRIBUTE BY HASH(a,b);
-- @case review/checks_spec_test.go
CREATE TABLE s.t_a (a INT, b INT, c INT, d INT) DISTRIBUTE BY HASH(a,b,c,d);
-- @case review/checks_spec_test.go
CREATE TABLE s.t_a (amt NUMERIC);
-- @case review/checks_spec_test.go
CREATE TABLE s.t_a (b INT) DISTRIBUTE BY HASH(zzz);
-- @case review/checks_spec_test.go
CREATE TABLE s.t_a (name VARCHAR(64), amt NUMERIC(18,2), n BIGINT, flag BOOLEAN);
-- @case gateway/schemasnapshot_test.go
CREATE TABLE snap_diff.t_order (id BIGINT NOT NULL AUTO_INCREMENT, code VARCHAR(32) NOT NULL, status TINYINT(1) NOT NULL DEFAULT 0,
-- @case gateway/schemasnapshot_test.go
CREATE TABLE snap_diff.t_user (id INT PRIMARY KEY, name VARCHAR(20))
-- @case gateway/schemasnapshot_test.go
CREATE TABLE snap_test.t_order (id BIGINT NOT NULL AUTO_INCREMENT, code VARCHAR(32) NOT NULL, status TINYINT(1) NOT NULL DEFAULT 0 COMMENT '状态',
-- @case osc/parse_test.go
CREATE TABLE t (a INT)
-- @case gateway/stripcomments_test.go
CREATE TABLE t (id BIGINT /*T![auto_rand] AUTO_RANDOM(5) */ PRIMARY KEY)
-- @case sqlutil/alter_test.go
CREATE TABLE t (id INT)
-- @case gateway/query_cells_test.go
CREATE TABLE t (id INTEGER, name TEXT, note TEXT, data BLOB, id_card TEXT)
-- @case gateway/risk_metadata_read_test.go
CREATE TABLE t (id int)
-- @case review/partition_regex_test.go
CREATE TABLE t (id int) PARTITION BY RANGE (id) (
		PARTITION p1 VALUES LESS THAN (10),
		PARTITION p2 VALUES LESS THAN (20),
		PARTITION p3 VALUES LESS THAN (MAXVALUE))
-- @case gateway/export_cancel_test.go
CREATE TABLE t (n INTEGER)
-- @case gateway/schema_test.go
CREATE TABLE t(id INTEGER, name TEXT)
-- @case review/review_test.go
CREATE TABLE t1 (id INT NOT NULL, PRIMARY KEY(id));
-- @case osc/binlog_mysql_test.go
CREATE TABLE t2 (a INT) /* orders */
-- @case gateway/window_scope_test.go
CREATE TABLE t3 (id INT PRIMARY KEY, v VARCHAR(10) DEFAULT 'a.b')
-- @case gateway/window_scope_test.go
CREATE TABLE t3 LIKE B.t2
-- @case gateway/window_scope_test.go
CREATE TABLE t3 LIKE t2
-- @case review/checks_notnull_temporal_test.go
CREATE TABLE t_a (
		id NUMBER(19) PRIMARY KEY,
		d DATE,
		ts TIMESTAMP,
		ts6 TIMESTAMP(6),
		tz TIMESTAMP(6) WITH TIME ZONE,
		ltz TIMESTAMP WITH LOCAL TIME ZONE,
		ds INTERVAL DAY(2) TO SECOND(6),
		ym INTERVAL YEAR TO MONTH
	);
-- @case review/checks_notnull_temporal_test.go
CREATE TABLE t_a (id BIGINT PRIMARY KEY, `created_at` DATETIME(6) DEFAULT NULL, d DATE, tm TIME, y YEAR(4), ts TIMESTAMP NULL);
-- @case review/checks_notnull_temporal_test.go
CREATE TABLE t_a (id BIGINT PRIMARY KEY, `date` VARCHAR(10)) COMMENT 'x';
-- @case review/checks_spec_test.go
CREATE TABLE t_a (id BIGINT PRIMARY KEY, create_time DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP);
-- @case review/checks_spec_test.go
CREATE TABLE t_a (id BIGINT PRIMARY KEY, create_time TIMESTAMP NOT NULL);
-- @case review/checks_notnull_temporal_test.go
CREATE TABLE t_a (id BIGINT PRIMARY KEY, created_at DATETIME COMMENT 'c') COMMENT 'x';
-- @case review/checks_notnull_temporal_test.go
CREATE TABLE t_a (id BIGINT PRIMARY KEY, name VARCHAR(64)) COMMENT 'x';
-- @case review/checks_notnull_temporal_test.go
CREATE TABLE t_a (id BIGINT PRIMARY KEY, name VARCHAR(64), created_at DATETIME(3), memo TEXT, `ts` TIMESTAMP) COMMENT 'x';
-- @case review/checks_notnull_temporal_test.go
CREATE TABLE t_a (id BIGINT PRIMARY KEY, status TINYINT);
-- @case review/checks_notnull_temporal_test.go
CREATE TABLE t_a (id BIGINT PRIMARY KEY, ts TIMESTAMP(6) NULL DEFAULT NULL, d DATE, tm TIME(3), y YEAR, dt DATETIME (3)) COMMENT 'x';
-- @case review/checks_spec_test.go
CREATE TABLE t_a (id BIGINT, card_type TINYINT, user_name VARCHAR(45));
-- @case review/checks_spec_test.go
CREATE TABLE t_a (id BIGINT, id_card VARCHAR(32));
-- @case review/checks_notnull_temporal_test.go
CREATE TABLE t_a (id NUMBER(19) PRIMARY KEY, name VARCHAR2(64));
-- @case review/checks_notnull_temporal_test.go
CREATE TABLE t_a (id NUMBER(19) PRIMARY KEY, name VARCHAR2(64), CREATED_AT TIMESTAMP(6) WITH TIME ZONE, d DATE);
-- @case sqlutil/split_exec_test.go
CREATE TABLE t_b (id INT)
-- @case sqlutil/split_exec_test.go
CREATE TABLE t_b (id INT);
-- @case sqlutil/refs_test.go
CREATE TABLE t_copy AS SELECT * FROM t_order
-- @case gateway/metadata_test.go
CREATE TABLE t_customer (id INTEGER PRIMARY KEY, name TEXT NOT NULL)
-- @case gateway/realrun_routing_test.go
CREATE TABLE t_demo (id INTEGER PRIMARY KEY)
-- @case gateway/realrun_routing_test.go
CREATE TABLE t_demo (id INTEGER PRIMARY KEY, name TEXT)
-- @case gateway/metadata_test.go
CREATE TABLE t_order (
			id      INTEGER PRIMARY KEY,
			amount  REAL NOT NULL DEFAULT 0,
			note    TEXT
		)
-- @case gateway/schemasnapshot_test.go
CREATE TABLE t_order (id BIGSERIAL PRIMARY KEY, code VARCHAR(32) NOT NULL UNIQUE, status SMALLINT NOT NULL DEFAULT 0,
-- @case gateway/schemasnapshot_test.go
CREATE TABLE t_order (id INTEGER PRIMARY KEY, code TEXT NOT NULL UNIQUE, status INTEGER DEFAULT 0, amount REAL)
-- @case gateway/export_raw_test.go
CREATE TABLE t_person (id INTEGER PRIMARY KEY, id_card TEXT)
-- @case gateway/metadata_test.go
CREATE TABLE t_tmp (id INTEGER)
-- @case review/checks_spec_test.go
CREATE TABLE t_user_profile (id BIGINT PRIMARY KEY);
-- @case review/review_test.go
CREATE TABLE tbl_a (id BIGINT NOT NULL COMMENT 'x', pid BIGINT NOT NULL COMMENT 'y', PRIMARY KEY(id), FOREIGN KEY (pid) REFERENCES tbl_b(id)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='a';
-- @case review/review_test.go
CREATE TABLE tbl_order (
-- @case gateway/columns_test.go
CREATE TABLE tc_a.orders (id INT, CustomerName VARCHAR(10))
-- @case gateway/columns_test.go
CREATE TABLE tc_b.orders (id INT, secret VARCHAR(10))
-- @case gateway/columns_test.go
CREATE TABLE tc_orders (id NUMBER, amount NUMBER)
-- @case gateway/columns_test.go
CREATE TABLE tc_s.orders (id int, amount numeric)
-- @case osc/mysql_test.go
CREATE TABLE ukey (code VARCHAR(20) NOT NULL, v INT, UNIQUE KEY uk_code (code))
-- @case review/checks_spec_test.go
CREATE TABLE user_profile (id BIGINT PRIMARY KEY);
-- @case gateway/sqldump_roundtrip_test.go
CREATE TABLE vx_dst
-- @case gateway/sqldump_roundtrip_test.go
CREATE TABLE vx_src
-- @case gateway/readonly_guard_test.go
CREATE TABLE vz_grd_log (n NUMBER)
-- @case gateway/readonly_guard_test.go
CREATE TABLE vz_guard.lg (n INT)
-- @case gateway/readonly_guard_test.go
CREATE TABLE vz_guard_lg (n INT)
-- @case osc/mysql_test.go
CREATE TABLE words (
		code VARCHAR(20) CHARACTER SET latin1 COLLATE latin1_general_ci NOT NULL,
		seq INT NOT NULL, v INT, UNIQUE KEY uk (code, seq)) ENGINE=InnoDB
-- @case gateway/readonly_guard_test.go
CREATE TABLE x (a int)
-- @case gateway/pg_tableddl_test.go
CREATE TEMPORARY TABLE "s"."t"
-- @case osc/mysql_test.go
CREATE TRIGGER trg AFTER INSERT ON audit FOR EACH ROW SET @x = 1
-- @case osc/mysql_test.go
CREATE TRIGGER trg AFTER INSERT ON orders FOR EACH ROW SET @x = 1
-- @case gateway/window_scope_test.go
CREATE TRIGGER trg AFTER INSERT ON t1 FOR EACH ROW UPDATE B.t2 SET v = NEW.v
-- @case gateway/window_scope_test.go
CREATE TRIGGER trg BEFORE INSERT ON t FOR EACH ROW BEGIN PREPARE st FROM @s; END
-- @case sqlutil/split_definer_test.go
CREATE TRIGGER trg BEFORE INSERT ON t FOR EACH ROW SET NEW.c = NOW();
SELECT CASE WHEN 1 = 1 THEN 1 END
-- @case gateway/window_scope_test.go
CREATE TRIGGER trg BEFORE INSERT ON t1 FOR EACH ROW SET NEW.v = OLD.v + 1
-- @case gateway/schemasnapshot_test.go
CREATE TYPE
-- @case sqlutil/refs_test.go
CREATE UNIQUE INDEX idx_n ON app.t_order (n)
-- @case osc/parse_test.go
CREATE UNIQUE INDEX u ON orders (a)
-- @case osc/parse_test.go
CREATE UNIQUE INDEX u USING BTREE ON orders (a, b) ALGORITHM=INPLACE LOCK=NONE
-- @case gateway/pg_tableddl_test.go
CREATE UNLOGGED TABLE "s"."t"
-- @case sqlutil/redact_test.go
CREATE USER
-- @case gateway/schemasnapshot_test.go
CREATE USER SNAP_IDX_OWNER IDENTIFIED BY "Snap_123"
-- @case sqlutil/redact_test.go
CREATE USER a IDENTIFIED WITH "caching_sha2_password" BY 'topsecret'
-- @case sqlutil/redact_test.go
CREATE USER a IDENTIFIED WITH 'mysql_native_password' BY 'hunter2'
-- @case sqlutil/redact_audit_test.go
CREATE USER u IDENTIFIED BY 'p'
-- @case sqlutil/redact_audit_test.go
CREATE USER u IDENTIFIED BY 's3cr3t'
-- @case sqlutil/redact_test.go
CREATE USER u IDENTIFIED BY PASSWORD '*ABCDEF0123'
-- @case gateway/window_scope_test.go
CREATE USER x IDENTIFIED BY 'p'
-- @case gateway/schemasnapshot_test.go
CREATE VIEW SNAP_V_ORDER AS SELECT ID FROM SNAP_T_ORDER
-- @case review/checks_spec_test.go
CREATE VIEW s.v_a AS SELECT a FROM s.t_a ORDER BY a;
-- @case review/checks_spec_test.go
CREATE VIEW s.v_a AS SELECT a FROM s.t_a;
-- @case gateway/schemasnapshot_test.go
CREATE VIEW snap_test.v_order AS SELECT id FROM snap_test.t_order
-- @case gateway/window_scope_test.go
CREATE VIEW v AS SELECT * FROM B.t2
-- @case gateway/schemasnapshot_test.go
CREATE VIEW v_order AS SELECT id FROM t_order
-- @case gateway/metadata_test.go
CREATE VIEW v_order_note AS SELECT id, note FROM t_order
-- @case gateway/sensitive_star_align_test.go
Create Table
-- @case gateway/window_scope_test.go
DECLARE CURSOR c IS SELECT id FROM t; BEGIN FOR r IN c LOOP pk_util.p(r.id); END LOOP; END;
-- @case gateway/window_scope_test.go
DECLARE c INTEGER; n INTEGER; BEGIN c := DBMS_SQL.OPEN_CURSOR; DBMS_SQL.PARSE(c, 'UPDATE hr.emp SET sal = 1', DBMS_SQL.NATIVE); n := DBMS_SQL.EXECUTE(c); END;
-- @case gateway/window_scope_test.go
DECLARE v_cnt NUMBER; BEGIN v_cnt := pk_util.cnt; pk_log.info('x'); END;
-- @case gateway/bizquery_test.go
DELETE FROM biztest.nums
-- @case gateway/verb_classify_test.go
DELETE FROM elsewhere_tbl
-- @case gateway/bizquery_test.go
DELETE FROM orders
-- @case gateway/risk_test.go
DELETE FROM orders WHERE 'x'='x'
-- @case gateway/risk_test.go
DELETE FROM orders WHERE /* nope */ 1=1
-- @case gateway/risk_test.go
DELETE FROM orders WHERE 1
-- @case gateway/risk_test.go
DELETE FROM orders WHERE 1 = 1
-- @case gateway/risk_test.go
DELETE FROM orders WHERE 1=1
-- @case gateway/risk_test.go
DELETE FROM orders WHERE 1=1 AND id=5
-- @case gateway/risk_test.go
DELETE FROM orders WHERE 1=1;
-- @case gateway/risk_test.go
DELETE FROM orders WHERE 1=2
-- @case gateway/data_action_test.go
DELETE FROM orders WHERE id = 1
-- @case osc/binlog_mysql_test.go
DELETE FROM orders WHERE id = ?
-- @case gateway/risk_test.go
DELETE FROM orders WHERE id=5
-- @case gateway/risk_test.go
DELETE FROM orders WHERE true
-- @case review/checks_spec_test.go
DELETE FROM s.t_a WHERE id = 1;
-- @case review/checks_spec_test.go
DELETE FROM s.t_a;
-- @case sqlutil/redact_test.go
DELETE FROM sessions WHERE created_at < '2026-01-01'
-- @case gateway/explain_test.go
DELETE FROM t
-- @case gateway/hash_comment_test.go
DELETE FROM t #
# WHERE id=1
-- @case gateway/hash_comment_test.go
DELETE FROM t # WHERE id=1
-- @case gateway/hash_comment_test.go
DELETE FROM t # 备注
WHERE id=1
-- @case gateway/hash_comment_test.go
DELETE FROM t WHERE a='#'
-- @case gateway/executor_timing_test.go
DELETE FROM t WHERE id = 1
-- @case gateway/hash_comment_test.go
DELETE FROM t WHERE id=1 # 备注
-- @case gateway/window_scope_test.go
DELETE FROM t1 WHERE t1.id = 1
-- @case review/message_ref_test.go
DELETE FROM t_order
-- @case sqlutil/refs_test.go
DELETE FROM t_order WHERE id = 2
-- @case review/review_test.go
DELETE FROM tbl_order WHERE id = 1 LIMIT 100;
-- @case review/review_test.go
DELETE FROM tbl_order;
-- @case gateway/risk_test.go
DELETE FROM users -- where
-- @case gateway/risk_test.go
DELETE FROM users WHERE id=1
-- @case gateway/window_scope_test.go
DELETE FROM x USING t1 x, B.t2 y
-- @case gateway/window_scope_test.go
DELETE b FROM t1 a JOIN B.t2 b ON a.id = b.id
-- @case gateway/window_scope_test.go
DELETE b FROM t1 a JOIN t2 b ON a.id = b.id WHERE a.k > 1.5
-- @case gateway/window_scope_test.go
DELETE t1, B.t2 FROM t1 JOIN B.t2 ON 1 = 1
-- @case review/review_test.go
DELETE without WHERE should be flagged
-- @case review/message_ref_test.go
DELETE 未带 WHERE 条件,将作用于全表
-- @case gateway/bizquery_forms_test.go
DESC "App"."Orders"
-- @case gateway/bizquery_forms_test.go
DESC "MixedCase"
-- @case gateway/explain_plan_test.go
DESC ANALYZE DELETE t FROM t JOIN t2 ON t2.id = t.id
-- @case gateway/explain_plan_test.go
DESC ANALYZE DELETE 不是只读
-- @case gateway/explain_plan_test.go
DESC ANALYZE SELECT id FROM t WHERE 'into outfile' = v
-- @case gateway/explain_plan_test.go
DESC ANALYZE UPDATE t JOIN t2 ON t2.id = t.id SET t.v = 500 WHERE t.id = 2
-- @case gateway/explain_plan_test.go
DESC DELETE t FROM t JOIN t2 ON t2.id = t.id
-- @case gateway/bizquery_forms_test.go
DESC FIN.salary
-- @case gateway/bizquery_forms_test.go
DESC FOR CONNECTION 12
-- @case gateway/explain_plan_test.go
DESC FORMAT=TREE SELECT * FROM t
-- @case gateway/bizquery_forms_test.go
DESC SELECT * FROM orders
-- @case gateway/explain_plan_test.go
DESC UPDATE t SET v = 1
-- @case gateway/explain_plan_test.go
DESC UPDATE t SET v = 9 WHERE id = 1
-- @case gateway/risk_metadata_read_test.go
DESC alter_log
-- @case gateway/bizquery_forms_test.go
DESC dbms_output.put_line
-- @case gateway/bizquery_forms_test.go
DESC finance_db.salary
-- @case gateway/bizquery_show_mysql_test.go
DESC nums
-- @case gateway/bizquery_forms_test.go
DESC orders
-- @case gateway/bizquery_forms_test.go
DESC orders amount
-- @case gateway/bizquery_forms_test.go
DESC orders amount extra
-- @case gateway/bizquery_forms_test.go
DESC orders x
-- @case gateway/bizquery_forms_test.go
DESC orders@remote
-- @case gateway/bizquery_forms_test.go
DESC sys.user$
-- @case gateway/bizquery_test.go
DESC t
-- @case gateway/dialect_audit_test.go
DESC t_orders
-- @case gateway/bizquery_test.go
DESC 不动
-- @case gateway/bizquery_test.go
DESC 别的库
-- @case gateway/explain_plan_test.go
DESCRIBE ANALYZE UPDATE t SET v = 1
-- @case gateway/bizquery_show_mysql_test.go
DESCRIBE biztest.nums n
-- @case gateway/risk_metadata_read_test.go
DESCRIBE create_task
-- @case gateway/explain_plan_test.go
DESCRIBE orders
-- @case gateway/bizquery_forms_test.go
DESCRIBE orders_db.orders
-- @case gateway/window_scope_test.go
DO B.f()
-- @case gateway/brokenconn_test.go
DROP DATABASE
-- @case gateway/window_scope_test.go
DROP DATABASE B
-- @case gateway/schemasnapshot_test.go
DROP DATABASE IF EXISTS schpick_test
-- @case gateway/schemasnapshot_test.go
DROP DATABASE IF EXISTS snap_diff
-- @case gateway/schemasnapshot_test.go
DROP DATABASE IF EXISTS snap_pair
-- @case gateway/schemasnapshot_test.go
DROP DATABASE IF EXISTS snap_test
-- @case gateway/schema_emptydb_test.go
DROP DATABASE IF EXISTS tc_empty_db
-- @case gateway/readonly_guard_test.go
DROP DATABASE vz_guard
-- @case gateway/readonly_guard_test.go
DROP FUNCTION IF EXISTS vz_guard.f
-- @case gateway/readonly_guard_test.go
DROP FUNCTION IF EXISTS vz_guard_f()
-- @case gateway/readonly_guard_test.go
DROP FUNCTION vz_grd_auto
-- @case osc/parse_test.go
DROP INDEX idx_c ON `app`.orders
-- @case sqlutil/refs_test.go
DROP INDEX idx_n
-- @case osc/alter_mysql_test.go
DROP PRIMARY KEY
-- @case osc/alter_mysql_test.go
DROP PRIMARY KEY, ADD PRIMARY KEY (id, a), ADD UNIQUE KEY uk_id (id)
-- @case gateway/explain_plan_test.go
DROP TABLE
-- @case gateway/window_scope_test.go
DROP TABLE IF EXISTS B.t2
-- @case osc/binlog_mysql_test.go
DROP TABLE IF EXISTS _orders_gho
-- @case gateway/bizquery_tidb_test.go
DROP TABLE IF EXISTS biz_tidb.orders
-- @case sqlutil/refs_test.go
DROP TABLE IF EXISTS t_old
-- @case gateway/columns_test.go
DROP TABLE IF EXISTS tc_a.orders
-- @case gateway/columns_test.go
DROP TABLE IF EXISTS tc_b.orders
-- @case gateway/columns_test.go
DROP TABLE IF EXISTS tc_s.orders
-- @case gateway/sqldump_roundtrip_test.go
DROP TABLE IF EXISTS vx_dst
-- @case gateway/sqldump_roundtrip_test.go
DROP TABLE IF EXISTS vx_src
-- @case gateway/readonly_guard_test.go
DROP TABLE IF EXISTS vz_guard.lg
-- @case gateway/readonly_guard_test.go
DROP TABLE IF EXISTS vz_guard_lg
-- @case gateway/stripcomments_test.go
DROP TABLE ORDERS
-- @case gateway/schemasnapshot_test.go
DROP TABLE SNAP_T_ORDER PURGE
-- @case gateway/schemasnapshot_test.go
DROP TABLE SNAP_T_OWN PURGE
-- @case sqlutil/delimiter_test.go
DROP TABLE T_SECRET
-- @case gateway/stripcomments_test.go
DROP TABLE T_X
-- @case gateway/window_scope_test.go
DROP TABLE `B`.`t2`
-- @case gateway/window_scope_test.go
DROP TABLE `orders_db`.`orders_2024_q3`
-- @case gateway/window_scope_test.go
DROP TABLE mv, B.dropme2
-- @case gateway/data_action_test.go
DROP TABLE orders
-- @case gateway/window_scope_test.go
DROP TABLE orders_2024_q3
-- @case gateway/window_scope_test.go
DROP TABLE orders_db.orders_2024_q3
-- @case osc/binlog_mysql_test.go
DROP TABLE other.orders
-- @case gateway/mariadb_exec_comment_test.go
DROP TABLE t
-- @case sqlutil/refs_test.go
DROP TABLE t_order
-- @case gateway/metadata_test.go
DROP TABLE t_tmp
-- @case review/checks_spec_test.go
DROP TABLE t_users;
-- @case gateway/columns_test.go
DROP TABLE tc_orders
-- @case sqlutil/split_test.go
DROP TABLE u
-- @case gateway/readonly_guard_test.go
DROP TABLE vz_grd_log
-- @case gateway/risk_test.go
DROP TABLE x
-- @case gateway/schemasnapshot_test.go
DROP USER SNAP_IDX_OWNER CASCADE
-- @case gateway/schemasnapshot_test.go
DROP VIEW SNAP_V_ORDER
-- @case gateway/risk_test.go
DrOp TaBlE x
-- @case gateway/explain_test.go
EXPLAIN (ANALYZE) DELETE FROM orders
-- @case gateway/data_action_test.go
EXPLAIN (ANALYZE) DELETE FROM orders WHERE id = 1
-- @case gateway/risk_test.go
EXPLAIN (ANALYZE) DELETE FROM users
-- @case gateway/explain_test.go
EXPLAIN (ANALYZE) DROP TABLE orders
-- @case gateway/explain_plan_test.go
EXPLAIN (ANALYZE) SELECT * FROM t FOR SHARE
-- @case gateway/explain_plan_test.go
EXPLAIN (ANALYZE) UPDATE t SET v = 1
-- @case gateway/explain_plan_test.go
EXPLAIN (ANALYZE, BUFFERS) (SELECT 1) UNION (SELECT 2)
-- @case gateway/explain_plan_test.go
EXPLAIN (ANALYZE, BUFFERS) (SELECT relname FROM pg_class LIMIT 5) UNION (SELECT 'x')
-- @case gateway/explain_test.go
EXPLAIN (ANALYZE, BUFFERS) DELETE FROM orders
-- @case gateway/data_action_test.go
EXPLAIN (ANALYZE, BUFFERS) DELETE FROM orders WHERE id = 1
-- @case gateway/explain_plan_test.go
EXPLAIN (ANALYZE, BUFFERS) SELECT * FROM t
-- @case gateway/explain_plan_test.go
EXPLAIN (ANALYZE, BUFFERS) SELECT count(*) FROM pg_class
-- @case gateway/explain_plan_test.go
EXPLAIN (ANALYZE, BUFFERS, TIMING) UPDATE
-- @case gateway/explain_plan_test.go
EXPLAIN (ANALYZE, BUFFERS, TIMING, FORMAT JSON) DELETE FROM t
-- @case gateway/explain_plan_test.go
EXPLAIN (ANALYZE, VERBOSE) (SELECT pg_sleep(10))
-- @case gateway/explain_test.go
EXPLAIN (BUFFERS, ANALYZE) DELETE FROM orders
-- @case gateway/explain_test.go
EXPLAIN (COSTS, VERBOSE) UPDATE t SET x = 1
-- @case gateway/explain_plan_test.go
EXPLAIN (FORMAT JSON) (SELECT 1)
-- @case gateway/explain_test.go
EXPLAIN (FORMAT JSON) DELETE FROM orders
-- @case gateway/explain_plan_test.go
EXPLAIN (FORMAT JSON) DELETE FROM t
-- @case gateway/explain_test.go
EXPLAIN (FORMAT JSON) INSERT INTO t
-- @case gateway/bizquery_explain_test.go
EXPLAIN (FORMAT JSON) SELECT * FROM orders
-- @case gateway/explain_plan_test.go
EXPLAIN (FORMAT JSON) SELECT 1
-- @case gateway/explain_plan_test.go
EXPLAIN (SELECT * FROM t FOR UPDATE) UNION (SELECT * FROM t2)
-- @case gateway/explain_plan_test.go
EXPLAIN (SELECT SLEEP(10)) UNION (SELECT 1)
-- @case gateway/explain_plan_test.go
EXPLAIN (SELECT a FROM t LIMIT 10) UNION (SELECT a FROM t2)
-- @case gateway/explain_plan_test.go
EXPLAIN (SELECT pg_sleep(10)) UNION (SELECT 1)
-- @case gateway/explain_plan_test.go
EXPLAIN (VERBOSE) (SELECT pg_sleep(10))
-- @case gateway/explain_test.go
EXPLAIN (analyze,verbose) TRUNCATE TABLE orders
-- @case gateway/dialect_audit_test.go
EXPLAIN ANALYSE DELETE FROM t
-- @case gateway/explain_plan_test.go
EXPLAIN ANALYSE VERBOSE DELETE FROM
-- @case gateway/explain_plan_test.go
EXPLAIN ANALYSE VERBOSE UPDATE t SET v = 1
-- @case gateway/explain_plan_test.go
EXPLAIN ANALYZE ((SELECT 1)) UNION ALL (SELECT 2)
-- @case gateway/explain_plan_test.go
EXPLAIN ANALYZE (SELECT * FROM t FOR UPDATE) UNION (SELECT * FROM t2)
-- @case gateway/explain_plan_test.go
EXPLAIN ANALYZE (SELECT SLEEP(10)) UNION (SELECT 1)
-- @case gateway/explain_plan_test.go
EXPLAIN ANALYZE (SELECT a FROM t LIMIT 10) UNION (SELECT a FROM t2)
-- @case gateway/explain_plan_test.go
EXPLAIN ANALYZE (SELECT pg_sleep(10)) UNION (SELECT 1)
-- @case gateway/explain_plan_test.go
EXPLAIN ANALYZE (SELECT relname FROM pg_class LIMIT 5) UNION (SELECT nspname FROM pg_namespace LIMIT 5)
-- @case gateway/explain_plan_test.go
EXPLAIN ANALYZE DELETE FROM
-- @case gateway/bizquery_test.go
EXPLAIN ANALYZE DELETE FROM orders
-- @case gateway/data_action_test.go
EXPLAIN ANALYZE DELETE FROM orders WHERE id = 1
-- @case gateway/explain_plan_test.go
EXPLAIN ANALYZE DELETE FROM t
-- @case gateway/explain_test.go
EXPLAIN ANALYZE DELETE executes the delete — strict mode must still fire
-- @case gateway/explain_plan_test.go
EXPLAIN ANALYZE DELETE t FROM t JOIN t2 ON t2.id = t.id WHERE t.id = 1
-- @case gateway/risk_test.go
EXPLAIN ANALYZE DROP TABLE users
-- @case gateway/explain_plan_test.go
EXPLAIN ANALYZE FORMAT=TREE DELETE FROM t
-- @case gateway/explain_test.go
EXPLAIN ANALYZE INSERT INTO a SELECT * FROM b
-- @case gateway/explain_plan_test.go
EXPLAIN ANALYZE SELECT "pg_sleep"(100)
-- @case gateway/explain_plan_test.go
EXPLAIN ANALYZE SELECT "pg_sleep_col", 'pg_advisory_lock(1)' FROM t
-- @case gateway/explain_plan_test.go
EXPLAIN ANALYZE SELECT * FROM t
-- @case gateway/explain_plan_test.go
EXPLAIN ANALYZE SELECT * FROM t FOR UPDATE
-- @case gateway/explain_plan_test.go
EXPLAIN ANALYZE SELECT * FROM t INTO OUTFILE '/tmp/x'
-- @case gateway/explain_plan_test.go
EXPLAIN ANALYZE SELECT * FROM t LOCK IN SHARE MODE
-- @case gateway/explain_plan_test.go
EXPLAIN ANALYZE SELECT * INTO t_copy FROM t
-- @case gateway/explain_plan_test.go
EXPLAIN ANALYZE SELECT 1--1 INTO OUTFILE '/tmp/x'
-- @case gateway/explain_plan_test.go
EXPLAIN ANALYZE SELECT COUNT(*) FROM information_schema.schemata
-- @case gateway/explain_plan_test.go
EXPLAIN ANALYZE SELECT MASTER_POS_WAIT('binlog.000001', 4)
-- @case gateway/explain_plan_test.go
EXPLAIN ANALYZE SELECT SLEEP(10) FROM t
-- @case gateway/explain_plan_test.go
EXPLAIN ANALYZE SELECT WAIT_FOR_EXECUTED_GTID_SET('x:1-9')
-- @case gateway/explain_plan_test.go
EXPLAIN ANALYZE SELECT `sleep_at` FROM t
-- @case gateway/explain_plan_test.go
EXPLAIN ANALYZE SELECT `sleep`(100)
-- @case gateway/explain_plan_test.go
EXPLAIN ANALYZE SELECT pg_advisory_lock(1)
-- @case gateway/explain_plan_test.go
EXPLAIN ANALYZE SELECT pg_catalog."pg_sleep"(100)
-- @case gateway/explain_plan_test.go
EXPLAIN ANALYZE SELECT pg_notify('c', 'x')
-- @case gateway/explain_plan_test.go
EXPLAIN ANALYZE SELECT pg_try_advisory_xact_lock(1, 2)
-- @case gateway/explain_test.go
EXPLAIN ANALYZE VERBOSE DELETE FROM t
-- @case gateway/explain_plan_test.go
EXPLAIN ANALYZE WITH d AS (DELETE FROM t RETURNING *) SELECT * FROM d
-- @case gateway/explain_plan_test.go
EXPLAIN ANALYZE WITH x AS (SELECT 1) SELECT * FROM x
-- @case gateway/bizquery_test.go
EXPLAIN ANALYZE 删除
-- @case gateway/explain_test.go
EXPLAIN DELETE FROM orders
-- @case gateway/data_action_test.go
EXPLAIN DELETE FROM orders WHERE id = 1
-- @case gateway/explain_plan_test.go
EXPLAIN DELETE FROM t
-- @case gateway/bizquery_forms_test.go
EXPLAIN FOR CONNECTION 12
-- @case gateway/explain_plan_test.go
EXPLAIN FORMAT = 'brief' DELETE FROM t
-- @case gateway/bizquery_forms_test.go
EXPLAIN FORMAT=JSON FOR CONNECTION 12
-- @case gateway/explain_plan_test.go
EXPLAIN FORMAT=JSON UPDATE t SET v = 1
-- @case gateway/bizquery_explain_test.go
EXPLAIN FORMAT=TREE SELECT * FROM orders
-- @case gateway/explain_plan_test.go
EXPLAIN FORMAT=TREE UPDATE t SET v = 1
-- @case gateway/explain_test.go
EXPLAIN INSERT INTO a SELECT * FROM b
-- @case gateway/dialect_audit_test.go
EXPLAIN PERFORMANCE DELETE FROM t
-- @case gateway/explain_plan_test.go
EXPLAIN PERFORMANCE SELECT * FROM t
-- @case gateway/explain_plan_test.go
EXPLAIN PERFORMANCE SELECT nextval('s') FROM t
-- @case gateway/bizquery_explain_test.go
EXPLAIN PLAN FOR
-- @case gateway/explain_plan_test.go
EXPLAIN PLAN FOR DELETE FROM
-- @case gateway/bizquery_explain_test.go
EXPLAIN PLAN FOR DELETE FROM orders
-- @case gateway/explain_plan_test.go
EXPLAIN PLAN FOR DELETE FROM t
-- @case gateway/bizquery_explain_test.go
EXPLAIN PLAN FOR SELECT * FROM dual WHERE dummy = 'X'
-- @case gateway/explain_test.go
EXPLAIN PLAN FOR SELECT * FROM orders
-- @case gateway/bizquery_explain_test.go
EXPLAIN PLAN FOR SELECT * FROM orders FOR UPDATE
-- @case gateway/bizquery_explain_test.go
EXPLAIN PLAN FOR SELECT * FROM orders WHERE id = 1
-- @case gateway/bizquery_explain_test.go
EXPLAIN PLAN FOR SELECT * FROM sys.user$
-- @case gateway/bizquery_explain_test.go
EXPLAIN PLAN FOR UPDATE orders SET a = 1
-- @case gateway/bizquery_explain_test.go
EXPLAIN PLAN FOR WITH t AS (SELECT id FROM orders) SELECT * FROM t
-- @case gateway/bizquery_explain_test.go
EXPLAIN PLAN INTO my_plan FOR SELECT * FROM orders
-- @case gateway/explain_plan_test.go
EXPLAIN PLAN SET STATEMENT_ID = 'mine' FOR UPDATE
-- @case gateway/explain_test.go
EXPLAIN PLAN SET STATEMENT_ID = 'plan for q3' FOR DELETE FROM t
-- @case gateway/explain_test.go
EXPLAIN PLAN SET STATEMENT_ID = 'q3' FOR UPDATE t SET x = 1
-- @case gateway/bizquery_explain_test.go
EXPLAIN PLAN SET STATEMENT_ID = 'x' FOR SELECT * FROM orders
-- @case gateway/explain_plan_test.go
EXPLAIN QUERY PLAN DELETE FROM t
-- @case gateway/explain_plan_test.go
EXPLAIN SELECT "pg_sleep"(100)
-- @case gateway/bizquery_explain_test.go
EXPLAIN SELECT * FROM orders
-- @case gateway/bizquery_test.go
EXPLAIN SELECT * FROM t
-- @case gateway/explain_plan_test.go
EXPLAIN SELECT * FROM t FOR SHARE
-- @case gateway/explain_plan_test.go
EXPLAIN SELECT * FROM t FOR UPDATE
-- @case gateway/explain_plan_test.go
EXPLAIN SELECT * FROM t INTO OUTFILE '/tmp/x'
-- @case gateway/explain_plan_test.go
EXPLAIN SELECT * FROM t LOCK IN SHARE MODE
-- @case gateway/risk_test.go
EXPLAIN SELECT * FROM users
-- @case gateway/explain_plan_test.go
EXPLAIN SELECT * INTO t_copy FROM t
-- @case gateway/verb_classify_test.go
EXPLAIN SELECT 1
-- @case gateway/explain_plan_test.go
EXPLAIN SELECT 1--1 INTO OUTFILE '/tmp/x'
-- @case gateway/explain_plan_test.go
EXPLAIN SELECT MASTER_POS_WAIT('binlog.000001', 4)
-- @case gateway/explain_plan_test.go
EXPLAIN SELECT SLEEP(10) FROM t
-- @case gateway/explain_plan_test.go
EXPLAIN SELECT WAIT_FOR_EXECUTED_GTID_SET('x:1-9')
-- @case gateway/explain_plan_test.go
EXPLAIN SELECT `sleep`(100)
-- @case gateway/explain_plan_test.go
EXPLAIN SELECT nextval('s') FROM t
-- @case gateway/explain_plan_test.go
EXPLAIN SELECT pg_advisory_lock(1)
-- @case gateway/explain_plan_test.go
EXPLAIN SELECT pg_catalog."pg_sleep"(100)
-- @case gateway/explain_plan_test.go
EXPLAIN SELECT pg_notify('c', 'x')
-- @case gateway/explain_plan_test.go
EXPLAIN SELECT pg_try_advisory_xact_lock(1, 2)
-- @case gateway/explain_plan_test.go
EXPLAIN UPDATE
-- @case gateway/explain_plan_test.go
EXPLAIN UPDATE t JOIN t2 ON t2.id = t.id SET t.v = 500 WHERE t.id = 2
-- @case gateway/explain_plan_test.go
EXPLAIN UPDATE t SET v = 1
-- @case gateway/realrun_routing_test.go
EXPLAIN UPDATE t_demo SET name = 'x' WHERE id = 1
-- @case gateway/explain_test.go
EXPLAIN VERBOSE SELECT 1
-- @case gateway/explain_plan_test.go
EXPLAIN VERBOSE UPDATE t SET v = 1
-- @case gateway/explain_plan_test.go
EXPLAIN WITH d AS (DELETE FROM t RETURNING *) SELECT * FROM d
-- @case gateway/bizquery_forms_test.go
EXPLAIN orders
-- @case gateway/bizquery_test.go
EXPLAIN 不动
-- @case sqlutil/redact_test.go
GRANT ALL ON db.* TO 'u'@'h' IDENTIFIED BY 'grantpw'
-- @case gateway/rule_identity_test.go
GRANT ALL ON x TO y
-- @case gateway/data_action_test.go
GRANT SELECT ON db.* TO reporter
-- @case sqlutil/refs_test.go
GRANT SELECT ON t_order TO alice
-- @case gateway/window_scope_test.go
INSERT B.t2 VALUES (1)
-- @case gateway/window_scope_test.go
INSERT IGNORE INTO B.t2 VALUES (1)
-- @case sqlutil/refs_test.go
INSERT IGNORE INTO t_order VALUES (1)
-- @case gateway/explain_plan_test.go
INSERT INTO
-- @case gateway/sqldump_test.go
INSERT INTO "T" ("id", "name") VALUES (2, NULL);
-- @case sqlutil/refs_test.go
INSERT INTO `t order` VALUES (1)
-- @case gateway/sqldump_test.go
INSERT INTO `t` (`id`, `name`) VALUES
(1, 'a'),
(2, NULL);
-- @case gateway/audit_2026_11_test.go
INSERT INTO attach VALUES (1)
-- @case gateway/dictionary_literals_test.go
INSERT INTO audit_log (action, detail) VALUES ('execute', 'user asked to drop the old report')
-- @case osc/mysql_test.go
INSERT INTO big VALUES (?, ?)
-- @case gateway/bizquery_tidb_test.go
INSERT INTO biz_tidb.orders VALUES (1, '2026-10-27 08:00:00'), (2, '2026-10-27 09:00:00'), (3, '2026-10-28 09:00:00')
-- @case gateway/bizquery_test.go
INSERT INTO biztest.nums VALUES
-- @case gateway/window_scope_test.go
INSERT INTO emp (id) VALUES (emp_seq.NEXTVAL)
-- @case gateway/window_scope_test.go
INSERT INTO emp@remote VALUES (1)
-- @case osc/mysql_test.go
INSERT INTO h VALUES (1)
-- @case gateway/bizquery_test.go
INSERT INTO nums VALUES (100000)
-- @case osc/mysql_test.go
INSERT INTO orders (id, name, note, qty, amount, created) VALUES (?, ?, ?, ?, ?, ?)
-- @case gateway/data_action_test.go
INSERT INTO orders VALUES (1)
-- @case review/checks_spec_test.go
INSERT INTO s.t_a (id, a) VALUES (nextval('s.seq_a'), 1);
-- @case gateway/dictionary_literals_test.go
INSERT INTO sys_menu (menu_name, perms) VALUES ('删除菜单', 'system:menu:delete')
-- @case gateway/dictionary_literals_test.go
INSERT INTO t (a) VALUES ('drop this row')
-- @case gateway/data_action_test.go
INSERT INTO t VALUES (1); DELETE FROM t WHERE 1=1
-- @case gateway/query_cells_test.go
INSERT INTO t VALUES (1, '', NULL, X'01FF', '310101199001011234')
-- @case gateway/schema_test.go
INSERT INTO t VALUES (1,'alice'),(2,'bob')
-- @case sqlutil/split_test.go
INSERT INTO t VALUES('a;b')
-- @case sqlutil/split_test.go
INSERT INTO t VALUES('a;b'); SELECT 1
-- @case sqlutil/split_test.go
INSERT INTO t VALUES('a\')
-- @case sqlutil/split_test.go
INSERT INTO t VALUES('a\'); DROP TABLE u; SELECT 1
-- @case sqlutil/split_test.go
INSERT INTO t VALUES('it''s; ok')
-- @case sqlutil/split_test.go
INSERT INTO t VALUES('it''s; ok'); SELECT 3
-- @case gateway/window_scope_test.go
INSERT INTO t1 (a) VALUES (1) ON DUPLICATE KEY UPDATE t1.a = VALUES(a)
-- @case gateway/window_scope_test.go
INSERT INTO t1 (a, b) VALUES (1, 'x.y@z.com'), (2, '$.a')
-- @case gateway/window_scope_test.go
INSERT INTO t1 SELECT * FROM B.t2
-- @case gateway/window_scope_test.go
INSERT INTO t1 SELECT * FROM t2 WHERE t2.v > 0
-- @case gateway/realrun_routing_test.go
INSERT INTO t_demo (name) VALUES ('a')
-- @case gateway/realrun_routing_test.go
INSERT INTO t_demo (name) VALUES ('a'), ('b')
-- @case gateway/realrun_routing_test.go
INSERT INTO t_demo VALUES (1, 'a')
-- @case sqlutil/refs_test.go
INSERT INTO t_log (msg) VALUES ('SELECT * FROM t_secret')
-- @case sqlutil/refs_test.go
INSERT INTO t_order (id) VALUES (1)
-- @case gateway/export_raw_test.go
INSERT INTO t_person (id, id_card) VALUES (1, '
-- @case gateway/masking_switch_test.go
INSERT INTO t_person VALUES (1, '
-- @case gateway/sqldump_roundtrip_test.go
INSERT INTO vx_src VALUES (1, 'O''Brien C:\\new\\n', NULL, '', '张三', X'00FF27', '2026-10-08 09:30:00', 12.50)
-- @case gateway/sqldump_roundtrip_test.go
INSERT INTO vx_src VALUES (1, 'O''Brien C:\new', NULL, N'张三', HEXTORAW('00FF27'), TIMESTAMP '2026-10-08 09:30:00', 12.5)
-- @case gateway/sqldump_roundtrip_test.go
INSERT INTO vx_src VALUES (1, E'O''Brien C:\\new\\n', NULL, '', '张三', '\x00ff27'::bytea, '2026-10-08 09:30:00', 12.50, true)
-- @case gateway/readonly_guard_test.go
INSERT INTO vz_guard_lg VALUES (2)
-- @case osc/mysql_test.go
INSERT INTO words VALUES (?, ?, ?)
-- @case gateway/dialect_audit_test.go
LOAD DATA INFILE '/tmp/x' INTO TABLE t
-- @case gateway/dialect_audit_test.go
MERGE INTO t USING s ON (1=1) WHEN MATCHED THEN UPDATE SET x=1
-- @case gateway/window_scope_test.go
RENAME TABLE a TO b
-- @case gateway/window_scope_test.go
RENAME TABLE mv TO B.moved
-- @case osc/binlog_mysql_test.go
RENAME TABLE orders TO orders_old
-- @case gateway/window_scope_test.go
REPLACE INTO B.t2 VALUES (1, 2)
-- @case gateway/window_scope_test.go
REPLACE INTO orders_db.t2 VALUES (1, 2)
-- @case osc/binlog_mysql_test.go
REPLACE INTO other VALUES (?, ?)
-- @case gateway/dialect_audit_test.go
REPLACE INTO t VALUES (1)
-- @case gateway/data_action_test.go
REVOKE SELECT ON db.* FROM reporter
-- @case gateway/sensitive_perf_test.go
SELECT
-- @case gateway/backslash_test.go
SELECT "a""", x
-- @case gateway/bizquery_test.go
SELECT "a\"", (SELECT 1 FROM mysql.user LIMIT 1) AS "z"
-- @case gateway/backslash_test.go
SELECT "a\"", x
-- @case gateway/window_scope_test.go
SELECT "dblink_exec"('dbname=b', 'x')
-- @case gateway/effectful_select_test.go
SELECT "dblink_exec"('host=x', 'DROP TABLE t')
-- @case gateway/sensitive_perf_test.go
SELECT "first name" AS n, a.b AS m, t.a.b AS k FROM t
-- @case gateway/explain_analyze_readonly_test.go
SELECT "for update" AS x FROM t
-- @case gateway/sensitive_test.go
SELECT "id_card" AS x, name FROM t_user
-- @case gateway/bizquery_blackbox_test.go
SELECT "pg_advisory_lock"(1)
-- @case gateway/bizquery_blackbox_test.go
SELECT "pg_sleep"(0.01)
-- @case gateway/explain_analyze_readonly_test.go
SELECT "pg_sleep"(100)
-- @case gateway/bizquery_blackbox_test.go
SELECT "pg_sleep"(2)
-- @case gateway/effectful_select_test.go
SELECT "pg_terminate_backend"(1)
-- @case gateway/bizquery_forms_test.go
SELECT "ts_stat"('select 1')
-- @case gateway/bizquery_blackbox_test.go
SELECT "user" FROM orders
-- @case sqlutil/split_test.go
SELECT $$'$$
-- @case sqlutil/split_test.go
SELECT $$'$$ ; DROP TABLE t
-- @case sqlutil/split_test.go
SELECT $$a;b$$
-- @case sqlutil/split_test.go
SELECT $$a;b$$ ; SELECT 2
-- @case sqlutil/split_exec_test.go
SELECT $$a;b$$; SELECT E'x\'y'
-- @case sqlutil/split_test.go
SELECT $tag$'$tag$
-- @case sqlutil/split_test.go
SELECT $tag$'$tag$ ; DROP TABLE t
-- @case gateway/hash_comment_test.go
SELECT '#' FROM t
-- @case gateway/hash_comment_test.go
SELECT '#1' FROM t
-- @case gateway/mariadb_exec_comment_test.go
SELECT '/*M! DROP TABLE t */'
-- @case gateway/audit_2026_11_test.go
SELECT 'ATTACH' FROM t
-- @case gateway/dictionary_literals_test.go
SELECT 'PREPARE s FROM @q' AS tip
-- @case gateway/hash_comment_test.go
SELECT 'a' INTO#x
OUTFILE '/tmp/a'
-- @case gateway/backslash_test.go
SELECT 'a''', x AS 'z'
-- @case gateway/bizquery_test.go
SELECT 'a\'' /* ', (SELECT 1 FROM mysql.user) AS x -- */
-- @case gateway/bizquery_test.go
SELECT 'a\'', (SELECT authentication_string FROM mysql.user LIMIT 1) AS 'z'
-- @case gateway/bizquery_test.go
SELECT 'a\'', 1 AS 'z'
-- @case gateway/bizquery_test.go
SELECT 'a\'', 1 INTO OUTFILE '/tmp/x' -- '
-- @case gateway/bizquery_test.go
SELECT 'a\'', SLEEP(30) AS 'z'
-- @case gateway/backslash_test.go
SELECT 'a\'', SLEEP(600) AS 'z'
-- @case gateway/backslash_test.go
SELECT 'a\'', id_card AS 'z' FROM t_user WHERE 'q' = 'q'
-- @case gateway/bizquery_test.go
SELECT 'a\'', s.* FROM finance_db.salary s WHERE 'x' = 'x'
-- @case gateway/backslash_test.go
SELECT 'a\'', x AS 'z'
-- @case gateway/backslash_test.go
SELECT 'a\''; DROP TABLE biztest.bs_victim; SELECT ''
-- @case gateway/client_commands_test.go
SELECT 'a\Gb' FROM t
-- @case gateway/backslash_test.go
SELECT 'a\\''', x
-- @case gateway/backslash_test.go
SELECT 'a\\', x
-- @case gateway/backslash_test.go
SELECT 'a\\\'', x
-- @case sqlutil/delimiter_test.go
SELECT 'delimiter //' AS x; SELECT 2;
-- @case gateway/sensitive_test.go
SELECT 'id_card' AS label, name FROM t_user
-- @case gateway/sensitive_perf_test.go
SELECT 'id_card' AS lit, concat(id_card, phone) AS c FROM t
-- @case gateway/writes_file_test.go
SELECT 'into outfile' AS s FROM t
-- @case gateway/hash_comment_test.go
SELECT 'into#
outfile'
-- @case gateway/effectful_select_test.go
SELECT 'pg_terminate_backend(1)' AS s
-- @case gateway/dictionary_literals_test.go
SELECT 'truncate' AS word FROM dual
-- @case gateway/hash_comment_test.go
SELECT 'x' INTO # aaa
 # bbb
 DUMPFILE '/tmp/a'
-- @case gateway/hash_comment_test.go
SELECT 'x' INTO#'
OUTFILE '/tmp/a'
-- @case gateway/hash_comment_test.go
SELECT 'x' INTO#x
OUTFILE '/tmp/a'
-- @case gateway/sensitive_selectstar_test.go
SELECT * -- 取全部
FROM `t_user`
-- @case osc/alter_mysql_test.go
SELECT * FROM
-- @case gateway/bizquery_forms_test.go
SELECT * FROM "SALARY"@"FIN.WORLD"
-- @case sqlutil/refs_test.go
SELECT * FROM "T_Order"
-- @case gateway/bizquery_blackbox_test.go
SELECT * FROM "information_schema"."columns"
-- @case gateway/bizquery_blackbox_test.go
SELECT * FROM "pg_authid"
-- @case gateway/bizquery_blackbox_test.go
SELECT * FROM "pg_catalog"."pg_shadow"
-- @case gateway/sensitive_selectstar_test.go
SELECT * FROM "t_user"
-- @case gateway/sensitive_selectstar_test.go
SELECT * FROM "whatever"
-- @case gateway/bizquery_test.go
SELECT * FROM (SELECT * FROM t LIMIT 5) x
-- @case gateway/bizquery_test.go
SELECT * FROM (SELECT * FROM t LIMIT 5) x
LIMIT 1001
-- @case gateway/sensitive_test.go
SELECT * FROM (SELECT 1 AS k FROM a WHERE 1=1) s, t_user
-- @case sqlutil/refs_test.go
SELECT * FROM (SELECT 1) x
-- @case gateway/bizquery_test.go
SELECT * FROM FINANCE_DB.SALARY
-- @case gateway/bizquery_blackbox_test.go
SELECT * FROM INFORMATION_SCHEMA.tables
-- @case gateway/effectful_select_test.go
SELECT * FROM OPENROWSET(BULK 'c:/x.txt', SINGLE_CLOB) AS t
-- @case gateway/audit_2026_11_b_test.go
SELECT * FROM ORDERS.t
-- @case gateway/bizquery_test.go
SELECT * FROM ORDERS_DB.orders
-- @case gateway/audit_2026_11_b_test.go
SELECT * FROM OTHER.t
-- @case gateway/bizquery_test.go
SELECT * FROM SYS.USER$
-- @case gateway/sensitive_selectstar_test.go
SELECT * FROM T_USER
-- @case gateway/bizquery_forms_test.go
SELECT * FROM XMLTABLE('fn:collection("oradb:/FIN/SALARY")')
-- @case gateway/audit_2026_11_b_test.go
SELECT * FROM `ORDERS`.t
-- @case gateway/bizquery_test.go
SELECT * FROM `finance_db`.`salary`
-- @case gateway/sensitive_selectstar_test.go
SELECT * FROM `orders_db`.`t_user`
-- @case gateway/sensitive_selectstar_test.go
SELECT * FROM `t_user`
-- @case gateway/sensitive_selectstar_test.go
SELECT * FROM `whatever`
-- @case gateway/sensitive_test.go
SELECT * FROM a, t_user
-- @case gateway/bizquery_blackbox_test.go
SELECT * FROM dbe_perf.session_stat
-- @case gateway/dialect_audit_test.go
SELECT * FROM dual
-- @case gateway/verb_classify_test.go
SELECT * FROM elsewhere
-- @case gateway/bizquery_test.go
SELECT * FROM finance_db.salary
-- @case sqlutil/refs_test.go
SELECT * FROM generate_series(1, 10) g
-- @case gateway/window_scope_test.go
SELECT * FROM hr.emp e WHERE e.id = pk_util.f(1)
-- @case gateway/audit_2026_11_b_test.go
SELECT * FROM hr.t
-- @case gateway/bizquery_blackbox_test.go
SELECT * FROM information_schema.columns
-- @case gateway/bizquery_test.go
SELECT * FROM information_schema.tables
-- @case sqlutil/refs_test.go
SELECT * FROM mydb.public.t_order
-- @case gateway/bizquery_test.go
SELECT * FROM mysql.user
-- @case gateway/exec_cancel_test.go
SELECT * FROM no_such_table_here
-- @case gateway/data_action_test.go
SELECT * FROM orders
-- @case gateway/bizquery_blackbox_test.go
SELECT * FROM orders /*!50000 FOR UPDATE */
-- @case gateway/bizquery_blackbox_test.go
SELECT * FROM orders FOR UPDATE
-- @case gateway/bizquery_test.go
SELECT * FROM orders INTO OUTFILE '/tmp/x'
-- @case gateway/bizquery_blackbox_test.go
SELECT * FROM orders LOCK IN SHARE MODE
-- @case gateway/bizquery_tidb_test.go
SELECT * FROM orders WHERE billtime > '2026-10-27 00:00:00' AND billtime < '2026-10-28 00:00:00'
-- @case gateway/bizquery_blackbox_test.go
SELECT * FROM orders WHERE id = 1 FOR SHARE
-- @case osc/mysql_test.go
SELECT * FROM orders WHERE id = 10
-- @case gateway/bizquery_test.go
SELECT * FROM orders WHERE name = 'O''Brien'
-- @case gateway/bizquery_blackbox_test.go
SELECT * FROM orders WHERE note = 'for update sleep(1) into'
-- @case gateway/bizquery_test.go
SELECT * FROM orders WHERE note = 'insert into finance_db.x for update sleep('
-- @case sqlutil/redact_test.go
SELECT * FROM orders WHERE note = 'password reset requested'
-- @case gateway/bizquery_blackbox_test.go
SELECT * FROM orders WHERE note = 'pg_authid information_schema'
-- @case gateway/bizquery_test.go
SELECT * FROM orders WHERE path = 'C:\\data'
-- @case gateway/bizquery_test.go
SELECT * FROM orders WHERE uid IN (SELECT id FROM hr_db.staff)
-- @case gateway/bizquery_blackbox_test.go
SELECT * FROM orders WHERE uid IN (SELECT id FROM users WHERE vip = 1)
-- @case gateway/bizquery_blackbox_test.go
SELECT * FROM orders WHERE uid IN (SELECT usesysid FROM pg_user)
-- @case gateway/bizquery_blackbox_test.go
SELECT * FROM orders o JOIN pg_namespace n ON n.oid = o.ns
-- @case gateway/bizquery_test.go
SELECT * FROM orders, hr_db.staff
-- @case gateway/audit_2026_11_b_test.go
SELECT * FROM orders.t
-- @case gateway/bizquery_test.go
SELECT * FROM orders_db.orders o JOIN orders_db.items i ON o.id = i.oid
-- @case gateway/sensitive_selectstar_test.go
SELECT * FROM orders_db.t_user
-- @case gateway/bizquery_test.go
SELECT * FROM orders_db.订单
-- @case gateway/hash_comment_test.go
SELECT * FROM other#x
.t
-- @case gateway/bizquery_blackbox_test.go
SELECT * FROM pg_catalog."pg_authid"
-- @case gateway/bizquery_blackbox_test.go
SELECT * FROM pg_catalog."pg_shadow"
-- @case gateway/bizquery_blackbox_test.go
SELECT * FROM pg_catalog.pg_authid
-- @case gateway/bizquery_blackbox_test.go
SELECT * FROM pg_orders
-- @case gateway/bizquery_blackbox_test.go
SELECT * FROM pg_roles
-- @case gateway/bizquery_blackbox_test.go
SELECT * FROM pg_settings
-- @case gateway/bizquery_blackbox_test.go
SELECT * FROM pg_stat_activity
-- @case gateway/bizquery_blackbox_test.go
SELECT * FROM pg_stat_statements
-- @case gateway/bizquery_blackbox_test.go
SELECT * FROM pg_toast.pg_toast_1234
-- @case gateway/bizquery_blackbox_test.go
SELECT * FROM pg_user
-- @case gateway/bizquery_blackbox_test.go
SELECT * FROM pgxc_node
-- @case gateway/bizquery_test.go
SELECT * FROM public.orders
-- @case gateway/bizquery_forms_test.go
SELECT * FROM salary@fin_link
-- @case gateway/audit_2026_11_b_test.go
SELECT * FROM scott.t
-- @case gateway/bizquery_test.go
SELECT * FROM t
-- @case gateway/bizquery_test.go
SELECT * FROM t
LIMIT 1001
-- @case gateway/bizquery_test.go
SELECT * FROM t -- 看看
-- @case gateway/bizquery_test.go
SELECT * FROM t -- 看看
LIMIT 1001
-- @case gateway/bizquery_test.go
SELECT * FROM t FETCH FIRST 1001 ROWS ONLY
-- @case gateway/bizquery_test.go
SELECT * FROM t FETCH FIRST 3 ROWS ONLY
-- @case gateway/bizquery_test.go
SELECT * FROM t FETCH FIRST 50000 ROWS ONLY
-- @case gateway/explain_analyze_readonly_test.go
SELECT * FROM t FOR UPDATE
-- @case gateway/writes_file_test.go
SELECT * FROM t INTO OUTFILE '/tmp/x'
-- @case gateway/bizquery_test.go
SELECT * FROM t LIMIT (SELECT 100000000)
-- @case gateway/bizquery_test.go
SELECT * FROM t LIMIT 10 OFFSET 0 + 1, 5
-- @case gateway/bizquery_test.go
SELECT * FROM t LIMIT 100000000
-- @case gateway/bizquery_test.go
SELECT * FROM t LIMIT 1001
-- @case gateway/bizquery_test.go
SELECT * FROM t LIMIT 1001 OFFSET 10
-- @case gateway/bizquery_test.go
SELECT * FROM t LIMIT 20, 1001
-- @case gateway/bizquery_test.go
SELECT * FROM t LIMIT 20, 99999999
-- @case gateway/bizquery_test.go
SELECT * FROM t LIMIT 5
-- @case gateway/bizquery_test.go
SELECT * FROM t LIMIT 99999 OFFSET 10
-- @case gateway/bizquery_test.go
SELECT * FROM t LIMIT 9999;
-- @case gateway/bizquery_test.go
SELECT * FROM t LIMIT @n
-- @case gateway/bizquery_test.go
SELECT * FROM t LIMIT ALL
-- @case gateway/hash_comment_test.go
SELECT * FROM t WHERE a = '#' # 尾注释
-- @case gateway/bizquery_test.go
SELECT * FROM t WHERE a = 'limit 1'
-- @case gateway/bizquery_test.go
SELECT * FROM t WHERE a = 'limit 1'
LIMIT 1001
-- @case gateway/client_commands_test.go
SELECT * FROM t WHERE c = 'x\Gy' ORDER BY id
-- @case gateway/explain_analyze_readonly_test.go
SELECT * FROM t WHERE note = "check into it"
-- @case gateway/client_commands_test.go
SELECT * FROM t \G
-- @case gateway/bizquery_test.go
SELECT * FROM t;
-- @case gateway/client_commands_test.go
SELECT * FROM t\G
-- @case gateway/client_commands_test.go
SELECT * FROM t\G;
-- @case gateway/client_commands_test.go
SELECT * FROM t\g
-- @case gateway/mask_evade_test.go
SELECT * FROM t_order
-- @case sqlutil/refs_test.go
SELECT * FROM t_order WHERE id = 1
-- @case gateway/sensitive_selectstar_test.go
SELECT * FROM t_other WHERE note = 'from t_user'
-- @case gateway/export_raw_test.go
SELECT * FROM t_person
-- @case gateway/mask_evade_test.go
SELECT * FROM t_user
-- @case gateway/sensitive_selectstar_test.go
SELECT * FROM t_user AS u
-- @case gateway/sensitive_selectstar_test.go
SELECT * FROM t_user u
-- @case gateway/sensitive_selectstar_test.go
SELECT * FROM t_user u JOIN t_order o ON o.uid = u.id
-- @case gateway/bizquery_forms_test.go
SELECT * FROM ts_stat($$select v from t$$)
-- @case sqlutil/refs_test.go
SELECT * FROM unnest(ARRAY[1,2]) u
-- @case gateway/bizquery_forms_test.go
SELECT * FROM users WHERE email = 'a@b.com'
-- @case gateway/bizquery_forms_test.go
SELECT * FROM users WHERE email = 'a@b.com' AND @@sql_mode IS NOT NULL
-- @case sqlutil/redact_test.go
SELECT * FROM users WHERE id = 1
-- @case gateway/sensitive_selectstar_test.go
SELECT * FROM whatever
-- @case gateway/explain_analyze_readonly_test.go
SELECT * INTO OUTFILE '/tmp/x' FROM t
-- @case gateway/explain_analyze_readonly_test.go
SELECT * INTO new_t FROM t
-- @case gateway/bizquery_blackbox_test.go
SELECT * INTO new_table FROM orders
-- @case gateway/sensitive_selectstar_test.go
SELECT *, 1 AS flag FROM t_user
-- @case gateway/sensitive_star_align_test.go
SELECT *, id_card AS x FROM t_user
-- @case sqlutil/split_exec_test.go
SELECT /*T! 1 + */ 1
-- @case gateway/audit_2026_11_test.go
SELECT 1
-- @case gateway/split_judge_test.go
SELECT 1 # 2
-- @case gateway/hash_comment_test.go
SELECT 1 # 备注
-- @case gateway/hash_comment_test.go
SELECT 1 #'
, (SELECT authentication_string FROM mysql.user LIMIT 1) #'
-- @case gateway/hash_comment_test.go
SELECT 1 #'
; UPDATE t SET a=1 WHERE id=1; -- '
-- @case sqlutil/split_test.go
SELECT 1 #x
-- @case gateway/stripcomments_test.go
SELECT 1 #x; DROP TABLE orders
-- @case gateway/killquery_test.go
SELECT 1 + 1
-- @case gateway/hash_comment_test.go
SELECT 1 -- # 
FROM t
-- @case gateway/backslash_test.go
SELECT 1 -- 'a\'
-- @case gateway/split_judge_test.go
SELECT 1 -- INTO OUTFILE '/tmp/p'
-- @case sqlutil/split_test.go
SELECT 1 -- drop; me
; SELECT 2
-- @case gateway/split_judge_test.go
SELECT 1 --'
; DROP TABLE t
-- @case gateway/hash_comment_test.go
SELECT 1 /* # */ FROM t
-- @case sqlutil/split_test.go
SELECT 1 /* a; b */; SELECT 2
-- @case sqlutil/split_test.go
SELECT 1 /*!40000 ; DROP TABLE t */
-- @case gateway/writes_file_test.go
SELECT 1 /*!50000 INTO OUTFILE '/tmp/p' */
-- @case gateway/mariadb_exec_comment_test.go
SELECT 1 /*M! , LOAD_FILE('/etc/passwd') */
-- @case gateway/mariadb_exec_comment_test.go
SELECT 1 /*M! , SLEEP(5) */
-- @case gateway/mariadb_exec_comment_test.go
SELECT 1 /*M! INTO OUTFILE '/tmp/a' */
-- @case sqlutil/split_test.go
SELECT 1 AS a$x$
-- @case sqlutil/split_test.go
SELECT 1 AS a$x$; UPDATE accounts SET balance=0; --$x$
-- @case gateway/brokenconn_test.go
SELECT 1 AS one
-- @case gateway/window_scope_test.go
SELECT 1 FROM (SELECT 1) AS B, B.t2
-- @case gateway/bizquery_explain_test.go
SELECT 1 FROM dual
-- @case sqlutil/split_test.go
SELECT 1 FROM dual /*!40000 INTO OUTFILE '/tmp/pwn' */
-- @case review/checks_spec_test.go
SELECT 1 FROM s.t_a JOIN s.t_b ON 1=1;
-- @case gateway/hash_comment_test.go
SELECT 1 INTO#
OUTFILE '/tmp/a'
-- @case gateway/writes_file_test.go
SELECT 1 INTO/**/OUTFILE '/tmp/p'
-- @case gateway/split_judge_test.go
SELECT 1--1 INTO OUTFILE '/tmp/p'
-- @case gateway/split_judge_test.go
SELECT 1--1, load_file('/etc/passwd')
-- @case review/review_test.go
SELECT 1;
-- @case gateway/split_judge_test.go
SELECT 1;
# note
DELETE FROM t
-- @case gateway/bizquery_test.go
SELECT 1; -- done
-- @case sqlutil/split_exec_test.go
SELECT 1; /* x */ UPDATE t SET v = 'a;b' WHERE id = 1 -- c
; DELETE FROM t WHERE id = 2
-- @case sqlutil/delimiter_test.go
SELECT 1; DELIMITER // SELECT 2;
-- @case gateway/data_action_test.go
SELECT 1; DROP TABLE orders;
-- @case gateway/data_action_test.go
SELECT 1; INSERT INTO orders VALUES (1);
-- @case gateway/bizquery_test.go
SELECT 1; SELECT 2
-- @case gateway/data_action_test.go
SELECT 1; SELECT 2;
-- @case gateway/readonly_guard_test.go
SELECT 1; SHOW TABLES; DESC t
-- @case gateway/data_action_test.go
SELECT 1; UPDATE orders SET x=1;
-- @case gateway/readonly_guard_test.go
SELECT 1; UPDATE t SET a = 1
-- @case sqlutil/split_test.go
SELECT 1;;  ;
-- @case gateway/set_statement_test.go
SELECT 2
-- @case sqlutil/delimiter_test.go
SELECT 2;
-- @case sqlutil/delimiter_test.go
SELECT 3
-- @case sqlutil/delimiter_test.go
SELECT 3;
-- @case sqlutil/delimiter_test.go
SELECT 4
-- @case sqlutil/delimiter_test.go
SELECT 4;
-- @case gateway/exec_session_test.go
SELECT 7
-- @case gateway/bizquery_blackbox_test.go
SELECT @@GLOBAL.datadir
-- @case gateway/bizquery_test.go
SELECT @@SESSION.max_execution_time
-- @case gateway/readonly_guard_test.go
SELECT @@SESSION.transaction_read_only
-- @case gateway/bizquery_blackbox_test.go
SELECT @@`hostname`
-- @case gateway/bizquery_blackbox_test.go
SELECT @@basedir
-- @case gateway/bizquery_blackbox_test.go
SELECT @@datadir
-- @case gateway/bizquery_blackbox_test.go
SELECT @@datadir, @@basedir
-- @case gateway/bizquery_blackbox_test.go
SELECT @@hostname
-- @case gateway/bizquery_blackbox_test.go
SELECT @@secure_file_priv
-- @case gateway/bizquery_blackbox_test.go
SELECT @@session . hostname
-- @case gateway/bizquery_blackbox_test.go
SELECT @@version, @@version_comment, @@time_zone, @@session.sql_mode
-- @case gateway/exec_session_test.go
SELECT @aegisdb_sess, CONNECTION_ID()
-- @case gateway/bizquery_blackbox_test.go
SELECT BENCHMARK(100000000, MD5('a'))
-- @case gateway/sensitive_error_test.go
SELECT CAST(id_card AS int) FROM t_other
-- @case gateway/sensitive_error_test.go
SELECT CAST(id_card AS int) FROM t_user
-- @case gateway/sensitive_error_test.go
SELECT CAST(name AS int) FROM t_user
-- @case gateway/term_session_test.go
SELECT COALESCE(@aegisdb_tab, 'none')
-- @case gateway/explain_plan_test.go
SELECT COALESCE(GROUP_CONCAT(CONCAT(id, ':', v) ORDER BY id), '空表') FROM
-- @case gateway/explain_plan_test.go
SELECT COALESCE(string_agg(id || ':' || v, ',' ORDER BY id), '空表') FROM
-- @case gateway/term_session_test.go
SELECT CONCAT(DATABASE(), ':', CONNECTION_ID(), ':', COALESCE(@aegisdb_tab, 'none'))
-- @case gateway/sensitive_test.go
SELECT CONCAT(id_card, '') AS x, name FROM t_user
-- @case gateway/exec_session_test.go
SELECT CONNECTION_ID()
-- @case gateway/explain_plan_test.go
SELECT COUNT(*) FROM
-- @case gateway/bizquery_test.go
SELECT COUNT(*) FROM biztest.nums
-- @case osc/binlog_mysql_test.go
SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? AND TABLE_NAME = '_orders_gho'
-- @case osc/alter_mysql_test.go
SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? AND TABLE_NAME = '_t_gho'
-- @case osc/alter_mysql_test.go
SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? AND TABLE_NAME = '_t_ocp'
-- @case osc/alter_mysql_test.go
SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? AND TABLE_NAME LIKE ?
-- @case osc/mysql_test.go
SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?
-- @case gateway/exec_session_test.go
SELECT COUNT(*) FROM information_schema.processlist WHERE id = ?
-- @case gateway/backslash_test.go
SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = 'biztest' AND table_name = 'bs_victim'
-- @case gateway/readonly_guard_test.go
SELECT COUNT(*) FROM lg
-- @case gateway/readonly_guard_test.go
SELECT COUNT(*) FROM vz_grd_log
-- @case gateway/readonly_guard_test.go
SELECT COUNT(*) FROM vz_guard.lg
-- @case gateway/readonly_guard_test.go
SELECT COUNT(*) FROM vz_guard_lg
-- @case gateway/bizquery_blackbox_test.go
SELECT COUNT(*), MAX(created_at) FROM orders LEFT JOIN items ON items.oid = orders.id
-- @case gateway/bizquery_blackbox_test.go
SELECT CURRENT_USER
-- @case gateway/term_session_test.go
SELECT DATABASE()
-- @case gateway/bizquery_blackbox_test.go
SELECT DATABASE(), USER(), @@hostname
-- @case gateway/bizquery_blackbox_test.go
SELECT DATABASE(), VERSION()
-- @case gateway/bizquery_blackbox_test.go
SELECT DATABASE(), VERSION(), @@version_comment, COUNT(*) FROM nums
-- @case gateway/bizquery_test.go
SELECT DBMS_RANDOM.VALUE FROM DUAL
-- @case gateway/bizquery_forms_test.go
SELECT DBURITYPE('/FIN/SALARY').getclob() FROM dual
-- @case osc/mysql_test.go
SELECT DISTINCT INDEX_NAME FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?
-- @case sqlutil/split_test.go
SELECT E'\''
-- @case sqlutil/split_test.go
SELECT E'\'' ; DROP TABLE t
-- @case gateway/bizquery_blackbox_test.go
SELECT GET_LOCK('x', 10)
-- @case gateway/bizquery_forms_test.go
SELECT HTTPURITYPE('http://x/').getclob() FROM dual
-- @case gateway/bizquery_test.go
SELECT LOAD_FILE('/etc/passwd')
-- @case gateway/bizquery_blackbox_test.go
SELECT REPLACE(name, ' ', ''), CONCAT(first, ' ', last), IFNULL(x, 0) FROM users
-- @case gateway/bizquery_test.go
SELECT SLEEP(1)
-- @case gateway/term_session_test.go
SELECT SLEEP(1.5)
-- @case gateway/killquery_test.go
SELECT SLEEP(10) /* aegisdb-prev */
-- @case gateway/bizquery_test.go
SELECT SLEEP(100)
-- @case gateway/killquery_test.go
SELECT SLEEP(30)
-- @case gateway/export_cancel_test.go
SELECT SLEEP(30) AS s /* export-cancel-probe */
-- @case gateway/bizquery_blackbox_test.go
SELECT SLEEP(5)
-- @case review/review_test.go
SELECT SQL_NO_CACHE * FROM t;
-- @case gateway/sensitive_test.go
SELECT SUBSTR("id_card", 1, 9) AS x, name FROM t_user
-- @case gateway/sensitive_perf_test.go
SELECT SUBSTR(ID_CARD,1,6) AS x, upper(Phone) y FROM t
-- @case gateway/sensitive_test.go
SELECT SUBSTR(id_card, 1, 6) AS x, name FROM t_user
-- @case gateway/mask_style_test.go
SELECT SUBSTR(id_card, 2, 17) AS x FROM t
-- @case gateway/mask_evade_test.go
SELECT SUBSTR(id_card,1,6) AS pre FROM t_user
-- @case gateway/mention_unicode_test.go
SELECT SUBSTR(身份证,1,6) AS x FROM t_user
-- @case gateway/bizquery_test.go
SELECT SUM(a.n * b.n * c.n) FROM nums a, nums b, nums c
-- @case gateway/bizquery_mariadb_test.go
SELECT SUM(a.seq * b.seq) FROM seq_1_to_100000 a, seq_1_to_100000 b
-- @case gateway/readonly_guard_test.go
SELECT SYSDATE FROM dual
-- @case gateway/bizquery_regress_test.go
SELECT SYSTEM_USER()
-- @case gateway/bizquery_blackbox_test.go
SELECT SYS_CONTEXT('USERENV', 'SERVER_HOST') FROM DUAL
-- @case gateway/bizquery_forms_test.go
SELECT URIFACTORY.getUri('http://x/') FROM dual
-- @case gateway/bizquery_blackbox_test.go
SELECT USER FROM DUAL
-- @case gateway/bizquery_blackbox_test.go
SELECT USER()
-- @case gateway/bizquery_forms_test.go
SELECT XMLQUERY('ora:view("FIN","SALARY")' RETURNING CONTENT) FROM dual
-- @case gateway/bizquery_blackbox_test.go
SELECT [sleep](3)
-- @case gateway/bizquery_blackbox_test.go
SELECT `SLEEP` (0.01)
-- @case gateway/bizquery_blackbox_test.go
SELECT `SLEEP` (3)
-- @case gateway/hash_comment_test.go
SELECT `a#b` FROM t
-- @case gateway/sensitive_test.go
SELECT `a,b`, `id_card` AS x FROM t_user
-- @case gateway/backslash_test.go
SELECT `a\`, x
-- @case gateway/bizquery_blackbox_test.go
SELECT `benchmark`(1000000, md5('a'))
-- @case gateway/dictionary_literals_test.go
SELECT `delete` FROM t
-- @case gateway/sensitive_test.go
SELECT `from`, `id_card` AS x FROM t_user
-- @case gateway/bizquery_blackbox_test.go
SELECT `get_lock`('aegisdb_bb', 0), `release_lock`('aegisdb_bb')
-- @case gateway/bizquery_blackbox_test.go
SELECT `get_lock`('x', 10)
-- @case gateway/sensitive_test.go
SELECT `id_card` AS x, name FROM t_user
-- @case gateway/explain_analyze_readonly_test.go
SELECT `into` FROM t
-- @case gateway/bizquery_blackbox_test.go
SELECT `sleep`(0.01)
-- @case gateway/bizquery_blackbox_test.go
SELECT `sleep`(3)
-- @case gateway/explain_analyze_readonly_test.go
SELECT `sleep`(5)
-- @case gateway/bizquery_blackbox_test.go
SELECT `sleep`, `into` FROM orders
-- @case gateway/bizquery_blackbox_test.go
SELECT `update`, `into`, `sleep` FROM t
-- @case gateway/bizquery_blackbox_test.go
SELECT `user`()
-- @case review/checks_spec_test.go
SELECT a FROM (SELECT a FROM dwd.t_a) x;
-- @case review/checks_spec_test.go
SELECT a FROM (SELECT a FROM s.t_a ORDER BY a) x;
-- @case review/checks_spec_test.go
SELECT a FROM dwd.t_a;
-- @case review/checks_spec_test.go
SELECT a FROM s.t_a ORDER BY a;
-- @case review/checks_spec_test.go
SELECT a FROM s.t_a WHERE id = (SELECT nextval('s.seq_a'));
-- @case review/checks_spec_test.go
SELECT a FROM s.t_a WHERE id NOT IN (1,2,3);
-- @case review/checks_spec_test.go
SELECT a FROM s.t_a WHERE id NOT IN (SELECT id FROM s.t_b);
-- @case review/checks_spec_test.go
SELECT a FROM s.t_a;
-- @case gateway/sensitive_perf_test.go
SELECT a FROM t WHERE x = 'it\'s' UNION SELECT phone FROM t_user
-- @case review/checks_spec_test.go
SELECT a FROM t_a;
-- @case gateway/hash_comment_test.go
SELECT a INTO @x FROM t
-- @case gateway/sensitive_perf_test.go
SELECT a, b FROM t_user /* id_card */
-- @case gateway/sensitive_star_align_test.go
SELECT a.*, b.* FROM t_user a JOIN t_user b ON a.id = b.id
-- @case gateway/sensitive_star_align_test.go
SELECT a.*, b.*, b.id_card AS x FROM t_user a JOIN t_user b ON a.id = b.id
-- @case gateway/window_scope_test.go
SELECT a.*, b.v FROM t1 a LEFT JOIN t2 b ON a.id = b.id
-- @case sqlutil/refs_test.go
SELECT a.id FROM t_order a JOIN t_item b ON b.oid = a.id JOIN t_item c ON c.oid = a.id
-- @case gateway/mask_evade_test.go
SELECT a.x FROM orders o JOIN (SELECT id_card AS x FROM t_user) a ON 1=1
-- @case gateway/tableref_unicode_test.go
SELECT a.x FROM t_order a JOIN 用户表 b ON a.uid = b.id
-- @case gateway/sensitive_perf_test.go
SELECT addr FROM `t_order`
-- @case gateway/sensitive_perf_test.go
SELECT addr FROM t_order
-- @case gateway/bizquery_forms_test.go
SELECT app.f(o.id), "Mixed" FROM orders o WHERE o.note = 'not_a_name' -- tail_comment
-- @case gateway/sensitive_star_align_test.go
SELECT count(*) AS n, id_card AS x FROM t_user GROUP BY id_card
-- @case gateway/sensitive_test.go
SELECT count(1) AS n FROM t_user WHERE id_card = 'x'
-- @case gateway/sensitive_selectstar_test.go
SELECT count(1) FROM t_user WHERE id_card = 'x'
-- @case gateway/term_session_test.go
SELECT current_database()
-- @case gateway/bizquery_blackbox_test.go
SELECT current_setting('data_directory')
-- @case gateway/bizquery_blackbox_test.go
SELECT current_user
-- @case gateway/bizquery_blackbox_test.go
SELECT current_user()
-- @case gateway/bizquery_blackbox_test.go
SELECT current_user, inet_server_addr()
-- @case gateway/bizquery_forms_test.go
SELECT database_to_xml(true, false, '')
-- @case gateway/bizquery_forms_test.go
SELECT database_to_xmlschema(true, false, '')
-- @case gateway/window_scope_test.go
SELECT dblink_exec('dbname=b', 'UPDATE t SET v = 1')
-- @case gateway/effectful_select_test.go
SELECT dblink_exec('host=x', 'DROP TABLE t')
-- @case sqlutil/delimiter_test.go
SELECT delimiter FROM t; SELECT 2;
-- @case sqlutil/delimiter_test.go
SELECT delimiters FROM t; SELECT 2;
-- @case gateway/sensitive_perf_test.go
SELECT email, salary FROM t_order
-- @case gateway/sensitive_perf_test.go
SELECT email, salary FROM t_user
-- @case gateway/readonly_guard_test.go
SELECT f()
-- @case gateway/readonly_guard_test.go
SELECT f() FROM t
-- @case gateway/bizquery_forms_test.go
SELECT f@remote(1) FROM dual
-- @case gateway/bizquery_forms_test.go
SELECT httpuritype.createuri('http://x/').getclob() FROM dual
-- @case osc/alter_mysql_test.go
SELECT id FROM _t_del
-- @case gateway/bizquery_regress_test.go
SELECT id FROM docs WHERE to_tsvector(body) @@ to_tsquery('foo')
-- @case gateway/bizquery_regress_test.go
SELECT id FROM docs WHERE tsv @@ plainto_tsquery('english', 'foo bar')
-- @case review/schema_qualified_unicode_test.go
SELECT id FROM dwd.t_user
-- @case gateway/bizquery_blackbox_test.go
SELECT id FROM orders WHERE `sleep`(1) = 0
-- @case osc/alter_mysql_test.go
SELECT id FROM ref
-- @case osc/alter_mysql_test.go
SELECT id FROM t
-- @case gateway/explain_analyze_readonly_test.go
SELECT id FROM t WHERE a = 1
-- @case gateway/effectful_select_test.go
SELECT id FROM t WHERE note = 'dblink_exec(x)'
-- @case gateway/mask_evade_test.go
SELECT id FROM t_order UNION SELECT id_card FROM t_user
-- @case review/schema_qualified_unicode_test.go
SELECT id FROM t_user
-- @case review/schema_qualified_unicode_test.go
SELECT id FROM 业务库.用户表
-- @case review/schema_qualified_unicode_test.go
SELECT id FROM 用户表
-- @case gateway/bizquery_blackbox_test.go
SELECT id INTO @x FROM orders LIMIT 1
-- @case gateway/bizquery_blackbox_test.go
SELECT id, SLEEP(1) FROM orders
-- @case gateway/bizquery_test.go
SELECT id, amount FROM orders WHERE id = 1
-- @case gateway/bizquery_blackbox_test.go
SELECT id, amount FROM public.orders WHERE id = 1
-- @case gateway/export_raw_test.go
SELECT id, id_card FROM t_person
-- @case gateway/sensitive_perf_test.go
SELECT id, id_card, phone FROM t_user
-- @case gateway/schema_test.go
SELECT id, name FROM t ORDER BY id
-- @case gateway/realrun_routing_test.go
SELECT id, name FROM t_demo
-- @case gateway/query_cells_test.go
SELECT id, name, note, data, id_card FROM t
-- @case gateway/sensitive_perf_test.go
SELECT id, ��id_card FROM t
-- @case gateway/mask_style_test.go
SELECT id_card AS phone FROM t
-- @case gateway/mask_style_test.go
SELECT id_card AS x FROM t
-- @case gateway/sensitive_star_align_test.go
SELECT id_card AS x, * FROM t_user
-- @case gateway/sensitive_test.go
SELECT id_card AS x, name FROM t_user
-- @case gateway/tableref_unicode_test.go
SELECT id_card FROM "用户表"
-- @case gateway/sensitive_test.go
SELECT id_card FROM ONLY t_user
-- @case gateway/tableref_unicode_test.go
SELECT id_card FROM `用户表`
-- @case gateway/sensitive_test.go
SELECT id_card FROM a STRAIGHT_JOIN t_user ON a.id = t_user.id
-- @case gateway/sensitive_test.go
SELECT id_card FROM a, t_user
-- @case gateway/mask_style_test.go
SELECT id_card FROM t
-- @case gateway/sensitive_test.go
SELECT id_card FROM t_other
-- @case gateway/sensitive_error_test.go
SELECT id_card FROM t_user
-- @case gateway/mask_evade_test.go
SELECT id_card FROM t_user WHERE note = 'union'
-- @case gateway/mask_evade_test.go
SELECT id_card FROM whatever
-- @case gateway/tableref_unicode_test.go
SELECT id_card FROM 业务库.用户表 u
-- @case gateway/tableref_unicode_test.go
SELECT id_card FROM 用户表
-- @case gateway/sensitive_test.go
SELECT id_card x, name FROM t_user
-- @case gateway/mask_evade_test.go
SELECT id_card, name FROM t_user
-- @case gateway/tableref_unicode_test.go
SELECT id_card, name FROM 用户表
-- @case gateway/sensitive_perf_test.go
SELECT id_card_no, xid_card, id_card2 FROM t
-- @case gateway/bizquery_blackbox_test.go
SELECT inet_server_addr()
-- @case gateway/effectful_select_test.go
SELECT lo_export(12, '/tmp/x')
-- @case gateway/effectful_select_test.go
SELECT load_file('/etc/passwd')
-- @case sqlutil/redact_test.go
SELECT mysql_native_password FROM t
-- @case gateway/readonly_guard_test.go
SELECT n FROM lg
-- @case gateway/bizquery_test.go
SELECT n FROM nums ORDER BY n
-- @case gateway/realrun_routing_test.go
SELECT name FROM t_demo WHERE id = 1
-- @case gateway/mask_evade_test.go
SELECT name FROM t_user UNION SELECT name FROM t_staff
-- @case gateway/readonly_guard_test.go
SELECT nextval('s')
-- @case gateway/bizquery_blackbox_test.go
SELECT nextval('seq')
-- @case gateway/bizquery_test.go
SELECT o.id, o.amount FROM orders o
-- @case gateway/bizquery_blackbox_test.go
SELECT o.id, o.created_at::date FROM orders o JOIN items i ON i.oid = o.id
-- @case gateway/bizquery_blackbox_test.go
SELECT o.user FROM orders o
-- @case gateway/bizquery_blackbox_test.go
SELECT o.user_id FROM orders o
-- @case gateway/bizquery_forms_test.go
SELECT order_to_xml_flag, convert_to_xml(id) FROM orders
-- @case gateway/sensitive_test.go
SELECT passwd FROM anything
-- @case gateway/bizquery_blackbox_test.go
SELECT pg_advisory_lock(1)
-- @case gateway/term_session_test.go
SELECT pg_backend_pid()
-- @case gateway/bizquery_blackbox_test.go
SELECT pg_catalog."pg_sleep"(0.01)
-- @case gateway/bizquery_blackbox_test.go
SELECT pg_catalog."pg_sleep"(2)
-- @case gateway/effectful_select_test.go
SELECT pg_reload_conf()
-- @case gateway/bizquery_blackbox_test.go
SELECT pg_size_pretty(1024::bigint), version(), current_database()
-- @case gateway/bizquery_blackbox_test.go
SELECT pg_size_pretty(sum(amount)::bigint) FROM orders
-- @case gateway/bizquery_test.go
SELECT pg_sleep(10)
-- @case gateway/bizquery_blackbox_test.go
SELECT pg_stat_get_backend_activity(1)
-- @case gateway/effectful_select_test.go
SELECT pg_terminate_backend(12345)
-- @case gateway/sensitive_error_test.go
SELECT phone + 0 FROM t_user
-- @case gateway/sensitive_perf_test.go
SELECT pwd$ AS p, pwd AS q, #tmp AS r FROM t
-- @case gateway/bizquery_forms_test.go
SELECT query_to_xml('select * from pg_authid', true, false, '')
-- @case gateway/bizquery_blackbox_test.go
SELECT rolname, rolpassword FROM pg_authid
-- @case gateway/bizquery_blackbox_test.go
SELECT rolname, rolpassword FROM pg_authid WHERE rolpassword IS NOT NULL
-- @case gateway/mask_evade_test.go
SELECT s.x FROM a JOIN ((SELECT id_card x FROM t_user)) s ON 1=1
-- @case gateway/mask_evade_test.go
SELECT s.x FROM a, (SELECT id_card x FROM t_user) s
-- @case gateway/mask_evade_test.go
SELECT s.x FROM a, LATERAL (SELECT id_card x FROM t_user) s
-- @case gateway/bizquery_forms_test.go
SELECT schema_to_xml('public', true, false, '')
-- @case gateway/bizquery_blackbox_test.go
SELECT session_user
-- @case gateway/bizquery_blackbox_test.go
SELECT session_user()
-- @case gateway/bizquery_regress_test.go
SELECT session_user, current_role FROM t
-- @case gateway/effectful_select_test.go
SELECT setval('seq_order', 1)
-- @case gateway/effectful_select_test.go
SELECT setval_count FROM t
-- @case gateway/bizquery_blackbox_test.go
SELECT sleep_minutes FROM orders
-- @case gateway/bizquery_regress_test.go
SELECT system_user FROM t
-- @case gateway/bizquery_blackbox_test.go
SELECT system_user()
-- @case gateway/bizquery_blackbox_test.go
SELECT table_name FROM information_schema.columns
-- @case gateway/bizquery_forms_test.go
SELECT table_to_xml('pg_authid', true, false, '')
-- @case gateway/bizquery_blackbox_test.go
SELECT to_char(created_at, 'YYYY-MM') FROM orders FOR_UPDATE_FLAG
-- @case gateway/bizquery_forms_test.go
SELECT to_tsvector('english', body) @@ to_tsquery('cat') FROM docs
-- @case gateway/bizquery_forms_test.go
SELECT ts_rewrite('a'::tsquery, 'select x, y from t')
-- @case gateway/bizquery_forms_test.go
SELECT ts_stat('select to_tsvector(pg_terminate_backend(1)::text)')
-- @case gateway/sensitive_selectstar_test.go
SELECT u.* FROM t_user u
-- @case gateway/sensitive_test.go
SELECT u.id_card AS x, u.name FROM t_user u
-- @case gateway/sensitive_test.go
SELECT u.id_card, o.amount FROM t_other o JOIN t_user u ON u.id = o.uid
-- @case gateway/bizquery_blackbox_test.go
SELECT usename, passwd FROM pg_shadow
-- @case gateway/bizquery_blackbox_test.go
SELECT user
-- @case gateway/bizquery_blackbox_test.go
SELECT user, user_name FROM orders WHERE user = 'a'
-- @case gateway/effectful_select_test.go
SELECT utl_http.request('http://x') FROM dual
-- @case gateway/bizquery_blackbox_test.go
SELECT version(), current_database()
-- @case gateway/readonly_guard_test.go
SELECT vz_grd_auto() FROM dual
-- @case gateway/readonly_guard_test.go
SELECT vz_guard_f()
-- @case gateway/bizquery_forms_test.go
SELECT word, ndoc FROM stats_ts_stat ORDER BY ndoc DESC
-- @case gateway/mask_evade_test.go
SELECT x FROM ((SELECT id_card x FROM t_user)) s
-- @case gateway/mask_evade_test.go
SELECT x FROM (SELECT `ID_CARD` AS x FROM t_user) s
-- @case gateway/mask_evade_test.go
SELECT x FROM (SELECT amount AS x FROM t_order) s
-- @case gateway/mask_evade_test.go
SELECT x FROM (SELECT id_card AS x FROM t_user) s
-- @case gateway/mask_evade_test.go
SELECT x FROM (SELECT id_card AS x FROM whatever) s
-- @case gateway/tableref_unicode_test.go
SELECT x FROM (SELECT id_card AS x FROM 用户表) s
-- @case gateway/mask_evade_test.go
SELECT x FROM (SELECT name AS x FROM t_user) s
-- @case gateway/mention_unicode_test.go
SELECT x FROM (SELECT 身份证 AS x FROM t_user) s
-- @case gateway/bizquery_forms_test.go
SELECT x.v FROM t, XMLTABLE('/r/v' PASSING t.doc COLUMNS v VARCHAR2(10) PATH '.') x
-- @case gateway/mask_evade_test.go
SELECT 星号
-- @case gateway/sensitive_perf_test.go
SELECT 身份证 AS a, 身份证号 AS b, x身份证 c FROM 用户
-- @case gateway/set_statement_test.go
SET /* c */ STATEMENT a=1 FOR /*!50000 DROP TABLE t */
-- @case gateway/session_scope_escape_test.go
SET @@ GLOBAL . read_only = 0
-- @case gateway/session_scope_escape_test.go
SET @@GLOBAL.read_only = 0
-- @case gateway/session_scope_escape_test.go
SET @@GLOBAL.super_read_only = 0
-- @case gateway/session_scope_escape_test.go
SET @@PERSIST.read_only = 0
-- @case gateway/session_scope_escape_test.go
SET @@SESSION.sort_buffer_size = 1048576
-- @case gateway/session_scope_escape_test.go
SET @@global.sql_log_bin = 0
-- @case gateway/session_scope_escape_test.go
SET @@persist_only.read_only = 0
-- @case gateway/window_scope_test.go
SET @@session.sql_mode = ''
-- @case gateway/session_scope_escape_test.go
SET @@sort_buffer_size = 1048576
-- @case gateway/set_statement_test.go
SET @a = 1
-- @case gateway/dialect_audit_test.go
SET @my_var = 1
-- @case gateway/session_scope_escape_test.go
SET @my_var = 42
-- @case gateway/exec_session_test.go
SET @aegisdb_sess = 42
-- @case gateway/term_session_test.go
SET @aegisdb_tab = 'A'
-- @case gateway/window_scope_test.go
SET @x = 1
-- @case gateway/session_scope_escape_test.go
SET @x = 1, @@GLOBAL.read_only = 0
-- @case gateway/dialect_audit_test.go
SET CHARACTER SET utf8mb4
-- @case gateway/dialect_audit_test.go
SET GLOBAL read_only = 0
-- @case gateway/set_statement_test.go
SET GLOBAL read_only=0
-- @case osc/binlog_mysql_test.go
SET GLOBAL time_zone = '+08:00'
-- @case osc/binlog_mysql_test.go
SET GLOBAL time_zone = 'SYSTEM'
-- @case gateway/session_scope_escape_test.go
SET LOCAL statement_timeout = '5s'
-- @case gateway/dialect_audit_test.go
SET LOCAL work_mem = '64MB'
-- @case gateway/dialect_audit_test.go
SET NAMES utf8mb4
-- @case gateway/readonly_guard_test.go
SET NAMES utf8mb4; SELECT 1
-- @case sqlutil/redact_test.go
SET PASSWORD = PASSWORD('fnsecret')
-- @case sqlutil/redact_test.go
SET PASSWORD FOR 'u'@'h' = 'newpass'
-- @case gateway/set_statement_test.go
SET PASSWORD FOR u='x'
-- @case gateway/dialect_audit_test.go
SET PERSIST max_connections = 1000
-- @case gateway/session_scope_escape_test.go
SET PERSIST read_only = 0
-- @case gateway/dialect_audit_test.go
SET PERSIST_ONLY max_connections = 1000
-- @case gateway/session_scope_escape_test.go
SET PERSIST_ONLY read_only = 0
-- @case gateway/set_statement_test.go
SET ROLE dba
-- @case gateway/dialect_audit_test.go
SET ROLE dba_admin
-- @case gateway/readonly_guard_test.go
SET SERVEROUTPUT ON
SELECT 1 FROM dual;
-- @case gateway/dialect_audit_test.go
SET SESSION AUTHORIZATION postgres
-- @case gateway/result_cache_test.go
SET SESSION cte_max_recursion_depth = 10000
-- @case gateway/bizquery_mariadb_test.go
SET SESSION max_statement_time = 0.5
-- @case gateway/session_scope_escape_test.go
SET SESSION sort_buffer_size = 1048576
-- @case gateway/session_scope_escape_test.go
SET SESSION sort_buffer_size=1, GLOBAL read_only=0
-- @case gateway/dialect_audit_test.go
SET SESSION sql_mode = 'STRICT_TRANS_TABLES'
-- @case gateway/set_statement_test.go
SET SESSION sql_mode=''
-- @case gateway/dialect_audit_test.go
SET SESSION statement_timeout = 60000
-- @case gateway/set_statement_test.go
SET STATEMENT a='\'' FOR DROP TABLE t #'
-- @case gateway/set_statement_test.go
SET STATEMENT a=(SELECT 1 FOR UPDATE) FOR SELECT 2
-- @case gateway/set_statement_test.go
SET STATEMENT a=1
-- @case gateway/set_statement_test.go
SET STATEMENT a=1 FOR DELETE FROM t
-- @case gateway/set_statement_test.go
SET STATEMENT a=1 FOR DELETE FROM t WHERE id=1
-- @case gateway/set_statement_test.go
SET STATEMENT a=1 FOR GRANT ALL ON *.* TO u
-- @case gateway/set_statement_test.go
SET STATEMENT a=1 FOR UPDATE t SET a=1 WHERE id=1
-- @case gateway/set_statement_test.go
SET STATEMENT max_statement_time=1 FOR DROP TABLE t
-- @case gateway/set_statement_test.go
SET STATEMENT max_statement_time=30 FOR SELECT * FROM t
-- @case gateway/set_statement_test.go
SET STATEMENT transaction_read_only=0 FOR SELECT f()
-- @case gateway/set_statement_test.go
SET STATEMENT tx_read_only=0 FOR SELECT f()
-- @case gateway/set_statement_test.go
SET STATEMENT … FOR 不是会话设置
-- @case gateway/dialect_audit_test.go
SET TIME ZONE 'Asia/Shanghai'
-- @case gateway/dialect_audit_test.go
SET TRANSACTION ISOLATION LEVEL READ COMMITTED
-- @case gateway/readonly_guard_test.go
SET search_path TO app; SELECT 1
-- @case gateway/dialect_audit_test.go
SET search_path TO dwd
-- @case gateway/dialect_audit_test.go
SET search_path TO dwd, public
-- @case gateway/set_statement_test.go
SET statement_timeout = 1000
-- @case gateway/set_statement_test.go
SET statement_timeout TO '5s'
-- @case gateway/explain_plan_test.go
SET v = 7
-- @case gateway/explain_plan_test.go
SET v = 9
-- @case gateway/explain_plan_test.go
SET v = 99 WHERE id = 2
-- @case gateway/bizquery_forms_test.go
SHOW ALL
-- @case gateway/bizquery_forms_test.go
SHOW BINARY LOGS
-- @case gateway/bizquery_forms_test.go
SHOW COLUMNS FROM information_schema.tables
-- @case gateway/bizquery_forms_test.go
SHOW COLUMNS FROM orders
-- @case gateway/bizquery_forms_test.go
SHOW COLUMNS FROM salary FROM finance_db
-- @case gateway/bizquery_forms_test.go
SHOW CREATE DATABASE finance_db
-- @case gateway/bizquery_forms_test.go
SHOW CREATE PROCEDURE p
-- @case gateway/risk_metadata_read_test.go
SHOW CREATE PROCEDURE p_drop_old
-- @case gateway/bizquery_forms_test.go
SHOW CREATE TABLE finance_db.salary
-- @case gateway/bizquery_show_mysql_test.go
SHOW CREATE TABLE nums
-- @case gateway/bizquery_forms_test.go
SHOW CREATE TABLE orders
-- @case gateway/bizquery_forms_test.go
SHOW CREATE TABLE orders_db.orders
-- @case gateway/risk_metadata_read_test.go
SHOW CREATE TABLE platform_event;
-- @case gateway/risk_metadata_read_test.go
SHOW CREATE TABLE t
-- @case gateway/sensitive_star_align_test.go
SHOW CREATE TABLE t_user
-- @case gateway/bizquery_forms_test.go
SHOW CREATE USER root
-- @case gateway/bizquery_forms_test.go
SHOW CREATE VIEW v_orders
-- @case gateway/bizquery_forms_test.go
SHOW DATABASES
-- @case gateway/bizquery_forms_test.go
SHOW ENGINE INNODB STATUS
-- @case gateway/bizquery_forms_test.go
SHOW FIELDS IN orders FROM orders_db
-- @case gateway/bizquery_show_mysql_test.go
SHOW FULL COLUMNS FROM nums
-- @case gateway/bizquery_forms_test.go
SHOW FULL PROCESSLIST
-- @case gateway/bizquery_forms_test.go
SHOW FULL TABLES
-- @case gateway/bizquery_forms_test.go
SHOW GLOBAL STATUS
-- @case gateway/bizquery_forms_test.go
SHOW GRANTS
-- @case gateway/risk_metadata_read_test.go
SHOW GRANTS FOR CURRENT_USER
-- @case gateway/bizquery_show_mysql_test.go
SHOW INDEX FROM nums
-- @case gateway/bizquery_forms_test.go
SHOW INDEX FROM orders
-- @case gateway/bizquery_forms_test.go
SHOW INDEX FROM user IN mysql
-- @case gateway/bizquery_forms_test.go
SHOW KEYS IN orders WHERE Key_name = 'PRIMARY'
-- @case gateway/bizquery_forms_test.go
SHOW PROCESSLIST
-- @case gateway/bizquery_forms_test.go
SHOW SLAVE STATUS
-- @case gateway/bizquery_forms_test.go
SHOW TABLE STATUS
-- @case gateway/bizquery_forms_test.go
SHOW TABLE STATUS FROM `finance_db`
-- @case gateway/bizquery_show_mysql_test.go
SHOW TABLE STATUS LIKE 'nums'
-- @case gateway/bizquery_forms_test.go
SHOW TABLE STATUS LIKE 'orders'
-- @case gateway/bizquery_forms_test.go
SHOW TABLES
-- @case gateway/risk_metadata_read_test.go
SHOW TABLES /*!40000 ; DROP TABLE t */
-- @case gateway/risk_metadata_read_test.go
SHOW TABLES /*T! ; DROP TABLE t */
-- @case gateway/audit_2026_11_b_test.go
SHOW TABLES FROM ORDERS
-- @case gateway/bizquery_forms_test.go
SHOW TABLES FROM finance_db
-- @case gateway/bizquery_forms_test.go
SHOW TABLES IN `ORDERS_DB`
-- @case gateway/bizquery_forms_test.go
SHOW TABLES IN mysql
-- @case gateway/risk_metadata_read_test.go
SHOW TABLES LIKE 'a\'; DROP TABLE t; -- '
-- @case gateway/bizquery_forms_test.go
SHOW TABLES WHERE Tables_in_orders_db IN (SELECT id FROM finance_db.salary)
-- @case gateway/bizquery_forms_test.go
SHOW TABLES; DROP TABLE orders
-- @case gateway/risk_metadata_read_test.go
SHOW TABLES; DROP TABLE t
-- @case gateway/client_commands_test.go
SHOW USER
-- @case gateway/bizquery_forms_test.go
SHOW search_path
-- @case gateway/window_scope_test.go
TRUNCATE TABLE B.t2
-- @case gateway/dictionary_literals_test.go
TRUNCATE TABLE audit_tmp
-- @case gateway/data_action_test.go
TRUNCATE TABLE orders
-- @case gateway/dialect_audit_test.go
TRUNCATE TABLE t
-- @case gateway/window_scope_test.go
TRUNCATE TABLE t1
-- @case sqlutil/refs_test.go
TRUNCATE TABLE t_order
-- @case osc/binlog_mysql_test.go
TRUNCATE orders
-- @case sqlutil/refs_test.go
TRUNCATE t_order
-- @case gateway/window_scope_test.go
UPDATE "B".t2 SET v = 1
-- @case gateway/window_scope_test.go
UPDATE "SCOTT".emp SET sal = 1
-- @case gateway/window_scope_test.go
UPDATE "scott".emp SET sal = 1
-- @case gateway/window_scope_test.go
UPDATE APP.t SET v = 1
-- @case gateway/window_scope_test.go
UPDATE B . t2 SET v = 1
-- @case gateway/window_scope_test.go
UPDATE B.t2 SET v = 1
-- @case gateway/window_scope_test.go
UPDATE ORDERS_DB.t SET v = 1
-- @case sqlutil/split_test.go
UPDATE `a;b` SET x=1
-- @case sqlutil/split_test.go
UPDATE `a;b` SET x=1; SELECT 2
-- @case sqlutil/split_test.go
UPDATE accounts SET balance=0
-- @case gateway/window_scope_test.go
UPDATE emp SET note = q'[hr.emp it's]' WHERE 1 = 1
-- @case gateway/window_scope_test.go
UPDATE emp SET sal = (SELECT 1 FROM hr . t)
-- @case gateway/window_scope_test.go
UPDATE emp SET sal = 1 WHERE empno = 7369
-- @case gateway/window_scope_test.go
UPDATE emp SET v = q'[it's]', w = (SELECT 1 FROM hr.t)
-- @case gateway/window_scope_test.go
UPDATE hr.emp SET sal = 1
-- @case gateway/window_scope_test.go
UPDATE hr.emp SET v = pk_util.f(1)
-- @case gateway/verb_classify_test.go
UPDATE nowhere_log SET x = 1
-- @case osc/binlog_mysql_test.go
UPDATE orders SET a = 1
-- @case gateway/bizquery_test.go
UPDATE orders SET amount = 0
-- @case osc/binlog_mysql_test.go
UPDATE orders SET amount = ? WHERE id = ?
-- @case osc/binlog_mysql_test.go
UPDATE orders SET amount = ?, name = ? WHERE id = ?
-- @case osc/binlog_mysql_test.go
UPDATE orders SET id = ? WHERE id = ?
-- @case gateway/risk_test.go
UPDATE orders SET s='x' WHERE 1=1
-- @case gateway/risk_test.go
UPDATE orders SET s='x' WHERE status='active'
-- @case sqlutil/redact_test.go
UPDATE orders SET status = 'paid' WHERE id = 7
-- @case gateway/window_scope_test.go
UPDATE orders SET status = 'x' WHERE id = 1
-- @case gateway/data_action_test.go
UPDATE orders SET x = 1
-- @case gateway/window_scope_test.go
UPDATE other_schema.t SET v = 1
-- @case gateway/window_scope_test.go
UPDATE scott.emp SET sal = 1
-- @case gateway/bizquery_test.go
UPDATE t SET a = 1
-- @case gateway/executor_timing_test.go
UPDATE t SET a = 1 WHERE id = 2
-- @case gateway/hash_comment_test.go
UPDATE t SET a = b # c WHERE id=1
-- @case gateway/hash_comment_test.go
UPDATE t SET a='#' WHERE id=1
-- @case gateway/hash_comment_test.go
UPDATE t SET a=1 # WHERE id=1
-- @case gateway/stripcomments_test.go
UPDATE t SET a=1 #c
 WHERE id=2
-- @case review/review_test.go
UPDATE t SET note = 'delete from t' WHERE id=1 LIMIT 1;
-- @case gateway/dictionary_literals_test.go
UPDATE t SET note = 'delete later' WHERE id = 1
-- @case gateway/hash_comment_test.go
UPDATE t SET note='#' WHERE id=1
-- @case gateway/window_scope_test.go
UPDATE t SET v = (SELECT COUNT(*) FROM TABLE(pk_util.list_ids))
-- @case gateway/window_scope_test.go
UPDATE t SET v = DBMS_RANDOM.VALUE(1, 10)
-- @case gateway/window_scope_test.go
UPDATE t SET v = other.f(1)
-- @case gateway/window_scope_test.go
UPDATE t SET v = pk_const.c_max WHERE id = 1
-- @case gateway/window_scope_test.go
UPDATE t SET v = pk_util.f(1) WHERE id = 2
-- @case gateway/verb_classify_test.go
UPDATE t SET x = 1
-- @case gateway/verb_classify_test.go
UPDATE t SET x = 1 WHERE id = 2
-- @case gateway/window_scope_test.go
UPDATE t1 /*!, B.t2 */ SET v = 1
-- @case gateway/window_scope_test.go
UPDATE t1 /*!50000 JOIN B.t2 x ON 1=1 */ SET v = 1
-- @case gateway/window_scope_test.go
UPDATE t1 /*T! JOIN B.t2 x ON 1=1 */ SET v = 1
-- @case gateway/window_scope_test.go
UPDATE t1 AS B JOIN B.t2 c ON 1 = 1 SET c.v = 1
-- @case gateway/window_scope_test.go
UPDATE t1 B JOIN t3 ON 1 = 1, B.t2 c SET c.v = 1
-- @case gateway/window_scope_test.go
UPDATE t1 JOIN t3 ON 1=1 --1
 JOIN B.t2 x SET x.v = 2
-- @case gateway/window_scope_test.go
UPDATE t1 SET v = 'a\'', w = (SELECT 1 FROM B.t2) WHERE 'x' = 'x'
-- @case gateway/window_scope_test.go
UPDATE t1 SET v = (SELECT v FROM B.t2 LIMIT 1)
-- @case gateway/window_scope_test.go
UPDATE t1 SET v = 1 # B.t2
 WHERE id = 1
-- @case gateway/window_scope_test.go
UPDATE t1 SET v = 1 # it's
, B.t2
-- @case gateway/window_scope_test.go
UPDATE t1 SET v = 1 /* B.t2 */ WHERE id = 1
-- @case gateway/window_scope_test.go
UPDATE t1 SET v = 1 WHERE B.t2.id = 1
-- @case gateway/window_scope_test.go
UPDATE t1 SET v = 1; USE B
-- @case gateway/window_scope_test.go
UPDATE t1 SET v = B.f(1)
-- @case gateway/window_scope_test.go
UPDATE t1 SET v = NOW(), w = CONCAT('a', 'b') -- B.t2 写在注释里
 WHERE id = 1
-- @case gateway/window_scope_test.go
UPDATE t1 a JOIN B.t2 b ON a.id = b.id SET b.v = 1
-- @case gateway/window_scope_test.go
UPDATE t1 a JOIN t2 b ON a.id = b.id SET b.v = a.v WHERE a.x = 'B.t2'
-- @case gateway/window_scope_test.go
UPDATE t1, B.t2 SET t2.v = 1
-- @case gateway/window_scope_test.go
UPDATE t1, t2 SET t1.v = t2.v WHERE t1.id = t2.id
-- @case gateway/realrun_routing_test.go
UPDATE t_demo SET name = 'c'
-- @case sqlutil/refs_test.go
UPDATE t_order SET n = 1 WHERE id = 2
-- @case gateway/sensitive_error_test.go
UPDATE t_user SET nme = 1 WHERE phone = '1'
-- @case gateway/risk_test.go
UPDATE users SET note = "where"
-- @case gateway/risk_test.go
UPDATE users SET note='... where id=1 ...'
-- @case gateway/risk_test.go
UPDATE users SET note='where'
-- @case gateway/risk_test.go
UPDATE users SET note='where' WHERE id=1
-- @case gateway/risk_test.go
UPDATE users SET note='x' WHERE id=1
-- @case gateway/window_scope_test.go
USE B
-- @case gateway/bizquery_test.go
USE finance_db
-- @case gateway/pg_tableddl_test.go
WITH (
-- @case gateway/pg_tableddl_test.go
WITH (orientation=column, compression=low)
-- @case gateway/export_cancel_test.go
WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x + 1 FROM c WHERE x < 500000000) SELECT x FROM c
-- @case gateway/realdb_timeout_test.go
WITH RECURSIVE cnt(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM cnt WHERE x < 100000)
-- @case gateway/realdb_timeout_test.go
WITH RECURSIVE cnt(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM cnt WHERE x < 5000000) SELECT count(*) FROM cnt
-- @case gateway/cte_verb_test.go
WITH RECURSIVE cnt(x) AS (VALUES(1)) SELECT x FROM cnt
-- @case gateway/cte_verb_test.go
WITH RECURSIVE g AS (SELECT 1 AS i UNION ALL SELECT i + 1 FROM g WHERE i < 3) SELECT INSERT('abcdef', 2, 3, 'XYZ'), TRUNCATE(1.2345, 2) FROM g
-- @case review/checks_spec_test.go
WITH RECURSIVE r AS (SELECT 1) SELECT * FROM r;
-- @case gateway/result_cache_test.go
WITH RECURSIVE s(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM s WHERE n < 2500) SELECT n FROM s
-- @case gateway/backslash_test.go
WITH a AS (SELECT 'x\'' AS c), d AS (DELETE FROM t RETURNING 1) SELECT ' ' FROM a
-- @case gateway/dialect_audit_test.go
WITH a AS (SELECT 1) TABLE a
-- @case gateway/sensitive_selectstar_test.go
WITH c AS (SELECT * FROM t_user) SELECT * FROM c
-- @case sqlutil/refs_test.go
WITH c AS (SELECT 1) SELECT * FROM c
-- @case gateway/bizquery_test.go
WITH d AS (DELETE FROM orders RETURNING *) SELECT * FROM d
-- @case gateway/cte_verb_test.go
WITH d AS (DELETE FROM orders RETURNING id) SELECT count(*) FROM d
-- @case gateway/dialect_audit_test.go
WITH d AS (DELETE FROM t RETURNING *) SELECT * FROM d
-- @case gateway/dictionary_literals_test.go
WITH d AS (DELETE FROM t RETURNING 1) SELECT * FROM d
-- @case gateway/risk_test.go
WITH d AS (DELETE FROM users RETURNING *) SELECT * FROM d
-- @case gateway/risk_test.go
WITH d AS (DELETE FROM users WHERE id=1 RETURNING *) SELECT * FROM d
-- @case gateway/risk_test.go
WITH d AS (SELECT * FROM users) SELECT * FROM d
-- @case gateway/risk_test.go
WITH i AS (INSERT INTO t SELECT * FROM s RETURNING *) SELECT count(*) FROM i
-- @case gateway/dialect_audit_test.go
WITH i AS (INSERT INTO t VALUES (1) RETURNING *) VALUES (1)
-- @case gateway/cte_verb_test.go
WITH p AS (SELECT REPLACE(partition_description, '''', '') AS d FROM information_schema.partitions) SELECT d FROM p
-- @case review/checks_spec_test.go
WITH r AS (SELECT 1 AS n) SELECT n FROM r;
-- @case gateway/mask_evade_test.go
WITH s AS (SELECT id_card AS x FROM t_user) SELECT x FROM s
-- @case gateway/cte_verb_test.go
WITH t AS (
-- @case gateway/cte_verb_test.go
WITH t AS (SELECT 'delete me' AS note FROM users) SELECT note FROM t
-- @case gateway/cte_verb_test.go
WITH t AS (SELECT 1 AS id) INSERT INTO orders SELECT id FROM t
-- @case gateway/bizquery_blackbox_test.go
WITH t AS (SELECT 1 AS id) SELECT * FROM t
-- @case gateway/cte_verb_test.go
WITH t AS (SELECT 1) INSERT INTO orders SELECT * FROM t
-- @case gateway/cte_verb_test.go
WITH t AS (SELECT 1) REPLACE INTO orders SELECT * FROM t
-- @case gateway/cte_verb_test.go
WITH t AS (SELECT 1) UPDATE orders SET x=1
-- @case gateway/cte_verb_test.go
WITH t AS (SELECT REPLACE(code, 'a', 'b') AS c FROM src) DELETE FROM orders WHERE 1=0
-- @case gateway/bizquery_blackbox_test.go
WITH t AS (SELECT id FROM orders) SELECT * FROM t
-- @case gateway/cte_verb_test.go
WITH t AS (SELECT id FROM users) SELECT * FROM t
-- @case gateway/mask_evade_test.go
WITH t AS (SELECT id, amount FROM whatever) SELECT amount FROM t
-- @case gateway/cte_verb_test.go
WITH t AS (SELECT replace (name, 'a', 'b') AS n FROM users) SELECT n FROM t
-- @case gateway/bizquery_blackbox_test.go
WITH t AS (SELECT uid, SUM(amount) s FROM orders GROUP BY uid) SELECT * FROM t ORDER BY s DESC
-- @case gateway/mask_evade_test.go
WITH u AS (SELECT id, name FROM t_user) SELECT name FROM u
-- @case gateway/mask_evade_test.go
WITH u AS (SELECT name FROM t_user) SELECT name FROM u
-- @case gateway/mention_unicode_test.go
WITH u AS (SELECT 姓名 FROM t_user) SELECT 姓名 FROM u
-- @case gateway/dialect_audit_test.go
WITH u AS (UPDATE t SET x=1 RETURNING *) TABLE u
-- @case gateway/risk_test.go
WITH u AS (UPDATE users SET tier='x' RETURNING id) SELECT * FROM u
-- @case gateway/window_scope_test.go
WITH x AS (SELECT 1 AS id) UPDATE B.t2 JOIN x USING(id) SET v = 1
-- @case gateway/window_scope_test.go
WITH x AS (SELECT 1 AS id) UPDATE t2 JOIN x USING(id) SET t2.v = x.id
-- @case gateway/bizquery_test.go
WITH x AS (SELECT 1) SELECT * FROM x
-- @case gateway/bizquery_test.go
WITH x AS (SELECT 1) SELECT * FROM x
LIMIT 1001
-- @case gateway/sensitive_error_test.go
WITH x AS (SELECT phone FROM t_user) SELECT phone + 0 FROM x
-- @case gateway/pg_tableddl_test.go
WITH 子句
-- @case gateway/window_scope_test.go
alter table T_BINGO_LUCKYNUM_DIY_LOG add constraint PK_BINGO_LUCKYNUM_DIY_LOG primary key (ID)
-- @case osc/parse_test.go
alter table `app`.`orders` add key `idx_x` (`a`, b(20))
-- @case osc/binlog_mysql_test.go
alter table `app`.`orders` drop index i
-- @case gateway/window_scope_test.go
create index I_LUCKYNUM_DIY_LV on T_BINGO_LUCKYNUM_DIY_LOG (LOGINNAME, VID, CREATETIME) tablespace ts_g04_idx01
-- @case gateway/window_scope_test.go
create table T_BINGO_LUCKYNUM_DIY_LOG ( id NUMBER(16) not null, createtime DATE default sysdate not null )
-- @case gateway/data_action_test.go
create table if not exists t_x (id integer); insert into t_x values (1); drop table t_x
-- @case gateway/data_action_test.go
create table t (id int)
-- @case gateway/dictionary_literals_test.go
delete from orders where id = 1
-- @case gateway/bizquery_forms_test.go
desc App.Orders;
-- @case gateway/bizquery_forms_test.go
desc app . orders
-- @case gateway/bizquery_forms_test.go
describe APP.orders;
-- @case gateway/explain_plan_test.go
describe DELETE FROM t WHERE id = 1
-- @case gateway/risk_test.go
drop table x
-- @case gateway/explain_plan_test.go
explain analyze (SELECT schema_name FROM information_schema.schemata LIMIT 5) UNION (SELECT table_name FROM information_schema.tables LIMIT 5) LIMIT 10
-- @case gateway/explain_plan_test.go
explain analyze format = 'brief' delete from t
-- @case gateway/explain_plan_test.go
explain analyze select /*+ USE_INDEX(t, idx_a) */ id from t where a = 1 limit 100
-- @case gateway/explain_test.go
explain analyze update t set x = 1
-- @case gateway/explain_plan_test.go
explain format = 'brief' delete from t
-- @case gateway/bizquery_explain_test.go
explain plan for
  select o.id from orders o join items i on i.oid = o.id;
-- @case gateway/sensitive_star_align_test.go
explain select count(*) from ( select mt.id, mt.PAYMENT_BANK_ACCOUNT_NO, tpt.BANK_ACCOUNT_NO MOBILE_NO from t_merchant_tran mt left join t_product_tran tpt ON tpt.BILL_NO = mt.bill_no ) t
-- @case gateway/explain_test.go
explain update t set x = 1
-- @case sqlutil/redact_test.go
grant identified by
-- @case sqlutil/refs_test.go
insert into app.t_order values (1)
-- @case gateway/writes_file_test.go
select '<?php ?>' into dumpfile '/var/www/x.php'
-- @case gateway/verb_classify_test.go
select * from t
-- @case gateway/bizquery_test.go
select * from t
limit
  1001
-- @case gateway/bizquery_test.go
select * from t
limit
  2000
-- @case gateway/mask_evade_test.go
select X from ( Select Id_Card as X From T_User ) s
-- @case gateway/sensitive_test.go
select id_card from t_user
-- @case gateway/effectful_select_test.go
select pg_catalog.pg_cancel_backend(pid) from pg_stat_activity
-- @case sqlutil/redact_test.go
set password
-- @case gateway/set_statement_test.go
set statement a=1, b='FOR' for drop table t
-- @case gateway/risk_metadata_read_test.go
show create table `event_platform`.`platform_event`
-- @case gateway/bizquery_forms_test.go
show create table `orders`;
-- @case gateway/cte_verb_test.go
with monthly as (
 select 1
)
select * from monthly
