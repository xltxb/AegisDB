package service

import "testing"

// The dial-time control rejects connections whose resolved IP is non-public,
// which closes the DNS-rebinding gap that a pre-request LookupIP can't (R20).
func TestCheckDialAddr_BlocksNonPublicTargets(t *testing.T) {
	orig := AllowPrivateWebhookTargets
	defer func() { AllowPrivateWebhookTargets = orig }()

	AllowPrivateWebhookTargets = false // prod posture
	for _, addr := range []string{
		"127.0.0.1:80",       // loopback
		"10.0.0.5:443",       // private
		"192.168.1.1:80",     // private
		"169.254.169.254:80", // cloud metadata (link-local)
		"0.0.0.0:80",         // unspecified
	} {
		if err := checkDialAddr("tcp", addr); err == nil {
			t.Errorf("expected %s to be blocked", addr)
		}
	}

	if err := checkDialAddr("tcp", "8.8.8.8:443"); err != nil {
		t.Errorf("public address should be allowed, got %v", err)
	}

	// dev bypass lets on-host receivers work
	AllowPrivateWebhookTargets = true
	if err := checkDialAddr("tcp", "127.0.0.1:80"); err != nil {
		t.Errorf("dev bypass should allow loopback, got %v", err)
	}
}
