-- 0034: 把承载"环境 / 分层代码"的那几列的排序规则对齐,修掉跨表 JOIN 的 1267。
--
-- 线上报的是这一条:
--
--   Error 1267 (HY000): Illegal mix of collations
--   (utf8mb4_unicode_ci,IMPLICIT) and (utf8mb4_0900_ai_ci,IMPLICIT) for operation '='
--   SELECT count(*) FROM tbl_audit_log
--     JOIN tbl_connection  ON tbl_connection.id  = tbl_audit_log.connection_id
--     JOIN tbl_environment ON tbl_environment.code = tbl_connection.env
--     JOIN tbl_env_tier    ON tbl_env_tier.code  = tbl_environment.tier_code
--
-- ## 成因
--
-- 0001_init.sql 建表时写的是 `DEFAULT CHARSET=utf8mb4`,**没有写 COLLATE**。
-- 那一行第 8 句给库设的是 utf8mb4_unicode_ci,但 MySQL 在这里不继承库的排序规则 ——
-- 只指定字符集而不指定排序规则时,用的是**该字符集的默认排序规则**,在 MySQL 8 上
-- 是 utf8mb4_0900_ai_ci。于是:
--
--   0001 那批表(tbl_connection / tbl_role_capability / tbl_risk_command / …)
--        → utf8mb4_0900_ai_ci
--   0007 起的每一张表都显式写了 COLLATE=utf8mb4_unicode_ci
--        (tbl_environment / tbl_env_tier 属于后者)
--
-- 同一个键(prod / gli / staging / uat / dev)横跨两种排序规则,一 JOIN 就是 1267。
--
-- 这个坑在开发环境**永远不会出现**:那边是 SQLite,根本没有排序规则这回事。所以它
-- 只会在生产上第一次跨表 JOIN 时炸出来 —— 这次就是。
--
-- 更糟的是它炸得很安静:CountProdInterceptions 的错误在 handler 里被 `_` 吞掉,
-- 界面上只是"拦截次数 0"。没有人会把一个 0 当成故障。
--
-- ## 这次改什么
--
-- 只动**承载环境/分层代码的那几列**,而不是整表 CONVERT:
--
--   tbl_connection.env            ← 这次报错的那一半
--   tbl_role_capability.tier_code ← 同一个键(0016 从 env 改名而来)
--   tbl_risk_command.tier_code    ← 同一个键(同上)
--
-- 三张都是小表(实例几十行、能力矩阵与高危字典各几百行),改列要重建索引,但代价
-- 是秒级的。类型、非空、默认值原样保留 —— MODIFY 会用给出的定义整个替换列定义,
-- 少写一个 NOT NULL 就是一次静悄悄的约束丢失。
--
-- ## 这次**不**改什么,以及为什么
--
-- tbl_audit_log 与 tbl_approval 上的 env / tier_code(0013 加的列,继承了表的
-- 0900_ai_ci)同样是这个键,但**今天没有任何查询拿它们跨表 JOIN** —— 它们是快照,
-- 是写下来给人读的,不是用来连表的。而这两张表是全库最大的两张(审计链只增不删),
-- 改列意味着整表重建:部署指引要求"停旧进程 → migrate → 起新进程",于是那段重建
-- 时间就是停机时间。
--
-- 所以留在这里说清楚,而不是顺手一起改:**将来若有查询要拿这两张表的 env /
-- tier_code 去 JOIN 别的表,先补一条迁移把它们也对齐**,并挑一个能承受整表重建的
-- 窗口。在那之前,这是一笔明知的欠账,不是被忽略的疏漏。
--
-- ## 防复发
--
-- 根因是"建表时忘了写 COLLATE",而它不会以任何形式报错。所以配了一条测试去扫
-- migrations/*.sql:新建的表必须显式声明 COLLATE(见 bootstrap 的
-- TestMigrationsDeclareCollation)。它挡的正是 0001 当年犯的那个错。

ALTER TABLE tbl_connection
  MODIFY COLUMN env VARCHAR(16) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL;

ALTER TABLE tbl_role_capability
  MODIFY COLUMN tier_code VARCHAR(16) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL;

ALTER TABLE tbl_risk_command
  MODIFY COLUMN tier_code VARCHAR(16) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL;
