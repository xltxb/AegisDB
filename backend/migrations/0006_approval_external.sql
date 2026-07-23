-- 0006_approval_external.sql — external (审批魔方) approval integration columns.
--
-- external_task_id: the vendor's task_id, used by the Phase-2 PATCH write-back.
-- lark_message_id : the real Lark message id (om_...) returned on callback, kept
--                   for the Phase-2 /reply回帖. Callback correlation uses our ApNo
--                   (echoed back as external_task_id), so these are not unique keys.
ALTER TABLE tbl_approval ADD COLUMN external_task_id VARCHAR(128) NOT NULL DEFAULT '';
ALTER TABLE tbl_approval ADD COLUMN lark_message_id  VARCHAR(128) NOT NULL DEFAULT '';
CREATE INDEX idx_approval_ext ON tbl_approval (external_task_id);
