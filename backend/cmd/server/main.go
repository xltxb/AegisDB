// Command server boots the AegisDB backend.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"aegisdb/internal/bootstrap"
	"aegisdb/internal/gateway"
	"aegisdb/internal/handler"
	"aegisdb/internal/repository"
	"aegisdb/internal/middleware"
	"aegisdb/internal/service"
	"aegisdb/internal/storemigrate"
	"aegisdb/pkg/crypto"
	"aegisdb/pkg/jwt"
	"aegisdb/pkg/logger"
)

// version is stamped at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	// Subcommands (production ops), each exits when done:
	//   init          — schema migration + reference data + platform admin
	//   migrate       — apply pending SQL migrations only (no seeding)
	//   verify-audit  — 重算审计哈希链;退出码 2 表示链断了(可直接挂进巡检)
	//   migrate-store — 把存储库从 MySQL 整体搬到 PostgreSQL(停机搬迁,见 docs/上线部署SOP-存储库搬迁到PG.md)
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "init":
			runInit(os.Args[2:])
			return
		case "migrate":
			runMigrate(os.Args[2:])
			return
		case "verify-audit":
			runVerifyAudit(os.Args[2:])
			return
		case "migrate-store":
			runMigrateStore(os.Args[2:])
			return
		case "version", "-v", "--version":
			fmt.Printf("aegisdb %s\n", version)
			return
		}
	}

	cfgPath := flag.String("config", "configs/config.yaml", "path to config file")
	flag.Parse()

	cfg, err := bootstrap.LoadConfig(*cfgPath)
	if err != nil {
		slog.Error("load config failed", "err", err)
		os.Exit(1)
	}
	// Serving signs tokens, so a strong prod JWT secret is mandatory here (but not
	// for the migrate/init subcommands above).
	if err := cfg.ValidateForServe(); err != nil {
		slog.Error("invalid configuration", "err", err)
		os.Exit(1)
	}
	logger.Setup(cfg.Server.Mode)

	db, err := bootstrap.OpenDB(cfg)
	if err != nil {
		slog.Error("database init failed", "err", err)
		os.Exit(1)
	}
	if err := bootstrap.RefuseIfStoreMoved(cfg, db); err != nil {
		slog.Error("refusing to start on a moved store", "err", err)
		os.Exit(1)
	}

	repo := repository.New(db)
	if cfg.Database.Seed {
		if err := bootstrap.Seed(repo, cfg); err != nil {
			slog.Error("seed failed", "err", err)
		}
	}

	// Initialize the at-rest secret key (encrypts stored DB-connection passwords)
	// before any connection is created/opened. Prefer a dedicated AEGISDB_SECRET_KEY;
	// falling back to the JWT secret couples the two, so rotating AEGISDB_JWT_SECRET
	// would make every stored ciphertext undecryptable (A2).
	secretKey, fellBack := cfg.SecretKeyResolved()
	if fellBack {
		slog.Warn("AEGISDB_SECRET_KEY 未设置，回落使用 JWT 密钥派生连接口令加密密钥；轮换 AEGISDB_JWT_SECRET 将导致已存储的连接口令无法解密。建议设置独立的 AEGISDB_SECRET_KEY。")
	}
	// 强度只告警、不拒启动(L-14):这把密钥一旦换掉,所有存量密文就再也解不开了 ——
	// 对一套已经在跑的部署,拒绝启动等于把它锁死。所以这里只负责让弱密钥**看得见**,
	// 换不换、什么时候换,是运维带着数据迁移计划去做的事。
	if !cfg.IsLocalDev() {
		if n := len(secretKey); n < 32 {
			slog.Warn("连接口令加密密钥过短，建议用 `openssl rand -hex 32` 生成后设入 AEGISDB_SECRET_KEY（更换密钥需同时重置已存储的连接口令）", "len", n)
		} else if distinct(secretKey) < 8 {
			slog.Warn("连接口令加密密钥字符种类过少（疑似弱密钥），建议用 `openssl rand -hex 32` 重新生成（更换密钥需同时重置已存储的连接口令）")
		}
	}
	crypto.SetSecretKey(secretKey)
	// 脱敏哈希的盐与静态加密密钥同源但不同用途,所以额外派生一层,避免把加密密钥
	// 的任何一段直接带进对外可见的打码值里(L-11)。
	gateway.SetMaskSalt(crypto.DeriveSalt(secretKey, "aegisdb:mask:v1"))
	// The webhook SSRF guard blocks private/loopback targets. Only a genuine
	// LOCAL dev run relaxes it by default (on-host receivers); every deployment
	// keeps it on unless webhook.allow_private (or AEGISDB_WEBHOOK_ALLOW_PRIVATE)
	// explicitly opts in for a trusted internal target.
	//
	// 原先的条件是 `cfg.Env != "prod"` —— staging / uat 也跟着整个关掉了防护,而那些
	// 恰恰跑在真实网络上:云主机里一个 169.254.169.254 的 webhook 目标就能把实例元
	// 数据(含临时凭据)带出去(L-15)。放宽是给开发者的便利,不该顺延到部署环境。
	service.AllowPrivateWebhookTargets = cfg.IsLocalDev() || cfg.Webhook.AllowPrivate
	// SSO 地址只有本地开发允许 http;任何部署环境下保存设置时都要求 https。
	service.AllowInsecureSSO = cfg.IsLocalDev()
	service.SSOForcedOff = cfg.SSO.Disabled
	middleware.AdminIPAllowlistForcedOff = cfg.AdminIP.AllowlistDisabled
	if cfg.SSO.Disabled {
		slog.Warn("SSO 已被部署配置强制关闭(sso.disabled / AEGISDB_SSO_DISABLED):设置页的 SSO 开关不生效,所有人使用账号口令登录")
	}
	if !cfg.IsLocalDev() && cfg.Webhook.AllowPrivate {
		slog.Warn("webhook.allow_private 已开启：出站 Webhook/飞书 SSRF 防护对内网/环回地址放行，请确认目标网络可信")
	}

	engine := gateway.NewRiskEngine(repo)
	jwtMgr := jwt.New(cfg.JWT.Secret, cfg.JWT.TTLHours)
	svc := service.New(repo, engine, jwtMgr)
	// Ensure the default upload/export directories exist under the run dir at startup.
	_ = os.MkdirAll(svc.ScriptSavePath(), 0o755)
	_ = os.MkdirAll(svc.ExportSavePath(), 0o755)
	// 审批人自检:这些审批环节还有没有人能批。只报告,不阻止启动 —— 审批人是谁
	// 是组织的决定,一个自检程序没有资格替人拿主意;它该做的是在人还看得见的时候
	// 把话说清楚。放在这里是因为 svc 刚好齐备,而服务还没开始收请求。
	// 敏感字段规则的取数入口。挂在这里而不是把规则一路传下去:整个网关只有两处
	// 把行读出来(RealRun / RealQueryEach),挂一次,新加的调用路径也天然被覆盖。
	gateway.SensitiveRulesProvider = svc.SensitiveRulesForGateway
	// 模拟数据只在开发环境生效 —— 生产上一律拒绝(见 gateway/simulation.go)。
	//
	// 打进启动日志,而不是让它成为一个只有读代码才知道的事实:一台"怎么点都出数据"
	// 的网关,和一台"点什么都说没配凭据"的网关,差别就在这一行,运维得看得见。
	//
	// 只有本地开发(IsLocalDev)才开,与种子数据同一个判断:staging / uat 跑在真实网络上,一台没配凭据的
	// 实例就该报没配凭据,而不是回一份看着像真的假数据(2026-11 审计 V-16)。
	gateway.AllowSimulation = cfg.IsLocalDev()
	slog.Info("simulation mode", "env", cfg.Env, "allowSimulatedData", gateway.AllowSimulation)
	svc.LogApprovalStaffing()
	svc.LogUnscopedCustomRoles()
	h := handler.New(svc, repo)

	r := bootstrap.NewRouter(cfg, h, repo, svc, jwtMgr)

	// Background: approval timeout sweep (FR-APPR-05 / backend doc §7).
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			svc.SweepApprovalTimeouts()
			// 查询授权到期:判定本身按时间比,不等这里;这里让列表与审计里的状态跟上事实。
			svc.ExpireQueryGrants()
			// 终端标签独占的连接:空闲超时(跟随空闲锁定)的断开,未提交事务由数据库回滚
			svc.SweepTermSessions()
			// Web 命令行"加载更多"的暂存结果:过期的丢掉,不等下一次有人暂存
			gateway.SweepResultCache(time.Now())
		}
	}()

	// Background: export retention — delete archives older than the configured
	// window (default 3 days).
	//
	// It runs ONCE at startup before the ticker: a gateway that was off over a
	// weekend would otherwise keep a month of production extracts on disk for
	// another hour after coming back, and the whole point of the window is that
	// those files stop existing.
	go func() {
		svc.SweepExportRetention()
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			svc.SweepExportRetention()
		}
	}()

	// Background: 两个会**主动去连生产库**的定时任务 —— 连通性巡检、元数据同步。
	//
	// 都**默认关着**:打开任何一个都意味着这台网关会周期性地登录你的每一台生产实例,
	// 那必须是一次明确的决定,不能因为升级了一个版本就自己开始跑。
	//
	// 都**启动时不跑**:进程重启是件很常见的事,而每次重启都顺手扫一遍所有生产库,会让
	// "重启网关"变成一个有副作用的动作。第一轮等到间隔到点。
	//
	// 间隔是运行时设置,所以定时器只是定期醒来、自己比对是否到点(见 runEvery)。
	go runEvery(time.Minute, svc.ConnCheckEnabled, svc.ConnCheckInterval, svc.SweepConnChecks)
	go runEvery(time.Hour, svc.MetaSyncEnabled, svc.MetaSyncInterval, svc.SweepMetadata)

	// Background: resume release pipelines whose approval has been decided.
	//
	// A sweeper rather than a callback, because a decision arrives through four
	// different doors — the console, the 飞书 card, the 审批魔方 callback and the
	// timeout expiry — and a hook on one of them would strand releases whose
	// approval came through another. It polls a row that already records the
	// outcome, so it works no matter who wrote it. 15s keeps the pipeline feeling
	// responsive without hammering the metadata DB.
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			svc.ResumeReleaseApprovals()
		}
	}()

	tls := cfg.Server.TLSCert != "" && cfg.Server.TLSKey != ""
	slog.Info("AegisDB listening", "version", version, "addr", cfg.Server.Addr, "env", cfg.Env, "driver", cfg.Database.Driver, "tls", tls)
	if cfg.Env == "prod" && !tls {
		slog.Warn("生产环境未启用 TLS:请配置 server.tls_cert/tls_key,或在前置反向代理终止 TLS,避免凭据 / JWT 明文传输")
	}
	// 带超时的 http.Server(bootstrap.NewHTTPServer),不再用 gin 的 r.Run(它底下没有任何超时)
	srv := bootstrap.NewHTTPServer(cfg.Server.Addr, r)
	var runErr error
	if tls {
		runErr = srv.ListenAndServeTLS(cfg.Server.TLSCert, cfg.Server.TLSKey)
	} else {
		runErr = srv.ListenAndServe()
	}
	if runErr != nil {
		slog.Error("server stopped", "err", runErr)
		os.Exit(1)
	}
}

