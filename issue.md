# Vela 数据库网关 — 代码审查问题清单

- 首次审查:2026-07-10;最新一轮(第四轮):2026-07-16 ~ 07-17
- 范围:后端(Go / Gin / GORM)+ 前端(Vue3 / TS)+ 迁移 / 配置 / 部署
- 方式:每轮 5 个并行子代理分维度审查(安全授权 / 后端并发与正确性 / 真连库执行与导出加密 / 前端 / 数据模型与部署),结果去重合并
- 说明:**已按 TDD 逐轮修复,详见下方各轮「修复状态 / 修复记录」。** 级别:严重 / 高 / 中 / 低。

## 概览

四轮审查累计发现并处置的问题(每轮均为红→绿→回归的 TDD 修复;详见对应「修复状态」段):

| 轮次 | 日期 | 严重 | 高 | 中 | 低 | 处置 |
| --- | --- | --- | --- | --- | --- | --- |
| 第一轮 | 2026-07-10 | 4 | 14 | 16 | 13 | 已修复(部分设计权衡保留) |
| 第二轮 R1–R29 | 2026-07-14~15 | 3 | 11 | 15 | — | 已修复 |
| 第三轮 V1–V7 | 2026-07-16 | — | 4 | 3 | — | 已修复 |
| 第四轮 A/B/C | 2026-07-16~17 | — | 4(A) | 9(B) | 10(C) | 已修复(23 项) |

**当前状态:** 上述各轮发现项均已闭环(仅少数明确记录的设计权衡/性能项保留);后端 `go vet` + 全量测试通过,前端 `type-check` / `build` 通过。

---

## 修复记录(2026-07-13)

逐条修复,后端 `go build ./...` + 全量测试(含新增 `TestLogoutRevokesToken`/`TestMFA_CodeCannotBeReplayed` 回归)通过,前端 `npm run build` 通过。

**已修复(代码级)**
- 严重:C1(登录改为强制 `CheckPassword` + 仅 `active` 可登录)、C2(审批 `ClaimApproval` 原子认领 pending→approved/rejected,超时 sweep 同样原子化,杜绝重复执行)、C3(连接口令 AES-256-GCM 静态加密 `EncryptSecret`/`DecryptSecret`,存量明文兼容读取)、C4(prod 缺失/占位/过短 JWT 密钥拒绝启动)。
- 高:H1(CORS 只回显白名单 Origin,空列表=同源不发 CORS 头)、H2(`SetTrustedProxies` 默认不信任,XFF 不可伪造)、H3(WS 校验停用 + token 版本)、H4(WS token 改走 `Sec-WebSocket-Protocol` 头)、H6(支持 `RunTLS`,prod 无 TLS 告警)、H7(MySQL `Config.FormatDSN`、PG 转义、Oracle `BuildUrl` + 库名白名单,禁 multiStatements)、H8(导出改 AES-256 zip)、H9(`StripComments` 剥离注释后再解析动词/字典/NoWhere)、H10(`auditMu` 串行化哈希链)、H11(export worker `recover` + 置 failed)、H12(计数器从 `MAX` 序号播种)、H14(prod `auto_migrate:false`,仅 `init` 迁移)。
- 中:M1(token 版本吊销:登出/改密 bump,中间件+WS 校验)、M2(settings 秘钥打码 + 回存哨兵保留)、M3(TOTP 计数器防重放 `ConsumeMFACounter`)、M4(新增 `security.mfaMandatory` 强制未注册用户 prod 前必须绑定 MFA,默认沿用 opt-in)、M5(`RiskEngine.strict` 改 `atomic.Bool`)、M6(sqlite `busy_timeout`+WAL+单写连接)、M8(导出行数上限 `exportMaxRows`)、M9(CSV 公式注入 `csvSanitize`)、M12(管理员口令 ≥12 位且 ≥3 类)、M13(seed 写入错误串联上报)、M14(前端异步统一 try/catch + toast)、M15(高危操作 `confirmAction` 二次确认)、M16(WS 鉴权连败停重连并跳登录)。
- 低:L1(`GenPassword` 拒绝采样去偏)、L2(webhook/飞书 SSRF 校验,prod 禁内网,dev 放行)、L4(导出口令默认打码+按需显示)、L5(登录预填仅 dev)、L6(idle 超时读 `security.idleMinutes` 并以 TTL 封顶)、L12(空菜单重定向自环→登出)、L13(`can()` 改白名单式默认拒绝)。

**部分缓解 / 需权衡(未完全重构)**
- H5(token 存 localStorage):维持现设计,但 M1 token 版本吊销已显著降低泄露影响;彻底改 httpOnly Cookie + CSRF 需较大改造,留待评估。
- H13(手写 DDL 与 AutoMigrate 双轨):H14 已把迁移收敛到显式 `init`(`init` 强制 AutoMigrate,运行态关闭),`0001_init.sql` 同步补齐 `mfa_last_ctr`/`token_version` 列;AutoMigrate 仍为结构权威,彻底以 SQL 为准留待后续。
- M7(分片全量内存缓冲):M8 行数上限已限制单任务规模、间接压低峰值内存;流式写临时文件的重构留待后续。
- M11(compose/config 弱默认):新增 `VELA_WEBHOOK_SECRET` env 覆盖通道;compose 明文口令属本地 dev 默认,prod 走 env。

**暂缓(性能/风格,非正确性/安全阻断)**:L3(每次新建连接)、L8(入队 goroutine)、L9(webhook 重试无 ctx)、L10(本地时区)、L11(collation)。

---

## 第二轮审查(2026-07-14)

对首轮修复 + 生产打包后的现状做并行五维度审查,重点找**修复代码引入的回归**。标 ⚠️ 者为本轮/上轮修复亲手引入。

### 修复状态(2026-07-14,TDD 逐条修)

**已修复 R1–R14(3 严重 + 11 高)**,后端 `go vet` + 全量测试通过、前端 `npm run build` 通过。新增 8 个回归测试:
- R2 `TestEvaluate_ExecutableCommentNotBypassed`、R12 `TestEvaluate_ExplainAnalyzeUsesRealVerb`(risk 包)
- R3 `TestSettings_SavingDoesNotWipeLarkSecret`、R5 `TestTerminalWS_RevokedSessionRejectedMidConnection`、R8 `TestAdmin_PatchUserStatusTakesEffect`、R11 `TestRiskCommands_PartialUpsertKeepsOtherEnvs`、R13 `TestApprovalTimeout_AutoEscalateOnlyOnce`、R10 `TestLoadConfig_RejectsCommittedDevSecretInProd`(bootstrap 黑盒)

要点:R1 改 `defer` 解锁并把 Dispatch 移出锁;R2 `StripComments` 改为**拆封** `/*! */` 而非删除;R12 `ParseVerb` 拆掉 `EXPLAIN [ANALYZE|(…)]` 前缀取真实动词;R3 后端不再回传密钥(改 `secretsSet`/`webhookHasSecret` 布尔位,空值=保持不变),前端改留空+"已配置"提示;R4 给 `/terminal/ws` 挂 `IPAllowlist` 中间件+句柄内 terminal 菜单校验;R5 消息循环每条 exec 重查 token 版本/停用;R6 登出请求显式携带 token(不再依赖已清空的 localStorage);R7 WS 连败先 REST 探测 `/auth/me` 确认 401 才登出;R8 `PatchUser` 改 `Updates(map)` 只写 status/role;R9 `createApproval` 返回错误并向上传播、`maxSeq` 出错记日志;R10 已提交 dev 密钥入黑名单;R11 前端只发选中 env、`alert` 不再映射 off;R13 加 `escalated` 列+原子认领,每单只升级一次;R14 移除 docker-compose 的 initdb 双源、迁移加 MySQL `GET_LOCK` 跨进程锁。

### 修复状态(2026-07-15,TDD 逐条修 R15–R29 中危)

**已修复 R15–R22、R24–R29(13 项)**;**R23(迁移无并发锁)已在 R14 用 MySQL `GET_LOCK` 修复**。后端 `go vet` + 全量测试(含新增 `internal/service` 包)通过、前端 `npm run build`/`type-check` 通过。新增回归测试:
- 后端:R16 `TestApproval_InitiatorCannotSelfApprove`、R19 `TestApproval_DecideAlreadyDecidedIsNotSuccess`、R15 `TestMFA_StepUpCodeCannotAlsoDisableMFA`、R18 `TestExecuteSafeScript_MFAUserWithCodeRunsWholeScript`、R25 `TestExport_PasswordEncryptedAtRest`、R20 `TestCheckDialAddr_BlocksNonPublicTargets`(service 包)、R26 `TestConfig_ProdServe*`、R24 `TestExport_StuckJobsFailedOnStartup`、R29 `TestLogin_FailureIsAudited`/`TestLogin_RateLimitedAfterRepeatedFailures`

要点:R15 `MFADisable` 消费 TOTP 计数器(阻断 step-up 码重放去关 MFA);R16 发起人不得自批(`actor.ID==InitiatorID` 拒绝);R17 去掉 WS `?token=` 查询回退,只走子协议;R18 `ExecuteSafeScript` 脚本级校验一次 MFA、抽出 `execJudged` 逐句执行不再重复 checkMFA;R19 认领失败/非 pending 返回 `ErrAlreadyDecided`(不再谎报 ok:true)+ 执行失败审计记 `warn` 而非 executed;R20 出站客户端禁重定向 + 拨号时 `Control` 校验真实解析 IP(闭合 DNS 重绑定 TOCTOU);R21 401 拦截器 `clearSession()` 重置整个 auth store;R22 秘钥泄露已由 R3 消除(输入改 password + "已配置"提示);R24 `New()` 启动时把残留 running/pending 导出任务标记 failed;R25 CSV 表头也过 `csvSanitize` + 导出口令 `EncryptSecret` 静态加密(列表时解密);R26 prod JWT 强校验从 `LoadConfig` 抽到 `ValidateForServe`(仅服务启动调用,migrate/init 不受阻);R27 WS 空 token 不再 `new WebSocket`(直接 onAuthError);R28 SettingsView/WebhookPanel 保存与开关加 try/catch+toast、乐观翻转失败回滚;R29 登录纳入 IP 白名单 + 失败写审计 + 按来源 IP 限速(5 次锁 5 分钟)。

**说明**:R19 的"认领后 panic 无补偿"需事务支持(执行器为模拟、真实执行返回 error 非 panic,已覆盖 error 路径);R22 的"secret 无法主动清空"属 UX 边角(空值=保持不变),安全泄露已解决。

### 修复状态(2026-07-15,低危项一并清理)

**已修复低危项**(后端 `go vet`+全量测试、前端 `build`/`type-check` 通过;新增 `pkg/sqlutil` 单测、`realdb`/`config` 等测试):
- **build.sh** 第29-30行 `$GO` 加引号(本机裸跑不再挂);**sqlite DSN** 已含 `?` 时用 `&` 追加 pragma(不再丢 busy_timeout/WAL);**audit_log** `prev_hash`/`hash` 模型标签改 `type:char(64)` 与 SQL 一致;**0001 头注释**改为"权威建表源,由 migrate 应用";**docker/systemd/DEPLOY** 补建 vela 用户+解包/opt+chown 步骤、`EnvironmentFile` 加 `-` 前缀。
- **死代码**删除:`can()`(auth.ts)、`buildExportCSV`(export.go)。
- **L6 闭环**:seed 加 `security.idleMinutes=15`,SettingsView 加编辑入口(空闲锁定时长真正可配)。
- **L3 连接缓存**:`openConn` 对网络型引擎(mysql/pg/oracle)按 (driver,dsn) 缓存 `*sql.DB`,返回 release 函数;SQLite 不缓存(避免持有文件句柄)。测 `TestOpenConn_SqliteNotCached`。
- **目标库 TLS**:MySQL `tls=preferred`、PG `sslmode=prefer`(支持则加密、否则回落)。测 `TestEngineDriver_PrefersTLS`。
- **语句分割引号感知**:新增 `pkg/sqlutil.SplitStatements`(引号/注释感知),`migrate` 与脚本执行共用,`'a;b'` 不再被切断。测 `TestSplitStatements_RespectsQuotesAndComments`。
- **L8**:`EnqueueExport` 非阻塞入队(满则置 failed,不再泄漏阻塞 goroutine);**L9**:webhook 重试加 2 分钟总时限 + 退避封顶 30s(高 retryMax 也不会产生长命 goroutine)。

**经评估不改(设计权衡,已核实)**:
- **审批 approve 审计 actor 记发起人**(命令归属),审批人已在 `tbl_approval_step` 完整记录 → 二者结合可完整溯源,属合理设计。
- **L10 本地时区**:统一改 UTC 改动面大、收益低、有展示回归风险;部署主机统一时区即可解决。
- **L11 collation**:现库/表 `utf8mb4_unicode_ci` 两侧已一致;改 `0900_ai_ci` 会影响 email 大小写敏感性,收益低风险高。

至此 **issue.md 第二轮审查发现的 R1–R29 + 低危项已全部处置**(除上述三项明确保留)。

### 第三轮复核 + 修复(2026-07-16)

对前三轮改动再做并行五维度复核,重点找**修复引入的新回归**,发现并修复 7 项(V1–V7),后端 `go vet`+全量测试、前端 `type-check`/`build` 通过。新增测试:`TestLoginLimiter_ThresholdNoReextendAndPrune`(handler)、`TestApproval_DeletedTargetConnReportedNotExecuted`、sqlutil 反斜杠用例。

- **V1(高·R14 回归)** MySQL `GET_LOCK` 是会话级锁,GORM 连接池下 lock/DDL/release 可能落在不同物理连接→并发保护失效。迁移期间把 MySQL 池 `SetMaxOpenConns(1)` 固定单连接,使会话锁真正覆盖全程。
- **V2(高)** `DecideApproval` 目标连接已删除(`conn==nil`)时跳过执行却记 `executed`、通知"已通过并执行"→改为记 `warn` + 结果"目标连接已不存在,未执行" + 如实通知。
- **V3(高·R29 新代码)** `loginLimiter` fails map 只在成功登录清理→无界增长(内存 DoS);锁定期内每次失败都续 `lockedUntil`(NAT 误伤放大)。加惰性 `pruneLocked` 清过期条目;`lockedUntil` 仅在越过阈值那一次设置。
- **V4(中·R25 回归)** `ListExportJobs` 解密失败(密钥轮换)保留 `enc:v1:` 密文当口令回显→改为占位"(口令不可用·密钥已轮换)"。
- **V5(中·L12 回归)** `firstVisibleRoute=''` 时 LoginView `router.push('')` 静默卡登录页→空则提示"无可访问的菜单,请联系管理员"。
- **V6(高)** `sqlutil.SplitStatements` 把 `\'` 当 MySQL 反斜杠转义,PG/NO_BACKSLASH_ESCAPES 下 `'a\'` 是完整串→隐藏语句被合并进字面量(PG 简单协议可多语句执行)。改为 SQL 标准(仅 `''` 双引号转义,不认反斜杠)=安全方向(只会过度切分不会合并)。
- **V7(中)** `ExecuteSafeScript` 对某句 `execJudged` 返回 Intercepted(能力矩阵 approve)时忽略 resp 仍 `executed++`→改为遇 Intercepted 停止并返回错误(该句需审批、未执行)。

**第三轮经评估不改(已核实/设计权衡)**:
- **MFA 计数器时钟漂移边界**(checkMFA +30s skew 消费未来 counter):仅在**会话中途时钟漂移**时触发(时钟稳定—同步或恒定偏移—均正常);改 skew 逻辑有可用性/测试回归风险,窄边界,记录不改。
- **presence map(secretsSet/webhookHasSecret)"泄露"**:核实 seed 菜单矩阵 `settings` 为 **admin 独有**,`GET /settings` 实际仅 admin 可达→**非漏洞**。
- **dbPoolCache 生命周期**(改口令/删连接后旧 `*sql.DB` 不驱逐)+ **RealQueryEach 大导出占用共享 pool**:纯性能/生命周期,执行器多为模拟、增长受配置变更频率限制,低实际影响,记录。
- **readRe 把 `EXPLAIN ANALYZE <DML>` 当只读**:风险引擎 `ParseVerb` 已拆 EXPLAIN 前缀正确送审批,仅审计读/写口径失真,不构成执行绕过。
- **FailStuckExportJobs 多实例误标**:当前单进程部署不触发;横向扩容时才需"按本进程拥有"过滤,记录待未来。
- **config.prod.yaml fallback DSN 弱口令**:前几轮已知,靠 `VELA_MYSQL_DSN` env 覆盖,文档已提示。

---

## 第四轮全面审查(2026-07-16)

对前三轮改动做并行五维度全面审查(认证/会话·风险引擎·审批审计·导出SSRF加密·前端·配置迁移),逐条抽验历史修复 C1–C4/H1–H14/M/R1–R29/V1–V7 **均真实闭合、无被绕过或严重回归**。本轮重点找**修复交汇处引入的新面**,发现 4 高 + 9 中 + 若干低,其中 A1/A2/A3/A4 已亲自复核确证。

### 待修清单(A=高,B=中,C=低)

**A1【高】终端单条命令不做语句分割,堆叠语句绕过能力矩阵。** `service/gateway.go:76,83-84`。终端 `Exec` 把整串 SQL 交 `execJudged`,`Evaluate` 只用 `ParseVerb` 取**首动词**定能力维度,尾部靠字典 `matchCommand` 全文兜底。而 seed 字典(`seed.go:110-114`)**不含 `UPDATE/INSERT/CREATE/REPLACE`**、dev 全 off。PG(lib/pq simple 协议允许分号堆叠)上 PROD `SELECT 1; UPDATE accounts SET balance=0` → 首动词 SELECT 判 allow、字典不含 UPDATE → 直接执行,绕过能力矩阵+审批。MySQL `multiStatements=false` 可挡。修复:`Exec` 入口统一 `SplitStatements` 逐条判定取最严,或多语句直接拒绝。

**A2【高】静态加密密钥复用 JWT 签名密钥。** `cmd/server/main.go:73` `crypto.SetSecretKey(cfg.JWT.Secret)`。连接口令 AES-256-GCM 静态加密密钥(C3)派生自 JWT 签名密钥(C4)。①轮换 `VELA_JWT_SECRET`(JWT 泄露后标准应急)→ 所有 `enc:v1:` 连接口令永久不可解密、网关连不上库(V4 把失败回显为占位串,故障隐蔽);②JWT 密钥泄露=既能伪造 admin 令牌又能离线解密整库连接口令。修复:独立 `VELA_SECRET_KEY`(缺省回落 JWT 密钥+启动告警),文档明确轮换流程。

**A3【高】`StripComments` 拆封 `/*! */` 后内层注释拆词躲字典(R2 修复新面)。** `gateway/risk.go:68-74`。`/*!40000 TR/**/UNCATE TABLE t */` 拆封+删块注释后成 `TR UNCATE`,字典 `\bTRUNCATE\b` 不匹配、`ParseVerb` 首词 `TR`。修复:字典扫描/动词解析前对拆封结果再彻底去注释后再判定。

**A4【高】审计哈希链串行化只在单进程/单连接成立,MySQL 生产多连接下分叉断链。** `service/service.go:108-136`+`repository.go`。H10 的 `auditMu`(sync.Mutex) 只在 sqlite `SetMaxOpenConns(1)` 下原子;MySQL 运行态多连接、无行锁/事务/唯一约束,高频审计写或横向扩容即让两行读到同一 prev → 链分叉。修复:`tbl_audit_log.prev_hash` 加唯一约束作数据库层兜底(分叉写第二条报冲突可重试),或 `SELECT…FOR UPDATE` 事务。

**B1【中】readRe 与 ParseVerb 动词判定不一致。** `executor.go:21`/`realdb.go:21` `readRe` 凡 `EXPLAIN` 开头即走 QueryContext;R12 只修 risk 侧。PG `EXPLAIN ANALYZE DELETE …` 被当只读执行,审计读/写口径失真。两侧应共用动词解析。

**B2【中】导出真实执行路径无字节上限。** `export.go:188`/`realdb.go:186`。`exportMaxRows` 只计行数不计字节;大 BLOB 列几千行即把单 worker `bytes.Buffer` 推高数百 MB,3 worker 近 GB → OOM。加 `exportMaxBytes` 累计校验。

**B3【中】前端审批 approve/reject 无二次确认(M15 遗漏)。** `ApprovalsView.vue:37-47`。最危险的"批准并执行高危 SQL"一点即执行。对 approve(尤其 `riskLevel==='high'`)补 `confirmAction`。

**B4【中】迁移锁连接固定不稳。** `migrate.go:71-82`。`SetMaxOpenConns(1)` 只限并发数不保证同一物理连接复用;连接因 busy_timeout/抖动重建时 session 级 `GET_LOCK` 静默消失,且 `RELEASE_LOCK` 未检查返回值。改用 `sqlDB.Conn(ctx)` 显式固定连接。

**B5【中】`SetTrustedProxies` 错误被丢弃。** `router.go:27` `_ =`。非法 CIDR 时 Gin 回退信任所有代理 → H2 XFF 防伪失效。校验错误并拒绝启动。

**B6【中】MySQL 路径 `auto_migrate:true` 与 SQL 迁移双源。** `db.go:51`。`OpenDB` 无条件对 mysql 也跑 AutoMigrate,仅靠配置纪律避免与 `0001_init.sql` 漂移。driver==mysql 时代码层禁止 AutoMigrate。

