-- 0005_export_password_widen.sql — widen the export archive password column.
--
-- The archive password is stored AES-encrypted at rest (R25): base64 of a 12-byte
-- nonce + ciphertext + 16-byte tag, plus a scheme prefix — ~68 chars for a 20-char
-- password. The original VARCHAR(64) was too small, so on MySQL strict mode the
-- final "job done" UPDATE failed with 1406 (Data too long) and the job was left
-- stuck in 'running'. Widen to 128. (SQLite ignores the length, so dev was fine.)
ALTER TABLE tbl_export_job MODIFY password VARCHAR(128);
