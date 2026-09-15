package bootstrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTempConfig writes a minimal config.yaml with the given env line and
// returns its path.
func writeTempConfig(t *testing.T, envLine string) string {
	t.Helper()
	body := envLine + `
database:
  postgres_dsn: "host=127.0.0.1 port=5432 dbname=vela_gateway sslmode=disable"
  seed: false
jwt:
  secret: "test-strong-secret-0123456789abcdef-xyz"
`
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	return p
}

// The JWT secret committed to configs/config.yaml is public, so serving in prod
// with it must be rejected even though it is >= 32 chars (R10). But loading the
// config for `migrate`/`init` (which don't sign tokens) must still succeed (R26).
func TestConfig_ProdServeRejectsCommittedDevSecretButMigrateLoads(t *testing.T) {
	t.Setenv("APP_ENV", "prod")
	t.Setenv("VELA_ENV", "")

	body := `env: "prod"
database:
  postgres_dsn: "host=127.0.0.1 port=5432 dbname=vela_gateway sslmode=disable"
jwt:
  secret: "change-me-vela-gateway-dev-secret"
`
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	// R26: loading succeeds — migrate/init only need the DSN.
	cfg, err := LoadConfig(p)
	if err != nil {
		t.Fatalf("LoadConfig for migrate/init must succeed, got %v", err)
	}
	// R10: but the serve path rejects the committed dev secret.
	if err := cfg.ValidateForServe(); err == nil {
		t.Fatal("serving in prod must reject the committed dev JWT secret")
	}
}

// A strong prod secret passes the serve-time validation.
func TestConfig_ProdServeAcceptsStrongSecret(t *testing.T) {
	t.Setenv("APP_ENV", "prod")
	t.Setenv("VELA_ENV", "")
	// prod 还要求 postgres_dsn 非空(见 TestValidateForServe_RejectsEmptyProdDSN),
	// 这里一并给上,好让这条用例只测密钥强度那一半。
	body := `env: "prod"
database:
  postgres_dsn: "host=127.0.0.1 port=5432 dbname=vela_gateway sslmode=disable"
jwt:
  secret: "a-strong-production-secret-0123456789abcd"
`
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := LoadConfig(p)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if err := cfg.ValidateForServe(); err != nil {
		t.Errorf("strong prod secret should pass serve validation, got %v", err)
	}
}

// env 仍然要解析(它决定 seed / JWT 强度校验 / CORS),但不再决定存储驱动 ——
// 存储只有 PostgreSQL 一种,VELA_DB_DRIVER 这个开关连同它的歧义一起没了。
func TestLoadConfigEnvResolution(t *testing.T) {
	cases := []struct {
		name    string
		envLine string // yaml `env:` line (may be empty)
		appEnv  string
		velaEnv string
		wantEnv string
	}{
		{"default is dev", "", "", "", "dev"},
		{"yaml env:prod", `env: "prod"`, "", "", "prod"},
		{"APP_ENV=prod overrides yaml dev", `env: "dev"`, "prod", "", "prod"},
		{"APP_ENV=production alias", "", "production", "", "prod"},
		{"VELA_ENV=prod when APP_ENV empty", "", "", "prod", "prod"},
		{"unknown env falls back to dev", "", "staging", "", "dev"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("APP_ENV", tc.appEnv)
			t.Setenv("VELA_ENV", tc.velaEnv)

			cfg, err := LoadConfig(writeTempConfig(t, tc.envLine))
			if err != nil {
				t.Fatalf("LoadConfig: %v", err)
			}
			if cfg.Env != tc.wantEnv {
				t.Errorf("Env = %q, want %q", cfg.Env, tc.wantEnv)
			}
			if cfg.Database.PostgresDSN == "" {
				t.Error("postgres_dsn 未读入")
			}
		})
	}
}

func TestLoadConfig_EnvOverridesDSN(t *testing.T) {
	t.Setenv("VELA_PG_DSN", "host=127.0.0.1 dbname=from_env sslmode=disable")
	cfg, err := LoadConfig(writeTempConfig(t, `env: "dev"`))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !strings.Contains(cfg.Database.PostgresDSN, "from_env") {
		t.Fatalf("VELA_PG_DSN 未覆盖: %q", cfg.Database.PostgresDSN)
	}
}