**B7【中】NoWhere 子串误判(M10 遗留)。** `risk.go:116-126`。`strings.Contains(low,"where")` 使 `DELETE FROM elsewhere_tbl` 判"有 WHERE",strict 全表写兜底失效。改 `\bwhere\b` 词边界。

**B8【中】MFA 启用码可复用一次做 step-up。** `gateway.go:659 MFAEnable` 不消费计数器且重置 `mfa_last_ctr=0`,启用码 ~90s 窗口内仍过一次 `checkMFA`。MFAEnable 成功后消费该 counter。

**B9【中】WS 长连接不即时收回 terminal 菜单权限。** `terminal.go:435`。R5 每条 exec 重查 status/token_version 漏查菜单;`SetRoleMenus` 撤销 terminal 菜单不切断在线会话。fresh 重查追加菜单校验或角色菜单变更 bump token_version。

**C(低,择要):** NAT64/IPv4-mapped SSRF 边界(`webhook.go:32`);登录用户枚举计时旁路(`service.go:141`);PatchUser status 无取值白名单(`admin.go:195`);前端 savePw 最小长度 6 位与后端 ≥12 位不一致;config.yaml 内联弱密钥仅黑名单枚举挡(无熵检测);`APP_ENV=staging` 静默降级 SQLite;`init` flag 帮助文本仍写 ≥6 位;docker-compose 3306 绑全网卡;csvSanitize 只判首字符;Lark 卡片命令未转义反引号。

### 修复状态(2026-07-16,TDD 逐条修 A1–A4)

**已修复 A1–A4(4 高)**,后端 `go vet` + 全量测试通过。新增回归测试:
- A1 `TestExec_StackedStatementTailIsJudged`(bootstrap)—— PROD `SELECT 1; UPDATE ...` 现被拦截而非直接执行
- A2 `TestConfig_SecretKeyDecoupledFromJWT`(bootstrap)—— `VELA_SECRET_KEY` 与 JWT 密钥解耦、缺省回落并标记
- A3 `TestStripComments_InnerCommentDoesNotSwallowVerb`(gateway)—— `/*!40000 /* c */ DROP TABLE y */` 等内层注释不再吞动词
- A4 `TestAudit_PrevHashUniquePreventsFork`(bootstrap)—— `prev_hash` 唯一约束拒绝分叉写

要点:
- **A1** `service/gateway.go` `Exec` 入口对整串 `sqlutil.SplitStatements`,多语句时 `strictestVerdict` 逐条 `Evaluate` 取最严(deny>approve>allow),抽出 `applyVerdict` 复用判定分支;单语句路径不变。**验证过真实可利用**:PG simple 协议下首动词 SELECT 曾绕过能力矩阵执行尾部 UPDATE。
- **A2** 新增 `Config.SecretKey`(yaml `secret_key` + env `VELA_SECRET_KEY`)与 `SecretKeyResolved()`;`main.go` 优先用独立密钥,回落 JWT 密钥时 `slog.Warn`。`deploy/vela.env.example`、`DEPLOY.md` 补充说明与轮换警示。
- **A3** `risk.go` `StripComments` 由正则改**线性扫描器**:执行注释 `/*!ver */` 仅剥标记保留 body、并剥离 body 内嵌普通注释,**不丢非注释文本**。旧非贪婪正则在内层 `*/` 处提前截断 + 块注释清除吞掉真实动词(`/*!40000 /* c */ DROP TABLE y */` 塌成空串判 allow,而 MySQL 真跑 DROP)。删除 `lineCommentRe/blockCommentRe/hashCommentRe/execCommentRe`。
- **A4** `model.AuditLog.PrevHash` 加 `uniqueIndex:uk_audit_prev`(dev/sqlite 走 AutoMigrate);新增 `migrations/0002_audit_prev_unique.sql`(prod/MySQL `ADD UNIQUE KEY`);`appendAudit` 改**取 tip→算 hash→插入**的重试循环(最多 5 次),冲突即重读链尾重挂,链在多连接/多进程下不再分叉。

### 修复状态(2026-07-16,TDD 逐条修 B1–B9 中危)

**已修复 B1–B9(9 中)**,后端 `go vet` + 全量测试通过、前端 `type-check`/`build` 通过。新增回归测试:
- B1/B7 `TestIsRead_ExplainAnalyzeIsWrite`、`TestNoWhere_WordBoundary`(gateway)
- B2 `TestExportLimits_ByteCapRejects`(service)
- B5 `TestValidateForServe_RejectsBadTrustedProxy`、B6 `TestShouldAutoMigrate_NeverForMySQL`、B8 `TestMFA_EnrollmentCodeCannotAlsoStepUp`、B9 `TestTerminalWS_MenuRevokedMidConnection`(bootstrap)

要点:
- **B1** 新增 `gateway.IsRead()` 以 `ParseVerb`(拆 EXPLAIN 前缀)判读写,`executor.go`/`realdb.go` 弃用 `readRe`;`EXPLAIN ANALYZE DELETE` 归为写。`EXPLAIN <DML>`(无 ANALYZE)保守归写=安全方向。
- **B2** `export.go` 新增 `exportMaxBytes`(默认 ~2GiB)+ 可测的 `exportLimitErr(rows, rawBytes)`,`produceExport` 逐行累计原始字节,超限即失败,堵大 BLOB 列内存/磁盘放大。
- **B3** `ApprovalsView.vue` `decide` 加 `confirmAction`(批准/驳回均二次确认,展示命令)。
- **B4** `migrate.go` 迁移锁改用 `sqlDB.Conn(ctx)` 专用连接持锁到底,`RELEASE_LOCK` 校验返回值;不再依赖 `SetMaxOpenConns(1)`(连接身份无保证)。MySQL 专属路径,无本地实例故按代码审阅验证,sqlite 幂等测试仍绿。
- **B5** `ValidateForServe` 校验 `trusted_proxies` 为合法 IP/CIDR,非法即拒绝启动(闭合 gin 静默信任所有代理 → XFF 伪造)。
- **B6** `db.go` 新增 `shouldAutoMigrate(driver, want)`:mysql 恒不 AutoMigrate(SQL 迁移唯一权威),消除双源漂移。
- **B7** `NoWhere` 改 `\bwhere\b` 词边界,`DELETE FROM elsewhere` 不再误判有 WHERE。
- **B8** `MFAEnable` 成功后 `ConsumeMFACounter` 消费启用码,防 ~90s 内复用做 step-up;测试辅助 `setupMFA` 改用上一时窗码启用以保留当前码给后续操作。
- **B9** WS 每条 exec 的 fresh 重查追加 `MenusForRole(RoleID)["terminal"]` 校验,角色菜单撤销即切断在线会话(菜单变更不 bump token 版本)。

### 修复状态(2026-07-17,TDD 逐条修 C1–C10 低危)

**已修复 C1–C10(10 低)**,后端 `go vet` + 全量测试通过、前端 `type-check`/`build` 通过。新增回归测试:
- C1 `TestIsDisallowedIP_NAT64AndMapped`、C9/C10 `TestCsvSanitize_LeadingWhitespace`/`TestLarkSafe_NeutralizesBackticks`(service)
- C2 `TestLogin_ConstantTimeForUnknownUser`、C3 `TestPatchUser_StatusWhitelist`、C5 `TestValidateForServe_RejectsLowEntropySecret`、C6 `TestIsKnownEnv`(bootstrap)

要点:
- **C1** `isDisallowedIP` 拆 NAT64(`64:ff9b::/96`)/IPv4-mapped 内嵌 IPv4 复判,拒 CGNAT `100.64/10`;不误伤 NAT64→公网。
- **C2** `Login` 未命中/非 active 分支跑一次 `crypto.CheckPassword(dummyPasswordHash, pw)` 抹平 bcrypt 时延(实测前 2.8ms vs 52ms → 后≈52ms),堵账号枚举计时旁路。
- **C3** `PatchUser` status 仅接受 `{active,disabled,invited}`,否则 `ErrBadRequest`。
- **C4** 后端 `AdminSetPassword` 与前端 `savePw` 从 6 位统一提到 8 位(原本已一致于 6,本次一并加固;≥12 仅平台管理员 init)。
- **C5** `ValidateForServe` 增 `distinctBytes >= 8` 熵下限,拒绝长但单一的密钥(如全 `a`、`changeme`×4)。
- **C6** `LoadConfig` 用 `isKnownEnv` 对未识别 env(如 `staging`)`slog.Warn`,避免静默降级 SQLite。
- **C7** `init` flag 帮助文本改为 ≥12 位/≥3 类,与 `validateAdminPassword` 一致。
- **C8** `docker-compose` MySQL/Redis 端口绑 `127.0.0.1`,头注明本地开发专用、禁用于生产。
- **C9** `csvSanitize` 先看首字节再看去空白后的首字节(抽出 `isFormulaLead`),覆盖 `" =cmd"` 前导空白变体。
- **C10** 新增 `larkSafe` 把命令/原因中的反引号替换为 U+02CB,防闭合 lark_md 代码围栏注入富文本。

至此第四轮 **A(4 高)+ B(9 中)+ C(10 低)共 23 项已全部 TDD 修复**,后端全绿、前端 build/type-check 通过。

---


### 严重
- **R1 ⚠️ recordAudit 手动 Lock/Unlock 无 defer,一次 panic 即全站审计死锁。** 位置:`service.go:91-114`。`auditMu.Lock()` 与 `Unlock()` 之间的 `LastAuditHash`/`InsertAudit` 若 panic(被 gin Recovery 吞),锁永不释放 → 之后所有 recordAudit(登录/Exec/审批/导出)永久阻塞,进程假活。修复:改 `defer s.auditMu.Unlock()`,Webhook.Dispatch 留锁外。(auditMu 修复引入)
- **R2 ⚠️ MySQL 可执行注释 `/*! … */` 绕过三层判定直接执行。** 位置:`risk.go` StripComments+matchCommand+Evaluate、`realdb.go` readRe。`/*!32302 DROP TABLE x */`:StripComments 后动词为空→映射 select、字典扫描扫的也是剥注释后的空串→漏,Evaluate 落 allow;readRe 不匹配→走 ExecContext,MySQL 服务端执行版本注释里的 DDL。StripComments 修复把字典兜底也一起绕过了。(StripComments 修复引入)
- **R3 ⚠️ 设置页保存即清空已配置的飞书签名密钥。** 位置:`admin.go` GetSettings(`all[k]=maskedSecret` 裸串)+`SettingsView.vue:72,90,130`。前端 `JSON.parse('__VELA_SECRET_UNCHANGED__')` 抛错→回落 `''`,保存/测试飞书时回传 `'notify.larkSecret':''`→后端 `''!=maskedSecret`→用空串覆盖真实密钥。M2 打码对走通用 settings map 的密钥起了反作用。(M2 修复引入)

### 高
- **R4 WS 通道绕过 IP 白名单与 terminal 菜单。** `router.go:48`:`/terminal/ws` 在 `auth+IPAllowlist` 分组外,TerminalWS 也不过 MenuGuard。开白名单后被封 IP 仍可经 WS 执行 SQL;无 terminal 菜单角色持有效 token 亦可。
- **R5 WS 长连接不受会话吊销/停用影响。** `terminal.go`:token 版本与 disabled 仅握手查一次,之后消息循环复用快照。登出/改密/停用终止不了已建立的 WS 会话。M1 即时吊销对最危险通道不成立。
- **R6 ⚠️ 前端 logout 竞态:服务端吊销从未生效。** `auth.ts:56-64`:`api.logout()` 后同步 `removeItem(token)`,axios 请求拦截器在微任务里才读 localStorage→已空→请求不带 Authorization→401,token 版本从未 bump。(M1 前端引入)
- **R7 ⚠️ failedOpens 把后端重启误判为鉴权失败,踢下线全体终端用户。** `wsTerminal.ts:94-101`:退避 1/2/4s,后端一次 >7s 普通重启即凑满 3 次→onAuthError→logout+跳登录。无法区分连接被拒与 401。(M16 引入)
- **R8 ⚠️ PatchUser 全字段 Save 回滚 token_version/mfa_last_ctr。** `admin.go:187-198`+`repository.go` UpdateUser(`db.Save`)。改 status/role 时用旧快照全列写回,并发窗口内覆盖刚 bump 的 token 版本、刚消费的 TOTP 计数器→已吊销 JWT 复活、已用 code 可重放。改用 `Updates(map)`。
- **R9 createApproval 吞错 + maxSeq 吞错回落基数 → 幽灵审批单。** `gateway.go:161`(`_ = CreateApproval`)+`repository.go` maxSeq(`_ =` 吞查询错)。启动时 DB 抖动致计数器回落 2294,ap_no 唯一键此后持续冲突且全被吞→用户看到"已拦截待审批"但队列里永无该单。
- **R10 config.yaml 自带 dev JWT 密钥绕过 prod 强校验。** `config.yaml:28` `change-me-vela-gateway-dev-secret` 长 33≥32 且不在 weakJWTSecrets 黑名单;头部注释又引导 `APP_ENV=prod` 直接切 prod→用公开密钥签发 token(同源用于连接口令加密)。C4 被自家默认值绕过。
- **R11 RiskRulesView 建规则会清掉该命令其它环境的等级。** `RiskRulesView.vue:56`+`repository.go` UpsertRiskCommand:发完整 envMap 其余环境为 off,对已存在命令(DROP prod=high)在 staging 建规则会把 prod 拦截清为 off。`alert` 动作也被映射为 off。
- **R12 EXPLAIN ANALYZE <DML>(PG)判为只读,绕能力矩阵+严格模式。** `risk.go` MapVerbToCapability(`EXPLAIN→select`)、NoWhere(leading verb 非 delete/update→false)。PG 上 `EXPLAIN ANALYZE DELETE …` 真实执行。
- **R13 ⚠️ 超时自动升级每 60s 重复记审计+发 webhook。** `gateway.go:497` auto-escalate 不改工单状态,种子默认 `approval.onTimeout=auto-escalate`→每个超时单每天刷 1440 条审计/通知,污染哈希链。
- **R14 迁移双源 + IF-NOT-EXISTS 契约:首个增量(ALTER)迁移必炸。** compose 把 migrations 挂 initdb.d 首启执行但不写 schema_migrations;之后 migrate 重放。0001 全 CREATE…IF NOT EXISTS 兜得住,但 MySQL 8 无 `ADD COLUMN IF NOT EXISTS`,0002 起 compose 与部分失败恢复都会重复应用报错。

### 中
- **R15 MFA 防重放未覆盖 enable/disable。** `service.go:586-605` 用 totp.Validate 不 ConsumeMFACounter;截获一枚 code 可 90s 内关 MFA 再无限操作。(M3 覆盖不全)
- **R16 审批链成员可自审自批。** `gateway.go:199` 只校验 isChainMember,不排除 `actor.ID==InitiatorID`。
- **R17 ⚠️ ?token= 回退 + gin.Logger 记查询串 → JWT 落访问日志。** `terminal.go:353` 保留 query 回退,`gin.Logger()` 默认输出 rawQuery。(H4 未彻底)
- **R18 ⚠️ ExecuteSafeScript 逐句 mfaCode 恒空,MFA 用户 PROD 脚本必失败。** `gateway.go:447`+`terminal.go:267`。方向安全但功能瘫痪,可能诱导关 requireMFA。(M3/M4 引入)
- **R19 DecideApproval 认领后异常无补偿 + 谎报成功。** `gateway.go:225-257`+`admin.go:269`:认领后 Run panic→工单停 approved 无审计且不可再操作;Run 失败仍记 ResultExecuted;认领失败返回 `(nil,nil)`→handler 仍回 `{ok:true,status:approved}`。
- **R20 SSRF 校验 TOCTOU + 不限重定向。** `webhook.go`:LookupIP 校验后 Do 重新解析(低 TTL 域可切内网),无 CheckRedirect,302 到内网被跟随。(L2 可绕)
- **R21 前端 401 不重置 store + 守卫 fetchMe 失败即登出。** `http.ts:29` 只删 localStorage 不清 store→僵尸界面 ping-pong;`router/index.ts:34` catch 不分 401/网络错→后端抖动即丢会话。
- **R22 WebhookPanel 明文回显哨兵串且 secret 无法清空。** `WebhookPanel.vue:78`。
- **R23 迁移无跨进程锁。** `migrate.go:52-99`:双 migrate 并发后者撞主键 exit 1;未来非幂等语句双重应用。
- **R24 导出队列纯内存,重启后 pending/running 永久悬挂。** `service.go:57`+`export.go`:`New` 不回灌未完成任务。
- **R25 导出 zip 口令明文存 export_jobs.password;CSV 表头未过 csvSanitize。** `export.go:119,215`:列别名 `AS "=HYPERLINK(…)"` 可经表头注入公式(M9 只清数据行)。
- **R26 prod migrate/init 被 JWT 强校验误伤。** 只需 DSN 的迁移任务缺 VELA_JWT_SECRET 就以无关错误失败。
- **R27 WS token 空串致 new WebSocket 抛错 → 永久假重连。** `wsTerminal.ts:52`:`['vela-token','']` 非法子协议,catch 只重连、不计 failedOpens→onAuthError 永不触发。
- **R28 ⚠️ SettingsView 整页保存无 try/catch/toast。** 权限最高的设置页未纳入 M14;开关乐观翻转失败不回滚。(M14 遗漏)
- **R29 登录无速率限制、不受 IP 白名单、失败不审计。** 白名单开启后仍可任意 IP 撞库。

### 低(择要)
- audit_log `prev_hash`/`hash` SQL 为 CHAR(64) 而模型推导 VARCHAR(64)(dev auto_migrate 会 MODIFY);splitSQLStatements 裸按 `;` 切埋雷;`build.sh` 第 29-30 行 `$GO` 未加引号,本机裸跑必挂;sqlite DSN 含 `?` 跳过全部 pragma;`security.idleMinutes` 设置项不存在(L6 未闭环);`can()`/`buildExportCSV` 死代码;审批 approve 审计记发起人而非审批人;目标库 `sslmode=disable`;systemd/DEPLOY 缺建用户与 chown 步骤;0001 头注释与"权威建表源"矛盾。

**模型↔0001_init.sql 漂移核对:** 18 表逐字段核对,仅 tbl_audit_log 的 CHAR/VARCHAR 一处差异,其余全部一致。

---

## 严重(Critical)

### C1. 空密码哈希账户可用任意密码登录(认证绕过)
- 位置:`backend/internal/service/service.go:105`
- 问题:`if u.PasswordHash != "" && !CheckPassword(...)` —— `PasswordHash` 为空时短路,校验被跳过直接登录成功。`Invite` 创建的用户(`admin.go:199`,状态 `invited`、无密码哈希)未被 `disabled` 拦截。
- 影响:攻击者用被邀请但未激活账户的邮箱 + 任意密码即可登录,拿到该角色合法 JWT,构成完整认证绕过 / 账户接管。

### C2. 审批结算存在 TOCTOU,高危命令可被重复执行
- 位置:`backend/internal/service/gateway.go:199-241`(配合 `repository.go` UpdateApprovalStatus)
- 问题:`DecideApproval` 先读状态判 `Pending`,再 `UpdateApprovalStatus` → `Executor.Run`,读-判-写无事务 / 无行锁 / 无版本号。两个审批人并发点批(或前端重复提交 / 双击)时都读到 `Pending` 并通过校验,导致 `Executor.Run(conn, ap.Command)` 执行两次。`SweepApprovalTimeouts` 也会与点批竞争。
- 影响:一条高危 SQL(DROP/DELETE 等)在网关侧被真实执行两次 —— 数据库网关最不可接受的后果;"仅需一人审批"的语义被破坏。正确做法:条件更新 `UPDATE ... WHERE id=? AND status='pending'` 检查 RowsAffected,并把状态流转 + 执行 + 审计放入同一事务。

### C3. 被管数据库连接口令明文落库
- 位置:`backend/internal/model/model.go:116`、`backend/migrations/0001_init.sql:80`、`backend/internal/service/admin.go:26`
- 问题:`Connection.Password` 为 `VARCHAR(255)` 明文;`CreateConnection` 直接 `Password: req.Password` 落库,全程无加密。仓库已有 `pkg/crypto` AES-256-GCM 但仅用于导出归档,未用于连接凭据。`json:"-"` 只是不出响应,库内仍是可逆明文。
- 影响:此表持有生产 / 预发所有实例的执行账号口令,是最高价值资产却零保护;库被拖走或运维越权读表即泄露全部后端数据库凭据。

### C4. 生产 JWT 密钥有已知默认值且不强制覆盖
- 位置:`backend/configs/config.prod.yaml:24`、`backend/internal/bootstrap/config.go:63`、`backend/cmd/server/main.go:51`;dev 同样为公开固定值 `config.yaml:28`
- 问题:`config.prod.yaml` 内置 `CHANGE-ME-set-VELA_JWT_SECRET`,仅当 `VELA_JWT_SECRET` 非空才覆盖;启动无任何"检测到默认密钥则拒绝启动"的护栏。HS256 对称签名。
- 影响:运维忘设环境变量即以已提交到仓库的公开密钥签发令牌,任何人可离线伪造 admin 令牌,彻底越权。

---

## 高(High)

