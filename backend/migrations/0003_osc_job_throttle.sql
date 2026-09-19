-- 这一次迁移到底限没限流,以及为什么。见 ADR 0011。
--
-- 发起页面上的 caveats 说的是**能力**(这套东西有没有限流),这一列记的是**事实**
-- (这一次到底限没限流)。两者会不一致:限流的前提是主库自己报得出从库,而那取决于
-- 目标实例当时的样子 —— 单机、从库没配 report_host、复制断着,都会让一次能力上
-- 支持限流的迁移实际跑在不限流的状态下。
--
-- 事后回答"那次把从库拖垮的迁移,当时限流开着吗",只有跑它的那个进程知道答案,
-- 所以答案要在当时就写下来。装人话而不是布尔值:没开起来的原因不止一种,
-- 而界面是原样渲染给人看的。
ALTER TABLE tbl_osc_job ADD COLUMN IF NOT EXISTS throttle VARCHAR(255) NOT NULL DEFAULT '';

-- 同一件事的布尔面。界面按它上色,不去解析上面那句人话 —— 后端改一次文案,
-- 界面上的警示就悄悄没了,而它恰恰是最不该丢的那一条。
ALTER TABLE tbl_osc_job ADD COLUMN IF NOT EXISTS throttled BOOLEAN NOT NULL DEFAULT FALSE;
