-- 0064: 两处列宽比模型窄,生产写入超长值会报 1406(严格模式)或被静默截断(非严格模式)。
--
-- 2026-11-16 把测试的存储库改跑真 MySQL 评估时查出。开发和测试一直是 SQLite —— GORM 按模型
-- 建表,SQLite 也不检查 VARCHAR 长度 —— 所以这两处从来没有在测试里出现过。现在有
-- TestMigrations_ColumnWidthsCoverModels_RealMySQL 在真 MySQL 上逐列比对模型与迁移后的表。
--
-- ## tbl_user.mfa_secret:VARCHAR(64) → VARCHAR(255)
--
-- 0001 按"base32 的 TOTP 种子"定的 64。后来种子改为静态加密后入库(L-5,模型 size:255),
-- 密文(enc:v2 前缀 + nonce + 种子 + 认证标签的 base64)超过 64,迁移没跟上:
--   - 严格模式(MySQL 8 默认):绑定 TOTP 直接失败;
--   - 非严格模式:密文被截断,读出时解密失败,User 按"没开 MFA"处理 —— 悄悄绕过了 MFA。
-- 非严格模式下已经截断的种子无法从库里恢复,部署后用下面这句找出来,让这些用户重新绑定:
--   SELECT id, email FROM tbl_user WHERE CHAR_LENGTH(mfa_secret) = 64;
--
-- ## tbl_connection.env:VARCHAR(16) → VARCHAR(32)
--
-- 0013 已经把它加宽到 32(环境编码可以由管理员自建,与 tbl_environment.code 同宽)。0034 对齐
-- 排序规则时照抄了 0001 的 VARCHAR(16),把它改了回去:编码超过 16 个字符的环境挂不上实例。
-- 排序规则保持 0034 定下的 utf8mb4_unicode_ci(否则跨表 JOIN 又是 1267)。
--
-- 两条都只加宽,不改已有数据;小表,重建代价可以忽略。
ALTER TABLE tbl_user
  MODIFY COLUMN mfa_secret VARCHAR(255) NULL;

ALTER TABLE tbl_connection
  MODIFY COLUMN env VARCHAR(32) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL;
