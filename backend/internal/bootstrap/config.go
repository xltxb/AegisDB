package bootstrap

import (
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
	"vela-dev-secret":                  true,
	"vela-gateway-secret":              true,
	"secret":                           true,
	"change-me-vela-gateway-dev-secret": true, // the value shipped in configs/config.yaml (public → compromised)
	"CHANGE-ME-set-VELA_JWT_SECRET":    true, // former config.prod.yaml placeholder
}

// Config is the full backend configuration loaded from configs/config.yaml.
type Config struct {
	// Env is the deployment profile: "dev" (local, SQLite) or "prod" (MySQL).
	// Resolved from APP_ENV/VELA_ENV env vars, falling back to this yaml field
	// then "dev". It drives the default database driver — see LoadConfig.
	Env    string `yaml:"env"`
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
		Driver      string `yaml:"driver"`
		MySQLDSN    string `yaml:"mysql_dsn"`
		SQLitePath  string `yaml:"sqlite_path"`
		AutoMigrate bool   `yaml:"auto_migrate"`
		Seed        bool   `yaml:"seed"`
	} `yaml:"database"`
	JWT struct {
		Secret   string `yaml:"secret"`
		TTLHours int    `yaml:"ttl_hours"`
	} `yaml:"jwt"`
	// SecretKey is the passphrase for at-rest encryption of stored DB-connection
	// passwords. Resolved from VELA_SECRET_KEY; when empty it falls back to the
	// JWT secret. Keeping it separate lets the JWT signing secret be rotated
	// without making previously encrypted ciphertext undecryptable (A2).
	SecretKey string `yaml:"secret_key"`
	Gateway struct {
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
		// VELA_WEBHOOK_ALLOW_PRIVATE) — e.g. to reach an on-host/internal receiver.
		// Only enable it when the target network is trusted: it re-opens SSRF to
		// internal services and the cloud metadata endpoint.
		AllowPrivate bool `yaml:"allow_private"`
	} `yaml:"webhook"`
}

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
	// Environment overrides (handy for docker / CI).
	if v := os.Getenv("VELA_MYSQL_DSN"); v != "" {
		cfg.Database.MySQLDSN = v
	}
	if v := os.Getenv("VELA_JWT_SECRET"); v != "" {
		cfg.JWT.Secret = v
	}
	if v := os.Getenv("VELA_WEB_DIR"); v != "" {
		cfg.Server.WebDir = v
	}
	if v := os.Getenv("VELA_TLS_CERT"); v != "" {
		cfg.Server.TLSCert = v
	}
	if v := os.Getenv("VELA_TLS_KEY"); v != "" {
		cfg.Server.TLSKey = v
	}
	if v := os.Getenv("VELA_WEBHOOK_SECRET"); v != "" { // M11: allow secret via env, not just file
		cfg.Webhook.Secret = v
	}
	if v := os.Getenv("VELA_SECRET_KEY"); v != "" { // A2: dedicated at-rest encryption key
		cfg.SecretKey = v
	}
	if v := os.Getenv("VELA_WEBHOOK_ALLOW_PRIVATE"); v != "" { // relax SSRF guard for internal receivers
		cfg.Webhook.AllowPrivate = isTruthy(v)
	}

	// Resolve the deployment profile and, from it, the database driver.
	// Precedence for the driver (highest first):
	//   1. VELA_DB_DRIVER      — explicit manual override (mysql|sqlite)
	//   2. APP_ENV / VELA_ENV / config.env  — dev→sqlite, prod→mysql
	// Local dev thus needs zero dependencies (SQLite) while prod uses MySQL,
	// switched from a single startup knob.
	rawEnv := firstNonEmpty(os.Getenv("APP_ENV"), os.Getenv("VELA_ENV"), cfg.Env)
	if rawEnv != "" && !isKnownEnv(rawEnv) {
		// e.g. APP_ENV=staging → falls through to dev (SQLite). Surface it rather
		// than silently degrading a would-be non-dev deployment to a local file (C6).
		slog.Warn("未识别的环境名,将按 dev 处理(使用 SQLite)", "env", rawEnv)
	}
	cfg.Env = normalizeEnv(firstNonEmpty(rawEnv, "dev"))
	if v := os.Getenv("VELA_DB_DRIVER"); v != "" {
		cfg.Database.Driver = v
	} else if cfg.Env == "prod" {
		cfg.Database.Driver = "mysql"
	} else {
		cfg.Database.Driver = "sqlite"
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

	if c.Env != "prod" {
		return nil
	}
	sec := strings.TrimSpace(c.JWT.Secret)
	if weakJWTSecrets[sec] {
		return fmt.Errorf("生产环境必须设置强 JWT 密钥：请通过 VELA_JWT_SECRET 或 config.prod.yaml 的 jwt.secret 配置(不能使用默认占位值)")
	}
	if len(sec) < 32 {
		return fmt.Errorf("生产环境 JWT 密钥过短(至少 32 字符),当前 %d 字符", len(sec))
	}
	// A long secret can still be trivially weak (a repeated char / short cycle).
	// Require a minimum character variety so an obviously low-entropy value —
	// which the length + blacklist checks miss — is refused (C5).
	if distinctBytes(sec) < minSecretDistinct {
		return fmt.Errorf("生产环境 JWT 密钥字符种类过少(疑似弱密钥),请用 `openssl rand -hex 32` 生成")
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
// on anything else so an env like "staging" doesn't silently degrade to SQLite.
func isKnownEnv(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "prod", "production", "release", "live", "dev", "development", "local":
		return true
	}
	return false
}

// SecretKeyResolved returns the passphrase used to encrypt stored DB-connection
// passwords at rest, and whether it fell back to the JWT secret. A dedicated
// VELA_SECRET_KEY decouples token signing from connection-password encryption so
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
