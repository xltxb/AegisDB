# ADR-0004:按引擎可插拔的判定方言(Dialect)

- 状态:已接受(2026-08-01)
- 相关:ADR-0003(外部飞书审批)、`issue.md` 第五轮 ER3

## 背景

网关的三层闸门(能力矩阵 · 高危命令字典 · 严格模式兜底)全部以两个概念表达:
**动词(verb)** 与 **能力维度(capability)**。这两个概念是通用的,但**如何从命令文本里
把它们读出来**不是——SQL 从首个关键字得到,MongoDB 从方法链得到。

此前判定层直接内联了 SQL 的读法(`ParseVerb` / `MapVerbToCapability` /
`NoWhere` / `IsRead` / `SplitStatements`)。把 MongoDB 接进来时,这套读法会给出
**带危险偏向的错误答案**:

```
db.orders.drop()
  → ParseVerb 取首词 "db"
  → 未知动词落到 select 维度(第五轮 ER3 已确认该分支的语义)
  → 判为无害读操作,不拦截、不送审
```

也就是说,单纯把 MongoDB 加进引擎下拉,会造出一条「删集合被当成查询」的通道。

## 决策

引入 `gateway.Dialect` 接口,把**命令的读法**做成可插拔,而**策略保持唯一**:

```go
type Dialect interface {
    Name() string
    Split(cmd string) []string          // 批量粘贴的切分
    Verb(cmd string) string             // 规范化动词(大写)
    Capability(verb string) string      // select | write | ddl | grant
    IsRead(cmd string) bool             // 执行路由 + 审计记账
    UnscopedMutation(cmd string) bool   // 严格模式:全表/全集合写
}
```

`DialectFor(engine)` 按引擎家族选择实现;`RiskEngine.EvaluateFor(roleIDs, engine, env, cmd)`
用它取动词与维度,**三层判定逻辑本身一行未改**。`EvaluateRoles` 保留为传空引擎的
包装,故 SQL 侧行为与既有测试完全不变。

### MongoDB 方言的几个取舍

- **链式命令按最危险的一环判定**,不按第一环:`db.getCollection("orders").drop()`
  以查找辅助方法开头、以销毁集合结尾。导航类方法(`getCollection`/`getSiblingDB`/
  `limit`/`sort` 等)显式登记为 select,避免抬高整条链的判定。
- **未识别的方法一律不算读**。`Capability` 对未知方法返回 `write`,`Verb` 让未知方法
  直接胜出——这正是本方言存在的理由,不能重蹈「未知即无害」。
- **严格模式的等价物**是「多文档写 + 空过滤器」:`deleteMany({})` / `updateMany({}, …)`
  / `remove({})` 会清空整个集合,对应 SQL 的「DELETE 无 WHERE」。`deleteOne({})`
  按定义只影响一条,不算。
- **切分**尊重字符串、文档 `{}` 与数组 `[]`,分号/换行只在顶层生效。

## 后果

- MongoDB 现在**可被正确判定**,但**仍不可执行**:没有驱动。二者是两个独立属性,
  且这个缺口是显式的——`engineDriver` 对 mongo 返回 not-ok,
  `TestEngineDriver_MongoIsJudgedButNotExecutable` 锁定该事实。
- **MongoDB 暂不进入引擎下拉**。前端 `lib/engines` 有一条测试断言「目录里每个可选
  引擎都必须能被网关驱动」,否则连接会保存成功却退化为模拟连接,运维以为实例已接入。
  待执行链路落地后再一并放开。
- 新增引擎只需实现一个 Dialect,不必再碰判定层。

## 未完成

执行链路:Mongo shell 语法(`db.coll.method(args)`)不能直接交给驱动,需要把参数解析
成 BSON 并映射到 `RunCommand`,还要把结果映射进现有的 `ExecResult{Columns, Data}`
形状、以及为数据库树提供 introspection。这是独立的一块工作,不在本 ADR 范围内。
