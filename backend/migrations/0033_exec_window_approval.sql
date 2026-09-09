-- 0033: 执行窗口(「班车」)改为**申请 → 审批 → 生效**。
--
-- 在此之前,窗口是管理员直接建的,建完立刻生效 —— 与高危命令字典、能力矩阵同级。
-- 那条理由是成立的:它们都是管控配置。但窗口和它们有一处不同 —— 后两者是把闸门
-- **调紧或调松的规则**,而窗口是一次性地**把闸门打开一段时间**,并且开着的那几个
-- 小时里,本该有人签字的中/高风险语句会一条不落地直接下发。
--
-- 一个人就能打开这样一扇门,等于给了他一条"先开窗口、再从窗口里进去"的路,而全程
-- 没有第二个人看过。所以现在:申请人提申请,单子走审批链,**通过之后窗口才开始生效**。
--
-- 落在数据上是三件事:
--
--  1. tbl_exec_window.status —— pending / approved / rejected。
--     判定只认 approved(见 repository.ExecWindowsFor)。这是本次改动的**闸门本身**:
--     没有它,新建的窗口在等待审批期间就已经在放行语句了。
--
--  2. tbl_exec_window.approval_id / ap_no —— 指回那张单,让"这扇门凭什么开着"在
--     界面上和审计里都能一路点回去。
--
--  3. tbl_approval.window_id —— 反向指回窗口。它和 release_id / export_job_id 起的是
--     同一个作用:把这张单挡在**手动执行**那条路之外(见 canExecuteApproved)。
--     窗口单的 Command 是一句描述,不是可执行语句;发起人若能点"执行",网关会把那句
--     描述当成 SQL 发给数据库。
--
-- 存量行一律置为 approved,不是 pending。
--
-- 它们是在"管理员直接建即生效"的规则下建的,当时那就是有效的授权;改成 pending 会
-- 在升级的那一刻**静默关掉**运维今晚可能正指望着的班车,而那不是纠正历史,是改变事实。
-- 从这次升级之后新建的窗口才走审批 —— 新规则约束新申请。

ALTER TABLE tbl_exec_window ADD COLUMN status      VARCHAR(16)  NOT NULL DEFAULT 'pending';
ALTER TABLE tbl_exec_window ADD COLUMN approval_id BIGINT       NOT NULL DEFAULT 0;
ALTER TABLE tbl_exec_window ADD COLUMN ap_no       VARCHAR(32)  NULL;
ALTER TABLE tbl_exec_window ADD COLUMN decided_at  DATETIME(3)  NULL;

-- 存量窗口保持原样有效 —— 理由见上。
UPDATE tbl_exec_window SET status = 'approved' WHERE status = 'pending';

ALTER TABLE tbl_approval ADD COLUMN window_id BIGINT NOT NULL DEFAULT 0;
CREATE INDEX idx_approval_window ON tbl_approval (window_id);
