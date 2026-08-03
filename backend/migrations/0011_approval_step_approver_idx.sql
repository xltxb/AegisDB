-- 0011: index the approval-step approver lookup.
--
-- Listing approvals asks "which tickets is this person an approver on", which
-- scanned tbl_approval_step in full on every request to the approvals page. The
-- same query now runs as a subquery for the paged listing, so it executes on
-- every page turn as well (ED13).
CREATE INDEX idx_approval_step_approver ON tbl_approval_step (approver_id);
