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