### H1. CORS 在 origins 为空时反射任意 Origin 且携带凭证
- 位置:`backend/internal/middleware/middleware.go:41-42`;prod 配置 `config.prod.yaml:13` 恰为 `cors_origins: []`
- 问题:`if allow[origin] || len(origins) == 0` —— 允许列表为空时对任意来源回写 `Access-Control-Allow-Origin: <origin>` 且 `Allow-Credentials: true`。
- 影响:违反 CORS 规范的"通配 + 凭证"组合;恶意站点可跨域读取受保护 API 响应(Bearer 头场景影响有限,但仍是危险默认)。

### H2. IP 白名单可被 X-Forwarded-For 伪造绕过
- 位置:`backend/internal/middleware/middleware.go:139-143`;`router.go:24` 用 `gin.New()` 未调用 `SetTrustedProxies`
- 问题:白名单用 `c.ClientIP()`,Gin 默认信任所有代理并采信 `X-Forwarded-For`。攻击者发 `X-Forwarded-For: 127.0.0.1` 命中 loopback 放行分支,或伪造成任一允许 IP。
- 影响:完全绕过 Session & Security 的 IP 白名单。

### H3. 终端 WebSocket 不校验停用状态,也不过 IP 白名单
- 位置:`backend/internal/handler/terminal.go:345-356`(对比 REST `middleware.go:73`);`upgrader.CheckOrigin` 恒 `true`(`terminal.go:331`)
- 问题:`/terminal/ws` 自行 Parse JWT + GetUserByID 后即升级连接执行 `Exec`,未检查 `u.Status == "disabled"`,也不经 `IPAllowlist`。
- 影响:账号被停用后旧 Token 未过期(TTL 8–24h 且无吊销)仍可通过 WS 执行数据库命令;IP 白名单对该通道失效;CheckOrigin 恒真无跨站握手防护。

### H4. WS 鉴权 token 通过 URL query string 传递
- 位置:`frontend/src/components/terminal/TerminalSession.vue:143-148`;`frontend/src/lib/wsTerminal.ts:46`
- 问题:`.../terminal/ws?token=${token}` 把长期有效的会话凭据放进查询串,会被反代 / 网关访问日志、APM、浏览器历史完整记录。
- 影响:token 落入日志即等同账户接管。建议握手后首帧发送 token 或用 `Sec-WebSocket-Protocol` 承载。

### H5. 会话 token 存 localStorage,无 httpOnly / CSRF,XSS 即可窃取
- 位置:`frontend/src/stores/auth.ts:18,43,56`;`frontend/src/api/http.ts:22-26`
- 问题:token 明文存 `localStorage.vela_token`,任一 XSS 可 `getItem` 读走外带。当前源码未见 `v-html`,XSS 面较小,但一旦出现即整账户失陷。
- 影响:纯前端长期凭据在 XSS 场景无隔离。建议评估 httpOnly Cookie + CSRF token。

### H6. 全程无 TLS / HTTPS,凭据与 JWT 明文传输
- 位置:`backend/cmd/server/main.go:70`(`r.Run`)、`DEPLOY.md:49-58`、`config.prod.yaml:11`
- 问题:纯 HTTP 监听,无 `RunTLS` / 证书项;DEPLOY 引导浏览器 `http://` 登录,也未提及需前置反代做 TLS 终止。
- 影响:登录口令、TOTP、Bearer JWT、经网关执行的 SQL 全部明文过网,可被中间人嗅探 / 重放。

### H7. DSN 参数注入:前端可控的 database 直接拼入连接串
- 位置:`backend/internal/gateway/realdb.go:27,31,35`;源头 `backend/internal/service/gateway.go:58`(`conn.Database = database`,仅做 tag 访问控制,不校验字符)
- 问题:凭据与库名未转义直接 `fmt.Sprintf` 拼 DSN。传 `database = "mydb?multiStatements=true&"`,MySQL DSN 变为开启堆叠多语句;配合"只看首个动词"的能力矩阵,`SELECT 1; DROP TABLE x` 可绕过写 / DDL 审批。Postgres 空格分隔键、Oracle URL 同样可被注入 / 破坏。
- 影响:审批与能力矩阵绕过、连接行为被篡改、含 `@ : / ? 空格` 的合法口令 / 库名连接不可用。应使用 `mysql.Config{}.FormatDSN()` / `url.URL` 转义 + database 白名单校验。

### H8. 导出使用传统 ZipCrypto 加密,可被已知明文攻击破解
- 位置:`backend/pkg/crypto/zip.go:18`(`yzip.StandardEncryption`);调用 `export.go:225`;被弃用的强方案 `cipher.go:35`(AES-256-GCM)
- 问题:ZipCrypto 对已知明文攻击(bkcrack 等)脆弱,~12 字节连续已知明文即可恢复密钥,与口令强度无关。而 CSV 明文素材充足:每分片首行都是列名、列名又出现在 job 的 SQL 里,加上固定分隔符与可预测取值。已有 AES-256-GCM 实现却仅用于测试。
- 影响:落盘的敏感导出可被离线破解,口令再随机也无用。(此前为兼容 Windows 资源管理器改用 ZipCrypto,应权衡改用 `AES256Encryption` 或复用 AES-GCM。)

### H9. 风险引擎可被前导 / 内联注释绕过
- 位置:`backend/internal/gateway/risk.go:49`(`verbRe`)、`:61`(`NoWhere`)、`:85`(空动词→select)、`:97`(`matchCommand`)
- 问题:`verbRe = ^\s*([a-z_]+)` 只识别以字母开头首词。`/*x*/DELETE FROM users` 使 `ParseVerb` 返回 `""` → `MapVerbToCapability("")` 回退为 `select`、`NoWhere` 因 `verb!="delete"` 直接返回 false,strict 无 WHERE 判定与能力矩阵双双失效;`matchCommand` 在原始 SQL 上匹配不剥注释,`TRUNC/**/ATE`、`DROP/**/TABLE` 可躲字典。
- 影响:高危写 / DDL 降级为低危直接执行,绕过审批链与 PROD 拦截。应做词法 / AST 级解析。

### H10. 审计哈希链在并发下会分叉 / 断链
- 位置:`backend/internal/service/service.go:65-92`(配合 `repository.go` LastAuditHash / InsertAudit)
- 问题:`recordAudit` 无锁执行"读 LastAuditHash → 算 Hash → InsertAudit"。所有 HTTP 处理 + 3 个导出 worker 都会调用。两个并发调用读到同一 `prev`,各自基于同一 `prevHash` 插入,链就地分叉,后续行只挂到 `id desc` 那条,另一条脱链。
- 影响:无任何篡改时哈希链验证也报"断链",防篡改审计的核心保证失效。应把"取上一跳 + 插入"整体串行化。

### H11. 导出 worker 无 panic 恢复,单任务可独占 worker 达 30 分钟
- 位置:`backend/internal/service/service.go:46-52`、`export.go:87-116`;`realdb.go:107`(ctx 30min)
- 问题:worker 循环 `for id := range exportQueue { runExportJob(id) }` 无 `recover()`,任一 panic 使 goroutine 退出,worker 从 3 掉到 0 后所有导出永远停在 pending/running。`RealQueryEach` 超时 30 分钟,仅 3 个 worker,3 个大导出即占满池,新任务饥饿。
- 影响:导出可被单个异常 / 慢查询拖垮且不可自愈;panic 时任务状态永久悬挂。应 `defer recover` 并置 failed。

### H12. AP / 审计 编号按"行数"播种,删除后回退与重复
- 位置:`backend/internal/service/service.go:42-43,56-62`;`gateway.go:148-164`
- 问题:重启时 `apCounter.Store(2294 + Count(Approval))`、`auditCounter.Store(77310 + Count(AuditLog))` 以行数为基。删过任一行后 Count 变小,`nextApNo()` 重发已用过的 `AP-xxxx`。且 `createApproval` 先消耗号再 `_ = CreateApproval(...)`(错误被吞),创建失败时号被消耗但无行落库。
- 影响:审批号 / 审计号可能重复或回退,破坏唯一性与可追溯性。应从现存 `MAX(...)` 播种并在事务内先落库再返回。

### H13. 生产双轨:手写 DDL 与 AutoMigrate 职责错位
- 位置:`backend/internal/bootstrap/db.go:36`、`config.prod.yaml:20`(`auto_migrate: true`)、`docker-compose.yml:24`、`build.sh:49`
- 问题:二进制部署路径(DEPLOY.md,手工建库)中 `0001_init.sql` 从不执行,建表完全靠 AutoMigrate;docker 本地路径先跑 DDL 再 AutoMigrate。真实 prod 结构由 AutoMigrate 决定,DDL 沦为"文档",漂移不可观测。
- 影响:结构一致性依赖人工同步且 prod 又不用 DDL,漂移无处报错。(当前已逐字段核对:两侧字段 / 索引一致,无实际漂移 —— 但风险仍在。)

### H14. 生产每次启动都跑 AutoMigrate
- 位置:`config.prod.yaml:20`、`main.go:37`
- 问题:`auto_migrate: true` 对普通运行也生效,每次启动 `OpenDB→AutoMigrate`,GORM 可能对已存在表发起 ADD COLUMN / ALTER(如 DDL `CHAR(64)` vs model `VARCHAR(64)`)。
- 影响:生产重启即可能触发意外 DDL / 表锁。应把迁移收敛到显式 `init`,运行态关闭 AutoMigrate。

---

## 中(Medium)

### M1. 登出为空操作,无 Token 吊销 / 刷新机制
- 位置:`backend/internal/handler/terminal.go:38`
- 问题:`Logout` 仅返回 `{ok:true}`,不使服务端 Token 失效;无黑名单 / 版本号 / 刷新令牌。
- 影响:Token 泄露或登出后在整个 TTL(最长 24h)内仍有效;改角色 / 权限不即时生效于已签发 Token。

### M2. Webhook / 飞书签名密钥泄露给 settings 只读用户
- 位置:`backend/internal/handler/admin.go:444-447`;`model.go:247`(`Secret json:"secret"`);`router.go:126`
- 问题:`GET /settings` 仅受 `menu("settings")` 保护(非 admin),返回体内联整个 `WebhookConfig`(含 `secret`)及 `all` settings(含 `notify.larkSecret`)。
- 影响:有 settings 只读权限的角色可读取签名密钥,伪造审计 Webhook 签名或滥用飞书机器人。

### M3. TOTP 无一次性防重放
- 位置:`backend/pkg/totp/totp.go:50-60`;调用 `gateway.go:71`
- 问题:接受 ±1 步(约 90s)窗口但不记录已用码 / 计数器,同一 6 位码窗口内可反复接受。
- 影响:MFA step-up 码若被日志 / 代理截获,90s 内可重放完成二次验证。

### M4. requireMFA 对"未绑定密钥"用户静默失效
- 位置:`backend/internal/service/gateway.go:513-515`
- 问题:`mfaRequired` 要求 `MFAEnabled && MFASecret != ""` 才触发,即使 `security.requireMFA=true` 且目标 PROD,未绑定 secret 的用户就完全不需要 MFA。
- 影响:"生产操作需 MFA"的强制策略对所有未自愿开启 MFA 的账户形同虚设。

### M5. RiskEngine.strict 数据竞争
- 位置:`backend/internal/gateway/risk.go:38-47,148-164`;`service.go:445-449`
- 问题:`Services.StrictMode` 用 `atomic.Bool`,但真正参与判定的 `RiskEngine.strict` 是裸 `bool`,`SetStrict` 写、`Evaluate`/`ScanStatement` 读,无同步。
- 影响:`go test -race` 必报 data race;严格模式切换与判定并发时行为未定义。应改 `atomic.Bool`。

### M6. SQLite 无 busy_timeout / 连接池限制,写错误被吞
- 位置:`backend/internal/bootstrap/db.go:27-31`;大量 `_ =` 写调用
- 问题:dev sqlite 未配 `busy_timeout` / `SetMaxOpenConns(1)`;3 worker + 定时 sweep + HTTP 并发写会 "database is locked"。而 `InsertAudit`/`UpdateExportJob`/`UpdateApprovalStatus`/`DecideActiveStep`/`SetApprovalResult`/`CreateNotification`/`InsertWebhookDelivery` 等写返回值统一 `_ =` 丢弃。
- 影响:并发下审计 / 审批 / 导出状态写入可能静默失败、数据丢失且无日志;与 C2/H10/H12 叠加放大。

### M7. 100MB 分片全量内存缓冲,内存放大明显
- 位置:`backend/internal/service/export.go:180-238`;`service.go:30`(`exportWorkers=3`)
- 问题:`partWriter.buf` 为内存 `bytes.Buffer`,阈值 100MiB,`flush` 时 `ZipEncrypt` 再持有压缩输入 + 输出;`bytes.Buffer` 倍增扩容,单分片瞬时可占 128–256MB,3 worker 并行峰值近 GB。且 `writeRow` 每行 `Flush()`。
- 影响:大导出并发时内存峰值放大,OOM 风险。应流式写临时文件而非全量缓冲。

### M8. 导出无行数 / 总量上限
- 位置:`backend/internal/service/export.go:22,128`;`realdb.go:107`(30min 超时)
- 问题:注释明确"There is no row limit",对行数 / 总字节 / 分片数量均无上限,无每用户配额、无磁盘剩余检查,持续写盘直至塞满导出目录。
- 影响:任意登录用户一条大表 / 笛卡尔积查询即可磁盘写满 + 内存膨胀,DoS。

### M9. CSV 公式注入(导出结果 + 审计导出)
- 位置:`backend/internal/service/export.go:201`(`writeRow`,单元格来源 `realdb.go:141` `cellString`);`backend/internal/service/admin.go:317-322`(`csvCell`)
- 问题:单元格以 `= + - @`(及 Tab/CR)开头未做前缀转义,`csv.Writer` 仅按 RFC4180 加引号。
- 影响:用户用 Excel/WPS 打开导出的 CSV 时会执行公式(`=HYPERLINK`/`=WEBSERVICE` 数据外带等)。应对危险首字符前置 `'` 或空格。

### M10. NoWhere 用子串 `where` 判定,漏报全表写
- 位置:`backend/internal/gateway/risk.go:70`
- 问题:`return !strings.Contains(low, "where")`。`DELETE FROM elsewhere`、`UPDATE nowhere SET ...`、`DELETE FROM t -- where` 都会被判为"有 WHERE"。
- 影响:strict 模式对真实无 WHERE 的全表删除 / 更新漏报,失去兜底。应词法解析。

### M11. 弱默认凭据硬编码,且 webhook secret 无 env 覆盖通道
- 位置:`docker-compose.yml:10-13,26`、`config.yaml:28,39`、`config.prod.yaml:18`、`config.go:60-68`
- 问题:compose 明文 `MYSQL_ROOT_PASSWORD: velaroot` / `MYSQL_PASSWORD: velapass`(healthcheck 亦带 `-pvelapass`);DSN 回退默认 `vela:velapass`;webhook secret `whsec_dev_change_me_3a9f` 明文提交。`LoadConfig` 只为 DSN/JWT/WEB_DIR 提供 env 覆盖,webhook.secret 无 `VELA_*` 覆盖入口。
- 影响:默认弱凭据可被直接利用;webhook 签名密钥只能明文入库 / 入盘。

### M12. 平台管理员口令最低长度仅 6 位
- 位置:`backend/internal/bootstrap/init.go:41`;`main.go:82`
- 问题:`UpsertAdmin` 仅校验 `len(password) < 6`,无复杂度 / 字典校验。
- 影响:唯一超管账户可用 6 位弱口令创建,叠加明文 HTTP 登录风险。

### M13. 引用数据 seed 忽略写入错误,init 幂等仅靠非事务 Count
- 位置:`backend/internal/bootstrap/seed.go:40-131`;`init.go:20-23`
- 问题:`seedReference` 中 menu/capability/riskCommand/setting 等大量 `db.Create` 未检查错误;幂等仅靠 `Count(&Role{})==0` 且非事务。中途失败重跑或并发 init 可能"角色已存在但菜单 / 能力矩阵不全",且因 Count>0 不再补齐。
- 影响:半初始化的权限矩阵可能放大 / 缩小实际权限,难察觉。

### M14. 前端大量异步操作缺 try/catch,静默失败 + 未捕获 Promise
- 位置:`ApprovalsView.vue:15-18,30-35`、`PermissionsView.vue:106,50-55,111-138,156-175`、`RiskRulesView.vue:62-69,84-100`、`ConnectionsView.vue:47-50,65-85`、`AuditView.vue:21-23,37-49`
- 问题:视图加载与变更处理器普遍直接 `await api.xxx()` 不捕获。onMounted 内失败抛未捕获 rejection(pageerror);变更类(审批 / 删规则 / 改矩阵 / 禁用用户 / 导出)失败既无提示也无回滚。
- 影响:审批 / 权限变更这类需明确反馈的操作静默失败 + 全局异常噪声。(AppLayout/ExportView/UploadView 已 try/catch,可见为遗漏。)

### M15. 前端高危管理操作无二次确认
- 位置:`PermissionsView.vue:124-128,134-138,171-175`、`RiskRulesView.vue:97-100`、`UploadView.vue:59-61`
- 问题:移除角色成员、禁用用户、重置 / 解绑他人 MFA、删除风控规则、删除脚本文件均一次点击立即执行,无 confirm。
- 影响:误点即造成权限 / 安全策略变更(如误删 PROD 的 DROP 拦截规则、误解绑管理员 OTP),叠加 M14 失败还无提示。

### M16. 前端 WS 鉴权失败无限重连,不触发重新登录
- 位置:`frontend/src/lib/wsTerminal.ts:70-84`;`TerminalSession.vue:149-151`
- 问题:HTTP 侧有 401 处理,WS 侧没有。token 过期 / 被踢时握手被拒,`onclose` 一直 `scheduleReconnect`(封顶 15s),既无"请重新登录"提示也不跳登录页。
- 影响:失效会话持续以无效 token 敲服务端;用户感知为静默卡死。(idle 锁定 / HTTP 401 会卸载 AppLayout 覆盖部分场景。)

---

## 低(Low)

### L1. GenPassword 取模偏置
- 位置:`backend/pkg/crypto/cipher.go:24-26`
- 问题:`out[i] = pwAlphabet[int(b)%len(pwAlphabet)]`,字母表长 56,`256%56=32`,前 32 个字符概率偏高。熵源 `crypto/rand` 正确但分布非均匀。应用拒绝采样 / `big.Int` 均匀取值。

### L2. Webhook / 飞书地址无 SSRF 校验
- 位置:`backend/internal/service/webhook.go:58,96,198`;配置入口 `admin.go:469-497`
- 问题:Endpoint / 飞书 Webhook 由后端主动 POST,无 scheme/host/内网地址校验,可指向 `169.254.169.254` / 内网。缓解:配置接口 admin-only。

### L3. 真连库每次执行新建连接(连接池形同虚设)
- 位置:`backend/internal/gateway/realdb.go:47,70,100`
- 问题:`RealRun`/`RealQueryEach` 每次 `sql.Open` + `SetMaxOpenConns(3)` + `PingContext(8s)`,返回即 `Close`。"一开一关"使连接池无意义,每次请求额外付出建连 + Ping 开销。资源释放本身正确。应按连接缓存 `*sql.DB`。

### L4. 导出任务密码明文展示且随轮询反复回传
- 位置:`frontend/src/views/ExportView.vue:56-58,147-151`
- 问题:zip 密码 `j.password` 明文 `<code>` 渲染 + 一键复制,随 `exportJobs()` 每 2s 轮询持续返回、留存前端内存 / DOM。建议默认打码、按需显示,仅完成瞬时下发一次。

### L5. 登录页硬编码预填演示账号 / 口令
- 位置:`frontend/src/views/LoginView.vue:16-17`
- 问题:`email='linwei@vela.io'`、`password='vela123'` 预填输入框。若构建物流入类生产即明示可用凭据。应以环境变量控制仅 dev 注入。

### L6. 会话 TTL 纯前端展示;idle 锁定固定 15 分钟且与 TTL 脱节
- 位置:`frontend/src/views/SettingsView.vue:94-95,113,121`;`AppLayout.vue:116`
- 问题:`security.sessionTTL`(4h/8h/24h)前端只做文案映射,无客户端强制;`IDLE_MS=15*60*1000` 写死,不读 TTL 设置。管理员改 TTL 不影响锁定行为。

### L7. DecideApproval 多次写非事务
- 位置:`backend/internal/service/gateway.go:221-240`
- 问题:批准路径 `UpdateApprovalStatus`→`DecideActiveStep`→`Executor.Run`→`SetApprovalResult`→`recordAudit` 无事务,中途崩溃 / 某步(被吞)失败会留下"已 approved 但无 result / 步骤未落决"的不一致工单。

### L8. EnqueueExport 用 goroutine 入队,队列满时堆积;通道不关闭
- 位置:`backend/internal/service/export.go:70`;`service.go:45-52`
- 问题:`go func(){ exportQueue <- job.ID }()` 每次入队起 goroutine;256 缓冲满且 worker 忙时全部阻塞累积;`exportQueue` 从不 `close`,无优雅停机。

### L9. Webhook 重试 goroutine 无 context 取消,可无界累积
- 位置:`backend/internal/service/webhook.go:53-84`
- 问题:每事件 `go func` 内 `retryMax` 次重试 + `time.Sleep(2^i s)`,单 goroutine 可存活十几分钟且不可取消;事件频繁 + 目标持续失败时累积。

