-- 删除审批魔方对接(ADR-0003,被 ADR-0020 取代)留下的一切。
--
-- 设置里有两条加密的凭据(token、callbackSecret)。不在库里留死凭据:它们不再有任何用途,
-- 只剩下被人从备份里拿走的那一种可能。
DELETE FROM tbl_setting WHERE k LIKE 'approval.external.%';

-- external_task_id 的两个用途(回调关联、超时回写取消)都随魔方消失,卡片追踪改由
-- tbl_im_card 承担。删列不能与旧二进制共存:升级顺序见 README(备份 → 停旧进程 → migrate)。
DROP INDEX IF EXISTS idx_approval_ext;
ALTER TABLE tbl_approval DROP COLUMN IF EXISTS external_task_id;
