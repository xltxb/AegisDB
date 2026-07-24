-- 0007_async_job.sql — background (async) long-running SQL execution jobs.
--
-- A job runs 30–60min+ decoupled from the HTTP request; server RAISE NOTICE
-- progress streams into `log`. Submit → poll `status` + `log`.
CREATE TABLE IF NOT EXISTS tbl_async_job (
  id            BIGINT       NOT NULL AUTO_INCREMENT,
  user_id       BIGINT       NOT NULL,
  connection_id BIGINT       NOT NULL,
  instance      VARCHAR(96)  NOT NULL DEFAULT '',
  db_name       VARCHAR(128) NOT NULL DEFAULT '',
  `sql`         TEXT,
  reason        VARCHAR(512) NOT NULL DEFAULT '',
  status        VARCHAR(16)  NOT NULL DEFAULT 'pending',  -- pending|running|done|failed
  log           MEDIUMTEXT,
  `rows`        INT          NOT NULL DEFAULT 0,
  error         VARCHAR(512) NOT NULL DEFAULT '',
  created_at    DATETIME     NULL,
  started_at    DATETIME     NULL,
  finished_at   DATETIME     NULL,
  CONSTRAINT pk_tbl_async_job PRIMARY KEY (id),
  KEY idx_async_user (user_id)
) DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
