# ADR 0013:无 WHERE 拦截改为按分层开关

- 状态:已采纳
- 日期:2026-09-04
- 相关:`backend/internal/gateway/risk.go`、`internal/model/model.go`、
  `migrations/0030_env_tier_strict_nowhere.sql`、
  `internal/bootstrap/strict_nowhere_backfill.go`

## 背景

判定有三层。前两层按**分层标签**存:能力矩阵是角色 × 能力 × 分层,高危命令字典是
命令 × 分层。第三层 —— 拦截不带 WHERE 的 DELETE / UPDATE —— 却是一个进程内的全局
布尔量,来自 `gateway.strict_mode`,于是它是唯一一道瞄不准的闸。

后果不是理论上的。dev 分层特意把整本字典设成 `off`,意思很明确:在开发库上清空一张
临时表是日常工作。可严格模式照样把那条 DELETE 判成 high 并要求审批,级别与 PROD 一样。
想让 dev 放行,唯一的办法是把这一层整个关掉 —— 连 PROD 一起关。运维要的是"分环境
开关",能拿到的只有"要么都开、要么都关"。

顺带查出来的:那个运行时开关**从不落库**。`SetStrict` 只改内存,重启就回到配置文件的值。
界面上关掉它,下一次重启它自己就回来了。

## 决定

`StrictNoWhere` 作为一个布尔列落在 `tbl_env_tier` 上,与 `require_mfa`、`danger_banner`
同构;引擎通过 `Store.StrictNoWhere(tier)` 按分层读它。全局开关退役 —— 不是留着当总闸,
而是删掉。

不做"全局总闸 AND 分层开关"的两级结构。那种结构会让"我在 PROD 上开了,为什么不生效"
成为一个需要翻两个页面才能回答的问题,而这一层的全部意义就是让人一眼看出哪个环境拦、
哪个不拦。

分层等级也没有做成 off / mid / high 三档(字典是那样的)。这一层回答的是"全表写要不要
拦",是个是非题;真要分轻重,该动的是字典。

## 迁移

列默认 TRUE,即所有既有分层继续拦。这是**保持现状**的方向:发行默认 `strict_mode: true`,
而运行时的改动从不持久化,所以升级前实际生效的状态就是"处处都开"。

少数显式把 `strict_mode` 设成 false 的部署,由 `backfillStrictNoWhere` 在迁移后按配置
回填成 false —— SQL 读不到 YAML,只能在 Go 侧折。这段回填**只跑一次**,由一行 setting
守卫:每次启动都重跑的话,运维把 PROD 重新打开,下次重启就被抹掉,开关会看着能改、其实
存不住。

新装(seed)与升级在 dev 上不一致:seed 把 dev 设成 false(产品意图),迁移把 dev 保留为
true(不擅自放松已经在生效的闸)。这个不一致是刻意的 —— 迁移没有资格替运维决定放开一道
正在拦的闸,而取消勾选只需要一次点击。

## 一个 GORM 陷阱

带 `default` 标签的字段,零值在 Create 时会被**省略**,数据库默认值随后写入,并且 GORM
会把那个默认值**回写进结构体**。于是"新建一个关掉该开关的分层"存下来是开着的,而且
`t.StrictNoWhere` 自己也变成了 true —— 开关看着保存成功,其实没有。`Select("*")` 在本项目
的 sqlite 驱动上并不能改变这一点。

写入路径因此是:先把目标值取出来,再 Create,再用一条点名该列的 UPDATE 落定。三处创建
分层的地方(seed、backfill、`CreateEnvTierFrom`)都要这么做,回归测试
`TestStrictNoWhere_SurvivesTierCreation` 钉住它。
