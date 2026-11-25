-- 0051: 在线变更的断点续跑(.scratch/osc-product 工单 07)。
--
-- checkpoint 是引擎的检查点(JSON:已拷贝到的键、已重放到的 binlog 位点、计数),随进度每几秒落一次。
-- 网关重启时,有检查点的进行中任务判为 interrupted(影子表保留),由人在工单里续跑或放弃。
-- 单条 ALTER 可以安全重跑(ADR 0016)。
ALTER TABLE tbl_osc_job
  ADD COLUMN checkpoint TEXT NULL;
