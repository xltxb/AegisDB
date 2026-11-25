-- 0065: 聊天审批卡片(Lark / Telegram)——.scratch/chat-approval-cards/PRD.md §5.8。
--
-- tbl_chat_binding:网关账号 ↔ 聊天账号。每个渠道一个用户最多一个绑定,一个聊天账号最多绑一个用户;
-- bot_id 是绑定时的机器人 / 应用标识,换了机器人旧绑定即失效。
-- tbl_chat_bind_code:一次性绑定码,只存摘要。一个用户每个渠道一行:重新生成就是改写它,旧码随之作废。
-- tbl_chat_card:要发给一个人的一张卡片,先落库、后台发;按钮令牌只存摘要。
-- idx_chat_card_state 给每 5 秒一次的扫描用,免得表长大后扫全部已收场的卡片。
--
-- tbl_approval.decided_via:决定来自哪个渠道(console / api / lark / telegram);没有人做决定的
-- (超时、撤回、作废)与这一列之前的老单为空。reject_reason:拒绝原因。
--
-- CREATE TABLE IF NOT EXISTS 与单条 ALTER 都可以安全重跑(ADR 0016)。只增不删:回滚后新表闲置无害。
CREATE TABLE IF NOT EXISTS tbl_chat_binding (
  id         BIGINT      NOT NULL AUTO_INCREMENT,
  user_id    BIGINT      NOT NULL,
  channel    VARCHAR(16) NOT NULL,
  bot_id     VARCHAR(64) NOT NULL,
  account_id VARCHAR(64) NOT NULL,
  chat_id    VARCHAR(64) NOT NULL DEFAULT '',
  created_at DATETIME(3) NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_chat_binding_user (channel, user_id),
  UNIQUE KEY uk_chat_binding_account (channel, bot_id, account_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS tbl_chat_bind_code (
  id         BIGINT      NOT NULL AUTO_INCREMENT,
  user_id    BIGINT      NOT NULL,
  channel    VARCHAR(16) NOT NULL,
  code_hash  VARCHAR(64) NOT NULL,
  expires_at DATETIME(3) NOT NULL,
  used_at    DATETIME(3) NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_chat_bind_code (code_hash),
  UNIQUE KEY uk_chat_bind_code_user (channel, user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS tbl_chat_card (
  id               BIGINT       NOT NULL AUTO_INCREMENT,
  approval_id      BIGINT       NOT NULL,
  user_id          BIGINT       NOT NULL,
  channel          VARCHAR(16)  NOT NULL,
  bot_id           VARCHAR(64)  NOT NULL DEFAULT '',
  state            VARCHAR(16)  NOT NULL DEFAULT 'queued',
  chat_id          VARCHAR(64)  NOT NULL DEFAULT '',
  message_id       VARCHAR(128) NOT NULL DEFAULT '',
  token_hash       VARCHAR(64)  NOT NULL DEFAULT '',
  partial          TINYINT(1)   NOT NULL DEFAULT 0,
  source_truncated TINYINT(1)   NOT NULL DEFAULT 0,
  attempts         INT          NOT NULL DEFAULT 0,
  next_at          DATETIME(3)  NOT NULL,
  last_error       VARCHAR(255) NOT NULL DEFAULT '',
  created_at       DATETIME(3)  NULL,
  updated_at       DATETIME(3)  NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_chat_card (approval_id, user_id, channel),
  KEY idx_chat_card_token (token_hash),
  KEY idx_chat_card_state (state, next_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

ALTER TABLE tbl_approval
  ADD COLUMN decided_via   VARCHAR(16)  NOT NULL DEFAULT '',
  ADD COLUMN reject_reason VARCHAR(512) NOT NULL DEFAULT '';
