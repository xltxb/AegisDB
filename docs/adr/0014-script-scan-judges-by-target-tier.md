# ADR 0014:脚本扫描按目标实例的分层判定

- 状态:已采纳
- 日期:2026-09-07
- 相关:`backend/internal/service/gateway.go`(ScanScript)、`internal/handler/terminal.go`、
  `internal/model/model.go`(EnvTier.ScanBaseline)

## 背景

一份 105 条语句的菜单初始化脚本(全是 `INSERT INTO sys_menu`)通过 `\i` 上传到 **UAT**
执行,被判成 **高危 P1**,整脚本提交审批。

查下来是两件事叠加:

1. **脚本扫描用的是固定的"扫描基准分层"**,发行默认是 prod。目标是 UAT 也照样按 prod
   的字典量。
2. **高危字典对语句全文做单词匹配,不排除字符串字面量**。脚本里的权限串
   `'system:menu:delete'`、`'system:role:delete'`、`'system:user:delete'` 命中了 DELETE,
   而 DELETE 在 prod 是 high。

本 ADR 只处理第一件。第二件(字符串字面量误报)另议 —— 那要在"漏报动态 SQL"和"误报
数据里的关键词"之间取舍。

## 决定

扫描按**目标实例所属分层**判定,`ScanScript` 从收 `(filename, content)` 改为收
`(connID, filename, content)`,分层由 `tierCodeOf(conn)` 解析。

固定基准原本的理由是:"扫描发生在上传时,目标未必已经选定;按 dev 宽松地判,同一个文件
转头就能带着'已扫干净'去 prod。"

这条理由在实际接口上不成立:

- `/scripts/scan` 与 `/scripts/execute` 收的是**同一个 connectionId**,目标一直是知道的;
- 执行时每条语句本来就按目标分层重判一次(`ExecuteSafeScript` → `execJudged`),
  扫描结论并不能替代那道闸。

所以那道固定镜头没有拦住任何东西,只是让报告说的和将要发生的事对不上。扫描报告的职责
是描述"这个脚本在这台实例上会怎样",它必须和执行用同一把尺子。

## 失败方向

目标实例或它的分层解析不出来时,**拒绝扫描**,不退回任何分层。理由与 ED3 一致:拿空分层
去扫,字典一行都匹配不到,每条语句(DROP TABLE 也包括)都会报安全,而且没有任何地方会
失败 —— 而一份全安全的扫描结果正是脚本免审直接执行的依据。

multipart 上传分支同样要带 `connectionId` 表单字段;取不到就是 0,连接查不到,扫描拒绝。

## ScanBaseline 标志的去留

它没有被删,因为还担着两个与扫描无关的职责:

- 新增高危命令时,按最严一档预填该分层的等级(`defaultRiskLevel`);
- 持有它的分层不可删除。

两者的实质都是"这是最严的参照分层",所以标志保留,但界面标签从「脚本扫描基准」改为
「基准分层」,提示语写明扫描已不再看它 —— 一个名字还在说自己管着某件事、实际却不管的
开关,比没有更糟。
