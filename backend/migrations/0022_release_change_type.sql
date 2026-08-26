-- 0022: 升级单变更类型 —— dml(数据订正)/ ddl(结构变更).
--
-- 一张单只能是一种类型:审批人按类型评估风险(DDL 看锁表与回滚方案,DML 看
-- 影响行数与备份),混装的单让两种评估都失效。提交时声明或由内容推断,声明
-- 与内容不符、或两类语句同单,都在提交时拒绝(service.releaseChangeType)。
-- 存量单回填为空串:历史单据没有被校验过,补一个猜测的标签只会伪造确定性。
ALTER TABLE tbl_release ADD COLUMN change_type VARCHAR(8) NOT NULL DEFAULT '';
