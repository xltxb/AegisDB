package bootstrap

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// weakJWTSecrets are placeholder/dev secrets that must never protect a prod
// deployment (a shipped default key lets anyone forge admin tokens).
var weakJWTSecrets = map[string]bool{
	"":                                 true,
	"CHANGE-ME":                        true,
	"change-me":                        true,
	"changeme":                         true,
	"dev-secret":                       true,
	"secret":                           true,
	"aegisdb-dev-secret":               true,
	"aegisdb-secret":                   true,
	"change-me-aegisdb-dev-secret":     true, // the value shipped in configs/config.yaml (public → compromised)
	"CHANGE-ME-set-AEGISDB_JWT_SECRET": true, // former config.prod.yaml placeholder
}

// Config is the full backend configuration loaded from configs/config.yaml.
type Config struct {
	// Env is the deployment profile: "dev" (local, PostgreSQL) or "prod" (MySQL).
	// Resolved from APP_ENV/AEGISDB_ENV env vars, falling back to this yaml field
	// then "dev". It drives the default database driver — see LoadConfig.
	Env string `yaml:"env"`
	// EnvRaw is the env name the operator actually asked for, BEFORE it was
	// collapsed into dev/prod. It matters because `Env` maps every unrecognized
	// name (staging, uat, gli, …) to "dev", so by the time we validate we can no
	// longer tell a genuine local `go run` apart from a real staging box that
	// fell through to the dev profile. The strong-secret gate (issue 33) keys on
	// this instead: empty / dev / local = local dev; anything else = a real
	// deployment that must not sign tokens with the shipped placeholder secret.
	EnvRaw string `yaml:"-"`
	Server struct {
		Addr        string   `yaml:"addr"`
		Mode        string   `yaml:"mode"`
		CORSOrigins []string `yaml:"cors_origins"`
		// WebDir is the built frontend directory to serve (SPA) in production.
		// Empty = don't serve static assets (dev runs the Vite server separately).
		WebDir string `yaml:"web_dir"`
		// TLSCert/TLSKey enable HTTPS directly on the gateway. When both are set
		// the server listens with TLS (RunTLS); otherwise plain HTTP (expect a
		// TLS-terminating reverse proxy in front). See H6.
		TLSCert string `yaml:"tls_cert"`
		TLSKey  string `yaml:"tls_key"`
		// TrustedProxies lists the reverse-proxy IPs/CIDRs whose X-Forwarded-For
		// may be trusted. Empty = trust none (ClientIP == direct peer), which is
		// the safe default and keeps the IP allowlist un-spoofable. See H2.
		TrustedProxies []string `yaml:"trusted_proxies"`
	} `yaml:"server"`
	Database struct {
		Driver   string `yaml:"driver"`
		MySQLDSN string `yaml:"mysql_dsn"`
		// PostgresDSN 是 driver: postgres 时的连接串(libpq 键值或 URL 形式均可)。
		PostgresDSN string `yaml:"postgres_dsn"`
		// AutoMigrate is kept only so old config files still parse. Nothing
		// auto-migrates any more (the schema comes from migrations/*.sql, applied
		// by `server migrate`); OpenDB warns when it is set to true.
		AutoMigrate bool `yaml:"auto_migrate"`
		Seed        bool `yaml:"seed"`
	} `yaml:"database"`
	JWT struct {
		Secret   string `yaml:"secret"`
		TTLHours int    `yaml:"ttl_hours"`
	} `yaml:"jwt"`
	// SecretKey is the passphrase for at-rest encryption of stored DB-connection
	// passwords. Resolved from AEGISDB_SECRET_KEY; when empty it falls back to the
	// JWT secret. Keeping it separate lets the JWT signing secret be rotated
	// without making previously encrypted ciphertext undecryptable (A2).
	SecretKey string `yaml:"secret_key"`
	Gateway   struct {
		DefaultPolicy      string `yaml:"default_policy"`
		StrictMode         bool   `yaml:"strict_mode"`
		ExecTimeoutSeconds int    `yaml:"exec_timeout_seconds"`
	} `yaml:"gateway"`
	Webhook struct {
		Endpoint string `yaml:"endpoint"`
		Secret   string `yaml:"secret"`
		Events   string `yaml:"events"`
		RetryMax int    `yaml:"retry_max"`
		Enabled  bool   `yaml:"enabled"`
		// AllowPrivate relaxes the outbound SSRF guard so webhook/Lark targets may
		// resolve to loopback/private/link-local addresses. Dev allows this by
		// default; prod keeps it off unless explicitly enabled here (or via
		// AEGISDB_WEBHOOK_ALLOW_PRIVATE) — e.g. to reach an on-host/internal receiver.
		// Only enable it when the target network is trusted: it re-opens SSRF to
		// internal services and the cloud metadata endpoint.
		AllowPrivate bool `yaml:"allow_private"`
	} `yaml:"webhook"`
	// SSO 的部署层总闸。平时 SSO 在【设置 · 会话与安全】里开关;这里写 true(或环境变量
	// AEGISDB_SSO_DISABLED=1)则无论设置页怎么存都按关闭处理,所有人回到账号口令登录。
	// 给"SSO 出了问题、又进不去设置页"的时候用,改完重启生效。
	SSO struct {
		Disabled bool `yaml:"disabled"`
	} `yaml:"sso"`
	// 管理后台 IP 白名单的部署层总闸(管理后台拆分,工单 08)。白名单在【设置】里配;这里写 true
	// (或环境变量 AEGISDB_ADMIN_IP_ALLOWLIST_DISABLED=1)则无论设置怎么存都不生效 —— 给"白名单配错、
	// 管理员全被挡在管理后台外面、又改不了设置"的时候用,改完重启生效。
	AdminIP struct {
		AllowlistDisabled bool `yaml:"allowlist_disabled"`
	} `yaml:"admin_ip"`
}

