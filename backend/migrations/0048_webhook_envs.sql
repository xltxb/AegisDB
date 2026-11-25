-- 0048: Webhook 按环境过滤。
--
-- envs 是要推送的环境代码(tbl_environment.code,逗号分隔),空 = 全部环境 —— 老配置原样,
-- 行为不变。按审计行上的环境快照(tbl_audit_log.env)匹配;没有环境的事件(登录、改设置)
-- 不受这一栏约束,仍只看「增删改查」订阅。见 service.Dispatcher.Dispatch。
ALTER TABLE tbl_webhook_config
  ADD COLUMN envs VARCHAR(1024) NOT NULL DEFAULT '';
