# 16 · 树上点表看 DDL + Web 终端 SQL 语法高亮

Status: ready-for-human（已实现，待真实环境验收）

## 需求

1. 左侧数据库树点击表名 → 弹窗展示建表 DDL。
2. Web 终端输入的 SQL 做语法高亮。

## 实现

### 表 DDL（复用 issue 15 的 object-source 通道，新增 type=table）

| 引擎家族 | DDL 来源 |
|---|---|
| MySQL 系 | `SHOW CREATE TABLE`(定义列改为按列名定位——TABLE 在第 1 列、例程在第 2 列,统一匹配 "Create …"/"SQL Original Statement") |
| PostgreSQL 系 | information_schema.columns 重建 CREATE TABLE + pg_indexes 的真实 indexdef(PG 无 SHOW CREATE TABLE,注释里说明是重建) |
| Oracle | `DBMS_METADATA.GET_DDL`(即 sqlcl ddl 同源);无权限时降级 ALL_TAB_COLUMNS 重建 |
| SQLite | sqlite_master 存储的 CREATE 语句 + 该表全部命名索引 |

前端:两处表行(平铺库级 / PG schema 级)可点击,悬停提示"查看建表 DDL",复用源码查看弹窗。
模拟连接返回演示 CREATE TABLE。

### 语法高亮（frontend/src/lib/sqlHighlight.ts）

一个 tokenizer(关键字/字符串/数字/注释/反斜杠元命令),两个渲染器:
- `highlightSqlAnsi` → xterm 输入行。接进 LineEditor:`redraw()` 用高亮文本(只加颜色,
  可见长度不变,光标数学不受影响);**保留 8e9d1bc 的无闪烁快速路径**——词中字符仍走
  追加路径,只在词边界字符(空格/分号/括号等)触发整行重绘,刚敲完的词此时上色。
- `highlightSqlHtml` → DDL/源码查看弹窗。先逐字符 HTML 转义再包 span(v-html 安全),
  token 颜色走设计 token(:deep 样式)。

## 回归测试

- objects_test 扩展:真实 SQLite `object-source?type=table` 返回 CREATE TABLE + 命名索引。
- 后端全量测试 + 前端构建通过。

## 待真实环境验收

- MySQL SHOW CREATE TABLE / Oracle DBMS_METADATA 在真实实例上的权限与输出;
- 终端高亮在超长语句、多行续行、中文注释下的观感。