// runInit handles `server init [flags]`: migrate schema + seed reference data +
// create the platform admin. Credentials come from flags or AEGISDB_ADMIN_* env.
func runInit(args []string) {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	cfgPath := fs.String("config", "configs/config.yaml", "path to config file")
	email := fs.String("admin-email", os.Getenv("AEGISDB_ADMIN_EMAIL"), "platform admin email")
	password := fs.String("admin-password", os.Getenv("AEGISDB_ADMIN_PASSWORD"), "platform admin password (>= 12 chars, >= 3 character classes)")
	name := fs.String("admin-name", os.Getenv("AEGISDB_ADMIN_NAME"), "platform admin display name (optional)")
	_ = fs.Parse(args)
	logger.Setup("release")

	cfg, err := bootstrap.LoadConfig(*cfgPath)
	if err != nil {
		slog.Error("load config failed", "err", err)
		os.Exit(1)
	}
	// `init` opens the DB without boot-time AutoMigrate, then migrates explicitly
	// (versioned SQL on MySQL, AutoMigrate on sqlite) before seeding.
	cfg.Database.AutoMigrate = false
	db, err := bootstrap.OpenDB(cfg)
	if err != nil {
		slog.Error("database init failed", "err", err)
		os.Exit(1)
	}
	if err := bootstrap.RefuseIfStoreMoved(cfg, db); err != nil {
		slog.Error("refusing to start on a moved store", "err", err)
		os.Exit(1)
	}
	if err := bootstrap.Migrate(cfg, db); err != nil {
		slog.Error("schema migration failed", "err", err)
		os.Exit(1)
	}
	repo := repository.New(db)
	if err := bootstrap.InitDatabase(repo, cfg, *email, *password, *name); err != nil {
		slog.Error("initialization failed", "err", err)
		os.Exit(1)
	}
	slog.Info("initialization complete", "driver", cfg.Database.Driver, "env", cfg.Env)
}

