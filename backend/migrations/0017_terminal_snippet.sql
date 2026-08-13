-- 0017: terminal snippets — short scripts an operator saves and binds to a hotkey.
--
-- A snippet holds text and nothing more. It is NOT a saved permission and NOT a
-- saved verdict: pressing the hotkey submits the text through the same path as
-- typing it, so the capability matrix, the high-risk dictionary and strict mode
-- judge it against the instance it is actually aimed at, at the moment it fires.
-- That distinction is the whole reason there is no risk or approval column here.
-- A verdict reached on a dev instance last month must never be what decides
-- whether the same keystroke may run on prod today.
--
-- slot is the hotkey 1-9, 0 meaning "saved but unbound". There is deliberately NO
-- unique key over (user_id, slot): 0 is the common value and many rows hold it at
-- once, so a unique key would have to store unbound as NULL — and GORM writes 0,
-- not NULL, for an int field, so every second unbound snippet would collide on
-- MySQL while sqlite (AutoMigrate, no such key) accepted it. That is the shape of
-- bug this schema is worst at surfacing: it appears only in production, only on
-- the second row. Binding a taken slot instead releases the previous holder, in
-- the same transaction as the write — see repository.SaveSnippet.
--
-- body is TEXT (65,535 bytes) and the service caps input at 8,192 BYTES. The cap
-- is small on purpose: this is a shortcut, not a migration. Big scripts go
-- through the upload path, which streams the file and re-reads it server-side
-- rather than carrying it in a row.
CREATE TABLE IF NOT EXISTS tbl_terminal_snippet (
  id          BIGINT       NOT NULL AUTO_INCREMENT,
  user_id     BIGINT       NOT NULL,
  name        VARCHAR(64)  NOT NULL,
  body        TEXT         NOT NULL,
  slot        INT          NOT NULL DEFAULT 0,
  created_at  DATETIME(3)      NULL,
  updated_at  DATETIME(3)      NULL,
  PRIMARY KEY (id),
  KEY idx_snippet_user (user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
