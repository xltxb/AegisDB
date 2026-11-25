-- 0056: 敏感字段脱敏、数据查询导出限制,改为按分层开关(2026-10-29)。
--
-- 两样原来对所有环境一视同仁:开发、演练库上的数据照样打码,数据查询照样要全局开关 + 授权
-- 「允许导出」才能导。运维要的是"只在生产(及放生产数据的预发布、法务)上生效"。
--
-- 列默认 TRUE:新建的、自定义的分层都按生产对待(漏了保护比多一层保护糟)。dev / uat 关掉
-- 由 bootstrap.backfillTierMaskExport 在迁移之后做一次 —— 与 strict_nowhere 同样的做法,只做一次,
-- 运维之后在分层页上改的值不会被重启改回去。
ALTER TABLE tbl_env_tier ADD COLUMN mask_sensitive BOOLEAN NOT NULL DEFAULT 1;
ALTER TABLE tbl_env_tier ADD COLUMN biz_export_restrict BOOLEAN NOT NULL DEFAULT 1;
