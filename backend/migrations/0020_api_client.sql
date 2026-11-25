-- 0020: 开放接口 —— 外部系统提交 SQL 升级单.
--
-- ---------------------------------------------------------------- API 客户端
--
-- 一个外部系统一把凭据(key + secret),而不是全局共享一把。三个理由:
--
--   1. 发布单需要一个**主体**。能力矩阵按角色判、标签范围按用户判、PROD 的 MFA
--      策略按用户判、审计链按 actor 记账 —— 没有主体的执行通道,是一条没有任何一层
--      能判定的通道。所以每个客户端绑定一个服务账号(user_id),它的角色决定了这个
--      外部系统能发布到哪些实例、什么变更要走审批。
--   2. 可单独吊销。共享密钥泄露只能整体轮换,连带打断所有正在对接的系统。
--   3. 可归属。审计行记 actor=服务账号、operator=API:<客户端名>,回答"哪个系统提的"。
--
-- secret_hash 是 bcrypt:明文只在创建时回显一次。一张能被读出明文的 API 密钥表,
-- 等价于一张生产写权限清单。
--
-- allow_ips 为空表示不限来源(仅凭密钥),这是**故意**的默认值 —— 创建凭据时往往还
-- 不知道 CI runner 的出口地址;要收紧就在控制台填 CIDR。
CREATE TABLE IF NOT EXISTS tbl_api_client (
  id           BIGINT       NOT NULL AUTO_INCREMENT,
  name         VARCHAR(64)  NOT NULL,
  `key`        VARCHAR(64)  NOT NULL,
  secret_hash  VARCHAR(255) NOT NULL,
  user_id      BIGINT       NOT NULL,
  user_name    VARCHAR(64)      NULL,
  allow_ips    VARCHAR(512)     NULL,
  scopes       VARCHAR(255) NOT NULL,
  enabled      TINYINT(1)   NOT NULL DEFAULT 1,
  last_used_at DATETIME(3)      NULL,
  created_by   BIGINT       NOT NULL DEFAULT 0,
  created_at   DATETIME(3)      NULL,
  PRIMARY KEY (id),
  UNIQUE KEY idx_apiclient_key (`key`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- ---------------------------------------------------------------- 发布单来源
--
-- source/client_id/client_name 记录这单是从哪扇门进来的、由哪个外部系统提的。
-- external_ref 是对方自己的单号(变更号 / 流水线构建号),两边对得上账。
--
-- idem_key = "<client_id>:<external_ref>",存在的唯一目的是挂 UNIQUE 索引:外部系统
-- 会重试,而一次重试如果生成第二张发布单,就是同一个变更被执行两次。它允许 NULL 且
-- 只有 API 单会写值 —— NULL 在唯一索引里互不冲突,空串会,否则每一张控制台发布单都
-- 会被判成前一张的重复。用数据库约束而不是"先查再插",因为并发的两次重试可以同时
-- 通过检查。
ALTER TABLE tbl_release ADD COLUMN source VARCHAR(16) NOT NULL DEFAULT 'console';
ALTER TABLE tbl_release ADD COLUMN client_id BIGINT NOT NULL DEFAULT 0;
ALTER TABLE tbl_release ADD COLUMN client_name VARCHAR(64) NULL;
ALTER TABLE tbl_release ADD COLUMN external_ref VARCHAR(128) NULL;
ALTER TABLE tbl_release ADD COLUMN idem_key VARCHAR(160) NULL;
ALTER TABLE tbl_release ADD INDEX idx_release_extref (external_ref);
ALTER TABLE tbl_release ADD UNIQUE INDEX uk_release_idem (idem_key);
