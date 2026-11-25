-- 0059: 发布阶段日志 / 发布单错误的机器可读身份(2026-10-30)。
--
-- 日志与错误是在 Go 里拼的中文,英文界面上整段露出来。log / error 两列照旧存规范中文(对外接口、
-- Webhook、审计读的都是它),这里各加一列存同一段话的 RuleRef(JSON),界面按读者的语言渲染。
-- 存量不回填:老单两列为空,界面回落显示原来的中文(用户 2026-10-30 确认)。
ALTER TABLE tbl_release_stage ADD COLUMN log_refs MEDIUMTEXT NULL;
ALTER TABLE tbl_release ADD COLUMN error_ref TEXT NULL;
