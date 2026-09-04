// Command server boots the DP DB GATEWAY backend.
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

	repo := repository.New(db)
	if cfg.Database.Seed {
		if err := bootstrap.Seed(repo, cfg); err != nil {
			slog.Error("seed failed", "err", err)
		}
	}

	// Initialize the at-rest secret key (encrypts stored DB-connection passwords)
	// before any connection is created/opened. Prefer a dedicated VELA_SECRET_KEY;
	// falling back to the JWT secret couples the two, so rotating VELA_JWT_SECRET
	// would make every stored ciphertext undecryptable (A2).
	secretKey, fellBack := cfg.SecretKeyResolved()
	if fellBack {
		slog.Warn("VELA_SECRET_KEY 未设置，回落使用 JWT 密钥派生连接口令加密密钥；轮换 VELA_JWT_SECRET 将导致已存储的连接口令无法解密。建议设置独立的 VELA_SECRET_KEY。")
	}
	crypto.SetSecretKey(secretKey)
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
	slog.Info("DP DB GATEWAY listening", "version", version, "addr", cfg.Server.Addr, "env", cfg.Env, "driver", cfg.Database.Driver, "tls", tls)
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
	// `init` opens the DB without boot-time AutoMigrate, then migrates explicitly
	// (versioned SQL on MySQL, AutoMigrate on sqlite) before seeding.
	cfg.Database.AutoMigrate = false
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
	slog.Info("initialization complete", "driver", cfg.Database.Driver, "env", cfg.Env)
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
	if err := bootstrap.Migrate(cfg, db); err != nil {
		slog.Error("migration failed", "err", err)
		os.Exit(1)
	}
	slog.Info("migration complete", "driver", cfg.Database.Driver, "env", cfg.Env)
}
