-- 0060: 存库提示文本的机器可读身份(2026-10-30,接口返回文案多语言第二批,见 .scratch/api-msg-i18n/PRD-batch2.md)。
--
-- 站内通知正文、导出 / 异步执行 / 在线变更任务的错误、审批单的执行结果,都是在 Go 里拼好的中文,
-- 英文界面原样露出来。原列照旧存规范中文(审计、对外接口读的都是它),这里各加一列存同一句话的
-- RuleRef(JSON),界面按读者语言渲染。固定句读出时按原句查表,不靠这一列;带参数的句子才用得上。
-- 存量不回填:老行这一列为空,固定句照样翻译,带参数的显示原来的中文(用户 2026-10-30 确认)。
ALTER TABLE tbl_notification ADD COLUMN body_ref TEXT NULL;
ALTER TABLE tbl_export_job ADD COLUMN error_ref TEXT NULL;
ALTER TABLE tbl_async_job ADD COLUMN error_ref TEXT NULL;
ALTER TABLE tbl_osc_job ADD COLUMN error_ref TEXT NULL;
ALTER TABLE tbl_approval ADD COLUMN result_ref TEXT NULL;
