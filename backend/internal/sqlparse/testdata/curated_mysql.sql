-- 手写样例:MySQL / MariaDB / PolarDB。
-- @case 同名函数不算写
SELECT REPLACE(a,'x','y') FROM t WHERE id=1
-- @case CTE 同名函数不算写
WITH c AS (SELECT REPLACE(a,'x','y') FROM t) SELECT * FROM c
-- @case 括号 UNION
(SELECT 1) UNION (SELECT 2)
-- @case 括号 UNION 的 EXPLAIN ANALYZE
EXPLAIN ANALYZE (SELECT 1) UNION (SELECT 2)
-- @case 普通 CTE
WITH c AS (SELECT 1) SELECT * FROM c
-- @case 索引加 ALGORITHM
ALTER TABLE t ADD INDEX i (a,b) COMMENT 'x', ALGORITHM=INPLACE, LOCK=NONE
-- @case 普通加索引
ALTER TABLE t ADD INDEX idx_pbc_id_status (pbc_id, status) COMMENT 'x'
-- @case RENAME 表
RENAME TABLE a TO b
-- @case ALTER RENAME 带加列
ALTER TABLE t RENAME TO u, ADD COLUMN c INT
-- @case 减负一不是注释
SELECT 1--1 INTO OUTFILE '/tmp/p'
-- @case 井号注释藏 OUTFILE
SELECT 1 # c
INTO OUTFILE '/tmp/p'
-- @case 井号注释藏 WHERE
DELETE FROM t # WHERE id=1
-- @case 字面量里的井号
UPDATE t SET note='it''s #1' WHERE id=1
-- @case SET STATEMENT
SET STATEMENT max_statement_time=1 FOR DELETE FROM t
-- @case 可执行注释
/*!50000 DROP TABLE t */
-- @case 可执行注释藏 OUTFILE
SELECT 1 /*!50000 INTO OUTFILE '/tmp/p' */
-- @case 存储过程
CREATE PROCEDURE p() BEGIN SELECT 1; END
-- @case 触发器
CREATE TRIGGER tr BEFORE INSERT ON t FOR EACH ROW SET NEW.a=1
-- @case EXPLAIN ANALYZE
EXPLAIN ANALYZE SELECT 1
-- @case EXPLAIN DELETE
EXPLAIN DELETE FROM t
-- @case SHOW CREATE
SHOW CREATE TABLE t
-- @case DESC
DESC t
-- @case LOAD DATA
LOAD DATA INFILE '/x' INTO TABLE t
-- @case 恒真 WHERE
DELETE FROM t WHERE 1=1
-- @case 多表删除
DELETE a FROM a JOIN b ON a.id=b.id
-- @case 多表更新带 WHERE
UPDATE a JOIN b ON a.id=b.id SET a.x=1 WHERE b.y=2
-- @case 字面量里的 WHERE
UPDATE t SET a='WHERE x'
-- @case 列名叫 where 的子串
UPDATE t SET elsewhere=1
-- @case 反引号关键字
SELECT `select` FROM `from`
-- @case 中文标识符
SELECT `姓名` FROM `用户表` WHERE `编号`=1
-- @case 分区表
CREATE TABLE t (id INT, d DATE) PARTITION BY RANGE (YEAR(d)) (PARTITION p0 VALUES LESS THAN (2000))
-- @case FOR UPDATE
SELECT * FROM t WHERE id=1 FOR UPDATE
-- @case INTO 变量
SELECT a INTO @x FROM t
-- @case 只读函数
SELECT SLEEP(5)
-- @case 读文件函数
SELECT LOAD_FILE('/etc/passwd')
-- @case MariaDB 动态 SQL
EXECUTE IMMEDIATE 'DROP TABLE t'
