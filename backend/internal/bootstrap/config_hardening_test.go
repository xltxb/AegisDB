package bootstrap

import "testing"

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
