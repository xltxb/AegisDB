-- MySQL 在线变更(gh-ost 那套)的任务表。见 ADR 0011。
--
-- 为什么要落库:一次迁移会跑几小时,期间可能被重启、被中止、被网络打断。状态只活在
-- 内存里的话,一次重启就让人失去了唯一的线索 —— 而库里还留着影子表和一段没追平的
-- binlog。这张表的读者不是进度条,是**收拾残局的人**:它要回答"有没有迁移死在半路,
-- 它的影子表叫什么"。
--
-- 这也是为什么 shadow 一建出来就写库,而不是等成功了再记。

CREATE TABLE IF NOT EXISTS tbl_osc_job (
  id            BIGSERIAL     PRIMARY KEY,
  connection_id BIGINT        NOT NULL,
  schema_name   VARCHAR(64)   NOT NULL,
  table_name    VARCHAR(64)   NOT NULL,
  -- 不叫 alter:那是 SQL 关键字,会让此后每条手写查询都得记着加引号。
  alter_clause  VARCHAR(512)  NOT NULL,
  status        VARCHAR(16)   NOT NULL,
  -- 影子表名。失败之后要靠它找到残留,所以一建出来就写,不等成功。
  shadow        VARCHAR(64)   NOT NULL DEFAULT '',
  copied_rows   BIGINT        NOT NULL DEFAULT 0,
  total_rows    BIGINT        NOT NULL DEFAULT 0,
  err           VARCHAR(1024) NOT NULL DEFAULT '',
  created_by    VARCHAR(64)   NOT NULL DEFAULT '',
  created_at    TIMESTAMPTZ   NOT NULL DEFAULT NOW(),
  updated_at    TIMESTAMPTZ   NOT NULL DEFAULT NOW(),
  finished_at   TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_osc_job_conn ON tbl_osc_job (connection_id);
-- 重启后挑残局用:未完成的那几个状态。
CREATE INDEX IF NOT EXISTS idx_osc_job_status ON tbl_osc_job (status);
