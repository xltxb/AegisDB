-- 0015: a script approval references its uploaded file instead of carrying it.
--
-- tbl_approval.command is TEXT — 65,535 bytes on MySQL. A script approval stored
-- the whole script body there, so submitting a real migration (megabytes) failed
-- outright with "Data too long for column 'command'", and the console reported
-- only "提交失败" because the handler dropped the reason.
--
-- Widening the column would have moved the wall rather than removed it: the body
-- also travels through every approvals-list page (the listing selects the row),
-- through the audit row, and through the hash chain. The script is already on
-- disk — the initiator uploaded it — so the approval now records WHERE it is and
-- WHAT IT HASHED TO, and `command` keeps a bounded excerpt for the approver to
-- read.
--
-- script_sha256 is not decoration. Execution re-reads the file and refuses unless
-- it still hashes to this value: between the moment a script is reviewed and the
-- moment it is approved, the file on disk can be replaced, and without the digest
-- the gateway would execute something nobody approved.
--
-- 0 / '' mean "the body is in command", which is every row written before this
-- and every non-script approval.
ALTER TABLE tbl_approval
  ADD COLUMN script_upload_id BIGINT      NOT NULL DEFAULT 0,
  ADD COLUMN script_sha256    VARCHAR(64) NOT NULL DEFAULT '';