### L10. 全程 time.Now() 本地时区
- 位置:`backend/internal/service/service.go:69-86`、`export.go:139`、`gateway.go:459`、`config.prod.yaml:18`(DSN `loc=Local`)
- 问题:审计 payload 用带偏移 RFC3339,库内时间按本地时区存;二进制部署主机时区未约束。改时区 / 跨环境时超时判定与审计时间展示偏移。建议统一 UTC。

### L11. utf8mb4 采用旧 collation,建表未显式 COLLATE
- 位置:`backend/migrations/0001_init.sql:7` 及各建表段;`docker-compose.yml:16-17`
- 问题:库级 `utf8mb4_unicode_ci`(非 MySQL8 推荐 `0900_ai_ci`),各表未带列级 COLLATE,AutoMigrate 建表又可能用服务器默认 collation,两路 collation 可能不一致(影响 email 唯一性大小写敏感度等)。

### L12. 路由守卫在"无任何菜单"用户下存在重定向自环
- 位置:`frontend/src/router/index.ts:42-45`;`stores/auth.ts:25-28`
- 问题:`firstVisibleRoute` 兜底恒为 `/terminal`,而守卫又校验 `menus['terminal']`;菜单全空的账号会 `/terminal`↔gate 失败自环。边缘场景。

### L13. 前端 `can()` 采用"默认放行"语义(当前未被使用)
- 位置:`frontend/src/stores/auth.ts:31-36`
- 问题:`return lvl !== 'deny'` —— 能力矩阵缺项(undefined)时返回 true。当前无调用,属潜在风险。若后续接入应改为仅 `'allow'` 放行的白名单式。

---

## 已核实为"正确防护"的项(避免误报,供参考)

- **越权后端强制**:角色 / 权限 / 用户凭据的所有**修改**接口均加了 `admin` 中间件,审批 `DecideApproval` 有 `isChainMember` 链成员校验,`db/rules/perms/settings` 菜单为 admin 独有 —— 前端 `isAdmin`/`canDecide` 只是体验层,后端已强制(H4/H5 之外的前端门控不构成越权)。
- **XSS**:源码内未发现 `v-html`/`innerHTML`,审批命令、审计命令、脚本内容、导出文件名等均走 Vue 文本插值转义;xterm 写入属终端作用域。
- **路径穿越**:导出下载 `ResolveExportFile` 同时做 `filepath.Abs` + 前缀校验与 `ownsExportFile` 归属校验;脚本上传 `sanitizeFilename` + `filepath.Base` + 按 userID 归属;`serveSPA` 用 `r.Static` + `NoRoute→index.html` 无穿越。
- **JWT 算法**:`Parse` 强制 `SigningMethodHMAC`,无 alg-none / RS-HS 混淆。
- **模型与迁移一致性**:逐字段核对 18 个模型与 `0001_init.sql`,当前字段 / 索引 / 保留字反引号(`` `sql` ``/`` `rows` ``/`` `database` ``/`is_read`)均一致,无实际漂移(但依赖人工维护,见 H13)。
- **并发正确项**:`partWriter` 为每 job 局部创建不共享;`metrics.Recorder` 全程持锁;`http.Client`、`math/rand` 全局复用并发安全。
- **加密正确项**:`HashPassword`(bcrypt)、`HMACSHA256`、`ChainHash`、`GzipEncrypt`(AES-256-GCM + rand nonce + 长度校验)实现健全(问题在于 GCM 未用于生产导出,见 H8)。

---

# 第五轮全面审查(2026-07-27)

**背景**:第四轮(2026-07-16)A/B/C 共 23 项已全部 TDD 修复关闭。此后 main 分支合入了大量新功能——外部飞书审批(审批魔方,Phase 1+2)、后台异步执行、终端三家客户端元命令 + 边框表格 + `\x` 竖排 + HTML 结果网格、多角色并集权限、后台直建账户、GLI 灰度环境、Oracle service name/SID、导出越权修复、Webhook 事件筛选。本轮对**全部代码**做六维度并行审查(认证会话RBAC · 风险引擎与终端执行 · 审批与外部回调 · 导出Webhook加密 · 数据层配置迁移 · 前端),重点是新功能引入的面。

**基线**:后端 `go vet` 干净、全部 Go 包测试通过;前端 `vue-tsc --noEmit` + `vite build` 通过。

**编号规则**:本轮统一用 `E<n>`(E = Edition 5)。前四轮编号(C/H/M/L/R/V/A/B/C)不复用。

## A 组 · 外部飞书审批(审批魔方)与审批/审计链

新增的外部审批回调是**全站唯一一个"未经用户 JWT 鉴权即可触发生产库 SQL 执行"的入口**(`router.go:55` 公开路由,不挂 `auth`、不挂用户 IP 白名单),因此该端点的每一处 fail-open 都直接等价于生产执行权限。以下 11 项已逐条对照源码核实。

### EA1【高】外部审批总开关不覆盖入站回调:功能"关闭"后回调仍能批准并执行
- 位置:`service/external_approval.go:181-193`(`VerifyExternalCallback`)、`:106`(`DecideApprovalExternal`)
- 问题:`extApprovalConfig()` 第 33 行取了 `enabled`,但**回调链路上没有任何一处读它**。出站的 `dispatchExternalApproval:63`、`cancelExternalApproval:91` 都检查了 `!cfg.enabled` 就返回,唯独入站不检查。
- 场景:运维试用外部审批后把 `approval.external.enabled` 关掉(或从未打开),只要 `callbackSecret` 还在库里,端点就仍然全功能可用。持有该密钥的一方(厂商、离职运维、从日志/DB 拿到密钥的人)`POST /api/v1/approvals/lark/callback` + `{"external_task_id":"AP-2301","approved":true}` 即可让网关以发起人身份在生产库执行该高危 SQL。
- 佐证:`bootstrap/external_approval_test.go:95-117` 的回归用例**只设了 `callbackSecret`**、`enabled` 保持 seed 默认 `false`(`seed.go:193`),回调依然把工单推到 approved 并执行——测试本身就是这条路径可用的证据。
- 修复:`VerifyExternalCallback` 首行加 `if !cfg.enabled { return ErrForbidden }`。

### EA2【高】回调不校验工单是否走过外部审批,且 ApNo 可枚举 → 一把密钥可批准任意待审工单
- 位置:`service/external_approval.go:106-123`、`service/service.go:99-101`(`nextApNo`)、`repository.go:594-600`
- 问题:关联键用我方 `ApNo`(`GetApprovalByApNo`),但**不检查** `ap.ExternalTaskID != ""`(这张单是否真派发给过厂商),也不把回调里的厂商 `cb.TaskID` 与存库值做一致性校验。而 ApNo 完全可预测:`fmt.Sprintf("AP-%d", counter)` 单调自增,发起人在 `/terminal/exec` 响应里就能看到自己的 `approvalNo`。
- 场景:密钥一旦泄露(见 EA3 日志面、EA7 库内明文面,或厂商侧被攻破),爆炸半径不是"已推到飞书的那几张单",而是**系统内全部 pending 工单**——包括从未派发到外部、本应只能由站内审批链成员决策的单。`for i in 2295..2400: POST {"external_task_id":"AP-$i","approved":true}`,每命中一张 pending 单就执行一条高危 SQL。即使不谈泄露,这也把第三方厂商的权限从"它经手的单"放大到"全部审批单"。
- 修复:`DecideApprovalExternal` 增加 `if ap.ExternalTaskID == "" { return ErrNotFound }`,并用 `cb.TaskID` 与存库 `ExternalTaskID` 比对;更稳妥的做法是给外发单生成不可猜的 nonce 作为回调关联键。

### EA3【高】`?secret=` 查询兜底把可触发生产执行的密钥写进访问日志(R17 同类回归)
- 位置:`handler/admin.go:348-351`、`bootstrap/router.go:28`(`r.Use(gin.Logger(), ...)`)、`docs/external-approval-setup.md:27`、`docs/adr/0003-*.md:86,100`
- 问题:`secret := bearerToken(...)`,空则回落 `c.Query("secret")`。`gin.Logger()` 默认 formatter 输出 `path + "?" + rawQuery`。**第二轮 R17 就是同一条**("`?token=` 回退 + gin.Logger 记查询串 → JWT 落访问日志"),当时的修复是删掉 query 回退;新代码在一个权限更高的入口重新引入了同一模式,且 `docs/external-approval-setup.md:27` 明确引导运维把密钥拼进回调 URL。
- 场景:厂商按文档注册 `https://gw/api/v1/approvals/lark/callback?secret=xxxx` → 每次回调在网关日志留一行明文密钥,同时该 URL 还出现在厂商侧配置与中间 TLS 终结代理/LB 的访问日志里。任何有日志读权限的人(通常不是审批链成员)就此获得 approve-anything 凭证,配合 EA2 可批准任意工单。
- 修复:删除 `?secret=` 兜底只保留 Bearer;若业务上必须保留,给该路径套自定义 formatter 抹掉 rawQuery。

### EA4【高】外部回调的决策被记成站内审批人的决策:审批步骤与审计链失真
- 位置:`service/gateway.go:354`/`:381`(`DecideActiveStep`)、`:367`/`:383`(`recordAudit(initiator, ...)`)、`model/model.go:265-283`(`AuditLog` 无 operator 字段)、`external_approval.go:131-134`
- 问题:`finalizeApproval` 被站内与外部回调共用,它 ① 把当前 `active` 的**站内**链步骤(预置 DBA 负责人)标成 approved 并盖 `acted_at`——飞书上是外部审批人点的通过,库里记的却是"张三已审批",不是少记而是**记错人**;② `recordAudit` 的 actor 是发起人,而 `AuditLog` 结构里**没有任何**审批人/决策来源字段,`operatorName` 只进了站内通知正文、不进哈希链;③ `cb.Reason` 全程丢弃(`SetApprovalResult(ap.ID, "", 0, now)`)。
- 关键:第二轮曾把"审批 approve 审计 actor 记发起人"评估为可不改,理由明写在 `issue.md:76`——"审批人已在 `tbl_approval_step` 完整记录"。**外部审批上线后这个前提不再成立**:外部审批人不是网关用户,既进不了 step 表也进不了审计链。工单 `02-callback-endpoint.md:17,26` 与 ADR:92 都要求"审计 operator = join(approver)、备注 = reason",该验收项未实现。
- 场景:一条生产 `DELETE` 由飞书上某人批准执行,事后审计时不可篡改的哈希链里只有"发起人执行了 DELETE",step 表里是一个从未操作过的站内 DBA。既无法追责真实审批人,也让被冤枉的站内审批人无法举证。
- 修复:`finalizeApproval` 增加 decider 上下文(来源 + 审批人标识 + reason);`AuditLog` 加 `operator`/`decision_source` 并纳入哈希 payload;外部回调不要改动站内 active 步骤。

### EA5【中】禁自审兜底在 approver 为空或身份格式不匹配时 fail-open
- 位置:`service/external_approval.go:157-174`、`:127`、`:131-134`
- 问题:三个 fail-open 点——`approver: []` 或字段缺失 → `len(approvers)==0` 直接 `return false`,禁自审不触发且 operator 退化成 `"审批魔方"`;发起人 email 为空/用户已删 → `init == ""` 也 `return false`;比对基准是网关库里的 email,而回调 `approver` 是飞书侧身份(ADR 样例里 `approver` 是 email 但 `user` 是账号名 `"pax"`,若厂商某些配置回传 open_id/显示名则 `!= init` 恒成立)。**后一点待验证**:需向厂商确认回调身份字段格式。
- 场景:发起人自己在飞书卡片上点通过,只要 approver 为空或不是他的网关 email,默认关闭的两人复核就形同虚设——正是 ADR:103 声称"SoD 不因对接降级"要挡的场景。
- 修复:`approver` 为空时 fail-closed 拒绝;比对时纳入发起人的全部已知标识。

### EA6【中】回调端点无速率限制、密钥无强度要求 → 可在线爆破 approve-anything 凭证
- 位置:`router.go:55`、`external_approval.go:186`、`:198-202`、`handler/admin.go:604-629`
- 问题:全站唯一未鉴权即可触发生产执行的入口,却没有 `handler/loginlimit.go` 那样的限速(登录在 R29 已加"5 次锁 5 分钟");`allowIPs` 默认空 = 放行所有来源(`seed.go:199`);`SaveSettings` 对 callbackSecret 只判空,`s3cr3t` 这种长度也照收;`subtle.ConstantTimeCompare` 在长度不等时立即返回,泄露密钥长度。鉴权失败只写 `slog.Warn`,不进审计链、不触发告警。
- 修复:复用 `loginLimiter` 限速 + 锁定;鉴权失败写审计链;callbackSecret 强制最小长度/熵,或由服务端生成。

### EA7【中】密钥解密失败 fail-open 成密文本身(V4 同类回归)
- 位置:`service/external_approval.go:46-55`
- 问题:`if v, err := crypto.DecryptSecret(raw); err == nil { return v }; return raw`。`DecryptSecret`(`pkg/crypto/cipher.go:83-111`)对无前缀值已原样返回,所以这条 `return raw` 只在**带 `enc:v1:` 前缀但解不开**时生效(密钥轮换/换库/密文截断)。此时 `cfg.callbackSecret` 变成 settings 表里那串密文本身 → 任何能读库或拿到备份的人直接持有可批准生产执行的凭证,恰恰是 `admin.go:19-25` 注释声明要防的事。**第三轮 V4 修的就是同一模式(导出口令),新代码又写了回来。**
- 修复:解密失败返回空串 + `slog.Error`(空串天然 fail-closed),前端提示"密钥已轮换,请重填"。

### EA8【中】出站与回调地址不强制 HTTPS
- 位置:`service/webhook.go:63-88`(`validateOutboundURL` 接受 `http`)、`:136-158`(`SendExternalApproval` 把 Bearer token 与 `payload.command` 高危 SQL 全文一起发出)、`external_approval.go:27-31`
- 问题:ADR:99 写明"HTTPS 强制:出站与回调地址均 https",代码无一处落实。`baseURL` 为 http → token + 完整高危 SQL 明文过网;`callbackBaseURL` 为 http → 我们主动告诉厂商用明文回调,厂商把 approve-anything 密钥明文发回,链路任一跳截获后可重放批准。
- 修复:两个 URL 保存时强制 `https://`(留显式 dev 例外),出站前再校验一次。

### EA9【低】站内决策不回写外部卡片,飞书卡片长期可点
- 位置:`service/gateway.go:332-387`(`finalizeApproval` 从不调 `cancelExternalApproval`)vs `:650`(仅 sweep 的 auto-reject 分支会调)
- 问题:Phase 2 的取消回写只覆盖"内部超时自动驳回"。站内审批人决策后飞书卡片仍是 pending 且可点,审批人点通过后厂商侧反馈成功而网关幂等返回原状态(`external_approval.go:120-123`),两侧观感不一致,诱发重复提单。
- 修复:把 `cancelExternalApproval` 提到 `finalizeApproval` 的两个终态分支统一回写。

### EA10【低】超时清扫与异步 dispatch 竞态:ExternalTaskID 未写回时取消被跳过
- 位置:`external_approval.go:61-80`(异步 `SetApprovalExternalTask`)、`:86-89`、`gateway.go:634-650`(sweep 用 claim 之前读出的快照)
- 问题:`timeoutMinutes` 很小或厂商响应慢时,sweep 读到的 `ExternalTaskID` 还是空 → 取消直接 return,卡片永不收敛;反向地 dispatch goroutine 可能在工单已 expired 之后才写回 task_id。仅影响卡片收敛,执行安全由幂等保证。
- 修复:取消前按 id 重读最新 `ExternalTaskID`;或 dispatch 写回时检查终态并补发 PATCH。

### EA11【低】未鉴权即完整解析并落日志任意大小 JSON 体
- 位置:`handler/admin.go:354-363`
- 问题:`ShouldBindJSON` 与随后的 `slog.Info("lark callback received", ...)` 都在 `VerifyExternalCallback`(:365)**之前**。gin 对 JSON body 无默认大小上限,未鉴权来源可让网关读入任意大报文并把字段写进日志。`slog` 会转义控制字符,不构成日志注入,但可放大内存/磁盘占用。
- 修复:该路由套 `http.MaxBytesReader`(如 64KB),日志字段用 `clip` 裁剪。

### A 组复核为"防护正确"的点(避免误报)
- **状态机原子性**:`DecideApproval` / `DecideApprovalExternal` / sweep auto-reject 三条终态转移全部走 `ClaimApproval` 条件更新 + `RowsAffected == 1`(`repository.go:621-626`),auto-escalate 走 `ClaimEscalation`;`UpdateApprovalStatus` 已无调用点。已取消/已超时单被回调时 `external_approval.go:120` 幂等分支先行返回,不会被"复活"执行。
- **重放与并发**:第二次回调进幂等分支;并发同时进入时 `ClaimApproval` 只有一方赢,输方走 `ErrAlreadyDecided` 返回当前状态,不重复执行。
- **审计链**:外部回调最终仍经 `finalizeApproval` → `recordAudit`,新事件类型均入链;A4 的 `defer Unlock` + `prev_hash` 唯一约束重试在回调并发路径下依旧成立。
- **新增日志的敏感信息**:`c72cf9f` 新增的 slog 只打 ip / apNo / 布尔 approved / 审批人数量 / 状态,未打印 callbackSecret、外部 token、SQL 全文或口令。唯一敏感面是 EA3 的 query string(由 gin.Logger 记录,非这些 slog 语句)。

## B 组 · 风险引擎 / SQL 判定 / 终端执行通道

**根因聚类——「判定串 ≠ 执行串」**:`Exec` 与导出都是先用 `sqlutil.SplitStatements` + `risk.StripComments` 把 SQL **净化**后判定,再把**原始串**交给目标库执行。只要净化器的词法与目标库真实词法有任何偏差,就产生"判定看到的是一条无害 SELECT、数据库执行的是两条"的错位。本轮找到 3 个可利用的偏差变体(ER1/ER2)+ 1 个动词解析空洞(ER3),它们**同时打穿终端与导出两条通道**。以下均已在本仓库用探针实测取证。

### ER1【严重】分割器不认 PG 美元引用与 `E''` 转义串 → 吞掉分号,堆叠语句整体逃过判定
- 位置:`pkg/sqlutil/split.go:30-46`、`service/gateway.go:97-100`、`service/export.go:27-33`、`gateway/realdb.go:299`
- 问题:`SplitStatements` 只认「双写引号转义」(`''`),不认 PostgreSQL 的美元引用 `$$...$$` 与 `E'\''` 转义串。遇到第一个 `'` 就进入"引号内逐字复制"直到下一个落单 `'`,后面没有了就**一路吞到串尾**,把 `;` 和后续语句一并并进同一条。
- 实测(直接调用被审代码):

  ```
  n=1  in="SELECT $$'$$ ; DROP TABLE t"   out=["SELECT $$'$$ ; DROP TABLE t"]
  n=1  in="SELECT E'\'' ; DROP TABLE t"  out=["SELECT E'\'' ; DROP TABLE t"]
  ```

  → `len(stmts)==1` 不触发 `strictestVerdict`,`ParseVerb`=SELECT → 能力维度 select=allow → 放行。而 PG 侧 `$$'$$` 是一个内容为单引号的合法字面量,语句在 `;` 处真实断开为两条。
- 后果:执行侧 `RealQueryEach`/`RealRun` 调 `QueryContext(ctx, query)` **不带参数**,`lib/pq` 在 `len(args)==0` 时走 **simple query 协议**,一次提交多条语句并全部执行。任何持 terminal 菜单的用户(含只读角色 `ro`)在 PROD 的 PG/GaussDB/DWS 连接上即可跑 DROP/DELETE/GRANT;**只有导出菜单的账号**经 `POST /api/v1/export` 同样可达(`exportSQLReadOnly` 用的是同一个分割器,`export.go:196` 的"纵深防御"复检也是同一个函数,双双放行)。
- 修复:分割器支持 `$tag$...$tag$` 与 `E''`;**未闭合引号一律判非法并拒绝**(而不是吞掉尾部);更根本的做法是把**判定后已归一化的语句**交给执行器,杜绝判定串与执行串分叉。

### ER2【严重】`#` 被无条件当行注释剥离,而 PG 中 `#` 是合法运算符 → 同一根因的第二个变体
- 位置:`pkg/sqlutil/split.go:56-60`、`gateway/risk.go:99-103`(`StripComments` 同样无条件吃 `#` 到行尾)
- 实测:`n=1  in="SELECT 1 #x; DROP TABLE orders;"  out=["SELECT 1"]`,`StripComments` 结果为 `"SELECT 1  "`——**字典扫描根本看不到 DROP**。
- 后果:PG 中 `#` 是整数按位异或,`SELECT 1 #2` 正常求值为 3,紧跟的 `DROP TABLE orders` 在同一 simple query 批里执行。判定链全绿(ParseVerb=SELECT、字典扫不到、能力 select=allow),数据库执行 DDL。把 DROP 换成 `UPDATE/INSERT` 连字典兜底都没有(见 ER5)。导出通道同样可达:`SELECT * FROM t #x; DROP TABLE t` 过 `exportSQLReadOnly`。
- 注:`split.go` 头注释论证"标准引号规则只会过分割(安全),绝不会合并"——**该论证对 `#` 不成立**。`#` 是 MySQL 专有注释,被无条件套到 PG 上就是合并方向。反引号有同样的不对称(`SELECT 1 \`; DROP TABLE t; \`` 实测 n=1),只是 PG 词法层会报错、MySQL 侧 `AllowMultiStatements=false` 挡住,目前不可利用但同属该缺陷类。
- 修复:`SplitStatements`/`StripComments` 增加 engine 维度(`#` 与反引号只在 MySQL 家族生效)。

