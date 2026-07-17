package bootstrap

import "testing"

// B5: an invalid trusted_proxies entry must refuse startup rather than be
// silently dropped — otherwise gin falls back to trusting ALL proxies, letting a
// client spoof X-Forwarded-For and bypass the IP allowlist (H2 regression).
func TestValidateForServe_RejectsBadTrustedProxy(t *testing.T) {
	base := func() *Config {
		c := &Config{}
		c.Env = "prod"
		c.JWT.Secret = "a-strong-prod-secret-0123456789-abcdefgh"
		return c
	}

	good := base()
	good.Server.TrustedProxies = []string{"127.0.0.1", "10.0.0.0/8", "::1"}
	if err := good.ValidateForServe(); err != nil {
		t.Errorf("valid proxies should pass, got %v", err)
	}

	bad := base()
	bad.Server.TrustedProxies = []string{"127.0.0.1", "not-an-ip"}
	if err := bad.ValidateForServe(); err == nil {
		t.Error("expected ValidateForServe to reject an invalid trusted proxy, got nil")
	}

	badCIDR := base()
	badCIDR.Server.TrustedProxies = []string{"10.0.0.0/99"}
	if err := badCIDR.ValidateForServe(); err == nil {
		t.Error("expected ValidateForServe to reject an invalid CIDR, got nil")
	}
}
