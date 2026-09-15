// Command server boots the AegisDB backend.
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"velagateway/internal/bootstrap"
	"velagateway/internal/gateway"
	"velagateway/internal/handler"
	"velagateway/internal/repository"
	"velagateway/internal/service"
	"velagateway/pkg/crypto"
	"velagateway/pkg/jwt"
	"velagateway/pkg/logger"
)

// version is stamped at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	// Subcommands (production ops), each exits when done:
	//   init     — schema migration + reference data + platform admin
	//   migrate  — apply pending SQL migrations only (no seeding)
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "init":
			runInit(os.Args[2:])
			return
		case "migrate":
			runMigrate(os.Args[2:])
			return
		case "version", "-v", "--version":
			fmt.Printf("vela-gateway %s\n", version)
			return
		}
	}

	cfgPath := flag.String("config", "configs/config.yaml", "path to config file")
	// --no-migrate 把「迁移」与「服务」重新分开:多副本滚动升级时,第一个重启的
	// 副本自己改共享库是一件需要能关掉的事(其余旧副本会当场对着新表结构服务)。
	//
	// 它**不是**「什么都不做」—— 跳过之后仍然验证 schema 已是最新,差一条就拒绝
	// 启动。不验证的话,「serve 不迁移、人也忘了跑」就把表不全的网关放上线,
	// 而那种进程会让 /healthz 变绿、让每个请求 500。
	noMigrate := flag.Bool("no-migrate", false,
		"跳过启动时的自动迁移(仍会验证 schema 是最新的;迁移需另行 `vela-gateway migrate`)")
	flag.Parse()

	cfg, err := bootstrap.LoadConfig(*cfgPath)
	if err != nil {
		slog.Error("load config failed", "err", err)
		os.Exit(1)
	}
	// Serving signs tokens, so a strong prod JWT secret is mandatory here (but not
	// for the migrate/init subcommands above — those call ValidateForDB, which
	// ValidateForServe subsumes).
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
	// 此前这一步藏在 OpenDB 的自动建表分支里,只有 dev 走到。现在 serve /
	// migrate / init 三条路都经过同一个 Migrate —— 顺带修好了一个旧缺陷:dev 的
	// serve 路径从来没跑过 backfillGliEnv / backfillEnvTiers / seedPipelineReference /
	// backfillApprovalExecuted 这四个回填,它们此前只挂在 migrate 子命令上。
	//
	// 失败必须 os.Exit。一台没有表的网关照样能监听端口 —— 每个请求 500,而进程
	// 看上去是活的,于是探活探到的是一个「跑着的坏进程」。下面的 Seed 同理:它从前
	// 只打日志不退出,留下一个没有角色、没有管理员却 /healthz 全绿的副本。
	if *noMigrate {
		// 不应用,但要验证。差一条都不放行 —— 见 VerifySchemaCurrent 的注释。
		if err := bootstrap.VerifySchemaCurrent(db, bootstrap.MigrationsFS()); err != nil {
			slog.Error("schema is not up to date and --no-migrate was given", "err", err)
			os.Exit(1)
		}
		slog.Info("skipping automatic migration (--no-migrate); schema verified up to date")
	} else if err := bootstrap.Migrate(cfg, db); err != nil {
		slog.Error("schema migration failed", "err", err)
		os.Exit(1)
	}

	repo := repository.New(db)

	// Initialize the at-rest secret key (encrypts stored DB-connection passwords)
	// before any connection is created/opened. Prefer a dedicated VELA_SECRET_KEY;
	// falling back to the JWT secret couples the two, so rotating VELA_JWT_SECRET
	// would make every stored ciphertext undecryptable (A2).
	//
	// **在 Seed 之前**。注释一直写着「在任何连接被创建或打开之前」,而 Seed 排在它前面 ——
	// 今天没炸只是因为种子不建带口令的连接。哪天有人往种子里加一台带凭据的实例,
	// EncryptSecret 会返回 "secret key not initialized",而那台实例会以**明文口令**
	// 或者干脆建不出来的形式出现,取决于调用方怎么处理那个 error。
	secretKey, fellBack := cfg.SecretKeyResolved()
	if fellBack {
		slog.Warn("VELA_SECRET_KEY 未设置，回落使用 JWT 密钥派生连接口令加密密钥；轮换 VELA_JWT_SECRET 将导致已存储的连接口令无法解密。建议设置独立的 VELA_SECRET_KEY。")
	}
	crypto.SetSecretKey(secretKey)

	if cfg.Database.Seed {
		// 同样必须 os.Exit,理由和上面 Migrate 那条一模一样:播种失败留下的是一个
		// 没有角色、没有管理员的库,而 /healthz 不碰数据库,照样返回 ok。编排系统
		// 于是把流量切给一个所有人都登不上的副本。prod 的 seed 本来就关着,所以
		// 这条只影响 dev / 演示 —— 而那正是它会咬人的地方。
		if err := bootstrap.Seed(repo, cfg); err != nil {
			slog.Error("seed failed", "err", err)
			os.Exit(1)
		}
	}
	// The webhook SSRF guard blocks private/loopback targets. Dev relaxes it by
	// default (on-host receivers); prod keeps it on unless webhook.allow_private
	// (or VELA_WEBHOOK_ALLOW_PRIVATE) explicitly opts in for a trusted internal target.
	service.AllowPrivateWebhookTargets = cfg.Env != "prod" || cfg.Webhook.AllowPrivate
	if cfg.Env == "prod" && cfg.Webhook.AllowPrivate {
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
	gateway.AllowSimulation = cfg.Env != "prod"
	slog.Info("simulation mode", "env", cfg.Env, "allowSimulatedData", gateway.AllowSimulation)
	svc.LogApprovalStaffing()
	h := handler.New(svc, repo)

	r := bootstrap.NewRouter(cfg, h, repo, svc, jwtMgr)

	// Background: approval timeout sweep (FR-APPR-05 / backend doc §7).
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			svc.SweepApprovalTimeouts()
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

	// Background: 元数据同步 —— 把远端库的表清单与表结构抓一份到本地。
	//
	// **默认关着**(meta.sync.enabled)。打开它意味着这台网关会周期性地登录你的每一台
	// 生产实例,那必须是一次明确的决定,不能因为升级了一个版本就自己开始跑。
	//
	// 与导出保留不同,这里**启动时不跑一次**:进程重启是件很常见的事,而每次重启都
	// 顺手扫一遍所有生产库,会让"重启网关"变成一个有副作用的动作。第一轮等到间隔到点。
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		last := time.Now()
		for range ticker.C {
			// 间隔是运行时设置,可能被改小/改大,所以每小时醒一次、自己比对是否到点 ——
			// 而不是按启动时读到的那个值把 ticker 钉死。
			if time.Since(last) < svc.MetaSyncInterval() {
				continue
			}
			last = time.Now()
			svc.SweepMetadata()
		}
	}()

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
	slog.Info("AegisDB listening", "version", version, "addr", cfg.Server.Addr, "env", cfg.Env, "tls", tls)
	if cfg.Env == "prod" && !tls {
		slog.Warn("生产环境未启用 TLS:请配置 server.tls_cert/tls_key,或在前置反向代理终止 TLS,避免凭据 / JWT 明文传输")
	}
	var runErr error
	if tls {
		runErr = r.RunTLS(cfg.Server.Addr, cfg.Server.TLSCert, cfg.Server.TLSKey)
	} else {
		runErr = r.Run(cfg.Server.Addr)
	}
	if runErr != nil {
		slog.Error("server stopped", "err", runErr)
		os.Exit(1)
	}
}

