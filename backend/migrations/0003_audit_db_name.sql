-- 0003_audit_db_name.sql — record the target database on each audit row.
--
-- Executed and risk (intercepted/approval-pending) commands must capture WHICH
-- database within the instance they ran against, not just the instance. The
-- approval table already carries db_name; this backfills the same on the audit
-- log. Existing rows keep NULL/empty (the target DB was not recorded then).
ALTER TABLE tbl_audit_log ADD COLUMN db_name VARCHAR(128) AFTER instance;
