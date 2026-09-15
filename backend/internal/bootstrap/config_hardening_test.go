package bootstrap

import (
	"testing"

	"velagateway/internal/repository"
	"velagateway/internal/testsupport"
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

// C6: an unrecognized environment (e.g. "staging") silently degrades to dev —
// which turns the demo seed on and drops the prod JWT-strength check.
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
// `APP_ENV=prod ./server` with it. Nothing gated the SEED, so an empty production
// database would be populated with the demo platform administrator — whose
// password is printed in the README. Production accounts come from
// `server init`, never from the demo seed.
func TestSeed_RefusesToPlantDemoDataInProduction(t *testing.T) {
	cfg := &Config{}
	cfg.Env = "prod"
	cfg.Database.Seed = true

	db := testsupport.NewDB(t)
	repo := repository.New(db)

	if err := Seed(repo, cfg); err == nil {
		t.Error("seeding a production database silently succeeded")
	}
	if _, err := repo.GetUserByEmail("linwei@vela.io"); err == nil {
		t.Error("the demo administrator was planted in a production database")
	}
}
