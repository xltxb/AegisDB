-- 0063: 审计行记下**这一下是从网关的哪个入口进来的** —— 来源。
--
-- 同样是一条 SELECT,从数据查询页来(按授权、只读守卫)和从 Web 命令行来(按角色、判定
-- 层)是两件事,而审计页此前完全看不出区别。取值:query / terminal / script / export /
-- approval / release / api / auth / admin / system(见 model.AuditSources)。
--
-- 老行留空,**不回填** —— 与 0039 / 0040 / 0041 同一条:这是一张哈希链审计表,按命令
-- 文本去猜当时从哪个页面进来,等于在证据上动手。空 = 这一行写下时还没有这一列,界面
-- 显示「未知 / 旧记录」。
--
-- 新行起这一列进哈希载荷(service.auditHashPayload 第六代的 "source" 键):改了来源就
-- 破链。老行仍按它写下那天的键集(第一至第五代)校验,不受影响。
--
-- 列名用 entry_source 而不是 source:SOURCE 在 MySQL 里是关键字(SOURCE_* 一族),各引擎
-- 对它保留与否不一,换个名字省掉所有引号问题。接口上仍叫 source。
--
-- 索引与 0040 的 idx_audit_action 同形:这一列是**精确**筛的(下拉选来源),总和时间窗
-- 一起用。
ALTER TABLE tbl_audit_log ADD COLUMN entry_source VARCHAR(16) NOT NULL DEFAULT '';

CREATE INDEX idx_audit_source ON tbl_audit_log (entry_source, occurred_at);
