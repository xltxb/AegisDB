-- 0047: 数据源的从库(只读副本)。
--
-- 配了从库的数据源,业务方的数据查询(执行、导出、库表下拉)都由从库提供;终端、审批执行、
-- 发布仍走主库。端口 / 账号 / 密码留空沿用主库(密码与主库一样 AES 加密存储)。
-- 从库连不上时数据查询回落主库,结果与审计里注明 —— 见 service/bizquery_replica.go。
--
-- 老行全部为空 = 没有从库,行为不变。
-- 一条 ALTER 加四列:MySQL 上一条 DDL 要么全成要么全不成,失败的迁移整文件重跑不会撞上
-- "列已存在"(ADR 0016)。
ALTER TABLE tbl_connection
  ADD COLUMN replica_host     VARCHAR(128) NOT NULL DEFAULT '',
  ADD COLUMN replica_port     INT          NOT NULL DEFAULT 0,
  ADD COLUMN replica_username VARCHAR(64)  NOT NULL DEFAULT '',
  ADD COLUMN replica_password VARCHAR(255) NOT NULL DEFAULT '';
