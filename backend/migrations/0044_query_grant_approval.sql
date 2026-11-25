-- 0044: 查询授权的审批单历史 —— 让 externalId 真正幂等。
--
-- tbl_query_grant 上只存当前那张审批单号,认不出"已被取代的旧单又发过来"。这张表记下每个
-- 外部系统(client_id)的每张审批单(external_id)作用过哪条授权(grant_id),下发时据此区分:
-- 同一张单重发 = 幂等不改;新单 = 更新;旧单 = 拒绝(见 service.UpsertQueryGrant)。
--
-- 已有的授权在这里没有历史:它们当前的单号会在下一次下发时补记,之前更早的单号本就无从知道。
--
-- 只有一条 CREATE TABLE IF NOT EXISTS,可以安全重跑(ADR 0016)。
CREATE TABLE IF NOT EXISTS tbl_query_grant_approval (
  id          BIGINT       NOT NULL AUTO_INCREMENT,
  client_id   BIGINT       NOT NULL,
  external_id VARCHAR(128) NOT NULL,
  grant_id    BIGINT       NOT NULL,
  created_at  DATETIME(3)  NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_query_grant_approval (client_id, external_id, grant_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