// runVerifyAudit handles `server verify-audit [flags]`:从头重算审计哈希链,报出第一处
// 对不上的行,并用**退出码**说结论(0 = 完好,2 = 断了)—— 这样它能直接挂进巡检脚本与
// 定时任务,不必去解析输出。
//
// 为什么要有一个命令行入口,而不是只在界面上放个按钮:出事的时候,最不该依赖的就是那套
// 可能已经被改过的应用本身。这一条只要有配置文件和一个数据库连接就能跑,也能在从备份恢
// 复出来的库上跑 —— 而"拿今天的库和上周的备份各验一遍"正是这条链最有用的用法。
func runVerifyAudit(args []string) {
	fs := flag.NewFlagSet("verify-audit", flag.ExitOnError)
	cfgPath := fs.String("config", "configs/config.yaml", "path to config file")
	limit := fs.Int("limit", 0, "只验最近 N 行(默认 0 = 全表)")
	_ = fs.Parse(args)
	logger.Setup("release")

	cfg, err := bootstrap.LoadConfig(*cfgPath)
	if err != nil {
		slog.Error("load config failed", "err", err)
		os.Exit(1)
	}
	// 只读的一趟,不要在这里动表结构:校验跑在一个可疑的库上,而迁移会改写它。
	cfg.Database.AutoMigrate = false
	db, err := bootstrap.OpenDB(cfg)
	if err != nil {
		slog.Error("database open failed", "err", err)
		os.Exit(1)
	}
	// 校验不签发任何东西,也不发任何请求,所以这里给一个空壳 Services 就够 —— 用完整的
	// New() 会顺带拉起导出/异步/发布三组 worker 和它们的 goroutine。
	svc := service.NewForOps(repository.New(db))
	rep, err := svc.VerifyAuditChain(*limit)
	if err != nil {
		slog.Error("verify failed", "err", err)
		os.Exit(1)
	}
	fmt.Println(rep.String())
	if !rep.OK {
		os.Exit(2)
	}
}

