package bootstrap

import (
	"testing"
	"time"

	"velagateway/internal/model"
	"velagateway/internal/service"
	"velagateway/pkg/totp"
)

// An MFA-enrolled user must be able to run a SAFE PROD script by presenting one
// TOTP code for the whole batch. Regression for ExecuteSafeScript checking MFA
// per statement with an always-empty code, which made every MFA user's PROD
// script fail (R18).
func TestExecuteSafeScript_MFAUserWithCodeRunsWholeScript(t *testing.T) {
	app := newTestApp(t)
	repo := app.svc.Repo

	u, err := repo.GetUserByEmail("linwei@vela.io") // admin: unrestricted, all-allow
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	secret := totp.GenerateSecret()
	if err := repo.UpdateUserMFA(u.ID, true, secret); err != nil {
		t.Fatalf("enroll mfa: %v", err)
	}
	u, _ = repo.GetUserByID(u.ID)

	var prod int64
	conns, _ := repo.ListConnections()
	for _, c := range conns {
		if c.Env == model.EnvProd {
			prod = c.ID
			break
		}
	}
	if prod == 0 {
		t.Fatal("no prod connection seeded")
	}

	// no code → the MFA gate still applies
	if _, err := app.svc.ExecuteSafeScript(u, prod, "SELECT 1;", "", ""); err != service.ErrMFARequired {
		t.Errorf("safe PROD script without a code should require MFA, got %v", err)
	}

	// one valid code covers the whole safe script
	n, err := app.svc.ExecuteSafeScript(u, prod, "SELECT 1;\nSELECT 2;", totp.Code(secret, time.Now()), "")
	if err != nil || n != 2 {
		t.Errorf("MFA user with a valid code should run all statements: n=%d err=%v", n, err)
	}
}