### ER3【严重】前导 `;` 使 `ParseVerb` 返回空 → 能力维度降级为 `select`,只读角色可在 PROD 执行任意写/DDL
- 位置:`gateway/risk.go:52`(`verbRe = ^\s*([a-z_]+)`,`;` 打头即无匹配)、`risk.go:214-219`(`MapVerbToCapability("") → "select"`)、`service/gateway.go:97-100`
- 输入:`;UPDATE accounts SET balance=0`
- 实测:`split(1)=["UPDATE accounts SET balance=0"]`(只有 1 条 → 不走 `strictestVerdict`,`execJudged` 拿到的是**含前导分号的原串**)→ `verb=""` → `cap="select"` → `ro` 在 prod 的 select=allow → 不 deny;字典默认不含 UPDATE → RiskOff;strict 模式 `NoWhere` 因 `verb==""` 非 delete/update 直接返回 false,全表写兜底也失效 → `ActionAllow` → `IsRead`=false → `ExecContext` → 目标库执行。
- 关键:`MapVerbToCapability` 的 `case ""` 处有一段注释,声称"任何内嵌高危命令仍会被 Evaluate 里的字典扫描抓到(如 `1; DROP TABLE x`)"——**这个补偿控制只对字典里有的动词成立**,而 seed 默认字典(`seed.go:159-168`)只有 `DROP/TRUNCATE/DELETE/ALTER/RENAME/GRANT/REVOKE`,不含 `UPDATE/INSERT/CREATE/REPLACE/MERGE/COPY`。注释所依赖的前提不成立。
- 目标库接受性:**SQLite 实测接受并执行**(探针中 `bal` 由 100 变 0);PG/GaussDB 语法上接受(`stmtmulti: stmtmulti ';' stmt`,`stmt` 可空);MySQL 会语法报错。PG 上 `;COPY t FROM PROGRAM 'cmd'` 在 superuser 账户下即 RCE。顺带 `RiskCheck`(`gateway.go:57`)也返回 allow,前端不会拦。
- 修复:`Exec` 不论条数一律对 `SplitStatements` 结果逐条判定取最严;`MapVerbToCapability("")` 从 `select` 改为保守的 `write` 或直接拒绝无法解析动词的语句。

### ER4【高】SQLite 连接的 `database` 参数不过 `dbNameRe`,可指向网关自身数据库
- 位置:`gateway/realdb.go:47`(`!strings.Contains(e,"sqlite")` 显式豁免校验)、`:52`、`service/gateway.go:74-76`、`async_exec.go:28-30`、`export.go:119-126`(三处都无条件 `conn.Database = database`,`canAccessConn` 只看 tag、与库名无关)
- 场景:环境里存在任一 engine 含 `sqlite` 的连接(本地/自托管默认形态),用户对它有 tag 访问权 → `POST /terminal/exec` 的 `database` 传网关自身 `vela.db` 路径 → 读/改**网关用户表、连接口令密文、审计链**。SELECT 在任何环境矩阵里都是 allow,判定不拦。第二轮 H7 只给网络引擎补了 `dbNameRe`,sqlite 留了口子;不存在的路径还会被创建。
- 修复:`database` 覆盖前做白名单校验(只允许 `ConnectionSchema` 列出的库名);sqlite 连接禁止运行时覆盖 `Database`。

### ER5【高】`RealRunAsync` 每个异步任务新建连接池且从不关闭,连接/FD 泄漏
- 位置:`gateway/realdb.go:342-348`
- 问题:`db, err := dialPool(drv, dsn)` 绕过 `dbPoolCache` 新开一个 `*sql.DB`,但只有 `if drv == "sqlite"` 分支 `defer db.Close()`。MySQL/PG 的池既不进缓存也不关闭,函数返回后成为孤儿池(`SetMaxOpenConns(3)`)。
- 后果:3 个 worker 反复跑长任务即可把目标实例 `max_connections` 顶满——普通用户可触发的 DoS。
- 修复:改用 `openConn(conn)`(走 `dbPoolCache` + release),或对非 sqlite 也 `defer db.Close()`。

### ER6【中】异步执行通道漏检维护态,绕过 FR-CONN-04
- 位置:`service/async_exec.go:23-41` 无 maint 分支(对照 `gateway.go:82-85`)。管理员把 PROD 置维护态后,用户改调 `/terminal/exec-async` 即可照常执行。

### ER7【中】异步任务审计只记 SQL 前 80 字符、风险恒为 `mid`、提交时不落审计
- 位置:`async_exec.go:152,157`(`"ASYNC "+clip(job.SQL, 80)` + 恒 `model.RiskMid`)、`:53-67`(allow 分支只入队不写审计)
- 后果:长脚本审计不可追责;风险等级与真实 verdict 脱节;进程崩溃则执行意图在审计链里完全消失。

### ER8【中】`NoWhere` 被字符串字面量里的 `where` 蒙蔽,strict 全表写兜底失效
- 位置:`risk.go:174-182` + `:77-93`。实测 `UPDATE users SET note='where'` → `noWhere=false`。B7 只修了词边界(`elsewhere`),字面量/引号标识符里的 `where` 是同一漏洞的另一半;在 write=allow 的 dev/staging/GLI 下即为直接执行的全表写。

### ER9【中】数据修改型 CTE(`WITH … AS (DELETE …)`)逃过 strict 且被 `IsRead` 判成读
- 位置:`risk.go:174-177`(`NoWhere` 只认首动词)、`:187-199`(`readVerbs` 含 `WITH`)。实测 `verb="WITH" isRead=true noWhere=false`:无 WHERE 的全表 DELETE 不被 strict 兜底,且走 `QueryContext` 被当读记账。

### ER10【中】连接的 `policy`(strict/approve-1/audit-only)从未参与任何判定,是纯装饰配置
- 位置:`model/model.go:115` + `service/admin.go:206-218` 落库并校验,但 `risk.go:295-335` 完全不读;全仓 `.Policy` 只出现在建/改连接与前端展示。属"安全控制存在但无效",且终端状态栏还把它显示给用户(`TerminalSession.vue:702`),造成虚假安全感。

### ER11【低】`RiskCheck` 不做多语句最严判定,与 `Exec` 口径不一致(仅提示层,服务端仍拦)。`service/gateway.go:52-65`
### ER12【低】默认风险字典缺 `UPDATE/INSERT/CREATE/REPLACE/MERGE`,是 ER2/ER3 得以落地的放大因子。`bootstrap/seed.go:159-168`

### B 组复核为"防护正确"的点
- **终端元命令无旁路**:`TerminalSession.vue:284-342` 把 `\dt/\d/\l/...` 翻译成 SQL 后经 `sendExec` → WS `exec` → `handler/terminal.go:522` → `Svc.Exec`,照常过三层判定与审计;纯本地命令(`\? \clear \c \u \x \conns \G/\g`)不产生 DB 交互;用户参数经 `ident()` 白名单(`[A-Za-z0-9_$.]`)过滤后才拼进字面量,**无注入**。
- **Oracle service/SID 无 DSN 注入**:`realdb.go:101-117` 用 `dbNameRe` 校验,`go-ora` 的 `BuildUrl` 对 user/password/service 做 `url.PathEscape`(会转义 `?`),options 走 `url.QueryEscape`。
- **MySQL DSN**:`AllowMultiStatements` 保持 false + `dbNameRe`,堆叠语句在 MySQL 侧执行不了(故 ER1/ER2 的可利用面主要在 PG 系)。
- `schema.go` 内省 SQL 全为常量,`conn.Database` 不进 SQL 文本;异步任务归属越权读取已挡住;`StripComments` 的 `/*! */` 处理(A3)未回归;`strictestVerdict` 逻辑本身正确。

## C 组 · 数据导出 / Webhook / 加密

**总结论:commit d17e11f「修复数据导出可绕过网关执行任意 SQL」的修复不彻底。** `exportSQLReadOnly` 依赖的分割器与真实数据库词法存在偏差(即 ER1/ER2,同一根因在导出通道的落点),且导出路径**完整绕过能力矩阵、风险字典、PROD MFA 步进与维护态**——后者被 C 组与 D 组两个独立审查方向各自发现并实测复现。

### EX1【严重】导出通道完全绕过能力矩阵 / 风险字典 / PROD MFA / 维护态(双方独立确认)
- 位置:`service/export.go:96-139`(`EnqueueExport`)、路由 `router.go:97`;对照完整判权链 `service/gateway.go:68-101` 与 `async_exec.go:31-38`
- 问题:`EnqueueExport` 只做 savePath 检查 + `canAccessConn`(标签) + `exportSQLReadOnly`(动词白名单)。**没有 `EvaluateRoles`/能力矩阵、没有风险字典、没有 `checkMFA`、没有 `conn.Status=="maint"` 判定。**
- 实测复现:把 `ro` 角色的矩阵设为 `select@prod=deny`,用 ro 账号对 prod 连接 `analytics-ro`——`POST /terminal/exec "SELECT * FROM events"` 返回 **40300 拒绝**;同一条 SQL 走 `POST /export` 返回 **code=0**,任务入队、worker 连真库把整表导成加密包供下载。
- 另一维:`checkMFA`(`gateway.go:696-711`,默认 `security.requireMFA=true`)在导出路径**从不调用**——被劫持的会话(有 JWT 无 TOTP)在终端跑不了 SELECT,却能用导出把整张生产表拖走。
- 再一维:"动词是 SELECT" 不等于无副作用。`select pg_read_file('/etc/passwd')` 实测 `readOnly=true`,可把服务器任意文件导成加密 zip。
- 修复:`EnqueueExport` 补 `EvaluateRoles(EffectiveRoleIDs(u), conn.Env, sql)`(deny 拒 / approve 走审批)+ 风险字典 + PROD `checkMFA` + maint 判定;对 PG 禁用 `pg_read_file` 一类函数或改用受限角色执行导出。

### EX2【严重/高】导出的只读闸可被 ER1/ER2 绕过(同根因落点)
- 位置:`service/export.go:27-33`(`exportSQLReadOnly` 用 `sqlutil.SplitStatements`)、`:196`(纵深防御复检用同一函数)、`:271-275` → `gateway.RealQueryEach(conn, job.SQL /* 原串 */, …)`
- 实测:`SELECT 1 FROM dual /*!40000 INTO OUTFILE '/tmp/pwn' */` → 分割器返回 `["SELECT 1 FROM dual"]`(执行注释被整段剥离),而 MySQL 会执行注释体,把结果写到**目标服务端文件系统**。这是 A3 修复在导出路径上的回归面:`risk.go` 的 `StripComments` 为修 A3 特意保留了 `/*!` 可执行体,`split.go:61-71` 却不识别 `/*!`——**两个注释剥离器语义不一致,校验用前者、执行用原文**。
- 加上 ER1(美元引用)/ER2(`#`)两个变体,一个**只有导出菜单**的账号即可在 PROD PG 上跑 DDL。
- 修复:统一到同一个 engine 感知的净化器;执行已归一化的语句。

### EX3【中】非 prod 环境 SSRF 防护整体关闭
- 位置:`cmd/server/main.go:83`(`AllowPrivateWebhookTargets = cfg.Env != "prod" || cfg.Webhook.AllowPrivate`)、`config.go:131-136`(把 `staging`/未知 env 静默降级为 dev)、`webhook.go:75`、`:95`
- 问题:开关一旦为 true,`validateOutboundURL` 与 dial 时的 `checkDialAddr` **双双直接 return nil**,只剩 http/https 检查,连 DNS 重绑定校验也跳过,且同时影响 Webhook / 飞书 / 审批魔方三个出站通道。利用前提是管理员账户(endpoint 由 admin 配置),属提权后打内网。

### EX4【中】导出产物无清理 / 无配额 / 无保留期
- 位置:`export.go:358-378`;全仓无删除导出文件的代码。加密包长期堆在磁盘上,既是容量问题也是数据留存合规问题。

### EX5【中】导出提交与失败均不入审计,成功审计只截 80 字符 SQL
- 位置:`failExport` 只写作业表;`export.go:226` 成功审计 `clip(sql, 80)`。试探性拖数据不留痕。

### EX6【中·待验证】`database` 参数对 sqlite 引擎连接零校验(同 ER4 在导出通道的落点)
- 位置:`realdb.go:47-52`。若环境存在 sqlite 连接,可指向 `../vela.db` 导出网关自身用户表/MFA secret;不存在的路径还会被创建。

### EX7【低】`WebhookConfig.Secret` 明文入库;`notify.larkWebhook` 会回显给非管理员
- 位置:对比 `admin.go:22-25` 只加密了 approval token;`notify.larkWebhook`(URL 内嵌 bot token)不匹配 `isSecretKey` 的子串过滤,被 `GET /settings` 返回给任何持 settings 菜单的用户(`router.go:147` 无 admin 中间件)。

### EX8【低】队列满时 `export.go:133-138` 返回 200 + 一个实际已 failed 的 job。
### EX9【低】`FailStuckExportJobs`(`repository.go:825`)无实例维度,多副本部署会误杀他实例在跑的作业(与 ED10 同源)。
### EX10【低】单分片 100 MiB 内存缓冲 × 3 worker + `ZipEncrypt` 再复制一份密文,低权限用户可自由触发 OOM。

### C 组复核为"防护正确"的点
- **重定向被无条件拒绝**,Bearer token 不会随 302 泄露到第三方主机。
- **`GenPassword` 用 `crypto/rand` + 拒绝采样**(`export.go:201` 的 `math/rand` 只用于模拟延迟,不涉密)。
- **AES-GCM nonce 每次随机、无复用**,解密失败不回显密文;`ZipEncrypt` 用 WinZip AES-256 而非弱 ZipCrypto。
- **下载有 base 前缀 + `ownsExportFile` 双重校验**,无路径穿越/越权;webhook 事件筛选的包含匹配无前缀误匹配。

---

## D 组 · 认证 / 会话 / RBAC / MFA / 用户管理

本组多数条目由并行审查方在 `internal/bootstrap` 的 httptest 黑盒 harness 上写临时用例**实测取证**(标 [已实测]),临时文件已清理。

### EU1【高】任何持有会话的人可无验证码关闭已启用的 MFA(MFA 自助降级)
- 位置:`service/gateway.go:744-754`(`MFASetup`)、路由 `router.go:68`
- 问题:`secret := u.MFASecret; if secret == "" || u.MFAEnabled { secret = totp.GenerateSecret() }` 之后直接 `UpdateUserMFA(u.ID, false, secret)`——`POST /auth/mfa/setup` 只挂 JWT,**不要求 TOTP 码也不要求重输密码**,对已绑定 MFA 的账户调用即刻 `mfa_enabled=false` 并换新密钥(新密钥还在响应里返回)。
- [已实测]:调用后 `/auth/me` 的 `mfaEnabled` 变 false、纯口令登录重新成功、`checkMFA`(`gateway.go:700`)判定为"未注册" → PROD 二次验证整体跳过;即使开 `mfaMandatory`,攻击者手里已有新 secret,可自行 `mfa/enable` 重新绑定继续绕。
- 对比:`MFADisable`(`:778`)要求有效且未消费的码——**两条路径强度不对等,攻击者只走弱的那条**。
- 修复:`MFASetup` 在已启用时先验一枚当前 TOTP 码(或重输密码);新密钥存 pending 字段,`MFAEnable` 通过后再替换,验证前绝不关闭已生效的 MFA。

### EU2【高】"移除角色成员"不撤销权限(主角色 `role_id` 残留)
- 位置:`repository.go:321-323`(`RemoveMember` 只 `DELETE FROM tbl_role_member`,从不动 `tbl_user.role_id`)+ `:336-354`(`EffectiveRoleIDs = u.RoleID ∪ tbl_role_member`)、路由 `router.go:119`、UI 入口 `PermissionsView.vue:162-169,355`
- 问题:所有建号路径都会让主角色同时有成员行,于是"移除成员"对主角色完全无效。
- [已实测]:`POST /users {roleIds:[adminRoleID]}` 建号 → 管理员点 X 移除该成员(返回 ok,角色详情里成员消失)→ 该用户**同一 token 继续 `POST /users` 建号仍 code=0**,`AdminOnly` 拿到的并集仍含 admin。**管理员以为收回了平台管理员权限,实际没收回。**
- 修复:`RemoveMember` 在 `roleID == u.RoleID` 时拒绝或同步重指 `role_id`,并 `BumpTokenVersion`。

### EU3【高】导出通道绕过能力矩阵(与 EX1 为同一问题,两个方向独立复现)
- 见 EX1。此处补记 D 组的实测口径:`ro` 矩阵 `select@prod=deny` 下,`/terminal/exec` 返回 40300 而 `/export` 返回 code=0 并真实导出。

### EU4【中】管理侧操作零审计,可无痕冒充审批人
- 位置:`service/admin.go` 全文、`service/gateway.go:795-835`、`handler/admin.go:161-224,454-534`
- 问题:全仓 `recordAudit` 调用点只有登录、exec/async/export、审批流转。`POST /users`、`PATCH /users/:id`、`/users/:id/password`、`/mfa/bind`、`/mfa/reset`、`PUT /roles/:id/menus|capabilities|tags`、角色成员增删**一条审计都不写**。
- 利用场景:拿到 admin 会话者改掉某 owner 的口令 + `mfa/bind` 拿其 TOTP 密钥(响应直接返回明文 secret,`handler/admin.go:527`),以该审批人身份登录批准自己的高危工单(绕开 R16 自批禁令与两人控制),事后改回口令。审计链里只有"该审批人批准了工单",**无任何一行记录口令/MFA 被谁改过——哈希链再完整也证明不了这段**。
- 修复:用户/角色/权限的每次写操作都过 `recordAudit`;`AdminBindMFA` 改为一次性链接而非明文 secret。

### EU5【中】管理控制台可绕过 M12 口令策略
- 位置:`service/admin.go:368`(`CreateUser` 只判 `len<8`)、`service/gateway.go:797`(`AdminSetPassword` 同)vs `bootstrap/init.go:89-116`(`validateAdminPassword` ≥12 位 + ≥3 类)——**强度策略只挂在 CLI 上,HTTP 侧没接**。
- [已实测]:`POST /users {password:"password", roleIds:[adminRoleID]}` → code=0 且该口令登录成功,得到一个平台管理员;`POST /users/1/password {"password":"12345678"}` → code=0,超管口令降为 8 位纯数字。配合 EU4 无审计 + 登录限速仅按来源 IP,M12 实际等于没有。
- 修复:抽公共 `ValidatePassword`,被授予 admin 角色码的账户强制 ≥12 位 + ≥3 类。

### EU6【中】WS 建连后不再校验 token 过期,`sessionTTL` 对终端通道不成立
- 位置:`handler/terminal.go:458-521`。握手时 `JWT.Parse` 校验 `exp`,但循环内的 fresh 重查(`:509-520`)只看 `disabled`/`TokenVersion`/菜单,**从不再看 `claims.ExpiresAt`**,也没有 read deadline / 最大寿命。
- 场景:在到期前 1 秒建连、靠客户端 `ping` 保活,即可在会话有效期结束后无限期继续执行 SQL;调小 `sessionTTL` 对已建连接无效。R5 当初只补了 token_version/disabled,**过期这一维漏了**。

### EU7【低】账户置为 `invited` 后仍保留活会话
- 位置:`middleware.go:78-81`、`terminal.go:472` 只拒 `disabled`,而 `PatchUser`(`admin.go:315-320`)允许置 `invited`。[已实测] 改为 invited 后 `GET /auth/me` 仍 code=0,会话照常可执行命令直到 TTL。当前前端只发 active/disabled,属 API 层面可达。

### EU8【低】邮箱未归一化:SQLite 下同一邮箱可存在两个账户
- 位置:`admin.go:401-415`(`Invite` 不 trim 不小写)vs `:364`(`CreateUser` 小写)vs `service.go:182`(`Login` 原样查)。[已实测] 先 invite `Dup@Vela.io` 再 create `dup@vela.io`,两条都 ok,停用/改权限只作用于其中一行。MySQL 默认 CI 排序规则下会被唯一键挡住(环境相关)。

### EU9【低】`PatchUser` 不校验 `roleId`
- 位置:`admin.go:322-324` 只有 `RoleIDs` 走 `validateRoleIDs`。`PATCH {"roleId":999999}` 落库 → `BuildMe` 的 `GetRole` 失败 → `/auth/me` 50000,**账号锁死**。若成员行也被删空,`EffectiveRoleIDs` 为空,`risk.go:276-278` 的 `capabilityLevelUnion` 空集返回 `LevelAllow` 是 fail-open(当前被 MenuGuard 与 `canAccessConn` 挡住不可利用,但默认值方向错)。

