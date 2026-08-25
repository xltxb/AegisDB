# 18 · 数据导出提交报 "Data too long for column 'sql'"

Status: ready-for-human（已修复，升级需跑 migrate）

## 现象

数据导出提交失败:`提交失败:Error 1406 (22001): Data too long for column 'sql' at row 1`。

## 根因

网关元数据库(MySQL)中 `tbl_export_job.sql` 为 TEXT(上限 64KB)。超长导出 SQL(常见:
几千个 ID 的 IN 列表)INSERT 被 MySQL 拒绝,原始驱动报错直接抛给了用户。同类列还有三处
一样的隐患:`tbl_async_job.sql`、`tbl_audit.command`(审计有意记全文,EX5)、
`tbl_approval.command`。

## 修复

- **迁移 0018**:四列统一 MODIFY 为 MEDIUMTEXT(16MB,与 async 日志列同级);模型 gorm
  标签同步 mediumtext(sqlite 开发环境无影响)。
- **服务层守卫**:导出/异步两个提交通道对 >15MB 的 SQL 直接拒绝,报错写明大小与上限并
  给出替代路径("改用脚本上传通道,或用临时表替代超长 IN 列表"),不再漏到数据库层报
  神秘驱动错误。

## 回归测试(export_longsql_test.go)

- ~120KB 的单条只读 IN 列表 → 提交成功(旧 64KB 上限已放开);
- >15MB → 明确拒绝且报错含"SQL 过长"。

## 升级注意

**本次有表结构变更**:升级需按标准流程 停服 → `./vela-gateway migrate`(应用 0018)→ 起服。
MEDIUMTEXT 放宽为原地元数据变更,对存量数据无影响。
