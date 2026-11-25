-- 0004_export_db_name.sql — record the target database on each export job.
--
-- A data export runs a query against a chosen database within the instance; the
-- job is async, so the selected database must persist on the job row for the
-- worker to target it. Existing rows keep NULL/empty (ran against the connection
-- default). Mirrors db_name on the approval and audit tables.
ALTER TABLE tbl_export_job ADD COLUMN db_name VARCHAR(128) AFTER instance;
