-- 0050: 在线变更任务记下"为什么在限流"(主库 Threads_running 超限 / 从库延迟超限 / 从库延迟未知)。
-- 只有 throttled 布尔时,面板只能说"限流中",人不知道该等主库降下来还是去看从库。
-- 单条 ALTER 可以安全重跑(ADR 0016)。
ALTER TABLE tbl_osc_job
  ADD COLUMN throttle_reason VARCHAR(255) NOT NULL DEFAULT '';
