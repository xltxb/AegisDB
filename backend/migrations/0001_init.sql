-- AegisDB — canonical schema (MySQL 8 / InnoDB / utf8mb4).
-- Naming: tbl_ prefix, pk_ primary keys, idx_ indexes. No physical FKs —
-- relational integrity is maintained at the application layer (backend doc §9).
-- This is the AUTHORITATIVE production schema, applied via `vela-gateway migrate`
-- and tracked in schema_migrations (see internal/bootstrap/migrate.go). GORM
-- AutoMigrate is used only for the SQLite dev/test path, not production.

CREATE DATABASE IF NOT EXISTS vela_gateway DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
USE vela_gateway;

CREATE TABLE IF NOT EXISTS tbl_role (
  id                BIGINT       NOT NULL AUTO_INCREMENT,
  code              VARCHAR(64)  NOT NULL,
  name              VARCHAR(64)  NOT NULL,
  layer             VARCHAR(32)  NOT NULL,
  description       VARCHAR(255),
  default_conn_role VARCHAR(64),
  can_approve       TINYINT(1)   NOT NULL DEFAULT 0,
  icon              VARCHAR(32),
  created_at        DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  CONSTRAINT pk_tbl_role PRIMARY KEY (id),
  UNIQUE KEY idx_role_code (code)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS tbl_user (
  id            BIGINT       NOT NULL AUTO_INCREMENT,
  name          VARCHAR(64)  NOT NULL,
  email         VARCHAR(128) NOT NULL,
  role_id       BIGINT       NOT NULL,
  status        VARCHAR(16)  NOT NULL DEFAULT 'active',  -- active|disabled|invited
  mfa_enabled   TINYINT(1)   NOT NULL DEFAULT 0,
  mfa_secret    VARCHAR(64),                        -- base32 TOTP secret (RFC 6238)
  mfa_last_ctr  BIGINT       NOT NULL DEFAULT 0,    -- last consumed TOTP counter (anti-replay)
  token_version BIGINT       NOT NULL DEFAULT 0,    -- session generation; bumped to revoke tokens
  password_hash VARCHAR(255),
  dept          VARCHAR(64),
  initials      VARCHAR(8),
  last_active   VARCHAR(32),
  created_at    DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  CONSTRAINT pk_tbl_user PRIMARY KEY (id),
  UNIQUE KEY idx_user_email (email),
  KEY idx_user_role (role_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS tbl_role_menu (
  role_id  BIGINT      NOT NULL,
  menu_key VARCHAR(32) NOT NULL,                  -- terminal|approve|db|rules|perms|audit|settings
  enabled  TINYINT(1)  NOT NULL DEFAULT 0,
  CONSTRAINT pk_tbl_role_menu PRIMARY KEY (role_id, menu_key)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS tbl_role_member (
  role_id BIGINT NOT NULL,
  user_id BIGINT NOT NULL,
  CONSTRAINT pk_tbl_role_member PRIMARY KEY (role_id, user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS tbl_role_tag (
  role_id BIGINT      NOT NULL,
  tag     VARCHAR(64) NOT NULL,                    -- grants access to connections carrying this tag
  CONSTRAINT pk_tbl_role_tag PRIMARY KEY (role_id, tag)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS tbl_role_capability (
  role_id    BIGINT      NOT NULL,
  capability VARCHAR(32) NOT NULL,                -- select|write|ddl|grant|conn|approve
  env        VARCHAR(16) NOT NULL,                -- prod|staging|dev
  level      VARCHAR(16) NOT NULL,                -- allow|approve|deny
  CONSTRAINT pk_tbl_role_capability PRIMARY KEY (role_id, capability, env)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS tbl_connection (
  id           BIGINT       NOT NULL AUTO_INCREMENT,
  name         VARCHAR(64)  NOT NULL,
  engine       VARCHAR(32)  NOT NULL,             -- mysql/postgres/clickhouse/redis/tidb
  host         VARCHAR(128) NOT NULL,
  port         INT          NOT NULL,
  env          VARCHAR(16)  NOT NULL,             -- prod|staging|dev
  policy       VARCHAR(32)  NOT NULL,             -- strict|approve-1|audit-only
  default_role VARCHAR(64),
  layer        VARCHAR(64),
  username     VARCHAR(64),                        -- real-execution credentials
  password     VARCHAR(255),
  db_name      VARCHAR(128),                       -- default schema / sqlite file
  tags         VARCHAR(255),                       -- comma-separated labels for group access
  status       VARCHAR(16)  NOT NULL DEFAULT 'online',  -- online|maint
  created_at   DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  CONSTRAINT pk_tbl_connection PRIMARY KEY (id),
  UNIQUE KEY idx_connection_name (name),
  KEY idx_connection_env (env)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS tbl_risk_command (
  command VARCHAR(32) NOT NULL,                   -- DROP/TRUNCATE/...
  env     VARCHAR(16) NOT NULL,                   -- prod|staging|dev
  level   VARCHAR(16) NOT NULL DEFAULT 'high',    -- high|mid|off
  CONSTRAINT pk_tbl_risk_command PRIMARY KEY (command, env)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS tbl_approval (
  id            BIGINT       NOT NULL AUTO_INCREMENT,
  ap_no         VARCHAR(32)  NOT NULL,
  connection_id BIGINT       NOT NULL,
  env           VARCHAR(16)  NOT NULL,
  instance      VARCHAR(64)  NOT NULL,
  command       TEXT         NOT NULL,
  keyword       VARCHAR(32),
  db_name       VARCHAR(128),                            -- selected target database
  initiator_id  BIGINT       NOT NULL,
  initiator     VARCHAR(64),
  reason        VARCHAR(512),
  risk_level    VARCHAR(16)  NOT NULL,            -- high|mid|low
  status        VARCHAR(16)  NOT NULL DEFAULT 'pending', -- pending|approved|rejected|expired
  audit_id      VARCHAR(32),
  result        TEXT,                                    -- execution output once approved
  result_rows   INT,
  escalated     TINYINT(1)   NOT NULL DEFAULT 0,          -- timeout escalation fired once
  decided_at    DATETIME(3),                             -- when approved/rejected
  created_at    DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  CONSTRAINT pk_tbl_approval PRIMARY KEY (id),
  UNIQUE KEY idx_approval_apno (ap_no),
  KEY idx_approval_status (status),
  KEY idx_approval_initiator (initiator_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS tbl_approval_step (
  id          BIGINT      NOT NULL AUTO_INCREMENT,
  approval_id BIGINT      NOT NULL,
  step_order  INT         NOT NULL,
  approver_id BIGINT      NOT NULL,
  approver    VARCHAR(64),
  status      VARCHAR(16) NOT NULL DEFAULT 'waiting', -- waiting|active|approved|rejected
  acted_at    DATETIME(3),
  CONSTRAINT pk_tbl_approval_step PRIMARY KEY (id),
  KEY idx_step_approval (approval_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS tbl_audit_log (
  id            BIGINT       NOT NULL AUTO_INCREMENT,
  occurred_at   DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  actor_id      BIGINT       NOT NULL,
  actor_name    VARCHAR(64),
  connection_id BIGINT,
  instance      VARCHAR(64),
  command       TEXT         NOT NULL,
  risk          VARCHAR(16)  NOT NULL,            -- high|mid|low
  result        VARCHAR(16)  NOT NULL,            -- executed|pending|rejected|warn
  approval_no   VARCHAR(32),
  prev_hash     CHAR(64),
  hash          CHAR(64)     NOT NULL,            -- SHA256(prev_hash + payload) chain
  CONSTRAINT pk_tbl_audit_log PRIMARY KEY (id),
  KEY idx_audit_time (occurred_at),
  KEY idx_audit_actor (actor_id),
  KEY idx_audit_risk (risk)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS tbl_webhook_config (
  id        BIGINT       NOT NULL AUTO_INCREMENT,
  endpoint  VARCHAR(255) NOT NULL,
  secret    VARCHAR(128) NOT NULL,
  events    VARCHAR(255) NOT NULL,                -- intercept,approve,exec,login
  retry_max INT          NOT NULL DEFAULT 5,
  enabled   TINYINT(1)   NOT NULL DEFAULT 1,
  CONSTRAINT pk_tbl_webhook_config PRIMARY KEY (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS tbl_setting (
  k VARCHAR(64) NOT NULL,
  v TEXT        NOT NULL,
  CONSTRAINT pk_tbl_setting PRIMARY KEY (k)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS tbl_webhook_delivery (
  id         BIGINT       NOT NULL AUTO_INCREMENT,
  event      VARCHAR(32)  NOT NULL,                -- intercept|approve|exec|login|test
  endpoint   VARCHAR(255),
  success    TINYINT(1)   NOT NULL,
  status     VARCHAR(128),                         -- "200 OK" or error text
  attempts   INT          NOT NULL,
  created_at DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  CONSTRAINT pk_tbl_webhook_delivery PRIMARY KEY (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS tbl_schema_object (
  id            BIGINT      NOT NULL AUTO_INCREMENT,
  connection_id BIGINT      NOT NULL,
  `database`    VARCHAR(64) NOT NULL,
  table_name    VARCHAR(64) NOT NULL,
  CONSTRAINT pk_tbl_schema_object PRIMARY KEY (id),
  KEY idx_schema_conn (connection_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS tbl_export_job (
  id            BIGINT       NOT NULL AUTO_INCREMENT,
  user_id       BIGINT       NOT NULL,
  connection_id BIGINT,
  instance      VARCHAR(96),
  `sql`         TEXT,
  name          VARCHAR(128),
  status        VARCHAR(16)  NOT NULL DEFAULT 'pending', -- pending|running|done|failed
  `rows`        INT,
  bytes         BIGINT,                                  -- total encrypted size across parts
  parts         INT,                                     -- number of ~100MB CSV files
  files         TEXT,                                    -- newline-joined part paths
  password      VARCHAR(64),
  error         VARCHAR(255),
  created_at    DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  finished_at   DATETIME(3),
  CONSTRAINT pk_tbl_export_job PRIMARY KEY (id),
  KEY idx_export_user (user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS tbl_script_upload (
  id         BIGINT       NOT NULL AUTO_INCREMENT,
  user_id    BIGINT       NOT NULL,
  filename   VARCHAR(255) NOT NULL,
  path       VARCHAR(512),
  size       BIGINT,
  source     VARCHAR(16),                             -- upload|terminal
  created_at DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  CONSTRAINT pk_tbl_script_upload PRIMARY KEY (id),
  KEY idx_upload_user (user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS tbl_notification (
  id         BIGINT       NOT NULL AUTO_INCREMENT,
  user_id    BIGINT       NOT NULL,
  type       VARCHAR(32)  NOT NULL,                -- approval-approved|approval-rejected|approval-expired
  title      VARCHAR(128) NOT NULL,
  body       VARCHAR(512),
  ref_no     VARCHAR(32),                          -- related ticket, e.g. AP-2295
  is_read    TINYINT(1)   NOT NULL DEFAULT 0,      -- `read` is a MySQL reserved word
  created_at DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  CONSTRAINT pk_tbl_notification PRIMARY KEY (id),
  KEY idx_notif_user (user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
