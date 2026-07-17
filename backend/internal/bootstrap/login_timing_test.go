package bootstrap

import (
	"testing"
	"time"
)

// C2: a login for an unknown (or non-active) account must still pay the bcrypt
// cost, so response time doesn't reveal whether an active account exists. Before
// the fix the unknown-user path returned without any bcrypt work and was orders
// of magnitude faster than a wrong-password attempt on a real account.
func TestLogin_ConstantTimeForUnknownUser(t *testing.T) {
	app := newTestApp(t)

	// min-of-N reduces scheduling noise; bcrypt cost dominates and is stable.
	measure := func(email, pw string) time.Duration {
		best := time.Hour
		for i := 0; i < 5; i++ {
			start := time.Now()
			_, _, _, _ = app.svc.Login(email, pw)
			if d := time.Since(start); d < best {
				best = d
			}
		}
		return best
	}

	unknown := measure("nobody@vela.io", "whatever")
	wrongPw := measure("linwei@vela.io", "definitely-wrong-password")

	if unknown < wrongPw/2 {
		t.Errorf("unknown-user login (%v) is far faster than wrong-password (%v): timing oracle for account enumeration", unknown, wrongPw)
	}
}
