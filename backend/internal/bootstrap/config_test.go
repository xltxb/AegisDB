package bootstrap

import (
	"os"
	"path/filepath"
	"testing"
)

// writeTempConfig writes a minimal config.yaml with the given env line and
// returns its path.
func writeTempConfig(t *testing.T, envLine string) string {
	t.Helper()
	body := envLine + `
database:
  mysql_dsn: "vela:velapass@tcp(127.0.0.1:3306)/vela_gateway"
  sqlite_path: "vela-gateway.db"
  auto_migrate: false
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
	t.Setenv("VELA_DB_DRIVER", "")

	body := `env: "prod"
database:
  mysql_dsn: "vela:velapass@tcp(127.0.0.1:3306)/vela_gateway"
  sqlite_path: "vela-gateway.db"
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
	t.Setenv("VELA_DB_DRIVER", "")
	body := `env: "prod"
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

// TestLoadConfigDriverResolution pins the dev(sqlite)/prod(mysql) switch and the
// precedence: VELA_DB_DRIVER > APP_ENV/VELA_ENV > config.env > default(dev).
func TestLoadConfigDriverResolution(t *testing.T) {
	cases := []struct {
		name       string
		envLine    string // yaml `env:` line (may be empty)
		appEnv     string
		velaEnv    string
		driverEnv  string
		wantEnv    string
		wantDriver string
	}{
		{"default is dev/sqlite", "", "", "", "", "dev", "sqlite"},
		{"yaml env:prod -> mysql", `env: "prod"`, "", "", "", "prod", "mysql"},
		{"APP_ENV=prod overrides yaml dev", `env: "dev"`, "prod", "", "", "prod", "mysql"},
		{"APP_ENV=production alias", "", "production", "", "", "prod", "mysql"},
		{"VELA_ENV=prod when APP_ENV empty", "", "", "prod", "", "prod", "mysql"},
		{"VELA_DB_DRIVER wins over prod env", `env: "prod"`, "prod", "", "sqlite", "prod", "sqlite"},
		{"unknown env falls back to dev", "", "staging", "", "", "dev", "sqlite"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("APP_ENV", tc.appEnv)
			t.Setenv("VELA_ENV", tc.velaEnv)
			t.Setenv("VELA_DB_DRIVER", tc.driverEnv)

			cfg, err := LoadConfig(writeTempConfig(t, tc.envLine))
			if err != nil {
				t.Fatalf("LoadConfig: %v", err)
			}
			if cfg.Env != tc.wantEnv {
				t.Errorf("Env = %q, want %q", cfg.Env, tc.wantEnv)
			}
			if cfg.Database.Driver != tc.wantDriver {
				t.Errorf("Driver = %q, want %q", cfg.Database.Driver, tc.wantDriver)
			}
		})
	}
}
