-- 0057: 导出任务记下载情况(2026-10-29)。
--
-- 导出完成后一份任务可能有好几个分卷;人下过一次之后,列表上看不出哪些下过,于是重复下载。
-- 每下载一个分卷就把它记进 downloaded_files(换行分隔的分卷路径),downloaded_at 是最近一次下载时间。
-- 记在服务端而不是浏览器里:换台电脑、清了缓存,标记也还在。
ALTER TABLE tbl_export_job ADD COLUMN downloaded_at DATETIME(3) NULL;
ALTER TABLE tbl_export_job ADD COLUMN downloaded_files MEDIUMTEXT NULL;