### EU10【低·待验证(需 MySQL)】超长邮箱可绕过登录失败审计并占用审计全局锁
- 位置:`service.go:227-230` 把原始 email 直接当 `ActorName`,而 `AuditLog.ActorName` 是 `size:64`,`LoginReq.Email` 无长度约束、无截断。MySQL 严格模式下超过 64 字符触发 1406 → `appendAudit` 在全局 `auditMu` 内连做 5 次失败插入后吞错。
- 后果:暴力破解只要把邮箱补到 65+ 字符就**不留失败登录审计**(绕过 R29),且每次请求串行占锁 5 次往返拖慢全进程审计写入。卡在本地只有 SQLite(不强制列长),需在 MySQL 上验证。

### D 组复核为"防护正确"的点
- **多角色并集在所有校验点一致**:MenuGuard(`middleware.go:139`)、AdminOnly(`:117`)、`canAccessConn`(`admin.go:153,178`)、`canSeeAllActivity`(`:437`)、RiskCheck/Exec/strictestVerdict/execJudged/SubmitScriptForApproval(`gateway.go:57,108,135,212`)、ExecAsync(`async_exec.go:38`)、WS fresh 重查(`terminal.go:483,517`)**全部走 `EffectiveRoleIDs`,无残留单 `RoleID` 判定**;`jwt.Claims.RoleID/RoleCode` 只用于展示,不参与授权。
- M1 吊销三通道成立;R8/C2/C3/R17/R29/M3/B8/R15 均在位;`resp.Abort` 用 `AbortWithStatusJSON` 确实中断链;路由无漏挂鉴权/白名单;CORS 与 `SetTrustedProxies` 无绕过。
- **设计确认项(非新缺陷)**:`security.idleLock`/`idleMinutes` 只有前端实现(`AppLayout.vue:111-122`),后端无消费点——空闲锁定对直接调 API / 已建 WS 无效,被盗 token 在整个 TTL 内始终可用。与 L6 定位一致,若要作为安全控制需服务端 `last_seen` 判定。

---

## E 组 · 数据层 / 模型 / 迁移 / 配置 / 启动部署

**模型 ↔ 迁移逐表核对结论:19 个模型 vs 0001–0007 SQL,列级无漂移。** 第四轮之后新增的字段/表都有对应增量迁移:`AuditLog.Database`→0003、`ExportJob.Database`→0004、`ExportJob.Password` 加宽→0005、`Approval.ExternalTaskID`+索引→0006、`AsyncJob` 整表→0007。Oracle service name/SID 没有新增列(复用 `tbl_connection.db_name`,`realdb.go:97-110` 用 `sid/` 前缀区分)。GLI 也没有新增列——**但它需要的是数据行,而那条路在生产升级流程上是断的(ED1)**。

### ED1【严重】GLI 灰度环境在生产升级路径上零风险管控(fail-open)
- 位置:`bootstrap/seed.go:34-73`(`seedGliEnv`),调用点仅 `seed.go:23`(受 `database.seed` 开关)与 `init.go:29`;`cmd/server/main.go:162-184`(`runMigrate` 只调 `bootstrap.Migrate`);`configs/config.prod.yaml:21`(`seed: false`);`DEPLOY.md:52-60`(每次发版跑 `migrate`)vs `DEPLOY.md:62`(`init` **仅首次**);`migrations/` 下无任何写 gli 行的迁移。
- 问题:已上线的生产库(GLI 提交 `dd4c72c` 之前完成 `init`)按文档升级只跑 `migrate` → `seedGliEnv` **永不执行** → `tbl_role_capability`/`tbl_risk_command` 里一条 `env='gli'` 都没有。此时**双重 fail-open**:`repository.go:246-252` `CapabilityLevel` 查不到行返回 `LevelAllow`(已核实,连 `err != nil` 也返回 allow);`gateway/risk.go:226-246` `matchCommand` 收集到 0 条字典行返回 `RiskOff`。
- 后果:**管理员一旦建出 `env=gli` 连接,`DROP TABLE`/`TRUNCATE` 在灰度库上直接执行,不拦截不送审**,任何角色(含研发只读)都拿到 allow。
- 为何测试是绿的:`gli_env_test.go` 走 sqlite + `Seed()` 路径,恰好覆盖不到生产的 `migrate` 路径。
- 修复:新增 `0008_seed_gli_env.sql`(`INSERT...SELECT` 从 staging 克隆 + `ON DUPLICATE KEY UPDATE`),或在 `runMigrate` 后无条件调 `seedGliEnv`(它本身幂等)。

### ED2【高】`APP_ENV=prod` + 仓库自带 `config.yaml` 会把演示管理员 `linwei@vela.io / vela123` 播种进生产 MySQL
- 位置:`configs/config.yaml:6-7`(注释明确引导 `APP_ENV=prod ./server`)+ `config.yaml:25`(`seed: true`)+ `main.go:65-69` + `seed.go:17-22` → `seedFreshData`(`seed.go:222-233`)
- 问题:`auto_migrate` 已被 `shouldAutoMigrate` 正确忽略(B6 未回归),但 **seed 没有任何 env 守卫**;`validateAdminPassword`(≥12 位/≥3 类)只作用于 `init` 路径。README 公开了这组凭据 → 生产平台管理员被公开口令接管。
- 修复:`main.go` 在 `cfg.Env=="prod"` 时拒绝 `Seed()`,并在 `seedFreshData` 入口断言非 prod。

### ED3【高】Repository 关键读取一律吞错并 fail-open:一次 DB 抖动 = 全量放行 + 标签隔离失效
- 位置:`repository.go:481-485`(`RiskCommands` 丢弃 error)、`:246-252`(`CapabilityLevel` `if err != nil { return LevelAllow }`,已核实)、`:142-150`(`TagsForRole` 丢弃 error)
- 问题:把"查询失败"与"查无此行=默认"混为一谈。MySQL 连接被 kill / 连接数打满 / 锁超时任一瞬时错误下,风险字典层与能力矩阵层**同时静默消失** → PROD 上 `DROP TABLE` 直接放行;`TagsForRoles`(`:440-456`)命中 `len(tags)==0 → unrestricted` → 受限角色瞬间看到并可操作全部连接。
- 与 ED8(MySQL 连接池完全未配置)叠加时,这不是理论故障——`wait_timeout` 后的 `invalid connection` 就足以触发。
- 修复:改返回 `(T, error)`,调用方对**查询错误**一律 fail-closed(区别于"无行=默认")。

### ED4【高】`/gateway/stats` 每 5 秒 × 每在线用户触发一次 `tbl_audit_log` 全表 JOIN
- 位置:`repository.go:950-957`(`CountProdInterceptions` JOIN `tbl_connection`)+ `0001_init.sql:138-155`(只有 time/actor/risk 三个索引,**无 `connection_id`**)+ `router.go:64`(无菜单门禁,所有登录用户可访)+ `frontend/src/components/AppLayout.vue:106`(`setInterval(fetchGwStats, 5000)`)
- 后果:审计表 append-only 持续增长,50 人在线 = 10 次/秒全表扫,百万行后打满网关自身 MySQL,进而触发 ED3 的 fail-open 连锁。
- 修复:加 `(connection_id)` 索引 + 进程内 30~60s 缓存。

### ED5【高】`CreateConnection` 不校验 `env`/`policy` 白名单 → 任意 env 字符串等于"零管控环境"
- 位置:`service/admin.go:27-46`(既无 `validPolicies` 也无 env 白名单;`UpdateConnection` 在 `:56` 校验了 policy 但**同样不校验 env**)、`dto/dto.go:130-139`(binding 只有 `required`)、`0001_init.sql:72-91`(裸 VARCHAR 无 CHECK)
- 问题:env 写成 `uat`/`pre`/拼写错误,就得到与 ED1 完全相同的 fail-open——**打字错误就能造出"无管控生产库"**。
- 修复:抽 `validEnvs = {prod,gli,staging,dev}`,Create/Update 双向校验。

### ED6【中】已发布迁移 0006 被就地改写,ledger 无内容校验
- `150bd84`(07-23)发布的 0006 含 `lark_message_id`,`ef3b98a`(07-24)修改了**同一已发布文件**删掉该行;`migrate.go:109-138` 的 ledger 只记文件名不记 hash。07-24 前跑过 migrate 的库会永久多一个孤儿列,且**无任何检测手段**。修复:确立迁移不可变约定 + `schema_migrations` 加 checksum 列。

### ED7【中】多语句迁移非事务非幂等,部分失败后 `migrate` 永远修不好
- `migrate.go:127-137` 任一条失败即 return 且 ledger 不写;`0006` 有 2 条语句(`ADD COLUMN`+`CREATE INDEX`)。第 2 条因锁超时失败后重跑必在第 1 条报 `Duplicate column name`,只能人工进库补。

### ED8【中】MySQL 连接池完全未配置
- `db.go:22-27` mysql 分支 `gorm.Open` 后直接 return(只有 sqlite 分支设了 `SetMaxOpenConns(1)`)。无上限连接数会打满 MySQL `max_connections`;无 `ConnMaxLifetime` 导致 `wait_timeout` 后的 `invalid connection`,**叠加 ED3 静默变成"全部放行"**。

### ED9【中】没有优雅关闭
- `main.go:112-121` 直接 `r.Run`,无 `http.Server.Shutdown`/信号处理;`main.go:99-105` 的清扫 goroutine 与 `service.go:65-84` 的 worker 池无退出通道。`systemctl restart` 时执行中的 SQL 被切断(可能已在目标库提交但审计行未写,**哈希链缺环**)。

### ED10【中】`FailStuck{Export,Async}Jobs` 无实例归属判定,多副本互杀
- `repository.go:825-830`/`887-892` 无条件把所有 pending/running 置 failed,`service.go:60,74` 在 `New` 里无条件执行。副本 B 启动会把副本 A 正在跑的 30–60 分钟任务写成 failed,A 跑完再覆写回 done,状态来回翻转。表上没有 owner/heartbeat 列。

### ED11【中】`VELA_SECRET_KEY` 无强度校验(第四轮 A2 解耦修复引入的新面)
- `config.go:154-190` 只校验 `JWT.Secret`;`config.go:224-229` + `crypto/cipher.go:51-54` 任意非空值直接 SHA-256。设 `VELA_SECRET_KEY=vela` 服务照常启动,**全库被管 DB 连接口令实际由 4 字符口令保护**。

### ED12【中】分页 limit ≤ 0 视为"不限"
- `repository.go:854-862`/`769-777`/`832-840`/`754-762` 都是 `if limit > 0`;`handler/terminal.go:161` `strconv.Atoi` 出错时 limit=0 且不钳上限。`GET /async-jobs?limit=abc` 一次拉全部任务连同每条完整 MEDIUMTEXT 日志(`0007:14`,单行 16MB),循环请求可 OOM。审计接口做了 500 钳制,这几个漏了。

### ED13【中】`tbl_approval_step.approver_id` 无索引
- `repository.go:568` 每次 `GET /approvals` 全表扫,并把命中的全部 approval_id Pluck 进内存拼 `IN(...)`(`:573,577`),`ListApprovals` 本身也无 LIMIT。上万单后会撞 prepared statement 参数上限。

### ED14–ED20【低】
`tbl_role_member` 无 user_id 索引而鉴权热路径按 user_id 查(`repository.go:326-330`,PK 前导列是 role_id);`0007` 时间列 `DATETIME NULL` 与 0001 的 `DATETIME(3) NOT NULL DEFAULT` 不一致;`maxSeq`(`:24-39`)启动时 `LIKE` 前缀 + 全量 Pluck 两个无索引列;启动不校验 schema 版本,漏跑 migrate 时静默启动且 `/healthz` 仍 ok;`/openapi.yaml`(`router.go:31`)未鉴权公开且 `build.sh:68` 真的打包进生产;`Services` 无 Close,worker goroutine 永不回收;审计深翻页 page 无上限(`service/admin.go:469-476` 只钳下限)。

### E 组复核为"防护正确"的点
- 模型↔SQL 列级无漂移;`shouldAutoMigrate` 对 mysql 恒 false(B6/H13/H14 未回归);迁移咨询锁用 `sqlDB.Conn(ctx)` 固定连接并校验 `RELEASE_LOCK`(B4 未回归);`splitSQLStatements` 引号/注释感知并跳过 `CREATE DATABASE`/`USE`。
- **repository 全层无字符串拼接进 Where/Order/Raw**(`maxSeq` 的 table/col 是包内常量);五个 Claim/Consume 方法都是条件 UPDATE + `RowsAffected==1`;五个 Set* 方法均在事务内。
- `weakJWTSecrets` 已收录 config.yaml 实际默认值 + 熵下限(R10/C5 未回归);`webhook.allow_private` 生产默认 false 且开启时 WARN;docker-compose 绑 127.0.0.1 且不再挂 initdb.d。
- **设置项缺失时代码侧都有安全默认**(`approval.external.enabled` 默认 false、`gateway.asyncExecTimeout` 默认 5400),所以"老库未播种新设置键"不构成缺陷——**这正是它与 ED1 的区别:ED1 走的是"表里无行=放行"的语义,没有代码侧默认兜底。**

---

## F 组 · 前端(Vue 3 + TS + xterm.js)

先记本轮**确认无问题**的项,避免后续重复排查:
- 全仓 `v-html` / `innerHTML` / `insertAdjacentHTML` / `document.write` / `eval` **零命中**,新增的 `ResultGrid.vue` 单元格走 `{{ }}` 文本插值(`:56`),**不存在存储型 XSS**。
- `src/locales/*.json5` 中除 `loginHint` 已用 `{'@'}` 正确转义外,无其他裸 `@` / `|` / `{}`,本轮新增文案不会触发 vue-i18n prod 编译报错(该坑未回归)。
- 后端终端结果集有 `maxResultRows = 200` 上限(`realdb.go:222`),ResultGrid 全量渲染不构成主线程卡死。
- `truncateDisp`/`dispWidth` 用 `for...of` 按码点迭代(`sqlResult.ts:162-179`),不会切坏代理对,也不存在死循环。
- `translateMetaSql` 的 `ident()` 白名单为 `[A-Za-z0-9_$.]`,引号/反斜杠均被剥离,拼进字面量无法逃逸。
- `wsTerminal.ts` 的心跳/退避/`MAX_FAILED_OPENS`/空 token 短路(R27)完好;`TerminalSession.onUnmounted`(248-252)与 `AppLayout`(157-161)定时器与监听器均已清理,**无泄漏**。

### EF1【严重】WS 断开走 REST 回退时丢掉目标库,语句在**错误的数据库**上执行
- 位置:`TerminalSession.vue:385`(另 `:547`)
- 问题:`if (sendExec(raw, '') === 'ws') return;` 之后 `const env = await api.exec(props.conn.id, raw)` —— **少了第 5 个参数 `database`**。`sendExec` 走 WS 时带 `database: targetDb.value`(`:449`),元命令 REST 分支带(`:356`),MFA 补验 REST 分支带(`:474`),**唯独最常用的普通 SQL REST 回退(:385)与审批提交 REST 回退(:547)没带**。后端 `gateway.go:74` 仅在 `database != ""` 时覆盖 `conn.Database`,否则用连接自身默认库。
- 场景:用户在树里选中 `orders_db`(提示符显示 `cluster/orders_db ❯`),WS 因网关重启/网络抖动断开(状态栏显示"已断开·重连中"但终端仍可用),执行 `DELETE FROM t WHERE id=1;` → **实际打在连接配置的默认库上**。`:547` 更糟:审批工单落库的 `ap.Database` 就是错的默认库,审批通过后网关按工单里的库代执行,**错误被永久固化**。
- 修复:两处补 `targetDb.value`(及 `:547` 的 `mfaCode` 位参),或让 `sendExec` 统一封装 REST 回退,杜绝调用点各写一份参数。

### EF2【严重】REST 回退下"能力矩阵拒绝"被渲染成 `✓ 执行成功`
- 位置:`TerminalSession.vue:453-457 / 518-523 / 533-541`
- 问题:后端 `resp.Fail` 一律返回 **HTTP 200 + 业务 code**(`pkg/resp/resp.go:113`),能力矩阵拒绝时返回 `code=40300` 且 `data` 为空(`handler/terminal.go:107-110`)。前端 `handleExecEnv` 只识别 `42800`,其余一律进 `renderExecEnvelope` → 非 42200 → `renderOutput({text: undefined, rows: undefined})` → 落到 `:523` 的 `else` 分支,打印绿色 **`✓ 执行成功`**,并把 `risk` 置为 `safe`。
- 场景:WS 断开时(REST 回退唯一被触发的场景)在 PROD 执行一条被能力矩阵 deny 的 DDL,**终端明确告诉用户"执行成功"**,而实际命令被拒绝、审计里记的是 rejected。`code=40001`(连接不存在)同样显示成功。WS 路径有 `type:"error"` 分支正确提示,两条路径行为不一致。
- 修复:`handleExecEnv` 增加 `env.code !== CODE_OK` 兜底分支按 `env.msg` 红色输出;`renderOutput` 的"无 text 无 columns"只有在 `code===0` 时才算成功。

### EF3【高】数据库结果内容里的 ANSI/OSC 转义序列被原样写入 xterm(输出伪造 + 输入行注入)
- 位置:`lib/sqlResult.ts:212`(`renderTable` 的 `clean` 只清 `\r\n\t`,**不清 ESC/C0/C1**)、`:197`(`renderVertical` 完全未过滤);另 `TerminalSession.vue:520`、`:492`。后端 `cellString`(`realdb.go:386-397`)把列值原样转字符串,不做控制字符清洗,`[][]string` 经 WS 直达 `term.write()`。
- 利用场景:攻击者只要能往被查询的表里写一行数据(常见:用户昵称、备注、日志表),在值里塞转义序列 —— DBA 一条 `SELECT * FROM users` 就会:①**改写已打印的滚动区**,例如抹掉 `⚠ 正在操作 PROD` 红色警示行,或伪造绿色 `✓ 执行成功` / 伪造提示符,做操作现场的视觉欺骗;②值含 `\x1b[6n`(DSR)时 xterm.js 会回应 `\x1b[<r>;<c>R`,该回应经 `term.onData` 进入 `LineEditor`,未匹配已知序列走 `lineEditor.ts:187` 的"跳过引导符"分支,剩余 `[12;5R` 被当可打印字符**插入当前 SQL 缓冲区**(`:211-217`);若此时正忙则塞进 `queued` 在 `resume()` 时回放到下一条语句。(能否直接注入 `\r` 触发自动执行**待验证**,但污染输入行已确证。)③破坏列宽计算,整张边框表格错位。
- 修复:在 `renderTable`/`renderVertical`/`renderOutput` 写入前统一 `replace(/[\x00-\x08\x0b\x0c\x0e-\x1f\x7f-\x9f]/g, '')`,仅保留前端自己生成的 ANSI 着色。

### EF4【高】粘贴多语句批次:取消审批 / 取消 MFA / 命令被拒后,**剩余语句照跑**
- 位置:`lib/lineEditor.ts:77-82`(`resume()` 无条件回放 `queued`)+ `TerminalSession.vue:557-561`(`cancelApproval`)、`:481-486`(`cancelMfa`)、`:367-372`(deny 分支)——三条"用户明确终止"的路径全都调了 `editor.resume()`。
- 场景:粘贴 `A; B; C; D;`,B 命中高危规则弹出审批框,用户看到 `DROP TABLE` 后点"取消"——终端打印"已取消提交,命令未执行",然后**立刻继续执行 C 和 D**。用户的取消语义是"停止这一批",实际只停了一条。`Ctrl+C` 才会清空 `queued`(`:169/242`),但弹窗里点取消不会触发。
- 修复:`resume(abortQueue = false)` 或新增 `dropQueued()`,在三处先清空队列再恢复,并提示"已丢弃剩余 N 条语句"。

### EF5【高】执行中 WS 断开 → 编辑器永久 busy,终端假死且无恢复路径
- 位置:`TerminalSession.vue:446-451`、`lib/wsTerminal.ts:97-113`
- 问题:`sendExec` 经 WS 发出后即返回 `'ws'`,`LineEditor.busy` 置 true,**只等 `onWsMessage` 里的 `editor.resume()`**。而 `WsTerminal.onclose` 只调 `onStatus('closed')`(仅更新状态灯),**没有任何"在途请求失败"的回调**。
- 场景:执行一条 30s 查询,期间网关重启/心跳 pong 超时触发 `ws.close()`。socket 重连成功,但那条命令的响应永远不会到达 → 编辑器一直 busy,之后所有按键被静默吞进 `queued`,终端看起来"完全没反应"。点"重连会话"也救不回来:`refreshSession()` 只 `printAbove` + `ws.reconnect()`,而 `printAbove` 在 busy 时连重绘都不做(`lineEditor.ts:65`),更不会 `resume()`。唯一出路是刷新整页。
- 修复:`WsTerminal` 增加 `onDisconnected` 回调,或在 `onStatus('closed')` 时若存在在途 `pendingSql` 就打印"连接中断,本条命令结果未知"并 `editor.resume()`;同时给 exec 加超时看门狗。

### EF6【高】后端 `session_revoked` 消息前端未处理:权限被回收时静默吞掉
- 位置:`TerminalSession.vue:488-495`,`onWsMessage` 的 if/else 链只处理 `output`/`intercept`/`mfa_required`/`error`,**其余 `return` 直接丢弃**。而后端两处会主动下发 `{"type":"session_revoked"}`:用户被停用/token 版本变更(`handler/terminal.go:511`)、以及 B9 新增的 terminal 菜单被回收(`:518`),发完即关闭 socket。
- 场景:管理员停用某用户或撤销其 terminal 菜单 → 该用户终端里刚提交的命令毫无反应(消息被丢弃、`resume()` 未调用,**叠加 EF5 直接卡死**),既不提示"会话已失效"也不跳登录。后续只能靠连续 3 次握手失败 → `onAuthError` → 探测 `/auth/me`;而**菜单回收并不会让 token 失效**,`/auth/me` 返回 200 → 重连 → 无限循环。
- 修复:增加 `session_revoked` 分支,打印 `m.message` 并 `auth.clearSession()` + 跳转 `/login`。

