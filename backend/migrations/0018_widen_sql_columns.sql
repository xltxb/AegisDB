-- 0018_widen_sql_columns.sql — widen every column that stores user-submitted SQL.
--
-- TEXT caps at 64KB on MySQL. A legitimately long export query (an IN-list of a
-- few thousand ids is enough) failed submission with the raw driver error
-- "Error 1406: Data too long for column 'sql'"; the async channel, the audit
-- trail (which records the FULL query on purpose — EX5) and approval tickets
-- carry the same class of text and were one long statement away from the same
-- failure. MEDIUMTEXT (16MB) matches the async job log's existing choice; the
-- service layer refuses anything larger with an actionable message.
ALTER TABLE tbl_export_job MODIFY `sql` MEDIUMTEXT;
ALTER TABLE tbl_async_job  MODIFY `sql` MEDIUMTEXT;
ALTER TABLE tbl_audit      MODIFY command MEDIUMTEXT NOT NULL;
ALTER TABLE tbl_approval   MODIFY command MEDIUMTEXT NOT NULL;
