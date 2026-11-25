-- 0058: 分层开关「允许自动执行」(2026-10-30)。
--
-- 发布流程新增「自动执行」阶段:前序阶段通过就直接下发,不等人点「确认执行」。只能用在打开了这个
-- 开关的分层上 —— 保存流程时校验一次,执行时再查一次(期间被关掉就退回等人工确认)。
--
-- 列默认 FALSE:新建的、自定义的分层都按生产对待。dev / uat 打开由 bootstrap.backfillTierAutoExecute
-- 在迁移之后做一次,与 0056 同样的做法,运维之后在分层页上改的值不会被重启改回去。
ALTER TABLE tbl_env_tier ADD COLUMN auto_execute BOOLEAN NOT NULL DEFAULT 0;
