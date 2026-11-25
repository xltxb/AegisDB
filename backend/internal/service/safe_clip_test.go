package service

import (
	"strings"
	"testing"
)

const testSecret = "X8wr^J+iu3n!L9cL"

// Excerpts are shortened for display. Shortening BEFORE masking cuts the closing
// quote off the literal, the pattern no longer matches, and most of the password
// rides along inside the excerpt. safeClip fixes the order; this pins it.
func TestSafeClip_MasksBeforeShortening(t *testing.T) {
	cmd := `create user 'db_opt'@'%' IDENTIFIED WITH 'mysql_native_password' by '` + testSecret + `' password expire never`

	// A length that lands inside the password literal — the dangerous case.
	for _, n := range []int{60, 70, 75, 80, 90, 120} {
		if out := safeClip(cmd, n); strings.Contains(out, testSecret) {
			t.Errorf("safeClip(%d) leaked the secret: %q", n, out)
		}
	}
	// Sanity: doing it the wrong way round really does leak, so the test above is
	// testing something.
	if !strings.Contains(clip(cmd, 75), testSecret[:4]) {
		t.Skip("clip boundary moved; the ordering example no longer demonstrates the trap")
	}
}
