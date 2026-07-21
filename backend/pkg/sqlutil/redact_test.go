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
