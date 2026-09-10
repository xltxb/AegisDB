-- 0035: 远端库的表清单与表结构,在本地留一份。
--
-- ## 为什么
--
-- 在此之前,树每展开一次就实时探一次目标库,而"在所有实例里找某张表"意味着挨个连
-- 88 台生产实例问一遍 —— 慢,而且把一次界面操作变成了一轮对生产的扫描。
--
-- ## 这份数据是什么,不是什么
--
-- 它是**只读的副本,不是真相**。判定、执行、导出一律仍然走实时的目标库:一份可能
-- 过期的结构如果被拿去做判断,它就从"快"变成了"错"。
--
-- 所以每一行都带 synced_at,界面必须把"这份数据是什么时候的"显示出来 —— 一个不说明
-- 年龄的缓存,读的人会当成现状。
--
-- ## 三张表
--
--   tbl_meta_table   一张表(或视图)一行
--   tbl_meta_column  一列一行
--   tbl_meta_sync    每台实例最近一次同步的结果
--
-- 第三张不是可有可无的:没有它,"这台实例为什么一张表都没有"没有答案 —— 是还没轮到
-- 它、连不上、账号没权限,还是它真的空着?这四种在界面上长得一模一样,而只有第一种
-- 是正常的。
--
-- ## 几处刻意的取舍
--
-- **不做外键。** 同步是"整台实例删了重写",外键只会把那次删除变成一场级联风暴;而
-- 这份数据本来就没有引用完整性可言 —— 它是一张照片。列靠与表相同的四元组定位
-- (连接 / 库 / schema / 表名)。
--
-- **schema_name 允许空串而不是 NULL。** 扁平引擎(MySQL / SQLite / Oracle 按 owner)
-- 没有这一层。空串参与唯一索引,NULL 不参与 —— 后者会让同一张表被写进去很多次而
-- 唯一索引一声不吭。
--
-- **db_name 而不是 `database`。** 后者是保留字,每次引用都要加反引号,而
-- tbl_connection / tbl_exec_window 早就用的是 db_name。
--
-- **唯一索引在(连接,库,schema,表[,列])上。** 同步走的是先删后插,理论上撞不了;
-- 但一次没删干净的重试会静静地把数据翻倍,而翻倍的表清单读起来完全正常。

CREATE TABLE IF NOT EXISTS tbl_meta_table (
  id            BIGINT       NOT NULL AUTO_INCREMENT,
  connection_id BIGINT       NOT NULL,
  db_name       VARCHAR(128) NOT NULL,
  schema_name   VARCHAR(128) NOT NULL DEFAULT '',
  table_name    VARCHAR(128) NOT NULL,
  kind          VARCHAR(16)  NOT NULL DEFAULT 'table',   -- table | view
  comment       VARCHAR(512) NOT NULL DEFAULT '',
  synced_at     DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  CONSTRAINT pk_tbl_meta_table PRIMARY KEY (id),
  UNIQUE KEY idx_meta_table_uniq (connection_id, db_name, schema_name, table_name),
  KEY idx_meta_table_name (table_name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS tbl_meta_column (
  id            BIGINT       NOT NULL AUTO_INCREMENT,
  connection_id BIGINT       NOT NULL,
  db_name       VARCHAR(128) NOT NULL,
  schema_name   VARCHAR(128) NOT NULL DEFAULT '',
  table_name    VARCHAR(128) NOT NULL,
  ordinal       INT          NOT NULL DEFAULT 0,
  column_name   VARCHAR(128) NOT NULL,
  data_type     VARCHAR(128) NOT NULL,
  nullable      TINYINT(1)   NOT NULL DEFAULT 1,
  col_default   VARCHAR(512) NOT NULL DEFAULT '',
  comment       VARCHAR(512) NOT NULL DEFAULT '',
  is_pk         TINYINT(1)   NOT NULL DEFAULT 0,
  synced_at     DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  CONSTRAINT pk_tbl_meta_column PRIMARY KEY (id),
  UNIQUE KEY idx_meta_col_uniq (connection_id, db_name, schema_name, table_name, column_name),
  KEY idx_meta_col_scope (connection_id, db_name, table_name),
  KEY idx_meta_col_name (column_name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS tbl_meta_sync (
  connection_id BIGINT       NOT NULL,
  started_at    DATETIME(3)  NULL,
  finished_at   DATETIME(3)  NULL,
  databases     INT          NOT NULL DEFAULT 0,
  tables        INT          NOT NULL DEFAULT 0,
  columns       INT          NOT NULL DEFAULT 0,
  -- 最近一次失败的原因。成功时清空 —— 留着上次的错误会让一台已经好了的实例永远
  -- 显示成坏的。
  err           VARCHAR(512) NOT NULL DEFAULT '',
  CONSTRAINT pk_tbl_meta_sync PRIMARY KEY (connection_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
