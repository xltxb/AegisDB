package sqlutil

import (
	"strings"
	"testing"
)

func TestRedactSecrets(t *testing.T) {
	cases := []struct {
		name, in, secret string
	}{
		{"mysql create user", "CREATE USER 'u'@'%' IDENTIFIED BY 'p@ss w0rd'", "p@ss w0rd"},
		{"mysql identified with plugin", "ALTER USER 'u' IDENTIFIED WITH caching_sha2_password BY 'topsecret'", "topsecret"},
		{"mysql identified by password hash", "CREATE USER u IDENTIFIED BY PASSWORD '*ABCDEF0123'", "*ABCDEF0123"},
		{"grant identified by", "GRANT ALL ON db.* TO 'u'@'h' IDENTIFIED BY 'grantpw'", "grantpw"},
		{"set password", "SET PASSWORD FOR 'u'@'h' = 'newpass'", "newpass"},
		{"postgres role password", "CREATE ROLE r LOGIN PASSWORD 'pgsecret'", "pgsecret"},
		{"postgres encrypted", "ALTER ROLE r ENCRYPTED PASSWORD 'encpw'", "encpw"},
		{"password function", "SET PASSWORD = PASSWORD('fnsecret')", "fnsecret"},
		// The plugin name may be QUOTED — it is written that way in MySQL's own
		// documentation, so it is the form people paste. Only the bare form was
		// covered, and the quoted one went out to the external approval service
		// with the password in the clear.
		{"quoted plugin name", `create user 'db_opt'@'%' IDENTIFIED WITH 'mysql_native_password' by 'X8wr^J+iu3n!L9cL' password expire never`, "X8wr^J+iu3n!L9cL"},
		{"double-quoted plugin name", `CREATE USER a IDENTIFIED WITH "caching_sha2_password" BY 'topsecret'`, "topsecret"},
		// IDENTIFIED WITH <plugin> AS '<hash>' carries the stored hash. Not a
		// cleartext password, but it is what gets replayed, cracked offline, or
		// copied to clone the account elsewhere.
		{"identified with as hash", `ALTER USER 'u'@'%' IDENTIFIED WITH 'mysql_native_password' AS '*6BB4837EB74329105EE4568DDA7DC67ED2CA2AD9'`, "*6BB4837EB74329105EE4568DDA7DC67ED2CA2AD9"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := RedactSecrets(c.in)
			if strings.Contains(out, c.secret) {
				t.Errorf("secret leaked: %q still contains %q", out, c.secret)
			}
			if !strings.Contains(out, "'***'") {
				t.Errorf("expected a masked literal in %q", out)
			}
		})
	}
}

// A statement without credentials must pass through untouched.
func TestRedactSecrets_NoSecretUnchanged(t *testing.T) {
	for _, q := range []string{
		"SELECT * FROM users WHERE id = 1",
		"UPDATE orders SET status = 'paid' WHERE id = 7",
		"DELETE FROM sessions WHERE created_at < '2026-01-01'",
	} {
		if got := RedactSecrets(q); got != q {
			t.Errorf("non-secret statement changed: %q -> %q", q, got)
		}
	}
}

// The failure this guards against is not "no mask" but "a mask in the wrong
// place". `password` used to match the TAIL of `mysql_native_password`, so the
// rule fired on `password' BY '` and produced
//
//	IDENTIFIED WITH 'mysql_native_password'***'X8wr^J+iu3n!L9cL' ...
//
// which contains a `***` and reads as redacted at a glance while the real
// password sits beside it. Anything that looks handled stops being checked, so
// a partial mask is worse than none: assert the statement is still intelligible
// and the secret is genuinely gone.
func TestRedactSecrets_MaskDoesNotLandMidStatement(t *testing.T) {
	const secret = "X8wr^J+iu3n!L9cL"
	in := `create user 'db_opt'@'%' IDENTIFIED WITH 'mysql_native_password' by '` + secret + `' password expire never`
	out := RedactSecrets(in)

	if strings.Contains(out, secret) {
		t.Fatalf("secret survived: %q", out)
	}
	// The plugin name is not a secret and must be left whole — it is how a reviewer
	// knows which authentication method the account was created with.
	if !strings.Contains(out, "'mysql_native_password'") {
		t.Errorf("plugin name was mangled, statement is no longer readable: %q", out)
	}
	if !strings.Contains(out, "by '***'") {
		t.Errorf("the mask should replace the password literal, got %q", out)
	}
}

// Redaction runs on some paths more than once (stored redacted, then redacted
// again on display). It must not chew through its own output.
func TestRedactSecrets_Idempotent(t *testing.T) {
	for _, in := range []string{
		`CREATE USER a IDENTIFIED WITH 'mysql_native_password' BY 'hunter2'`,
		`SET PASSWORD FOR 'bob'@'%' = 'hunter2'`,
		`CREATE ROLE r WITH ENCRYPTED PASSWORD 'hunter2'`,
	} {
		once := RedactSecrets(in)
		if twice := RedactSecrets(once); twice != once {
			t.Errorf("not idempotent:\n once: %q\n twice: %q", once, twice)
		}
	}
}

// A word that merely ENDS in "password" is not the keyword, and a column called
// password_hint is ordinary data.
func TestRedactSecrets_LeavesNonKeywordsAlone(t *testing.T) {
	for _, q := range []string{
		"SELECT * FROM orders WHERE note = 'password reset requested'",
		"SELECT mysql_native_password FROM t",
	} {
		if got := RedactSecrets(q); got != q {
			t.Errorf("non-credential statement changed: %q -> %q", q, got)
		}
	}
}
