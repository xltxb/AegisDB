package bootstrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The SQLite store is gone: asking for it (env override or config file) must fail
// loudly instead of silently falling back to another driver.
func TestConfig_SQLiteDriverRejected(t *testing.T) {
	t.Setenv("APP_ENV", "")
	t.Setenv("AEGISDB_ENV", "")

	t.Run("env override", func(t *testing.T) {
		t.Setenv("AEGISDB_DB_DRIVER", "sqlite")
		_, err := LoadConfig(writeTempConfig(t, ""))
		if err == nil || !strings.Contains(err.Error(), "已不再支持") {
			t.Fatalf("AEGISDB_DB_DRIVER=sqlite should be rejected with 已不再支持, got %v", err)
		}
	})
	t.Run("config file", func(t *testing.T) {
		t.Setenv("AEGISDB_DB_DRIVER", "")
		p := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(p, []byte("database:\n  driver: sqlite\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := LoadConfig(p)
		if err == nil || !strings.Contains(err.Error(), "已不再支持") {
			t.Fatalf("driver: sqlite in the config file should be rejected with 已不再支持, got %v", err)
		}
	})
}

func TestConfig_DevDefaultsToPostgres(t *testing.T) {
	cases := []struct {
		name, appEnv, want string
	}{
		{"unset", "", "postgres"},
		{"dev", "dev", "postgres"},
		{"staging", "staging", "postgres"},
		{"prod", "prod", "mysql"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("APP_ENV", tc.appEnv)
			t.Setenv("AEGISDB_ENV", "")
			t.Setenv("AEGISDB_DB_DRIVER", "")
			cfg, err := LoadConfig(writeTempConfig(t, ""))
			if err != nil {
				t.Fatalf("LoadConfig: %v", err)
			}
			if cfg.Database.Driver != tc.want {
				t.Errorf("Driver = %q, want %q", cfg.Database.Driver, tc.want)
			}
		})
	}
}
