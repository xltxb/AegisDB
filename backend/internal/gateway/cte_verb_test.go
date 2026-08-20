package gateway

import "testing"

// The verb that governs a WITH statement is its effective action: the main
// clause's verb for a read, the mutation wherever one hides (a CTE body's
// DELETE really deletes — ER9). Keying on the literal WITH filed every CTE
// into the default write capability, refusing read-only CTE exports.
func TestParseVerb_CTE(t *testing.T) {
	cases := map[string]string{
		"WITH t AS (SELECT id FROM users) SELECT * FROM t":                        "SELECT",
		"WITH RECURSIVE cnt(x) AS (VALUES(1)) SELECT x FROM cnt":                  "SELECT",
		"with monthly as (\n select 1\n)\nselect * from monthly":                  "SELECT",
		"WITH d AS (DELETE FROM orders RETURNING id) SELECT count(*) FROM d":      "DELETE",
		"WITH t AS (SELECT 1) UPDATE orders SET x=1":                              "UPDATE",
		"WITH t AS (SELECT 1) INSERT INTO orders SELECT * FROM t":                 "INSERT",
		"WITH t AS (SELECT 'delete me' AS note FROM users) SELECT note FROM t":    "SELECT", // quoted text must not trigger
		"WITH t AS (":                                                             "WITH",   // unparseable stays WITH (write-tier default)
	}
	for sql, want := range cases {
		if got := ParseVerb(sql); got != want {
			t.Errorf("ParseVerb(%q) = %q, want %q", sql, got, want)
		}
	}
	if !IsRead("WITH t AS (SELECT id FROM users) SELECT * FROM t") {
		t.Error("read-only CTE must classify as read")
	}
	if IsRead("WITH d AS (DELETE FROM orders RETURNING id) SELECT count(*) FROM d") {
		t.Error("mutating CTE must NOT classify as read")
	}
}