// runInit handles `server init [flags]`: migrate schema + seed reference data +
// create the platform admin. Credentials come from flags or VELA_ADMIN_* env.
func runInit(args []string) {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	cfgPath := fs.String("config", "configs/config.yaml", "path to config file")
	email := fs.String("admin-email", os.Getenv("VELA_ADMIN_EMAIL"), "platform admin email")
	password := fs.String("admin-password", os.Getenv("VELA_ADMIN_PASSWORD"), "platform admin password (>= 12 chars, >= 3 character classes)")
	name := fs.String("admin-name", os.Getenv("VELA_ADMIN_NAME"), "platform admin display name (optional)")
	_ = fs.Parse(args)
	logger.Setup("release")

	cfg, err := bootstrap.LoadConfig(*cfgPath)
	if err != nil {
		slog.Error("load config failed", "err", err)
		os.Exit(1)
	}
	// Same storage guard serve uses. Without it a prod config with an empty
	// postgres_dsn does not fail — libpq connects to the local default database
	// (dbname = OS user) and `init` happily creates the admin THERE.
	if err := cfg.ValidateForDB(); err != nil {
		slog.Error("invalid configuration", "err", err)
		os.Exit(1)
	}
	// `init` opens the DB (OpenDB never creates tables), then migrates explicitly
	// with the versioned SQL before seeding.
	db, err := bootstrap.OpenDB(cfg)
	if err != nil {
		slog.Error("database init failed", "err", err)
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
	slog.Info("initialization complete", "env", cfg.Env)
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
	// Same storage guard serve uses — and this is the path where it matters most:
	// an empty prod postgres_dsn would otherwise build all 36 tables in the local
	// default database (dbname = OS user) and exit 0.
	if err := cfg.ValidateForDB(); err != nil {
		slog.Error("invalid configuration", "err", err)
		os.Exit(1)
	}
	db, err := bootstrap.OpenDB(cfg)
	if err != nil {
		slog.Error("database open failed", "err", err)
		os.Exit(1)
	}
	if err := bootstrap.Migrate(cfg, db); err != nil {
		slog.Error("migration failed", "err", err)
		os.Exit(1)
	}
	slog.Info("migration complete", "env", cfg.Env)
}