// runMigrate handles `server migrate [flags]`: apply pending SQL migrations to
// the configured database and exit. Idempotent — safe to run on every deploy.
func runMigrate(args []string) {
	fs := flag.NewFlagSet("migrate", flag.ExitOnError)
	cfgPath := fs.String("config", "configs/config.yaml", "path to config file")
	_ = fs.Parse(args)
	logger.Setup("release")

	cfg, err := bootstrap.LoadConfig(*cfgPath)
	if err != nil {
		slog.Error("load config failed", "err", err)
		os.Exit(1)
	}
	cfg.Database.AutoMigrate = false // migration is done explicitly below
	db, err := bootstrap.OpenDB(cfg)
	if err != nil {
		slog.Error("database open failed", "err", err)
		os.Exit(1)
	}
	if err := bootstrap.RefuseIfStoreMoved(cfg, db); err != nil {
		slog.Error("refusing to start on a moved store", "err", err)
		os.Exit(1)
	}
	if err := bootstrap.Migrate(cfg, db); err != nil {
		slog.Error("migration failed", "err", err)
		os.Exit(1)
	}
	slog.Info("migration complete", "driver", cfg.Database.Driver, "env", cfg.Env)
}

// distinct counts how many different bytes a string uses — a long key made of a
// repeated character is long and still trivially weak.
func distinct(s string) int {
	var seen [256]bool
	n := 0
	for i := 0; i < len(s); i++ {
		if !seen[s[i]] {
			seen[s[i]] = true
			n++
		}
	}
	return n
}

// runEvery 每 tick 醒一次,到点就跑一轮 sweep。
//
// 三处刻意的写法,每一处都对应审查时发现的一个错:
//
//   - **先看 enabled 再读 interval。** 关着的功能不该每分钟查一次设置表 —— 默认关着的
//     部署也会 7×24 地承担这笔查询。
//   - **允许半个 tick 的误差。** tick 在 t0+k·T 到,而 last 是在那之后一点才盖的时间戳,
//     严格比较会让第 N 个 tick 差那么一点点而被跳过 —— 设 1 分钟实际跑 2 分钟。
//   - **sweep 跑完再盖 last。** 否则一轮 sweep 耗时超过间隔时,下一个 tick 立刻又开跑,
//     网关会一直处在扫描里,"间隔"名存实亡。
func runEvery(tick time.Duration, enabled func() bool, interval func() time.Duration, sweep func()) {
	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	last := time.Now()
	for now := range ticker.C {
		if !enabled() {
			continue
		}
		if now.Add(tick/2).Sub(last) < interval() {
			continue
		}
		sweep()
		last = time.Now()
	}
}

