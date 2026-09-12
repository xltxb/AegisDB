-- 0036: webhook 密钥加密落库,列宽跟着放大。
--
-- ## 为什么
--
-- 同类的 approval.external.token 一直是加密存的,webhook 的密钥却是明文。一份库备份、
-- 一次配错的只读账号,拿到的就是一枚能冒充这个网关往事件中心推数据的凭据 —— 而事件
-- 中心那边没有别的办法分辨那到底是不是我们发的。
--
-- ## 为什么要动列宽
--
-- 密文是 AES-256-GCM 加 base64,比明文长出六十多个字符。原来的 VARCHAR(128) 装得下
-- 一枚短密钥的密文,装不下长的 —— 而超长的后果在 STRICT 模式的 MySQL 上是保存直接
-- 报错,在非 STRICT 上更糟:**静默截断**,于是存下来的是一段解不开的密文,推送从此
-- 全部 401,而界面上那枚密钥看起来配得好好的。255 与 tbl_connection.password 对齐。
--
-- ## 存量怎么办
--
-- 不动。crypto.DecryptSecret 认不出前缀就原样返回(legacy plaintext),所以旧的明文
-- 密钥继续可用;下一次在界面上保存时自然变成密文。这条迁移只保证"新写进来的装得下"。
--
-- MODIFY COLUMN 重复执行不报错,所以这条是幂等的(ADR 0016)。

ALTER TABLE tbl_webhook_config
  MODIFY COLUMN secret VARCHAR(255) NOT NULL DEFAULT '';
