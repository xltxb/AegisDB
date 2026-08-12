-- 0012: split the instance tier into a control tier and an environment.
--
-- tbl_connection.env used to be one of four hardcoded strings that carried two
-- jobs at once: it keyed the rule tables (tbl_role_capability, tbl_risk_command)
-- AND it grouped instances. That made "add a second production cluster"
-- impossible to express — a new group meant a new rule key, and both rule
-- lookups treat a missing row as permission granted (CapabilityLevel → allow,
-- matchCommand → off), so an unregulated environment is what you would get.
--
-- The two jobs are now separate tables: a tier owns the rules, an environment
-- groups the instances, and one tier backs many environments. Adding prod-hk to
-- the prod tier copies nothing and is regulated from its first second.
--
-- No data moves. The seeded environments are named after their tier, so every
-- existing tbl_connection.env value is already a valid environment code, and
-- tbl_role_capability.env / tbl_risk_command.env already hold tier codes — those
-- columns change meaning, not content.

CREATE TABLE IF NOT EXISTS tbl_env_tier (
  code              VARCHAR(16)  NOT NULL,
  display_name      VARCHAR(64)  NOT NULL,
  sort_order        INT          NOT NULL DEFAULT 0,
  require_mfa       TINYINT(1)   NOT NULL DEFAULT 0,
  danger_banner     TINYINT(1)   NOT NULL DEFAULT 0,
  counts_in_pending TINYINT(1)   NOT NULL DEFAULT 0,
  -- Exactly one tier carries scan_baseline; the application maintains that
  -- invariant (no partial-unique index on MySQL 8).
  scan_baseline     TINYINT(1)   NOT NULL DEFAULT 0,
  conn_layer        VARCHAR(64)  NOT NULL DEFAULT '',
  default_role      VARCHAR(64)  NOT NULL DEFAULT '',
  PRIMARY KEY (code)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS tbl_environment (
  code         VARCHAR(32) NOT NULL,
  display_name VARCHAR(64) NOT NULL,
  tier_code    VARCHAR(16) NOT NULL,
  sort_order   INT         NOT NULL DEFAULT 0,
  PRIMARY KEY (code),
  KEY idx_environment_tier (tier_code)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- The four built-ins. Property flags reproduce today's behaviour exactly: only
-- prod forces MFA, shows the terminal danger banner, counts toward the pending
-- figure and is the script-scan baseline. conn_layer / default_role are lifted
-- verbatim from the connEnvMeta map they replace.
INSERT INTO tbl_env_tier
  (code, display_name, sort_order, require_mfa, danger_banner, counts_in_pending, scan_baseline, conn_layer, default_role)
VALUES
  ('prod',    '生产环境 · PROD',   0, 1, 1, 1, 1, 'L1 核心 · 写',  'dba_l2'),
  ('gli',     '灰度 · GLI',        1, 0, 0, 0, 0, 'L2 灰度',       'dba_l2'),
  ('staging', '演练UAT · STAGING', 2, 0, 0, 0, 0, 'L3 演练UAT',    'dba_l2'),
  ('dev',     '测试 · DEV',        3, 0, 0, 0, 0, 'L4 沙盒',       'developer')
ON DUPLICATE KEY UPDATE code = code;

-- One environment per tier, named after it — this is what keeps every existing
-- tbl_connection.env value valid without a backfill.
INSERT INTO tbl_environment (code, display_name, tier_code, sort_order)
VALUES
  ('prod',    '生产环境 · PROD',   'prod',    0),
  ('gli',     '灰度 · GLI',        'gli',     1),
  ('staging', '演练UAT · STAGING', 'staging', 2),
  ('dev',     '测试 · DEV',        'dev',     3)
ON DUPLICATE KEY UPDATE code = code;
