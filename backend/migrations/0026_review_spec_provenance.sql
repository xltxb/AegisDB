-- 0026: 规范审查规则的**出处**。
--
-- 规则库这一版是按公司四份规范(docs/ 下 MySQL / TiDB / Oracle / Huawei DWS 規範)
-- 重写的。每条规则记下它出自哪一级、哪一节,理由有两条:
--
--   1. 一条规则被拦下来的时候,人要能回去读原文。"禁止 NOT IN (子查询)"没有出处,
--      看起来就像平台的个人偏好;标上"DWS §11.1",它就是一条能去查、能去争的公司
--      规定。
--   2. 库里并非每条规则都出自规范 —— 有些是规范没写、平台自带的防护。空出处把这
--      件事说清楚,免得有人拿平台的默认值当规范原文去引用。
--
-- spec 与 level 刻意分成两列:level 是这条发现在本平台值多少钱(error 拦发布),
-- spec 是规范怎么定性这条要求。运维把某条降成告警是运营决定,不等于改了公司规范;
-- 合成一列会让这两件事看起来是一回事。
ALTER TABLE tbl_sql_review_rule ADD COLUMN spec VARCHAR(16) NOT NULL DEFAULT '';
ALTER TABLE tbl_sql_review_rule ADD COLUMN spec_ref VARCHAR(128) NOT NULL DEFAULT '';
