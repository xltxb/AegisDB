-- 0019: 数据库变更发布流水线 (CI/CD) + 数据库规范审查规则库.
--
-- Two features, one migration, because they are only useful together: a release
-- pipeline's first stage is the review, and the review's reason to exist is that
-- something stops a release when it fails.
--
-- ---------------------------------------------------------------- 规范审查规则库
--
-- One row per rule. `code` is the identity: for a builtin rule it is what binds
-- the row to its implementation in internal/review, which is why the code is
-- unique and why an operator may not rename it. Everything a policy decision
-- legitimately owns — level, enabled, params, message — is a column here, so
-- lowering "表必须有主键" from error to warn is a row update rather than a
-- redeploy.
--
-- `level` is what a finding COSTS (error blocks a release, warn/info report),
-- and it is deliberately per rule rather than per finding: whether a missing
-- table comment stops a release is an organisational decision made once.
--
-- `dialect` is a comma-separated set ("mysql,tidb") or "all". Scoping matters
-- because a review that reported rules the target database cannot violate — an
-- Oracle naming rule fired at a TiDB statement — trains people to ignore it.
CREATE TABLE IF NOT EXISTS tbl_sql_review_rule (
  id          BIGINT       NOT NULL AUTO_INCREMENT,
  code        VARCHAR(64)  NOT NULL,
  name        VARCHAR(128) NOT NULL,
  dialect     VARCHAR(64)  NOT NULL DEFAULT 'all',
  category    VARCHAR(32)  NOT NULL,
  level       VARCHAR(16)  NOT NULL DEFAULT 'warn',
  kind        VARCHAR(16)  NOT NULL DEFAULT 'builtin',
  enabled     TINYINT(1)   NOT NULL DEFAULT 1,
  params      TEXT             NULL,
  message     VARCHAR(512)     NULL,
  sort_order  INT          NOT NULL DEFAULT 0,
  created_at  DATETIME(3)      NULL,
  updated_at  DATETIME(3)      NULL,
  PRIMARY KEY (id),
  UNIQUE KEY idx_review_code (code)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- ---------------------------------------------------------------- 发布流程模板
--
-- A pipeline is an ordered list of stages. `tier_code` holds an EnvTier.Code
-- (never an Environment.Code — see tbl_role_capability for why that distinction
-- is load-bearing): empty means the template applies to every tier, a value
-- narrows it to one, which is what keeps a dev flow from being selected for a
-- production release.
--
-- Exactly one template may carry is_default; repository.SavePipeline clears the
-- others in the same transaction, because two defaults would make "the default
-- flow" whichever row the database happened to return first.
CREATE TABLE IF NOT EXISTS tbl_pipeline (
  id          BIGINT       NOT NULL AUTO_INCREMENT,
  name        VARCHAR(128) NOT NULL,
  description VARCHAR(512)     NULL,
  tier_code   VARCHAR(16)      NULL,
  enabled     TINYINT(1)   NOT NULL DEFAULT 1,
  is_default  TINYINT(1)   NOT NULL DEFAULT 0,
  created_by  BIGINT       NOT NULL DEFAULT 0,
  created_at  DATETIME(3)      NULL,
  updated_at  DATETIME(3)      NULL,
  PRIMARY KEY (id),
  KEY idx_pipeline_tier (tier_code)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- Stage DEFINITIONS. `config` is per-type JSON: {"failOn":"error"} for review,
-- {"sql":"…"} for backup/verify, {"note":"…"} for a manual gate.
--
-- on_failure is abort|continue. The execute type is forced to abort in the
-- service layer regardless of what is stored: carrying on after a failed
-- execution would let the verify and notify stages announce a change that never
-- happened.
CREATE TABLE IF NOT EXISTS tbl_pipeline_stage (
  id          BIGINT       NOT NULL AUTO_INCREMENT,
  pipeline_id BIGINT       NOT NULL,
  step_order  INT          NOT NULL,
  name        VARCHAR(64)  NOT NULL,
  type        VARCHAR(16)  NOT NULL,
  config      TEXT             NULL,
  on_failure  VARCHAR(16)  NOT NULL DEFAULT 'abort',
  PRIMARY KEY (id),
  KEY idx_pstage_pipeline (pipeline_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- ---------------------------------------------------------------- 发布单
--
-- One run: one change, one instance, one snapshot of the flow it went through.
--
-- pipeline_name, env, tier_code and engine are SNAPSHOTS, for the same reason
-- tbl_approval carries its own (see 0013): a template edited next month, an
-- environment rebound to another tier, or an instance moved between environments
-- must not rewrite what this release actually did. Nothing here is ever updated
-- to "correct" them; that would not be a correction, it would be a forgery.
--
-- `sql` is MEDIUMTEXT for the same reason as tbl_approval.command (0018): a real
-- migration is bigger than 64KB. A release whose body is an uploaded script
-- stores the reference + digest instead and re-reads the file at execute time,
-- refusing if it no longer hashes to what was reviewed.
--
-- `risk` records the gateway verdict captured at SUBMIT time. The dictionary can
-- change while a release waits for approval, so the level that authorised the
-- run is the one worth keeping — the execute stage still re-judges before it
-- applies anything.
CREATE TABLE IF NOT EXISTS tbl_release (
  id               BIGINT       NOT NULL AUTO_INCREMENT,
  rel_no           VARCHAR(32)  NOT NULL,
  title            VARCHAR(128) NOT NULL,
  pipeline_id      BIGINT       NOT NULL,
  pipeline_name    VARCHAR(128)     NULL,
  connection_id    BIGINT       NOT NULL,
  instance         VARCHAR(96)      NULL,
  db_name          VARCHAR(128)     NULL,
  env              VARCHAR(32)      NULL,
  tier_code        VARCHAR(16)      NULL,
  engine           VARCHAR(32)      NULL,
  `sql`            MEDIUMTEXT       NULL,
  script_upload_id BIGINT       NOT NULL DEFAULT 0,
  script_sha256    VARCHAR(64)      NULL,
  reason           VARCHAR(512)     NULL,
  creator_id       BIGINT       NOT NULL,
  creator          VARCHAR(64)      NULL,
  status           VARCHAR(16)  NOT NULL DEFAULT 'pending',
  risk             VARCHAR(16)      NULL,
  error            VARCHAR(512)     NULL,
  created_at       DATETIME(3)      NULL,
  started_at       DATETIME(3)      NULL,
  finished_at      DATETIME(3)      NULL,
  PRIMARY KEY (id),
  UNIQUE KEY idx_release_relno (rel_no),
  KEY idx_release_creator (creator_id),
  KEY idx_release_status (status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- Stage RUNS — what the pipeline view draws.
--
-- `findings` keeps the review stage's JSON result so a blocked release can still
-- be explained after the rule library has moved on. `approval_id`/`approval_no`
-- link an approve stage to the ticket it waits on: the resume sweeper reads that
-- link, which is why it lives on the row rather than only in the log.
CREATE TABLE IF NOT EXISTS tbl_release_stage (
  id          BIGINT       NOT NULL AUTO_INCREMENT,
  release_id  BIGINT       NOT NULL,
  step_order  INT          NOT NULL,
  name        VARCHAR(64)  NOT NULL,
  type        VARCHAR(16)  NOT NULL,
  config      TEXT             NULL,
  on_failure  VARCHAR(16)  NOT NULL DEFAULT 'abort',
  status      VARCHAR(16)  NOT NULL DEFAULT 'pending',
  log         MEDIUMTEXT       NULL,
  findings    MEDIUMTEXT       NULL,
  approval_id BIGINT       NOT NULL DEFAULT 0,
  approval_no VARCHAR(32)      NULL,
  `rows`      INT          NOT NULL DEFAULT 0,
  started_at  DATETIME(3)      NULL,
  finished_at DATETIME(3)      NULL,
  PRIMARY KEY (id),
  KEY idx_rstage_release (release_id),
  KEY idx_rstage_approval (approval_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- ---------------------------------------------------------------- 审批单关联发布
--
-- release_id links a ticket raised by a pipeline's approve stage back to its
-- release, and it also SUPPRESSES execution on approval: the pipeline owns the
-- execute stage (and re-judges the statement before applying it), so letting
-- finalizeApproval run the command as well would apply the same change twice —
-- the second time to a pipeline that still believes it has not run. Zero on
-- every ordinary ticket, which is exactly the previous behaviour.
ALTER TABLE tbl_approval ADD COLUMN release_id BIGINT NOT NULL DEFAULT 0;
ALTER TABLE tbl_approval ADD INDEX idx_approval_release (release_id);
