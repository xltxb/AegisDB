-- 0053: AI 巡检报告(.scratch/inspection-report)。
--
-- AI 巡检系统每天给每个实例推一份报告:完整 HTML 压缩后存文件(系统设置 inspection.savePath),
-- 这张表存元数据与结构化摘要。一个实例一天一份,再推覆盖(uk_inspection_report)。永久保留。
--
-- report_date 用 VARCHAR(10) 存 YYYY-MM-DD:与 SQLite(开发 / 测试)行为一致,字典序即日期序。
-- instance_name 是上传时的实例名快照,实例改名、删除后历史报告仍可辨认。
--
-- 只有一条 CREATE TABLE IF NOT EXISTS,中途失败可整文件重跑(ADR 0016)。
CREATE TABLE IF NOT EXISTS tbl_inspection_report (
  id             BIGINT       NOT NULL AUTO_INCREMENT,
  connection_id  BIGINT       NOT NULL,
  instance_name  VARCHAR(64)  NOT NULL,
  report_date    VARCHAR(10)  NOT NULL,
  inspected_at   DATETIME(3)  NULL,
  score          INT          NULL,
  level          VARCHAR(16)  NOT NULL,
  critical_count INT          NOT NULL DEFAULT 0,
  warning_count  INT          NOT NULL DEFAULT 0,
  info_count     INT          NOT NULL DEFAULT 0,
  findings       MEDIUMTEXT   NULL,
  file_path      VARCHAR(512) NOT NULL,
  size_bytes     BIGINT       NOT NULL DEFAULT 0,
  sha256         VARCHAR(64)  NOT NULL,
  api_client_id  BIGINT       NOT NULL DEFAULT 0,
  created_at     DATETIME(3)  NULL,
  updated_at     DATETIME(3)  NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_inspection_report (connection_id, report_date),
  KEY idx_inspection_date (report_date),
  KEY idx_inspection_level (level)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
