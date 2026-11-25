-- 0055: 执行窗口可勾选同一台实例上的多个库(2026-10-22)。
--
-- 一张窗口覆盖的库放进子表 tbl_exec_window_db,一行一个库;主表的 db_name 留作"第一个库",
-- 给老数据与按单个库展示的地方用。判定按 (实例, 库) 取窗口时走 idx_window_db_name。
-- 已有窗口把它的 db_name 回填进子表。可安全重跑(ADR 0016):建表带 IF NOT EXISTS,回填用 INSERT IGNORE。
CREATE TABLE IF NOT EXISTS tbl_exec_window_db (
  window_id BIGINT       NOT NULL,
  db_name   VARCHAR(128) NOT NULL,
  PRIMARY KEY (window_id, db_name),
  KEY idx_window_db_name (db_name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

INSERT IGNORE INTO tbl_exec_window_db (window_id, db_name)
SELECT id, db_name FROM tbl_exec_window WHERE db_name <> '';
