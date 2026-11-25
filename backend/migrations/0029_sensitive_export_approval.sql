-- 0029: 含敏感字段的导出要审批之后才跑。
--
-- 导出默认脱敏(ADR 0009)。但确实存在需要原值的场景 —— 对账、迁移、监管调取。
-- 让人自己勾一个开关就放行是不行的:一份带身份证号的 CSV 落到磁盘、发进聊天工具,
-- 比在终端上看一眼跑得远得多,而这恰恰是脱敏在导出这一路最要紧的原因。
--
-- 所以放开它要有人签字:勾了"包含敏感字段"的任务不进队列,停在 awaiting 等审批,
-- 批准之后才由 worker 后台执行。
--
-- tbl_approval.export_job_id 指回导出任务,它同时是一道闸:和 release_id 一样,
-- 把这张单挡在"发起人手动执行"那条路之外。导出单的 command 是一句描述而不是可执行
-- 的语句,若能被手动执行,网关会把那句描述当成命令发给数据库。
ALTER TABLE tbl_export_job ADD COLUMN include_sensitive TINYINT(1) NOT NULL DEFAULT 0;
ALTER TABLE tbl_export_job ADD COLUMN approval_id BIGINT NOT NULL DEFAULT 0;
ALTER TABLE tbl_export_job ADD COLUMN ap_no VARCHAR(32) NULL;
CREATE INDEX idx_export_approval ON tbl_export_job (approval_id);

ALTER TABLE tbl_approval ADD COLUMN export_job_id BIGINT NOT NULL DEFAULT 0;
CREATE INDEX idx_approval_export ON tbl_approval (export_job_id);

-- 存量任务不需要回填:include_sensitive 默认 0 = 照常脱敏,这与它们运行时的实际
-- 行为一致(那时脱敏是无条件的)。这一次的默认值就是历史事实,不是"暂且如此"。
