package bootstrap

import (
	"path/filepath"
	"testing"

	"velagateway/internal/repository"
)

// C5: a prod JWT secret that is long enough and not blacklisted can still be
// trivially weak (e.g. a repeated character). Require a minimum character variety
// so an obviously low-entropy secret is refused.
func TestValidateForServe_RejectsLowEntropySecret(t *testing.T) {
	mk := func(sec string) *Config {
		c := &Config{}
		c.Env = "prod"
		c.JWT.Secret = sec
		return c
	}

	// 34 identical chars: passes length + blacklist, but near-zero entropy.
	if err := mk("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa").ValidateForServe(); err == nil {
		t.Error("expected a repeated-character secret to be rejected")
	}
	// "changeme" x4 = 32 chars, only 7 distinct — still weak.
	if err := mk("changemechangemechangemechangeme").ValidateForServe(); err == nil {
		t.Error("expected a low-variety secret to be rejected")
	}
	// a realistic openssl rand -hex 32 style secret passes.
	if err := mk("9f2c1a7e4b0d63859a1e2f7c4d8b6031aa55cc77ee99bb00").ValidateForServe(); err != nil {
		t.Errorf("expected a high-entropy secret to pass, got %v", err)
	}
}

// issue 33: the strong-secret gate must not be skipped just because an
// unrecognized env (staging/uat/…) collapsed to the dev profile. A real
// deployment that requested such an env still signs tokens, so it must carry a
// strong secret — otherwise it runs on the public placeholder and anyone with
// the repo can forge an admin token.
func TestValidateForServe_NonLocalEnvRequiresStrongSecret(t *testing.T) {
	placeholder := "change-me-vela-gateway-dev-secret" // the value shipped in configs/config.yaml

	// A genuine local-dev run (env empty, or dev/local) stays exempt: zero-config
	// dev must keep working with the placeholder.
	for _, raw := range []string{"", "dev", "development", "local"} {
		c := &Config{}
		c.Env = normalizeEnv(raw)
		c.EnvRaw = raw
		c.JWT.Secret = placeholder
		if err := c.ValidateForServe(); err != nil {
			t.Errorf("local dev env %q with placeholder secret should pass, got %v", raw, err)
		}
	}

	// Any other requested env is a real deployment — the placeholder (and any
	// weak/short secret) must be refused, even though Env normalized to "dev".
	for _, raw := range []string{"staging", "uat", "gli", "qa", "bogus"} {
		c := &Config{}
		c.Env = normalizeEnv(raw) // == "dev"
		c.EnvRaw = raw
		c.JWT.Secret = placeholder
		if err := c.ValidateForServe(); err == nil {
			t.Errorf("non-local env %q with the public placeholder secret must be rejected", raw)
		}
		// A strong secret lets the same deployment start.
		c.JWT.Secret = "9f2c1a7e4b0d63859a1e2f7c4d8b6031aa55cc77ee99bb00"
		if err := c.ValidateForServe(); err != nil {
			t.Errorf("non-local env %q with a strong secret should pass, got %v", raw, err)
		}
	}
}

// C6: an unrecognized environment (e.g. "staging") silently degrades to SQLite.
// isKnownEnv lets LoadConfig warn instead of failing silently.
func TestIsKnownEnv(t *testing.T) {
	known := []string{"prod", "production", "dev", "development", "local", ""}
	for _, s := range known {
		if !isKnownEnv(s) {
			t.Errorf("isKnownEnv(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"staging", "stage", "qa", "uat", "bogus"} {
		if isKnownEnv(s) {
			t.Errorf("isKnownEnv(%q) = true, want false", s)
		}
	}
}

// ED2: config.yaml ships with seed: true and its header invites operators to run
// `APP_ENV=prod ./server` with it. auto_migrate is already ignored for MySQL, but
// nothing gated the SEED, so an empty production database would be populated with
// the demo platform administrator — whose password is printed in the README.
// Production accounts come from `server init`, never from the demo seed.
func TestSeed_RefusesToPlantDemoDataInProduction(t *testing.T) {
	cfg := &Config{}
	cfg.Env = "prod"
	cfg.Database.Driver = "sqlite"
	cfg.Database.SQLitePath = filepath.Join(t.TempDir(), "prod-seed.db")
	cfg.Database.AutoMigrate = true
	cfg.Database.Seed = true

	db, err := OpenDB(cfg)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if sqlDB, derr := db.DB(); derr == nil {
		t.Cleanup(func() { sqlDB.Close() })
	}
	repo := repository.New(db)

	if err := Seed(repo, cfg); err == nil {
		t.Error("seeding a production database silently succeeded")
	}
	if _, err := repo.GetUserByEmail("linwei@vela.io"); err == nil {
		t.Error("the demo administrator was planted in a production database")
	}
}

// 2026-10 安全审计 M6:staging / uat 等非本地环境的 Env 归一后不是 "prod",旧的守卫只拦 prod,
// 用默认 config.yaml(seed: true)起一个空库就会建出口令公开的管理员。只有本地开发才许种演示数据。
func TestSeed_RefusesNonLocalEnvs(t *testing.T) {
	for _, raw := range []string{"staging", "uat", "test"} {
		t.Run(raw, func(t *testing.T) {
			cfg := &Config{EnvRaw: raw}
			cfg.Env = "dev"
			cfg.Database.Driver = "sqlite"
			cfg.Database.SQLitePath = filepath.Join(t.TempDir(), "seed.db")
			cfg.Database.AutoMigrate = true
			cfg.Database.Seed = true
			db, err := OpenDB(cfg)
			if err != nil {
				t.Fatalf("open db: %v", err)
			}
			if sqlDB, derr := db.DB(); derr == nil {
				t.Cleanup(func() { sqlDB.Close() })
			}
			repo := repository.New(db)
			if err := Seed(repo, cfg); err == nil {
				t.Error("非本地环境种演示数据不应成功")
			}
			if _, err := repo.GetUserByEmail("linwei@vela.io"); err == nil {
				t.Error("非本地环境被种入了演示管理员")
			}
		})
	}
}