// errSQLiteUnsupported is returned when a config asks for the retired SQLite store.
var errSQLiteUnsupported = errors.New("SQLite 存储库已不再支持,请改用 PostgreSQL(见 README「本地开发」)")

func isSQLiteDriver(v string) bool { return strings.EqualFold(strings.TrimSpace(v), "sqlite") }

// LoadConfig reads YAML config from path, applying env overrides for secrets/DSN.
func LoadConfig(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg := &Config{}
	if err := yaml.Unmarshal(raw, cfg); err != nil {
		return nil, err
	}
	if isSQLiteDriver(cfg.Database.Driver) {
		return nil, errSQLiteUnsupported
	}
	// Environment overrides (handy for docker / CI).
	if v := os.Getenv("AEGISDB_MYSQL_DSN"); v != "" {
		cfg.Database.MySQLDSN = v
	}
	if v := os.Getenv("AEGISDB_POSTGRES_DSN"); v != "" {
		cfg.Database.PostgresDSN = v
	}
	if v := os.Getenv("AEGISDB_JWT_SECRET"); v != "" {
		cfg.JWT.Secret = v
	}
	if v := os.Getenv("AEGISDB_WEB_DIR"); v != "" {
		cfg.Server.WebDir = v
	}
	if v := os.Getenv("AEGISDB_TLS_CERT"); v != "" {
		cfg.Server.TLSCert = v
	}
	if v := os.Getenv("AEGISDB_TLS_KEY"); v != "" {
		cfg.Server.TLSKey = v
	}
	if v := os.Getenv("AEGISDB_WEBHOOK_SECRET"); v != "" { // M11: allow secret via env, not just file
		cfg.Webhook.Secret = v
	}
	if v := os.Getenv("AEGISDB_SECRET_KEY"); v != "" { // A2: dedicated at-rest encryption key
		cfg.SecretKey = v
	}
	if v := os.Getenv("AEGISDB_WEBHOOK_ALLOW_PRIVATE"); v != "" { // relax SSRF guard for internal receivers
		cfg.Webhook.AllowPrivate = isTruthy(v)
	}
	if v := os.Getenv("AEGISDB_SSO_DISABLED"); v != "" { // SSO 总闸:强制关闭,回到口令登录
		cfg.SSO.Disabled = isTruthy(v)
	}
	if v := os.Getenv("AEGISDB_ADMIN_IP_ALLOWLIST_DISABLED"); v != "" { // 管理后台 IP 白名单总闸
		cfg.AdminIP.AllowlistDisabled = isTruthy(v)
	}

	// Resolve the deployment profile and, from it, the database driver.
	// Precedence for the driver (highest first):
	//   1. AEGISDB_DB_DRIVER      — explicit manual override (mysql|postgres)
	//   2. APP_ENV / AEGISDB_ENV / config.env  — dev→postgres, prod→mysql
	//      存储库搬到 PG 之后用 AEGISDB_DB_DRIVER=postgres(写在 aegisdb.env)—— 不读配置文件里的
	//      driver:升级包会覆盖 configs/config.yaml,写在那里下一次升级就悄悄连回旧 MySQL。
	// Local dev thus uses PostgreSQL while prod uses MySQL, switched from a single
	// startup knob. The SQLite store is no longer supported and is rejected.
	rawEnv := firstNonEmpty(os.Getenv("APP_ENV"), os.Getenv("AEGISDB_ENV"), cfg.Env)
	cfg.EnvRaw = strings.TrimSpace(rawEnv) // remember the requested name for the strong-secret gate (issue 33)
	if rawEnv != "" && !isKnownEnv(rawEnv) {
		// e.g. APP_ENV=staging → falls through to dev. Surface it rather than
		// silently treating a would-be non-dev deployment as a local dev box (C6).
		slog.Warn("未识别的环境名,将按 dev 处理", "env", rawEnv)
	}
	cfg.Env = normalizeEnv(firstNonEmpty(rawEnv, "dev"))
	if v := os.Getenv("AEGISDB_DB_DRIVER"); v != "" {
		if isSQLiteDriver(v) {
			return nil, errSQLiteUnsupported
		}
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "mysql", "postgres":
			cfg.Database.Driver = strings.ToLower(strings.TrimSpace(v))
		default:
			return nil, fmt.Errorf("AEGISDB_DB_DRIVER=%q 无效:只支持 mysql 或 postgres", v)
		}
	} else if cfg.Env == "prod" {
		cfg.Database.Driver = "mysql"
	} else {
		cfg.Database.Driver = "postgres"
	}
	// NOTE: the prod JWT-secret enforcement lives in ValidateForServe (called on
	// the serve path only). `migrate`/`init` need just the DSN and must not be
	// blocked by a missing JWT secret with an unrelated error (R26).
	return cfg, nil
}

