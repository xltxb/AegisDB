-- 0009: persist the verdict that authorised an async job.
--
-- The worker audits when the job finishes, which can be an hour after it was
-- submitted, and it had no access to the verdict — so it recorded every async
-- execution as "mid" regardless of what the engine actually decided, making the
-- risk column useless for filtering. The dictionary may also have changed in the
-- meantime, so the level worth recording is the one that permitted the run.
ALTER TABLE tbl_async_job ADD COLUMN risk VARCHAR(16) NOT NULL DEFAULT '';