### EF7【高】审批提交的 REST 回退:成功执行不打印任何输出,失败被空 catch 吞掉
- 位置:`TerminalSession.vue:543-555`。`code === 0`(命令实际已执行)时什么都不打印;`catch { /* ignore */ }` 把网络/500 也完全静默。
- 场景:WS 断开时提交高危命令审批,若该命令实际未命中拦截,后端直接执行并返回结果——**终端只是回到提示符,用户看不到任何执行痕迹**,也看不到影响行数。修复:补 `else renderExecEnvelope(env)`,`catch` 里打印 `termExecFail`。

### EF8【中】DbTree 切换实例存在竞态,旧实例的 schema 会覆盖新实例
- 位置:`DbTree.vue:15-26`,`watch(() => props.selectedId, ...)` 内 `schema.value = await api.connectionSchema(id)` **无请求序号/取消**。
- 场景:先点实例 A(内省真连库常需数秒),再点 B(快)。B 先渲染,随后 A 的响应覆盖 `schema.value`,但树上高亮的是 B → 点某个库触发 `selectDb(B.id, "A 的库名")` → 用一个 B 上并不存在的库名开标签页,**后续执行按 EF1 的路径再打到默认库上**。`dbLoading`/`schemaOpen` 也未随实例切换重置。
- 修复:引入请求序号(`const seq = ++reqId; ... if (seq !== reqId) return`)或 AbortController。

### EF9【中】`RiskRulesView` 的 isAdmin 判定没走多角色并集
- 位置:`RiskRulesView.vue:17` `auth.me?.roleCode === 'admin'`,对比 `ConnectionsView.vue:15` / `PermissionsView.vue:25` 的 `roleCodes?.includes('admin') ?? (roleCode === 'admin')`。
- 场景:持有 `[ro, admin]`、主角色为 `ro` 的账号,在连接配置页和权限页是管理员,**到高危规则页却全部只读**——切换等级、增删命令、严格模式开关全部静默 `return`(`:98/116/126/139`)。权限模型不一致,且这是"最该能改"的风控页。修复:抽 `useIsAdmin()` 三处共用。

### EF10【中】`RiskRulesView.toggleStrict` 乐观翻转失败不回滚
- 位置:`RiskRulesView.vue:97-106`,翻转在 `try` 内、`await` 之前,`catch` 无回滚。同一个"严格模式"开关在 `SettingsView.toggleStrict`(`:158-167`)已按 R28 修成失败回滚,**风控页这份副本没修**。后果:保存失败后页面显示已开启而服务端仍关闭,管理员误以为兜底防护生效。

### EF11【中】设置页用"翻译后的 label"当持久化取值:切语言后保存会静默重置审批超时与会话 TTL
- 位置:`SettingsView.vue:113 / 134-137 / 170-172`。`apprTimeout`/`ttl` 两个 ref 存的是**当前语言下的显示文案**,`save()` 再用 `t()` 反查。
- 场景:服务端配置为 `auto-reject` + `24h`,用户点右上角中/EN 切换语言(`ui.setLang` 不会同步这两个 ref)→ 下拉显示的仍是旧语言文案且不在新 options 里 → 点"保存设置",两个比较全部落空 → **静默把配置改成 `auto-escalate` + `8h`**。审批超时策略与会话有效期被悄悄放宽,无任何提示。
- 修复:ref 存内部 key(`'auto-reject'`/`'4h'`),渲染时再 `t()` 映射(`AuditView` 的 `riskLabel` 按索引映射是正确写法)。

### EF12【中】审计导出不检查 `res.ok`,且绕过 401 拦截器
- 位置:`AuditView.vue:82-99`。裸 `fetch` 后直接 `res.blob()` 存盘并提示"✓ 已导出":①服务端返回 401/403/500 时把错误 JSON/HTML 存成 `audit_export.csv` 并显示成功;②绕过 `api/http.ts:30-42` 的 401 拦截器,会话过期不会清 store、不会跳登录。

### EF13【中】LineEditor 单行重绘假设在"SQL 超过终端宽度"时花屏 + 光标错位
- 位置:`lineEditor.ts:101-105`(另 `:248-253` Ctrl+L)。`\r\x1b[2K` 只擦当前一行,`\x1b[{n}D` 到列 0 即停、不跨行。
- 场景:输入超过终端列宽的 SQL(SQL 控制台常态,尤其配合左侧树 + 右侧检查面板后终端只剩中间一栏),缓冲区折行显示;此时按退格/←→/Home/Ctrl+U,上面几行残留旧文本,光标停在错误位置,后续输入插到错误位置。文件头注释虽把"单行光标数学"写成有意简化,但它在真实使用中直接产生显示错乱与误编辑风险。
- 修复:按 `(promptLen + buf.length) / term.cols` 计算占用行数,重绘前 `\x1b[{k}A` 回首行逐行清除,光标用 CUP 绝对定位。

### EF14–EF20【低】
`renderVertical` 标签宽度用 `col.length` 而非 `dispWidth`,CJK 列名竖排错位(`sqlResult.ts:190/195`);`WebhookPanel.loadDeliveries` 无 try/catch 产生 unhandled rejection(`:76-82`);`URL.revokeObjectURL` 紧跟 `a.click()` 且 anchor 未入 DOM,Firefox/Safari 大文件下载可能被取消(`ExportView.vue:97-98`、`UploadView.vue:56-57`、`AuditView.vue:92-93`);异步执行页 `setInterval` 两请求不排序、整体赋值互相覆盖导致日志抖动,且列表接口返回完整 mediumtext 日志每 2s 反复搬运(`AsyncExecView.vue:65/33-44`);新建高危规则表单的"规则名称" `rfName` 从未提交(`RiskRulesView.vue:51-68`);连接页"导入实例""新建连接"两个按钮无 `@click`(`ConnectionsView.vue:176-177`);结果网格把 SQL NULL 与空字符串都渲染成 `∅` 无法区分(`ResultGrid.vue:56`)。

---

## 第五轮汇总

| 组 | 严重 | 高 | 中 | 低 | 小计 |
|---|---|---|---|---|---|
| A 外部审批 | 0 | 4 | 4 | 3 | 11 |
| B 风险引擎/终端 | 3 | 2 | 5 | 2 | 12 |
| C 导出/Webhook/加密 | 2 | 0 | 4 | 4 | 10 |
| D 认证/RBAC/MFA | 0 | 3 | 3 | 4 | 10 |
| E 数据层/迁移/配置 | 1 | 4 | 8 | 7 | 20 |
| F 前端 | 2 | 5 | 6 | 7 | 20 |
| **合计** | **8** | **18** | **30** | **27** | **83** |

(EU3 与 EX1 为同一问题的两次独立复现,汇总计一次;EX6 与 ER4 同源、EX9 与 ED10 同源,均已交叉标注。)

### 建议修复顺序

**第一梯队——判定可被绕过(先修,互相有依赖)**
1. **ER1/ER2/EX2 分割器与净化器**:统一为 engine 感知的实现,未闭合引号拒绝,`#`/反引号仅 MySQL 生效;更根本地改为"执行判定后已归一化的语句",一次性关掉"判定串 ≠ 执行串"这一整类。
2. **ER3 `MapVerbToCapability("")` 改为保守值** + `Exec` 一律逐条判定。注意其注释所依赖的"字典兜底"前提不成立(ER12),两处要一起改。
3. **EX1/EU3 导出补齐完整判权链**(能力矩阵 + 风险字典 + PROD MFA + maint)。
4. **ED1 GLI 环境播种** + **ED5 env 白名单**:两者是同一个"表里无行=放行"语义的两个入口。
5. **ED3 fail-open 吞错**:把"查询失败"与"无行=默认"分开,否则前四项修好也会被一次 DB 抖动全部旁路。

**第二梯队——鉴权与凭证**
EA1(开关不覆盖回调)、EA2(工单归属)、EA3(密钥进日志)、EU1(MFA 自助降级)、EU2(移除成员不撤权)、ED2(prod 播种演示管理员)。

**第三梯队——审计完整性**
EA4(外部审批人无处可查)、EU4(管理侧零审计)、ER7(异步审计失真)、EX5(导出不留痕)。这四项合起来决定"事后能否追责",建议一并设计 `AuditLog.operator`/`decision_source` 字段后统一改。

**第四梯队——前端正确性**
EF1(错库执行)、EF2(拒绝显示成功)两项会直接误导操作者,优先级高于其余前端项;EF3(ANSI 注入)需后端 `cellString` 与前端渲染两侧同时加固。

### 本轮方法说明
- 六个方向并行独立审查,互不共享中间结论,因此 EX1/EU3 的双向命中可视为交叉验证。
- 标注实测的条目分别通过:`pkg/sqlutil` 探针(ER1/ER2/EX2 的分割器行为)、`internal/bootstrap` httptest 黑盒 harness(EU1/EU2/EU5/EU7/EU8、EX1/EU3)、`internal/gateway` 探针(ER3/ER8/ER9)。所有临时测试文件已删除,工作树除 `issue.md` 外无改动。
- 前四轮 C1–C4 / H1–H14 / M / L / R1–R29 / V1–V7 / A1–A4 / B1–B9 / C1–C10 已逐条抽验,**未发现回归**;但有三处"同类缺陷在新代码里重现":EA3 之于 R17、EA7 之于 V4、EX2 之于 A3。建议把这三条的修复要点固化为提交前检查项。
---

## 修复状态(2026-07-28,TDD 逐条修第一梯队)

**第一梯队 8 项已修复**(ER1/ER2/ER3/EX1/EX2/ED1/ED3/ED5),后端 `go vet` 干净、全部包测试通过,前端 `vue-tsc` + `vite build` 通过。每项均先写失败测试(RED)取证、再最小实现(GREEN),逐个 vertical slice 推进。

### 新增回归测试
| 测试 | 位置 | 锁定的行为 |
|---|---|---|
| `TestSplitStatements_DollarQuoteDoesNotMergeStatements` | `pkg/sqlutil` | `$$…$$` / `$tag$…$tag$` 内的引号不再吞掉分隔符 |
| `TestSplitStatements_EscapeStringDoesNotMergeStatements` | `pkg/sqlutil` | `E'\''` 按反斜杠转义解析,不再吞尾 |
| `TestSplitStatements_HashDoesNotHideStackedStatement` | `pkg/sqlutil` | `#` 后的堆叠语句仍被切出来判定 |
| `TestSplitStatements_KeepsExecutableCommentBody` | `pkg/sqlutil` | `/*!…*/` 体保留进判定文本 |
| `TestStripComments_HashDoesNotSwallowFollowingText` | `internal/gateway` | 字典扫描与 NoWhere 不再被 `#` 蒙蔽 |
| `TestEvaluate_FailsClosedWhenStoreErrors` | `internal/gateway` | 风险数据读取失败 → deny,而非 allow |
| `TestExec_LeadingSeparatorDoesNotDowngradeCapability` | `internal/bootstrap` | `;UPDATE …` 不再降级为 select 维度 |
| `TestExport_RejectsDialectLexerSmuggles` | `internal/bootstrap` | 导出拒绝 OUTFILE/DUMPFILE 与三类词法走私 |
| `TestExport_HonoursCapabilityMatrixAndMaintenance` | `internal/bootstrap` | 导出与终端同一道闸 |
| `TestConnection_RejectsUnknownEnvironment` | `internal/bootstrap` | 只接受 prod/gli/staging/dev |
| `TestMigrate_BackfillsGliEnvironmentRules` | `internal/bootstrap` | `migrate` 路径也落 GLI 规则 |
| `TestTagsForRoles_QueryFailureIsNotUnrestricted` | `internal/repository` | 标签查询失败不等于"无限制" |

### 各项要点

**ER1/ER2/EX2 — 统一"判定串 = 执行串"的词法契约。** `split.go` 头注释现在把**不对称原则**写成硬约束:过分割安全(片段照样逐条判定,最坏是过拦),合并即绕过。据此三处改动:①新增 PG 美元引用(`dollarTag` 识别 `$$`/`$tag$`,按 PG 规则排除 `$1` 占位符)与 `E'…'` 反斜杠转义解析——**不支持它们才会合并**;②`#` 不再当注释——MySQL 是注释但 PG 是运算符,跳到行尾会把真实语句从判定文本里删掉,改为在此切分,MySQL 侧退化为过分割(安全方向);③`/*!…*/` 改为**保留 body 只删标记**,与 `risk.StripComments`(A3)对齐,消除"两个注释剥离器语义不一致"。`risk.StripComments` 同步去掉 `#` 分支。

**ER3 — `Exec` 一律判定 split 后的文本。** 原来只在 `len(stmts) > 1` 时逐条判定,单条走原串,于是前导 `;` 让 `ParseVerb` 取不到动词、落入 read 维度。现在无论几条都走 `strictestVerdict`(空输入才回落原串)。`MapVerbToCapability("")` **保持 select 未改**:一度改成 write,但它会拦掉 `1`、`42;`、`-- comment` 这类无害输入(`TestBlankInput_NotInterceptedButStillScanned` 明确锁定了该行为),而拆分归一化后真实命令必有动词,该分支不再承重。改的是注释——原注释声称"字典扫描兜底"是**错的**(seed 字典无 UPDATE/INSERT/CREATE),现在写明真正的保证来自调用方先 split,并警告不要拿原始输入直接套这个映射。

**EX1 — 导出补齐完整判权链。** `EnqueueExport` 增加维护态判定与 `EvaluateRoles`,非 allow 一律拒绝并记 `intercept` 审计。导出没有审批通道可承接 approve,故 approve 也拒绝并引导用户走终端。种子矩阵 `select` 三环境均为 allow,默认部署行为不变,只有管理员显式设 deny 时才生效——正是被绕过的那条路径。**PROD MFA 步进未纳入本次**:`ExportReq` 无 `mfaCode` 字段,后端单方面加校验会让已登记 MFA 的用户导出直接失败(R18 同型故障),需与前端补验弹窗一并改,留待第二梯队。

**EX2 — OUTFILE/DUMPFILE 显式拒绝。** 保留可执行注释体后,`SELECT … INTO OUTFILE` 的动词仍是 SELECT,动词白名单结构上抓不到,故加 `writesFileRe` 显式匹配(先 `StripComments` 再匹配,防注释拆词)。

**ED1 — `Migrate` 尾部无条件 `backfillGliEnv`。** 根因是"新版本引入的**参考数据**只在 seed 落地,而生产升级只跑 migrate"。把 `seedGliEnv` 抽成接受 `*gorm.DB` 的 `backfillGliEnv`,`Seed` 与 `Migrate` 共用;幂等,MySQL/SQLite 两条路径都覆盖。注释里点明通用教训:**缺行=放行的语义下,发新环境必须同时回填**。

**ED3 — 读取失败与"无规则"分开。** `gateway.Store` 接口改为 `CapabilityLevel(...) (string, error)` / `RiskCommands() ([]RiskCommand, error)`;`EvaluateRoles` 任一层读取出错即 `unavailableVerdict` → **deny + 记错误日志**;`ScanStatement` 出错时按 high 报而非清零。`repository.CapabilityLevel` 保留 `ErrRecordNotFound → allow`(无规则=放行是设计),但真实查询错误上抛。`TagsForRole` 拆出带 error 的 `tagsForRole`,`TagsForRoles` 增加 error 返回,`canAccessConn` 出错即拒绝、`AccessibleConnections` 出错即上抛——堵住"标签查询失败 → len==0 → unrestricted → 看见全部实例"。

**ED5 — 连接 env 白名单。** `validEnvs`(prod/gli/staging/dev)在 `CreateConnection`/`UpdateConnection` 双向校验。注释写明为何这是安全问题而非参数校验洁癖:env 是能力矩阵与字典的查询键,查不到行就放行,所以一个拼错的 `uat` 等于一个**零管控环境**。

### 仍未修(按梯队顺序推进中)
- **第二梯队(鉴权与凭证)**:EA1 外部审批开关不覆盖回调、EA2 回调可批准任意工单、EA3 密钥进访问日志、EU1 MFA 自助降级、EU2 移除成员不撤权、ED2 prod 播种演示管理员。
- **第三梯队(审计完整性)**:EA4、EU4、ER7、EX5 —— 建议统一设计 `AuditLog.operator`/`decision_source` 后一并改。
- **第四梯队(前端正确性)**:EF1 错库执行、EF2 拒绝显示成功优先;EF3 ANSI 注入需前后端同时加固。
- **另行处理**:导出 PROD MFA(见 EX1 说明,需前端配合)、ER4/EX6 sqlite database 路径、ER5 异步连接池泄漏、ER6 异步维护态、ER8/ER9 NoWhere 字面量与 CTE、ER10 policy 未生效。
---

## 修复状态(2026-07-28,TDD 逐条修第二梯队)

**第二梯队 6 项已修复**(EA1/EA2/EA3/EU1/EU2/ED2),后端 `go vet` 干净、全部包测试通过,前端 build 通过。

### 新增回归测试
| 测试 | 锁定的行为 |
|---|---|
| `TestExternalApproval_CallbackRefusedWhenFeatureDisabled` | 关掉外部审批后回调即失效 |
| `TestExternalApproval_CallbackRejectsMismatchedVendorTask` | 回调引用他单 task_id 被拒 |
| `TestExternalApproval_CallbackSecretIsNotWrittenToAccessLog` | 密钥不进访问日志,URL 传参仍可用 |
| `TestMFA_SetupCannotDisarmAnEnabledFactorWithoutProof` | 重新绑定不能卸掉在用的二次验证 |
| `TestMultiRole_RemovingMemberRevokesThePrimaryRoleToo` | 移除成员真正收回主角色权限 |
| `TestMultiRole_RemovingTheOnlyRoleIsRefused` | 不允许把用户的最后一个角色移光 |
| `TestSeed_RefusesToPlantDemoDataInProduction` | prod 拒绝播种演示管理员 |

### 各项要点

**EA1 — `VerifyExternalCallback` 首行校验 `cfg.enabled`。** 出站的 dispatch/cancel 早就检查了开关,入站漏检,导致关掉功能后只要 `callbackSecret` 还在库里端点就仍能驱动生产执行。RED 复现:`enabled=false` 下回调把 PROD 工单推到 `approved`。修复后 4 个既有回调测试转红——它们**从来没开过这个开关**(正是 EA1 的佐证),已逐个补上 `approval.external.enabled: true`,它们测的是回调鉴权而非开关本身。

**EA2 — 厂商 task_id 交叉校验。** 关联键是可预测的 `AP-<自增>`,而回调里的厂商 `task_id` 收下却从不比对。现在 `ap.ExternalTaskID` 与 `cb.TaskID` **都非空时**必须相等,否则 403。**刻意不要求"必须已派发"**:`ExternalTaskID` 是 dispatch 后异步写回的,强制要求会把 EA10 那个竞态(厂商回调快于我方写库)变成对合法回调的误拒。这样取到的是"有据可查时必须对得上",无误拒风险。残留面:从未派发的工单仍可被持密钥者决策,彻底封堵需要在 dispatch 前同步落一个"已外发"标记(需加列),留待后续。

**EA3 — 改的是日志,不是接口。** 既有测试 `TestExternalApproval_CallbackSecretViaQueryParam` **明确要求** `?secret=` 可用(审批魔方无法发自定义头),所以不能按审查建议直接删。真正的缺陷是 gin 默认 formatter 把 path+rawQuery 写进访问日志。新增 `accessLogger()`:自定义 formatter,对 `secret`/`token`/`access_token` 三个参数值打码后再拼路径。**关键坑**:`gin.LogFormatterParams.Path` 已经把 rawQuery 拼进去了,必须用 `p.Request.URL.Path` 重建,否则打码等于没做(第一版就踩了,测试抓住了)。残留风险(URL 仍会出现在厂商侧配置与中间代理日志)已在注释中写明。

**EU1 — 判定条件是"已武装",不是"已启用"。** `MFASetup` 为发新密钥会先把 `mfa_enabled` 置 false,等于**未经验证就卸掉二次验证**;而 `MFADisable` 达到同样效果却要求有效验证码,攻击者自然走便宜的那扇门。RED 实测:调用后纯口令登录重新成功。第一版守卫写成 `if u.MFAEnabled` 就打挂了 `TestMFA_EnrollmentCodeCannotAlsoStepUp`——因为**测试夹具里所有用户都是 `MFAEnabled: true` 但 secret 为空**,这种状态什么也没保护(`checkMFA` 本就按 secret 判定未登记),挡它会让这些用户根本无法首次绑定。最终条件改为 `MFAEnabled && MFASecret != ""`。丢失验证器的恢复路径是管理员 `POST /users/:id/mfa/reset`。