// ValidateForServe enforces the secrets required to actually run the server:
// in production the JWT signing key must be strong and not a shipped placeholder
// (C4/R10). Schema migration and initialization skip this — they don't sign
// tokens. Call this right before starting the HTTP listener.
func (c *Config) ValidateForServe() error {
	// Trusted proxies must parse as IPs/CIDRs. gin silently falls back to trusting
	// ALL proxies when SetTrustedProxies is given a bad value, which would let a
	// client spoof X-Forwarded-For and bypass the IP allowlist (B5/H2). Validate
	// here — in every env — so a typo refuses startup instead of degrading safety.
	for _, p := range c.Server.TrustedProxies {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, _, err := net.ParseCIDR(p); err == nil {
			continue
		}
		if net.ParseIP(p) != nil {
			continue
		}
		return fmt.Errorf("trusted_proxies 配置非法(需为 IP 或 CIDR):%q", p)
	}

	// Strong-secret enforcement applies to prod AND to every non-local deployment.
	// `Env` alone is not enough: staging/uat/unknown all normalize to "dev", so a
	// real staging gateway would otherwise sign tokens with the public placeholder
	// secret and skip this check entirely (issue 33 — the forged-admin-token PoC).
	// Gate on the operator's REQUESTED env: only a genuine local dev run (env
	// empty, or dev/development/local) is exempt, keeping zero-config dev working;
	// anything else the operator explicitly asked for is a deployment and must
	// carry a strong signing key.
	if c.Env != "prod" && isLocalDevEnv(c.EnvRaw) {
		return nil
	}
	sec := strings.TrimSpace(c.JWT.Secret)
	if weakJWTSecrets[sec] {
		return fmt.Errorf("非本地部署(prod / staging 等)必须设置强 JWT 密钥：请通过 AEGISDB_JWT_SECRET 或 config.prod.yaml 的 jwt.secret 配置(不能使用仓库里的默认占位值,它是公开的)")
	}
	if len(sec) < 32 {
		return fmt.Errorf("非本地部署 JWT 密钥过短(至少 32 字符),当前 %d 字符", len(sec))
	}
	// A long secret can still be trivially weak (a repeated char / short cycle).
	// Require a minimum character variety so an obviously low-entropy value —
	// which the length + blacklist checks miss — is refused (C5).
	if distinctBytes(sec) < minSecretDistinct {
		return fmt.Errorf("非本地部署 JWT 密钥字符种类过少(疑似弱密钥),请用 `openssl rand -hex 32` 生成")
	}
	return nil
}

// minSecretDistinct is the minimum number of distinct bytes a strong secret must
// contain. Hex (16 symbols) / base64 secrets easily exceed it; repeated or
// short-cycle strings fall below.
const minSecretDistinct = 8

func distinctBytes(s string) int {
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

// isKnownEnv reports whether s is a recognized environment alias. LoadConfig warns
// on anything else so an env like "staging" doesn't silently pass as a known one.
// isLocalDevEnv reports whether the operator's REQUESTED env name is a genuine
// local-dev run (empty, or dev/development/local). Everything else — prod,
// staging, uat, gli, any unrecognized name — is a real deployment that must not
// be treated as zero-config dev for the strong-secret / docs-exposure gates
// (issues 33 & 35).
// IsLocalDev reports whether this is a genuine local development run. Used by
// callers that relax a protection for developer convenience — the relaxation
// must not extend to staging/uat, which are deployments on real networks.
func (c *Config) IsLocalDev() bool { return c.Env != "prod" && isLocalDevEnv(c.EnvRaw) }

func isLocalDevEnv(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "dev", "development", "local":
		return true
	}
	return false
}

func isKnownEnv(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "prod", "production", "release", "live", "dev", "development", "local":
		return true
	}
	return false
}

// SecretKeyResolved returns the passphrase used to encrypt stored DB-connection
// passwords at rest, and whether it fell back to the JWT secret. A dedicated
// AEGISDB_SECRET_KEY decouples token signing from connection-password encryption so
// the JWT secret can be rotated without making stored ciphertext undecryptable
// (A2). When it falls back, the caller should warn.
func (c *Config) SecretKeyResolved() (string, bool) {
	if c.SecretKey != "" {
		return c.SecretKey, false
	}
	return c.JWT.Secret, true
}

// normalizeEnv maps assorted aliases to the canonical "dev" | "prod".
func normalizeEnv(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "prod", "production", "release", "live":
		return "prod"
	default:
		return "dev"
	}
}

// isTruthy interprets a boolean-ish env var. Anything in the affirmative set is
// true; everything else (incl. "0"/"false"/"") is false.
func isTruthy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
