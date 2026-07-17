package bootstrap

import "testing"

// A2: the at-rest encryption key for stored connection passwords must be
// resolvable independently of the JWT signing secret, so the JWT secret can be
// rotated (e.g. after a leak) without making every stored ciphertext
// undecryptable. VELA_SECRET_KEY provides that decoupling; when unset it falls
// back to the JWT secret and reports the fallback so startup can warn.
func TestConfig_SecretKeyDecoupledFromJWT(t *testing.T) {
	t.Setenv("VELA_JWT_SECRET", "")
	t.Setenv("VELA_SECRET_KEY", "")
	path := writeTempConfig(t, `env: "dev"`)

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	// No dedicated key set → falls back to the JWT secret and flags it.
	if got, fell := cfg.SecretKeyResolved(); !fell || got != cfg.JWT.Secret {
		t.Errorf("fallback: got (%q, fell=%v), want (jwt secret, true)", got, fell)
	}

	// A dedicated key overrides and is independent of the JWT secret.
	t.Setenv("VELA_SECRET_KEY", "dedicated-at-rest-key-abcdef0123456789")
	cfg2, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load config 2: %v", err)
	}
	got, fell := cfg2.SecretKeyResolved()
	if fell {
		t.Error("with VELA_SECRET_KEY set, expected no fallback to JWT secret")
	}
	if got == cfg2.JWT.Secret {
		t.Error("at-rest key must be independent of the JWT secret when set")
	}
	if got != "dedicated-at-rest-key-abcdef0123456789" {
		t.Errorf("secret key: got %q, want the dedicated env value", got)
	}
}