**EU2 — 移除成员同时改主角色,并 bump token 版本。** 权限是 `tbl_user.role_id ∪ tbl_role_member`,而建号路径两边都写,于是"移除成员"对主角色完全无效——管理员被告知收回成功,实际没收回。新增 `Services.RemoveRoleMember`:若被移除的正是主角色,则把 `role_id` 改指向该用户仍持有的其它角色,再删成员行并 `BumpTokenVersion`(撤权必须对已签发会话生效)。**并发新约束:不允许移除用户的最后一个角色**——`role_id` 指向不存在的角色会让 `/auth/me` 整体失败,是锁死账户而非降权;而且空角色集在 `capabilityLevelUnion` 里返回 `LevelAllow`(EU9 那个 fail-open),更不能放任。改为明确报错"请先分配其他角色"。这条新约束打挂了 `TestApprovalChain_FallsBackToAdminWhenOwnerEmpty`(它要清空 owner 角色),已按真实管理流程调整:先授予替补角色再撤 owner。

**ED2 — prod 拒绝播种演示数据。** `config.yaml` 自带 `seed: true` 且头注释引导 `APP_ENV=prod ./server`,空库启动即植入 README 公开口令的平台管理员。`Seed()` 在 `cfg.Env == "prod"` 且需要建种子数据时**直接返回错误**而非静默跳过——静默跳过会留下一个没有角色的半初始化库,同样不可用却不易察觉;报错则明确指向正确路径(`database.seed=false` + `server init`)。`auto_migrate` 早已对 MySQL 屏蔽(B6),这次补上的是 seed 这一半。

### 仍未修
- **第三梯队(审计完整性)**:EA4、EU4、ER7、EX5 —— 需先定 `AuditLog.operator`/`decision_source` 字段设计(含迁移),再一并改。
- **第四梯队(前端)**:EF1 错库执行、EF2 拒绝显示成功、EF3 ANSI 注入(需前后端同时改)、EF4 取消后剩余语句照跑、EF5 断连假死、EF6 `session_revoked` 未处理、EF7 静默吞错。
- **其余**:导出 PROD MFA(需前端补验弹窗)、EA5/EA6/EA7/EA8、EU5/EU6、ER4~ER10、ED4/ED6~ED13、EX3~EX10。
---

## 生产故障诊断(2026-07-28):执行 DDL 时 WS 断开且终端此后无法操作

**现场**:`[GIN] 2026/07/28 - 14:58:36 | 200 | 2m54s | 127.0.0.1 | GET "/api/v1/terminal/ws"`,偶发于执行 DDL 时;前端显示「已断开」→ 自动重连成功 → 但 Web 命令行**无法做任何操作**。

两个独立缺陷叠加,一个是**新发现**(不在第五轮清单内),一个是已记录的 EF5 被生产验证。

### EW1【严重·新发现】WS 读循环与命令执行共用 goroutine,长命令把自己的连接掐断
- 位置:`handler/terminal.go` `TerminalWS` 消息循环;`frontend/src/lib/wsTerminal.ts:127-139`
- 机制:服务端是**严格串行**的单循环 —— `conn.ReadJSON` → 收到 exec → `h.Svc.Exec(...)` **同步阻塞**直到目标库返回。阻塞期间循环读不到客户端的 `{"type":"ping"}`,自然也发不出 `pong`。而浏览器发不了原生 WS ping 帧,`wsTerminal.ts` 每 **20s** 发一次应用层 ping 并只等 **5s** pong,超时即判定半开连接并 `ws.close()`。
- **结论:任何执行时间超过约 25s 的语句都会把自己的连接掐断**——这正是"只在 DDL 时偶发"的原因,普通查询跑不到这个时长。日志里的 2m54s 是整条 socket 的存活时长(连上后闲置一段 + DDL 开始 + ~25s 后被客户端关闭),不是超时值。
- 附带后果:服务端命令**照常执行完**,结果 `WriteJSON` 写进已死的 socket、错误被丢弃,操作者永远不知道自己的 DDL 到底生效没有。
- 复现(确定性,~5s):`TestTerminalWS_AnswersHeartbeatWhileCommandRuns`(`internal/bootstrap/ws_longexec_test.go`)—— 建一个 sqlite 目标连接,发一条约 5s 的递归 CTE,紧接着发 ping,断言 2s 内收到 pong。修复前红:`no pong while a command was running`。
- 修复:读与执行拆成两个 goroutine。读 goroutine 独占 `ReadJSON`,**立即**回 pong,把 exec 请求经 `execCh`(缓冲 1)交给执行 goroutine;执行仍是一次一条(终端本就是单语句控制台)。gorilla/websocket 允许一读一写并发,所有写统一走 `writeMu` 保护的 `send()`。读 goroutine 退出即 `close(execCh)`,执行循环随之结束并 `conn.Close()`,socket 死亡时两边都能收敛,无 goroutine 泄漏。
- **未用竞态检测器验证**:本机无 gcc,`-race` 需要 cgo。共享面已逐项人工核对:`ReadJSON` 单一读者;两个 goroutine 的写全部经 `send()` 加锁;`u` 仅执行循环写;`claims` 只读;`execCh` 由读者关闭、执行者 range。建议在有 gcc 的环境补跑一次 `-race`。

### EW2【高】= 第五轮 EF5,已被生产验证
- 位置:`frontend/src/components/terminal/TerminalSession.vue` `onStatus`
- 机制:`sendExec` 后 `LineEditor.busy = true`,而**唯一**能解除的是收到回复时的 `editor.resume()`。socket 在命令在途时断开 → 回复永不到达 → busy 永久为真 → 之后所有按键被吞进 `queued`(`lineEditor.ts:167-173`),终端看起来完全无反应。点「重连会话」也救不回:`printAbove` 在 busy 时跳过重绘,更不会 resume。**这就是"重连后无法做任何操作"的直接原因**,唯一出路是刷新整页。
- 修复:`onStatus` 收到 `closed` 且 `editor.running` 时,打印黄色提示并 `editor.resume()`。提示文案(新增 i18n `termLostWhileRunning`)明确告知**该命令是否已生效未知,请先查审计日志再重试**——因为按 EW1 的分析,服务端很可能已经执行完成。
- **无自动化测试席位**:该 glue 在 Vue SFC 内,仓库没有 vitest/组件测试基建(只有一个 Playwright e2e)。按诊断流程,席位缺失本身即为发现:**这块 WS↔编辑器状态机的衔接目前无法被回归测试锁定**。若要补,建议把「在途命令 + 连接状态」的状态机从 SFC 中抽出为可在 Node 下测试的模块(`LineEditor` 已经是这种形态:只依赖 `write`/`onData`/`clear`,可用 stub 终端驱动)。

### 运维建议(与代码修复无关)
- `gateway.exec_timeout_seconds` 默认 **30s**。真正耗时数分钟的 DDL 即便连接不再掉线,也会在 30s 被 `context deadline exceeded` 取消。长 DDL 应走**异步执行通道**(`POST /terminal/exec-async`,前端「异步执行」页),它就是为 30–60min+ 的语句设计的,不受请求生命周期约束。
- 若中间有 Nginx/LB,另需确认其 `proxy_read_timeout` 大于心跳间隔,否则会是第三个独立的断连来源。
---

## 修复状态(2026-07-28,TDD 逐条修第三梯队 · 审计完整性)

**第三梯队 4 项已修复**(EA4/EU4/ER7/EX5),后端 `go vet` 干净、全部包测试通过,前端 build 通过。这组的共同主题是:**审计链记的是"发生了什么",但记不出"是谁授权的"**。

### 新增回归测试
| 测试 | 锁定的行为 |
|---|---|
| `TestExternalApproval_AuditIdentifiesTheExternalApprover` | 外部审批人写进审计链 |
| `TestAdmin_AccountMutationsAreAudited` | 改口令/绑 MFA/改状态留痕 |
| `TestAdmin_PermissionChangesAreAudited` | 能力矩阵/标签授予留痕 |
| `TestAsyncExec_AuditsFullCommandAndRealRisk` | 异步审计记全量 SQL + 真实风险等级 |
| `TestExport_SubmissionIsAudited` | 导出提交即留痕、记全量查询 |

### 数据结构改动
- **`tbl_audit_log.operator`**(迁移 `0008_audit_operator.sql`,VARCHAR(128) NOT NULL DEFAULT ''):记录**实际授权者**,当其不等于 actor 时。空 = 二者同一人。
- **`tbl_async_job.risk`**(迁移 `0009_async_job_risk.sql`,VARCHAR(16) NOT NULL DEFAULT ''):提交时的裁决等级。
- `operator` **已纳入哈希载荷**,因此是防篡改的。注意:此前写入的行按当时的载荷形状计算哈希,**校验工具必须按行所属版本计算,不能拿新载荷去重算老行**——已写进迁移注释。

### 各项要点

**EA4 — `recordAuditBy` 携带授权人。** `appendAudit` 增加 `operator` 参数,`recordAudit` 保持原签名(内部传空),新增 `recordAuditBy` 给"代他人执行"的场景。`finalizeApproval` 两条终态分支都改用它,把 `operatorName` 落库——**站内与外部审批都记**,不只外部。原先的设计理由(审批人已在 `tbl_approval_step`)在外部审批上线后失效:飞书审批人不是网关用户,既进不了 step 表也进不了哈希链,一条生产 DROP 在链上只显示"发起人执行了它"。

**EU4 — 两个半:账户变更 + 权限变更。**
- 账户侧:`AdminSetPassword`/`AdminBindMFA`/`AdminResetMFA`/`PatchUser` 增加 `actor` 参数并写审计。审计文本用**稳定可 grep 的键**加目标,如 `admin.password.reset user=chenhao@vela.io`、`admin.user.patch status=disabled user=...`,便于后续按前缀检索。
- 权限侧:新增 `Services.AuditRoleChange`,由 `SetRoleMenus`/`SetRoleCapabilities`/`SetRoleTags`/`AddRoleMember` 四个 handler 调用,记 `admin.role.capabilities role=ro value={...}`(value 裁到 400 字符)。理由写在注释里:这些授予决定了系统里每一次权限判定,**改它就是提权动作**;否则有人可以放宽角色→操作→再收窄,链上只有操作、没有那次授予。
- **webhook 暂不投递**:事件词汇表是 `exec/login/intercept/approve` 四种,admin 事件传空 eventType(仅入审计链),等订阅端有对应词汇再接。

**ER7 — 异步审计记全量 SQL + 真实风险。** 原来 `"ASYNC "+clip(job.SQL, 80)` 且风险恒 `RiskMid`。80 字符对迁移脚本毫无意义(截在语句中间);风险恒定则让该字段完全失去筛选价值。改为记全量 SQL,并把提交时的裁决 `v.Risk` **持久化到 `tbl_async_job.risk`** —— worker 可能一小时后才审计,期间字典可能已变,**值得记的是当初授权这次执行的那个等级**。老数据无该列时回落 `mid`(`asyncAuditRisk`)。

**EX5 — 审计提交而非仅审计成功。** 原来只有成功才写审计、且 SQL 截 80 字符;提交与失败都不写,于是"提交→失败→改→再提交"的试探式拖数据完全不留痕。现在**提交即写**(`EXPORT <全量 SQL>`,result=pending),理由写在注释:提交是唯一保证会到达的点(任务可能失败、可能被队列丢弃、可能被重启回收),而且它才是"某人索取了这份数据"的时刻。完成时仍写一条 executed,同样不再截断。

### 仍未修
- **第四梯队(前端)**:EF1 错库执行、EF2 拒绝显示成功、EF3 ANSI 注入(需前后端同改)、EF4 取消后剩余语句照跑、EF6 `session_revoked` 未处理、EF7 静默吞错、EF8~EF13。(EF5 已在 2026-07-28 生产故障诊断中修复,见 EW2。)
- **其余**:导出 PROD MFA(需前端补验弹窗)、EA5/EA6/EA7/EA8、EA2 残留面(未派发工单仍可被决策)、EU5/EU6/EU9、ER4~ER6/ER8~ER12、ED4/ED6~ED20、EX3/EX4/EX6~EX10、EW1 的 `-race` 复验。
---

## 修复状态(2026-07-28,TDD 逐条修第四梯队 · 前端)

**第四梯队 6 项已修复**(EF1/EF2/EF3/EF4/EF6/EF7),前端 `vue-tsc` + `vite build` 通过,新增单元测试 14 个全绿,后端未受影响。

### 先补上了缺失的测试席位
第三梯队结束时记录过:**SFC glue 无自动化测试席位**(仓库只有一个 Playwright e2e,无 vitest/组件测试)。本轮先建席位再修:

- 新增 `frontend/playwright.unit.config.ts` —— 复用仓库**已有的** Playwright runner 跑纯逻辑测试(`testDir: tests/unit`,不启浏览器、不启 dev server),**零新增依赖**。
- 新增 `npm run test:unit`。
- 新增 `frontend/src/api/codes.ts`:把业务 code 常量从 `api/http.ts` 抽出成无副作用模块(`http.ts` 原样 re-export,调用点不受影响)。原因:纯逻辑要判断 code,而 `http.ts` 会拉起 axios 与 `import.meta.env`,在 Node 下直接报错。

新增测试 14 个:`sqlResult.spec.ts`(5)、`lineEditor.spec.ts`(3)、`execOutcome.spec.ts`(6)。

### 各项要点

**EF3【高】结果内容里的控制字符不再进 xterm。** `sqlResult.ts` 新增 `sanitizeCell`(单行:换行/制表→空格,其余 C0/C1 全删)与 `sanitizeMultiline`(竖排 `\G` 用:**保留真实换行**并转 CRLF,其余控制字符删)。`renderTable` 的表头与单元格、`renderVertical` 的值全部经过。
- **一个差点造成的功能回归**:第一版对竖排也用了 `sanitizeCell`,把换行压成空格 —— 而 `\G` 存在的意义就是显示 `SHOW CREATE TABLE` 这类多行值。补了「竖排必须保留换行、同时仍剥离转义」的测试才发现,于是拆出 `sanitizeMultiline`。
- 测试覆盖 `\x1b[2K\x1b[1A`(重绘滚动区)、`\x1b[6n`(DSR,xterm 会在**输入通道**回应,污染用户正在输入的 SQL)、OSC、BEL、NUL,并断言 CJK/emoji 正常显示。

**EF4【高】取消后不再继续跑剩余语句。** `LineEditor` 新增 `discardQueued()`(返回被丢弃的字符数);SFC 新增 `abandonBatch()`,在**取消审批 / 取消 MFA / 命中拒绝**三处调用(原先三处都直接 `resume()`,而 `resume()` 无条件回放 `queued`)。用户看到 `DROP TABLE` 点了取消,终端却继续执行后面的语句 —— 现在丢弃并提示 `termBatchAbandoned`。测试同时锁定了"正常粘贴批次仍逐条执行"与"Ctrl+C 仍丢弃批次"两条既有行为不回归。

**EF1【严重】REST 回退不再丢目标库。** 根因是 `api.exec(connectionId, sql, reason='', mfaCode='', database='')` 用**位置参数 + 尾部默认值**,四个调用点各写一份,其中两个漏了最后一个参数。修法是消除这一类:SFC 内新增 `execRest(sql, reason, mfaCode)`,**只此一处**拼装并恒带 `targetDb.value`,四个回退点全部改走它。原先审批回退那处更严重——错误的库名会被写进工单,审批通过后网关按工单代执行,错误被永久固化。

**EF2【严重】拒绝不再显示成功。** 新增 `src/lib/execOutcome.ts` 的纯函数 `classifyExecEnvelope(env)`,返回 `mfa | intercepted | ok | failed`。后端所有业务结果都是 HTTP 200 + 信封 code,而前端只特判了 42800,其余一律当结果渲染;拒绝没有 data,于是落进"无输出"分支打出绿色 `✓ 执行成功`——审计里记的却是 rejected。`renderExecEnvelope` 改为按 outcome 分派,`failed` 用红色打印服务端 msg。**保留了「code=0 但 payload 为空仍算成功」**(DDL 本就无返回),这正是原先被混为一谈的区分点,测试专门锁了这一条。

**EF6【高】`session_revoked` 不再被丢弃。** `onWsMessage` 的 if/else 链原先让它落到 `else return`,消息被丢、`resume()` 不执行 → 终端冻死;而菜单回收并不会让 token 失效,`/auth/me` 仍返回 200,于是无限重连。现在打印原因 → `resume()` → 关闭 socket → `clearSession()` → 跳登录。

**EF7【高】审批回退不再静默。** `catch { /* ignore */ }` 改为打印失败;`code === 0`(规则在预检与提交之间被放宽、命令实际已执行)时补 `renderExecEnvelope(env)`,否则操作者看不到任何执行痕迹。

### 仍未修
- **EF5 已于 2026-07-28 生产故障诊断中修复**(见 EW2)。
- 前端剩余:EF8 DbTree 切换实例竞态、EF9 RiskRulesView isAdmin 未走并集、EF10 严格模式开关失败不回滚、EF11 设置页用译文当取值、EF12 审计导出不查 `res.ok`、EF13 LineEditor 超宽行重绘错乱、EF14~EF20 低危。
- 后端剩余:导出 PROD MFA(需前端补验弹窗)、EA2 残留面、EA5~EA8、EU5/EU6/EU9、ER4~ER6/ER8~ER12、ED4/ED6~ED20、EX3/EX4/EX6~EX10、EW1 的 `-race` 复验。
- **仍无席位**:`TerminalSession.vue` 内的 WS↔编辑器状态机(EF1/EF6/EF7 的接线)依旧只能靠人工核对。EF2 已通过抽出 `execOutcome` 拿到席位,同样手法可继续用于其余 glue。
---

## 修复状态(2026-07-28,续修余项)

**5 项已修复**(ER8/ER9/ER6/EF11 + `keyForLabel` 席位),后端 `go vet` 干净、全部包测试通过;前端 build 通过、单元测试 18 个全绿。

### 新增回归测试
| 测试 | 锁定的行为 |
|---|---|
| `TestNoWhere_NotFooledByWhereInsideALiteral`(gateway) | 字面量里的 `where` 不再冒充 WHERE 子句 |
| `TestDataModifyingCTE_IsAWriteAndStrictModeSeesIt`(gateway) | 改数据的 CTE 算写、且受 strict 兜底 |
| `TestAsyncExec_RefusedWhileInstanceIsInMaintenance`(bootstrap) | 维护态下异步通道同样拒绝 |
| `settingOptions.spec.ts`(前端,4 个) | 配置项取值跨语言切换保持不变 |

### 各项要点

**ER8【中】`NoWhere` 不再被字面量蒙蔽。** 新增 `blankQuoted()`:把字符串字面量与引号标识符的**内容**替换为空格、保留定界符与其余结构,关键字启发式只在「结构」上跑。`UPDATE users SET note='where'` 原本满足 `\bwhere\b` 从而躲过 strict 全表写兜底。B7 当初用词边界修掉了 `elsewhere`(标识符那一半),字面量是同一个洞的另一半。

**ER9【中】改数据的 CTE 算写。** PG 的 `WITH d AS (DELETE ... RETURNING *) SELECT * FROM d` 真的删数据,但首动词是 `WITH`(在只读集合里),于是被当读走 `QueryContext`、按读记账、并完全跳过 strict 兜底。
- `IsRead`:`WITH` 打头时若结构里出现改数据动词则判为写。
- `NoWhere`:`WITH` 打头且结构含 `delete|update` 时按该 DML 处理,于是无 WHERE 的 CTE 全表删除会被 strict 拦下。
- 只读 CTE(`WITH d AS (SELECT ...)`)仍是读,测试锁定。

**ER6【中】异步通道补维护态判定。** `ExecAsync` 增加 `conn.Status == "maint"` 分支,与 `/terminal/exec` 一致:记 warn 审计并返回受限提示(`AsyncSubmitResp.Output`,新增字段)。原先终端被拦的人只要把同一条语句改投异步就能照常执行。

**EF11【中】设置项不再用译文当取值。** 新增 `src/lib/settingOptions.ts`:`APPROVAL_TIMEOUT_KEYS` / `SESSION_TTL_KEYS` 为权威取值,`labelOf(key, t)` 只用于显示,`keyOf` 校验存量值,`keyForLabel` 做下拉回填。`SettingsView` 的 model 改为持 **key**,`VSelect` 显示层做 label↔key 转换。原先 model 持译文、保存时再用 `t()` 反查,切语言后比较全部落空 → **静默把审批超时改成 auto-escalate、会话有效期改成 8h**,且无任何提示。
- 测试用「按 message id 索引」的两套假字典模拟中英切换 —— 第一版假字典按取值索引,与 vue-i18n 真实行为不符,**测试本身不忠实**,已改正。

### 仍未修
- **前端**:EF8 DbTree 切换实例竞态、EF9 RiskRulesView isAdmin 未走角色并集、EF10 严格模式开关失败不回滚、EF12 审计导出不查 `res.ok`、EF13 超宽行重绘错乱、EF14~EF20 低危。
- **后端**:ER5 `RealRunAsync` 非 sqlite 连接池不关闭(**无本地 MySQL/PG,难以建可靠席位**,建议在有真实目标库的环境验证)、ER4/EX6 sqlite `database` 任意路径、ER10 连接 policy 未参与判定、ER11/ER12、EU5/EU6/EU9、EA2 残留面、EA5~EA8、ED4/ED6~ED20、EX3/EX4/EX7~EX10。
- **导出 PROD MFA**:需前端补验弹窗配合,单改后端会让已登记 MFA 的用户导出直接失败。
- **EW1 的 `-race` 复验**:本机无 gcc,仍未跑。
