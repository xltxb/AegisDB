-- 0043: 业务方数据查询 —— 业务查询源标记 + 查询授权。
--
-- 编号跳过 0042:0042 留给 SRD 授权登录(feature/sso-srd 分支上的 tbl_user SSO 绑定列)。
-- 两个分支先后合入 main 时不会撞号。
--
-- biz_query:只有标记为业务查询源的实例能被授权引用。约定它指向从库、用只读库账号,
-- 主库永远不会出现在授权候选里(见 .scratch/biz-data-query/PRD.md 第 4.1 节)。
--
-- tbl_query_grant:某人,在某段时间内,可以只读查询某个业务查询源上的某个库。
-- 一个人对一个库只有一行(uk_query_grant),再次下发即更新。授权由外部审批系统经开放
-- 接口下发,来源(client_id / client_name / external_id / approver)一并记下。
--
-- 迁移中途失败会从第一条重跑(见 ADR 0016),所以建表用 IF NOT EXISTS;ALTER 这一条
-- 放在最后,它之前的语句都可以安全重跑。
CREATE TABLE IF NOT EXISTS tbl_query_grant (
  id            BIGINT       NOT NULL AUTO_INCREMENT,
  user_id       BIGINT       NOT NULL,
  connection_id BIGINT       NOT NULL,
  db_name       VARCHAR(128) NOT NULL,
  max_rows      INT          NOT NULL DEFAULT 0,
  timeout_sec   INT          NOT NULL DEFAULT 0,
  allow_export  TINYINT(1)   NOT NULL DEFAULT 0,
  starts_at     DATETIME(3)  NOT NULL,
  ends_at       DATETIME(3)  NOT NULL,
  status        VARCHAR(16)  NOT NULL DEFAULT 'active',
  client_id     BIGINT       NOT NULL DEFAULT 0,
  client_name   VARCHAR(64)  NOT NULL DEFAULT '',
  external_id   VARCHAR(128) NOT NULL DEFAULT '',
  approver      VARCHAR(128) NOT NULL DEFAULT '',
  reason        VARCHAR(512) NOT NULL DEFAULT '',
  revoked_at    DATETIME(3)  NULL,
  revoked_by    VARCHAR(128) NOT NULL DEFAULT '',
  created_at    DATETIME(3)  NULL,
  updated_at    DATETIME(3)  NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_query_grant (user_id, connection_id, db_name),
  KEY idx_query_grant_ends (ends_at),
  KEY idx_query_grant_ext (external_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

ALTER TABLE tbl_connection ADD COLUMN biz_query TINYINT(1) NOT NULL DEFAULT 0;
