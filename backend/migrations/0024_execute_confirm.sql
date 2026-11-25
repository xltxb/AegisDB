-- 0024: 执行变更的人工闸 —— execute 阶段必须由人为确认点击后才落库.
--
-- 审批回答"这个变更可不可以做",执行确认回答"现在做"。变更窗口、业务低峰、
-- 上下游是否就绪,只有到点的人知道;审批通过的单在队列里自动落库,等于把
-- "何时执行"交给了调度器。
--
-- confirmed_by 记录点击的人(空 = 尚未确认,execute 到达即停 waiting)。谁能点:
-- 创建者本人(审批已由别人把关,执行时机归发起人)或审批角色。确认动作按
-- 决策语义入审计链:actor = 变更归属人,operator = 点击的人。
ALTER TABLE tbl_release_stage ADD COLUMN confirmed_by VARCHAR(64) NOT NULL DEFAULT '';
