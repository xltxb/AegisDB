-- 0038: 把承载分层 / 环境代码的列放宽到 VARCHAR(32)。
--
-- ## 为什么
--
-- 代码由 service.codeRe 约束:`^[a-z0-9][a-z0-9-]{0,31}$` —— 最长 32 个字符。而这些列里
-- 有一半是 VARCHAR(16),其中 tbl_connection.env 更是被 0034 从 32 **收窄**回 16 的
-- (那条迁移的本意只是对齐排序规则,注释还写着「类型原样保留」)。
--
-- SQLite(开发与测试)对 VARCHAR 的长度不做约束,所以这件事在本地永远不会暴露:建一个
-- 20 字符的分层、把实例挂上去,一切正常。到了 MySQL 上:
--
--   · STRICT 模式 → Data too long for column (1406),写不进去
--   · 非 STRICT   → **静默截断**,而截断后的 code 解析不到任何分层,那台实例的每一条
--                   命令都被拒。fail-closed 是对的,但没人知道为什么
--
-- ## 为什么是 32 而不是别的数
--
-- 因为正则就是按 32 写的。两者必须是同一个数,所以配了一条用例按 codeRe 的上限去量
-- 每一列(model.TestCodeColumnsFitWhatTheRegexAllows),它同时挡住「改了正则忘了改列」
-- 和「改了列忘了改正则」—— 0034 那次正是后者。
--
-- MODIFY COLUMN 重复执行不报错,所以这条是幂等的(ADR 0016)。
-- 排序规则沿用 0034 对齐过的那一套,别在这里把它改回去。

ALTER TABLE tbl_connection
  MODIFY COLUMN env VARCHAR(32) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL;

ALTER TABLE tbl_env_tier
  MODIFY COLUMN code VARCHAR(32) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL;

ALTER TABLE tbl_environment
  MODIFY COLUMN tier_code VARCHAR(32) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL;

ALTER TABLE tbl_role_capability
  MODIFY COLUMN tier_code VARCHAR(32) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL;

ALTER TABLE tbl_risk_command
  MODIFY COLUMN tier_code VARCHAR(32) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL;

ALTER TABLE tbl_approval
  MODIFY COLUMN tier_code VARCHAR(32) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

ALTER TABLE tbl_audit_log
  MODIFY COLUMN tier_code VARCHAR(32) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

ALTER TABLE tbl_pipeline
  MODIFY COLUMN tier_code VARCHAR(32) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

ALTER TABLE tbl_release
  MODIFY COLUMN tier_code VARCHAR(32) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
