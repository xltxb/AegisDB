# 15 · 终端左侧树:函数/存储过程/包/触发器 浏览与源码查看

Status: ready-for-human（已实现，待真实环境验收）

## 需求

Web 命令行左侧数据库树在"表"之外展开**函数、存储过程、包**等程序对象，并可点开查看
其源码。Oracle 要求对齐 sqlcl 的能力，其他数据源做同等功能。

## 实现说明

SQLcl 是 Oracle 的 Java 客户端工具,网关是单二进制 Go 服务,不外挂进程;其 `ddl` 能力
的本质是读目录视图(DBMS_METADATA/ALL_SOURCE),网关按同样方式统一实现,各引擎等价:

| 引擎家族 | 对象清单 | 源码 |
|---|---|---|
| MySQL/MariaDB/TiDB/PolarDB | information_schema.routines + triggers(按库) | SHOW CREATE FUNCTION/PROCEDURE/TRIGGER(第 2 列;标识符白名单校验+反引号) |
| PostgreSQL/DWS/GaussDB | pg_proc×pg_namespace(按 schema;prokind 区分 function/procedure,老版本无 prokind 自动降级) | pg_get_functiondef(全部重载拼接) |
| Oracle | all_objects(按 owner;FUNCTION/PROCEDURE/PACKAGE) | all_source 按行拼接,包=spec+body,前缀 CREATE OR REPLACE |
| SQLite | sqlite_master 触发器 | sqlite_master.sql |

## 改动

- **backend/internal/gateway/objects.go**(新):`RealObjects` / `RealObjectSource`,15s 目录查询超时,
  `objectIdentRe` 白名单挡住无法参数绑定处(SHOW CREATE)的注入。
- **service/admin.go**:`ConnectionObjects` / `ConnectionObjectSource` — 与 schema 树同一访问校验
  (canAccessConn);模拟连接(无凭据)返回按引擎家族合成的演示对象与标明"模拟连接"的源码。
- **handler + router**:`GET /connections/:id/objects?scope=` 与
  `GET /connections/:id/object-source?scope=&type=&name=`,menu("terminal") 门禁。
  scope = 库(MySQL)/schema(PG 系)/owner(Oracle),SQLite 忽略。
- **frontend**:`ObjectGroups.vue`(新,分组渲染,每节点独立折叠) 挂在 DbTree 的平铺库级与
  PG schema 级两处;懒加载(展开库/схema 时拉取);点击对象弹只读源码查看器;zh/en 文案。

## 回归测试(bootstrap/objects_test.go)

- 真实 SQLite:建触发器 → objects 列出、object-source 返回 CREATE TRIGGER 全文;
  非法对象名(`` x`; DROP TABLE … ``)被拒。
- 模拟连接:返回演示对象集,源码自我声明"模拟连接"。

后端全量测试 + 前端构建通过。同轮修了 issue 13 回归测试的偶发超时
(64B 分卷导致第二个导出任务写数千个文件,恢复默认分卷后再跑不设上限段)。

## 待真实环境验收

- MySQL:SHOW CREATE 需要账号有查看例程定义的权限,否则返回明确报错("账号可能缺少查看权限")。
- Oracle/DWS:目录视图行为以真实实例为准(DWS 无 prokind 的降级路径已内置)。
