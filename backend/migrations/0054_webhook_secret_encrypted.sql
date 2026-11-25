-- 0054: Webhook 推送口令改为加密存储(2026-10 安全审计 M4)。
--
-- 密文(前缀 + base64(nonce + 密文 + tag))比明文长,128 只容得下约 62 字符的明文口令。
-- 放宽到 512。已有的明文行照常可读,下次保存时变成密文。单条 ALTER,可安全重跑(ADR 0016)。
ALTER TABLE tbl_webhook_config
  MODIFY COLUMN secret VARCHAR(512) NOT NULL;
