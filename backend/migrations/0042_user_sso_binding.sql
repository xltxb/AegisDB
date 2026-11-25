-- 0042: 账号与公司 SSO 的**绑定**。
--
-- 绑定 = 管理员拿 SSO 的用户名单按邮箱核对过,确认"网关里这个账号,就是 SSO 里那个人"。
-- 绑定之后这个账号只能走 SSO 登录(管理员除外,保留口令作应急门)—— 逐人收紧,而不是
-- 一个全局开关一刀切:邮箱对不上的人不会被绑定,也就不会被锁在门外。
--
-- sso_user_name:SSO 里的用户名,核对时记下,排查"是不是同一个人"时有用。
-- sso_bound_at:什么时候绑的。解绑时清空,两列一起回到"未绑定"。
--
-- 存量账号一律未绑定:绑定必须来自一次明确的核对,不能是迁移替人做的决定。
ALTER TABLE tbl_user ADD COLUMN sso_bound TINYINT(1) NOT NULL DEFAULT 0;
ALTER TABLE tbl_user ADD COLUMN sso_user_name VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT '';
ALTER TABLE tbl_user ADD COLUMN sso_bound_at DATETIME NULL;
