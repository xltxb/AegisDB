-- 发布单执行阶段挂在 OSC 长任务上所需的三列。见 ADR 0011 与
-- docs/superpowers/specs/2026-09-19-osc-auto-route-design.md。
--
-- 为什么执行阶段要有游标:一条走 OSC 的语句要跑几小时,阶段在那期间停在 waiting。
-- 恢复时必须知道**前面几条已经执行过了** —— 不记的话,恢复会把已经执行过的语句
-- 再执行一遍,而那是一次重复的生产变更。

ALTER TABLE tbl_release_stage ADD COLUMN IF NOT EXISTS exec_cursor INT    NOT NULL DEFAULT 0;
ALTER TABLE tbl_release_stage ADD COLUMN IF NOT EXISTS osc_job_id  BIGINT NOT NULL DEFAULT 0;

-- 发起人对这一单的单次覆盖:'' = 按策略,force = 强制走,skip = 强制直发。
ALTER TABLE tbl_release ADD COLUMN IF NOT EXISTS osc_mode VARCHAR(8) NOT NULL DEFAULT '';

-- 任务结束时要反查"是哪个阶段在等它"。没有索引的话,每个任务结束都要全表扫一遍。
CREATE INDEX IF NOT EXISTS idx_release_stage_osc_job ON tbl_release_stage (osc_job_id);
