-- 手写样例:DWS。第一批还没有解析器,这份给工单 05 用,语法按文档和经验写,没在真实实例上验证。
-- @case 普通查询
SELECT * FROM t WHERE a=1 LIMIT 5
-- @case UPDATE FROM
UPDATE t SET a=1 FROM s WHERE t.id=s.id
-- @case 列存哈希分布
CREATE TABLE t (id int) WITH (orientation=column, compression=middle) DISTRIBUTE BY HASH (id)
-- @case 轮询分布
CREATE TABLE t (id int) DISTRIBUTE BY ROUNDROBIN
-- @case 复制分布
CREATE TABLE t (id int) DISTRIBUTE BY REPLICATION
-- @case 改分布列
ALTER TABLE t DISTRIBUTE BY HASH (id)
-- @case 范围分区
CREATE TABLE t (id int) PARTITION BY RANGE (id) (PARTITION p1 VALUES LESS THAN (10))
-- @case 加分区
ALTER TABLE t ADD PARTITION p2 VALUES LESS THAN (20)
-- @case 存储过程
CREATE OR REPLACE PROCEDURE p() AS BEGIN NULL; END;
-- @case 外表
CREATE FOREIGN TABLE f (a int) SERVER gsmpp_server OPTIONS (location 'obs://b/x', format 'orc') DISTRIBUTE BY ROUNDROBIN
-- @case 兼容函数
SELECT nvl(a,1), sysdate FROM t
-- @case VACUUM
VACUUM FULL t
