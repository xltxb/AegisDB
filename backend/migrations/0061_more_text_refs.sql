-- 0061: 接口返回文案多语言收尾(2026-10-31,见 .scratch/api-msg-i18n/PRD-batch4.md)。
--
-- 异步执行日志里网关自己写的几行(开始 / 完成 / 失败……)与在线变更的限流原因,也是在 Go 里拼的中文。
-- 原列照旧存规范中文,这里另存它们的机器可读身份,界面按读者语言显示;存量不回填(老行显示原句)。
ALTER TABLE tbl_async_job ADD COLUMN log_refs MEDIUMTEXT NULL;
ALTER TABLE tbl_osc_job ADD COLUMN throttle_reason_ref TEXT NULL;
