-- 0049: 在线加 / 删索引的任务(.scratch/osc-product,ADR 0011)。
--
-- 进度由运行器每隔几秒落到这里,页面读库;网关重启时 running 的任务据此判失败(不自动续跑)。
-- 审批单上挂一个 osc_job_id(与导出、执行窗口工单同一套机制)。
--
-- CREATE TABLE IF NOT EXISTS 与单条 ALTER 都可以安全重跑(ADR 0016)。
CREATE TABLE IF NOT EXISTS tbl_osc_job (
  id                BIGINT       NOT NULL AUTO_INCREMENT,
  connection_id     BIGINT       NOT NULL,
  instance          VARCHAR(64)  NOT NULL,
  db_name           VARCHAR(128) NOT NULL,
  tbl_name          VARCHAR(64)  NOT NULL,
  statement         TEXT         NOT NULL,
  change_json       TEXT         NOT NULL,
  approval_id       BIGINT       NOT NULL DEFAULT 0,
  ap_no             VARCHAR(32)  NULL,
  release_id        BIGINT       NOT NULL DEFAULT 0,
  initiator_id      BIGINT       NOT NULL,
  initiator         VARCHAR(64)  NULL,
  size_rows         BIGINT       NOT NULL DEFAULT 0,
  size_bytes        BIGINT       NOT NULL DEFAULT 0,
  warnings          TEXT         NULL,
  status            VARCHAR(16)  NOT NULL,
  phase             VARCHAR(16)  NULL,
  rows_estimated    BIGINT       NOT NULL DEFAULT 0,
  rows_copied       BIGINT       NOT NULL DEFAULT 0,
  chunks            INT          NOT NULL DEFAULT 0,
  rows_replayed     BIGINT       NOT NULL DEFAULT 0,
  cut_over_attempts INT          NOT NULL DEFAULT 0,
  throttled         TINYINT(1)   NOT NULL DEFAULT 0,
  paused            TINYINT(1)   NOT NULL DEFAULT 0,
  error             TEXT         NULL,
  old_table         VARCHAR(64)  NULL,
  created_at        DATETIME(3)  NULL,
  started_at        DATETIME(3)  NULL,
  finished_at       DATETIME(3)  NULL,
  updated_at        DATETIME(3)  NULL,
  PRIMARY KEY (id),
  KEY idx_osc_job_conn (connection_id),
  KEY idx_osc_job_approval (approval_id),
  KEY idx_osc_job_status (status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- 审批单:osc_job_id 指回任务;notice / notice_ref 是要审批人特别留意的一句话(大表直接 DDL 的提示)。
-- 一条 ALTER 加三列:要么全成要么全不成,整文件重跑不会撞上"列已存在"(ADR 0016)。
ALTER TABLE tbl_approval
  ADD COLUMN osc_job_id BIGINT NOT NULL DEFAULT 0,
  ADD COLUMN notice     TEXT   NULL,
  ADD COLUMN notice_ref TEXT   NULL;
