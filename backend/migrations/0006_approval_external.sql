-- 0006_approval_external.sql — external (审批魔方) approval integration column.
--
-- external_task_id: the vendor's task_id, used by the timeout PATCH write-back.
-- Callback correlation uses our ApNo (echoed back as external_task_id), so it is
-- not a unique key.
ALTER TABLE tbl_approval ADD COLUMN external_task_id VARCHAR(128) NOT NULL DEFAULT '';
CREATE INDEX idx_approval_ext ON tbl_approval (external_task_id);