// runMigrateStore handles `server migrate-store`:把网关存储库从 MySQL 搬到 PostgreSQL。
//
// 退出码:0 = 搬完且核对通过,可以切换;1 = 没搬成或核对没过(不要切换,MySQL 原样可用);
// --estimate 只读源库、不碰目标。
func runMigrateStore(args []string) {
	fs := flag.NewFlagSet("migrate-store", flag.ExitOnError)
	from := fs.String("from-mysql", "", "源 MySQL 存储库 DSN,如 user:pw@tcp(host:3306)/aegisdb(可用环境变量 AEGISDB_MIGRATE_FROM_MYSQL)")
	to := fs.String("to-postgres", "", "目标 PostgreSQL 库 DSN,如 'host=… port=5432 user=… password=… dbname=aegisdb sslmode=disable'(可用 AEGISDB_MIGRATE_TO_POSTGRES)")
	tz := fs.String("tz", "", "写下这些数据的网关所在时区,如 Asia/Shanghai(必填:MySQL 的时间不带时区)")
	workers := fs.Int("workers", 4, "同时拷贝的表数")
	reset := fs.Bool("reset", false, "目标库里有上次没搬完的存储库表时,先清空再搬")
	estimate := fs.Bool("estimate", false, "只读源库:列出各表行数、校验审计链,不写目标")
	acceptBroken := fs.Bool("accept-broken-chain", false, "源库审计链本来就验不过(且已知原因)时仍然搬;默认拒绝,因为那通常是 --tz 填错了")
	_ = fs.Parse(args)
	logger.Setup("release")

	if *from == "" {
		*from = os.Getenv("AEGISDB_MIGRATE_FROM_MYSQL")
	}
	if *to == "" {
		*to = os.Getenv("AEGISDB_MIGRATE_TO_POSTGRES")
	}
	if *from == "" || *tz == "" || (*to == "" && !*estimate) {
		fmt.Fprintln(os.Stderr, "用法: aegisdb migrate-store --from-mysql <DSN> --to-postgres <DSN> --tz Asia/Shanghai [--workers 4] [--reset] [--estimate] [--accept-broken-chain]")
		os.Exit(1)
	}
	loc, err := time.LoadLocation(*tz)
	if err != nil {
		fmt.Fprintf(os.Stderr, "时区 %q 不认识: %v\n", *tz, err)
		os.Exit(1)
	}
	time.Local = loc

	rep, err := storemigrate.Run(context.Background(), storemigrate.Options{
		MySQLDSN: *from, PostgresDSN: *to, Workers: *workers, Reset: *reset, Estimate: *estimate,
		AcceptBrokenChain: *acceptBroken,
		ApplySchema:       bootstrap.RunSQLMigrations,
		Log:         func(f string, a ...any) { fmt.Printf(f+"\n", a...) },
	})
	if rep != nil && !*estimate {
		fmt.Println()
		fmt.Printf("%-32s %12s %12s  %s\n", "表", "源行数", "目标行数", "内容摘要")
		for _, t := range rep.Tables {
			mark := "一致"
			if t.SourceSum != t.TargetSum || t.SourceRows != t.TargetRows {
				mark = "不一致"
			}
			fmt.Printf("%-32s %12d %12d  %s\n", t.Name, t.SourceRows, t.TargetRows, mark)
		}
		if rep.TargetChain != nil {
			fmt.Printf("审计链(目标):%s\n", rep.TargetChain)
		}
	}
	if rep != nil && *estimate {
		fmt.Println()
		var total int64
		for _, t := range rep.Tables {
			fmt.Printf("%-32s %12d\n", t.Name, t.SourceRows)
			total += t.SourceRows
		}
		fmt.Printf("%-32s %12d\n", "合计", total)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "\n搬迁失败:%v\n", err)
		os.Exit(1)
	}
	if *estimate {
		fmt.Printf("\n预估完成(用时 %s,只读)\n", rep.Took.Round(time.Millisecond))
		return
	}
	fmt.Printf("\n搬迁完成,核对通过(用时 %s)。下一步:配置改为 database.driver: postgres + postgres_dsn,启动网关。\n", rep.Took.Round(time.Second))
}
