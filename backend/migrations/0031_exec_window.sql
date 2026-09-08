-- 0031: 执行窗口(「班车」)。
--
-- 在指定时间、对指定的库,把本来需要**审批**的中/高风险语句直接放行。定期维护和发版
-- 车次里,一晚上几十条 DDL 逐条等人批是不现实的,而现实里的做法是提前约好一个时间段。
-- 这张表把那个口头约定变成系统里可查、可审计、到点自动关上的东西。
--
-- 它改的是"要不要人来批",不是"有没有权限":能力矩阵判 deny 的仍然 deny,只读角色不会
-- 因为开了窗口就能写库。见 service/exec_window.go 的 relaxByWindow。
--
-- 按**库**开,不按实例、也不按分层:一台实例底下往往混着不同业务的库,而一次变更通常
-- 只动其中一两个。放开面越小,窗口开着的那几个小时里能出的事就越少。
--
-- 两种时间模型共用一张表:
--   once      起止时刻,用完即废(starts_at / ends_at)
--   recurring 固定时段反复生效(weekdays + start_min/end_min + timezone),
--             可用 not_after 给整条班线设一个停运时刻
--
-- 时区存 IANA 名字而不是偏移量:运维说的"凌晨两点"是他所在时区的两点,而网关可能跑在
-- UTC 上;偏移量还会在夏令时切换时失真。
--
-- 窗口放行的每一条命令都会在审计的 operator 字段里指回是哪个窗口放的,而 operator 在
-- 审计链的哈希里 —— 事后改不了。
CREATE TABLE IF NOT EXISTS tbl_exec_window (
  id            BIGINT       NOT NULL AUTO_INCREMENT,
  name          VARCHAR(64)  NOT NULL,
  enabled       TINYINT(1)   NOT NULL DEFAULT 1,
  connection_id BIGINT       NOT NULL,
  db_name       VARCHAR(128) NOT NULL,
  kind          VARCHAR(16)  NOT NULL,
  timezone      VARCHAR(64)  NOT NULL,
  starts_at     DATETIME(3)  NULL,
  ends_at       DATETIME(3)  NULL,
  weekdays      VARCHAR(32)  NULL,
  start_min     INT          NOT NULL DEFAULT 0,
  end_min       INT          NOT NULL DEFAULT 0,
  not_after     DATETIME(3)  NULL,
  reason        VARCHAR(255) NULL,
  created_by    BIGINT       NOT NULL DEFAULT 0,
  created_at    DATETIME(3)  NULL,
  updated_at    DATETIME(3)  NULL,
  PRIMARY KEY (id),
  -- 判定发生在每条命令的执行路径上,所以这条查询必须走索引:按 (实例, 库) 取回来的
  -- 是个位数行,再在应用层判时间。
  KEY idx_window_scope (connection_id, db_name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
