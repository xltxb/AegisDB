-- 0039: 审计行记下它自己是**哪一类事件**。
--
-- 事件类型(intercept / approve / exec / login)此前只是一个参数:每个调用点传给
-- recordAudit,用来决定 webhook 要不要发,然后就丢掉了 —— 真正写库的那段根本没收到它。
-- 于是这一列不存在,而它带来三个具体后果:
--
--   审计页与导出只能按风险和时间筛      没法回答"只看登录"或"只看被拦下来的"。
--   推出去的事件比存下来的知道得多      webhook 载荷里有 event_type,审计表里没有,
--                                       两边对不上。
--   投递失败之后补不回来                重试用光这条就没了,而 tbl_webhook_delivery
--                                       只记了事件名、没有指回审计行的字段。
--
-- 而它**推不出来**:(risk, result) 区分不开这几类 —— intercept 会配 rejected 也会配
-- pending,approve 同样两者都配。想在读取时倒推就是猜。
--
-- 老行留空,不回填。空表示"当时没有记",这是事实;按 result 猜一个填进去,等于在一张
-- 哈希链审计表里造证据 —— 那比缺这一列糟得多。
--
-- 新行起,这一列进哈希载荷(service.appendAudit 里的 "event" 键):一个改了也不破链的
-- 字段不算证据。老行的哈希不重算,与当初加 env / tier 时同一个做法。
ALTER TABLE tbl_audit_log ADD COLUMN event_type VARCHAR(16) NOT NULL DEFAULT '';

-- 按类型筛是这一列存在的第一个理由,而审计表是按时间倒序翻的,所以联合索引把时间带上。
CREATE INDEX idx_audit_event ON tbl_audit_log (event_type, occurred_at);
