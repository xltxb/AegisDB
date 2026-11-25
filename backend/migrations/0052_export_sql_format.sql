-- 0052: 数据导出支持 SQL 文件格式。
--
-- export_format:csv(默认,存量任务即此)| sql(写成目标库可直接执行的 INSERT,单表查询附建表语句)。
-- target_table:SQL 格式时 INSERT 写入的表名;提交时留空则从查询的 FROM 识别。
-- 单条 ALTER,可安全重跑(ADR 0016)。
ALTER TABLE tbl_export_job
  ADD COLUMN export_format VARCHAR(8) NOT NULL DEFAULT 'csv',
  ADD COLUMN target_table VARCHAR(128) NULL;
