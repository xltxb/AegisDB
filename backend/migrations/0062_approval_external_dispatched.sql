-- 0062: 审批单是否已外发给审批魔方(2026-11 白盒审计 V-08)。
--
-- task id 是异步写回的,审批魔方也不保证回,回调不能只靠它判断这张单外发过没有。存量不回填:升级前
-- 建的、还没有 task id 的待审单只能在应用内审批。
ALTER TABLE tbl_approval ADD COLUMN external_dispatched TINYINT(1) NOT NULL DEFAULT 0;
